// Package cache is a small in-process typed TTL cache with singleflight loading, a size bound
// (LRU eviction) and invalidation by exact key, prefix or tag.
package cache

import (
	"container/list"
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

type entry[V any] struct {
	key     string
	val     V
	expires time.Time
	tags    []string
}

// Cache holds values of type V keyed by string.
type Cache[V any] struct {
	mu     sync.Mutex
	ttl    time.Duration
	max    int
	items  map[string]*list.Element
	lru    *list.List // front = most recent
	tags   map[string]map[string]struct{}
	sf     singleflight.Group
	gen    atomic.Uint64 // bumped on invalidation so in-flight loads don't resurrect stale data
	now    func() time.Time
	hits   atomic.Uint64
	misses atomic.Uint64
}

// New creates a cache. maxEntries <= 0 means 1024.
func New[V any](ttl time.Duration, maxEntries int) *Cache[V] {
	if maxEntries <= 0 {
		maxEntries = 1024
	}
	return &Cache[V]{ttl: ttl, max: maxEntries, items: map[string]*list.Element{}, lru: list.New(), tags: map[string]map[string]struct{}{}, now: time.Now}
}

// Get returns a live entry.
func (c *Cache[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		e := el.Value.(*entry[V])
		if c.now().Before(e.expires) {
			c.lru.MoveToFront(el)
			c.hits.Add(1)
			return e.val, true
		}
		c.removeLocked(el)
	}
	c.misses.Add(1)
	var zero V
	return zero, false
}

// Set stores v under key with the default TTL and optional tags.
func (c *Cache[V]) Set(key string, v V, tags ...string) { c.SetTTL(key, v, c.ttl, tags...) }

// SetTTL stores with an explicit TTL.
func (c *Cache[V]) SetTTL(key string, v V, ttl time.Duration, tags ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setLocked(key, v, ttl, tags)
}

func (c *Cache[V]) setLocked(key string, v V, ttl time.Duration, tags []string) {
	if el, ok := c.items[key]; ok {
		c.removeLocked(el)
	}
	e := &entry[V]{key: key, val: v, expires: c.now().Add(ttl), tags: tags}
	c.items[key] = c.lru.PushFront(e)
	for _, t := range tags {
		if c.tags[t] == nil {
			c.tags[t] = map[string]struct{}{}
		}
		c.tags[t][key] = struct{}{}
	}
	for c.lru.Len() > c.max {
		c.removeLocked(c.lru.Back())
	}
}

func (c *Cache[V]) removeLocked(el *list.Element) {
	e := el.Value.(*entry[V])
	c.lru.Remove(el)
	delete(c.items, e.key)
	for _, t := range e.tags {
		if m := c.tags[t]; m != nil {
			delete(m, e.key)
			if len(m) == 0 {
				delete(c.tags, t)
			}
		}
	}
}

// GetOrLoad returns the cached value or runs load once for all concurrent callers of the key.
// Errors are not cached.
func (c *Cache[V]) GetOrLoad(ctx context.Context, key string, load func(ctx context.Context) (V, error), tags ...string) (V, error) {
	return c.GetOrLoadTagged(ctx, key, func(ctx context.Context) (V, []string, error) {
		v, err := load(ctx)
		return v, tags, err
	})
}

// GetOrLoadTagged is GetOrLoad where the loader also decides the tags (e.g. derived from the loaded value).
func (c *Cache[V]) GetOrLoadTagged(ctx context.Context, key string, load func(ctx context.Context) (V, []string, error)) (V, error) {
	if v, ok := c.Get(key); ok {
		return v, nil
	}
	gen := c.gen.Load()
	res, err, _ := c.sf.Do(key, func() (any, error) {
		if v, ok := c.Get(key); ok { // filled while we waited
			return v, nil
		}
		// The loader must outlive a single caller's cancellation, but keep its values.
		v, tags, err := load(context.WithoutCancel(ctx))
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		if c.gen.Load() == gen { // not invalidated while loading
			c.setLocked(key, v, c.ttl, tags)
		}
		c.mu.Unlock()
		return v, nil
	})
	if err != nil {
		var zero V
		return zero, err
	}
	return res.(V), nil
}

// Delete removes one key.
func (c *Cache[V]) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen.Add(1)
	if el, ok := c.items[key]; ok {
		c.removeLocked(el)
	}
}

// InvalidatePrefix removes every key starting with prefix.
func (c *Cache[V]) InvalidatePrefix(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen.Add(1)
	for k, el := range c.items {
		if strings.HasPrefix(k, prefix) {
			c.removeLocked(el)
		}
	}
}

// InvalidateTag removes every entry stored with tag.
func (c *Cache[V]) InvalidateTag(tag string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen.Add(1)
	for k := range c.tags[tag] {
		if el, ok := c.items[k]; ok {
			c.removeLocked(el)
		}
	}
}

// Clear drops everything.
func (c *Cache[V]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen.Add(1)
	c.items = map[string]*list.Element{}
	c.tags = map[string]map[string]struct{}{}
	c.lru.Init()
}

// Len is the number of stored entries (including not-yet-purged expired ones).
func (c *Cache[V]) Len() int { c.mu.Lock(); defer c.mu.Unlock(); return c.lru.Len() }

// Stats returns hit/miss counters.
func (c *Cache[V]) Stats() (hits, misses uint64) { return c.hits.Load(), c.misses.Load() }

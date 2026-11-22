package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTTLAndGet(t *testing.T) {
	c := New[int](time.Minute, 10)
	now := time.Now()
	c.now = func() time.Time { return now }
	c.Set("a", 1)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatal("expected hit")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := c.Get("a"); ok {
		t.Fatal("expected expiry")
	}
}

func TestMaxSizeEvictsLRU(t *testing.T) {
	c := New[int](time.Hour, 3)
	for i := 0; i < 3; i++ {
		c.Set(fmt.Sprint(i), i)
	}
	c.Get("0") // 0 becomes most recent
	c.Set("3", 3)
	if _, ok := c.Get("1"); ok {
		t.Fatal("1 should have been evicted")
	}
	if _, ok := c.Get("0"); !ok {
		t.Fatal("0 should remain")
	}
	if c.Len() != 3 {
		t.Fatalf("len=%d", c.Len())
	}
}

func TestSingleflight(t *testing.T) {
	c := New[int](time.Hour, 10)
	var calls atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.GetOrLoad(context.Background(), "k", func(context.Context) (int, error) {
				calls.Add(1)
				time.Sleep(30 * time.Millisecond)
				return 42, nil
			})
			if err != nil || v != 42 {
				t.Error("bad result")
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("loader ran %d times", calls.Load())
	}
}

func TestErrorsNotCached(t *testing.T) {
	c := New[int](time.Hour, 10)
	_, err := c.GetOrLoad(context.Background(), "k", func(context.Context) (int, error) { return 0, errors.New("boom") })
	if err == nil {
		t.Fatal("want error")
	}
	v, _ := c.GetOrLoad(context.Background(), "k", func(context.Context) (int, error) { return 7, nil })
	if v != 7 {
		t.Fatal("error was cached")
	}
}

func TestInvalidatePrefixAndTag(t *testing.T) {
	c := New[string](time.Hour, 10)
	c.Set("lookups:u1", "a", "lookups")
	c.Set("lookups:u2", "b", "lookups")
	c.Set("dash:u1", "c", "dash")
	c.InvalidatePrefix("lookups:u1")
	if _, ok := c.Get("lookups:u1"); ok {
		t.Fatal("prefix not invalidated")
	}
	if _, ok := c.Get("lookups:u2"); !ok {
		t.Fatal("u2 must stay")
	}
	c.InvalidateTag("lookups")
	if _, ok := c.Get("lookups:u2"); ok {
		t.Fatal("tag not invalidated")
	}
	if _, ok := c.Get("dash:u1"); !ok {
		t.Fatal("dash must stay")
	}
}

func TestInvalidateDuringLoadDoesNotStoreStale(t *testing.T) {
	c := New[int](time.Hour, 10)
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.GetOrLoad(context.Background(), "k", func(context.Context) (int, error) {
			close(started)
			<-release
			return 1, nil
		})
	}()
	<-started
	c.Delete("k")
	close(release)
	<-done
	if _, ok := c.Get("k"); ok {
		t.Fatal("stale value stored after invalidation")
	}
}

package app_test

import (
	"net"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"btn-ops/internal/app/apptest"
	"btn-ops/internal/auth"
)

// BenchmarkLookupsCachedHTTP measures GET /api/lookups through a real TCP listener with 32 concurrent
// clients (cache warm, session cache warm). Run: go test ./internal/app -run xxx -bench Lookups -benchtime 5s
func BenchmarkLookupsCachedHTTP(b *testing.B) {
	e := apptest.New(b)
	u := e.User(auth.RoleGeneralManager)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	go e.App.Listener(ln)
	defer e.App.Shutdown()
	url := "http://" + ln.Addr().String() + "/api/lookups"
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 64}}
	do := func(etag string) (int, time.Duration) {
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("X-Requested-With", "btn-ops")
		req.AddCookie(&http.Cookie{Name: "btn_session", Value: u.Token})
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		t0 := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			b.Error(err)
			return 0, 0
		}
		resp.Body.Close()
		return resp.StatusCode, time.Since(t0)
	}
	// warm up and learn the ETag
	req, _ := http.NewRequest("GET", url, nil)
	req.AddCookie(&http.Cookie{Name: "btn_session", Value: u.Token})
	resp, _ := client.Do(req)
	etag := resp.Header.Get("ETag")
	resp.Body.Close()

	for _, mode := range []struct{ name, etag string }{{"200-cached", ""}, {"304-etag", etag}} {
		b.Run(mode.name, func(b *testing.B) {
			var mu sync.Mutex
			var lat []time.Duration
			var wg sync.WaitGroup
			per := b.N/32 + 1
			b.ResetTimer()
			for w := 0; w < 32; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					local := make([]time.Duration, 0, per)
					for i := 0; i < per; i++ {
						if _, d := do(mode.etag); d > 0 {
							local = append(local, d)
						}
					}
					mu.Lock()
					lat = append(lat, local...)
					mu.Unlock()
				}()
			}
			wg.Wait()
			b.StopTimer()
			sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
			if len(lat) > 0 {
				b.ReportMetric(float64(lat[len(lat)/2].Microseconds())/1000, "p50_ms")
				b.ReportMetric(float64(lat[len(lat)*95/100].Microseconds())/1000, "p95_ms")
				b.ReportMetric(float64(lat[len(lat)*99/100].Microseconds())/1000, "p99_ms")
			}
		})
	}
}

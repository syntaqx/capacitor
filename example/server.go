package main

import (
	"fmt"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// demoServer is a fake backend that advertises its capacity through response
// headers, the way a real capacity-aware service would. Its "true" capacity
// changes over time to simulate autoscaling and load spikes, so a well-behaved
// client visibly tracks it, while a naive client visibly overwhelms it.
type demoServer struct {
	start time.Time

	inFlight     atomic.Int64
	peakInFlight atomic.Int64
	total        atomic.Int64
	ok           atomic.Int64
	shed         atomic.Int64 // 503s returned because we were overloaded

	mu          sync.Mutex
	windowStart time.Time
	windowCount int
}

const (
	rateLimitQuota  = 3000 // requests per rate-limit window
	rateLimitWindow = 10 * time.Second
	phaseDuration   = 6 * time.Second
)

// capacityPhases is the server's true concurrent-request capacity over time.
// It rises and falls to exercise both throttling and recovery.
var capacityPhases = []int{16, 5, 28, 8, 40, 6}

func newDemoServer() *demoServer {
	now := time.Now()
	return &demoServer{start: now, windowStart: now}
}

// capacityAt returns the server's true capacity at the given elapsed time,
// stepping through phases with a gentle wobble so it is never perfectly flat.
func (s *demoServer) capacityAt(elapsed time.Duration) int {
	base := float64(capacityPhases[int(elapsed/phaseDuration)%len(capacityPhases)])
	wobble := base * 0.15 * math.Sin(elapsed.Seconds()*1.5)
	return int(math.Max(1, math.Round(base+wobble)))
}

func (s *demoServer) handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/__stats" {
		s.writeStats(w)
		return
	}

	elapsed := time.Since(s.start)
	capacity := s.capacityAt(elapsed)

	cur := s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	s.total.Add(1)
	s.recordPeak(cur)

	load := float64(cur) / float64(capacity)
	remaining, resetIn := s.rateLimit()

	h := w.Header()
	h.Set("X-Capacity-Suggested-Concurrency", fmt.Sprintf("%d", capacity))
	h.Set("X-Capacity-Worker-Load-Factor", fmt.Sprintf("%.2f", load))
	h.Set("X-Capacity-Tasks-Running", fmt.Sprintf("%d", capacity))
	h.Set("X-Capacity-Tasks-Desired", fmt.Sprintf("%d", capacity))
	h.Set("X-Capacity-Status", statusFor(load))
	h.Set("X-RateLimit-Limit", fmt.Sprintf("%d", rateLimitQuota))
	h.Set("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))
	h.Set("X-RateLimit-Reset", fmt.Sprintf("%d", int(resetIn.Seconds())))

	// Out of quota: hard rate limit until the window resets.
	if remaining <= 0 {
		s.shed.Add(1)
		h.Set("Retry-After", fmt.Sprintf("%d", int(resetIn.Seconds())+1))
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}

	// Overloaded: shed load with 503 + a short Retry-After. The more overloaded
	// we are, the more aggressively we reject.
	if cur > int64(capacity) && float64(cur)/float64(capacity) > 1.25 {
		s.shed.Add(1)
		h.Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	// Serve the request; latency grows with load to simulate real backpressure.
	latency := 15*time.Millisecond + time.Duration(load*float64(60*time.Millisecond))
	time.Sleep(latency)

	s.ok.Add(1)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"ok":true,"capacity":%d,"inflight":%d,"load":%.2f}`, capacity, cur, load)
}

func statusFor(load float64) string {
	switch {
	case load >= 1.5:
		return "at_limit"
	case load >= 1.0:
		return "degraded"
	case load >= 0.75:
		return "busy"
	default:
		return "healthy"
	}
}

func (s *demoServer) recordPeak(cur int64) {
	for {
		p := s.peakInFlight.Load()
		if cur <= p || s.peakInFlight.CompareAndSwap(p, cur) {
			return
		}
	}
}

func (s *demoServer) rateLimit() (remaining int, resetIn time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Sub(s.windowStart) >= rateLimitWindow {
		s.windowStart = now
		s.windowCount = 0
	}
	s.windowCount++
	remaining = max(0, rateLimitQuota-s.windowCount)
	return remaining, rateLimitWindow - now.Sub(s.windowStart)
}

func (s *demoServer) writeStats(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"total":%d,"ok":%d,"shed":%d,"peakInFlight":%d}`,
		s.total.Load(), s.ok.Load(), s.shed.Load(), s.peakInFlight.Load())
}

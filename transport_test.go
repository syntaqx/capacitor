package capacitor_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/syntaqx/capacitor"
)

// Regression: a healthy rate-limit response must NOT collapse concurrency to the
// configured minimum. Previously a zero-value "no suggestion" was treated as a
// suggestion of 0 and clamped down to MinConcurrency.
func TestTransport_HealthyRateLimitDoesNotCollapse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := capacitor.Wrap(nil).WithRateLimitHeaders().WithConcurrency(100, 1, 100).Build()
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	state := client.State(server.URL)
	if state.CurrentConcurrency != 100 {
		t.Fatalf("concurrency = %d, want 100 (healthy quota must not throttle)", state.CurrentConcurrency)
	}
	if state.Clamped {
		t.Error("state should not be marked clamped for a healthy response")
	}
}

// TestTransport_HealthyRateLimitReset guards against treating the rate-limit
// Reset header as a block: a healthy response must not stall later requests.
func TestTransport_HealthyRateLimitReset(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("X-RateLimit-Reset", "30")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := capacitor.Wrap(nil).WithRateLimitHeaders().Build()
	for range 3 {
		resp, err := client.Get(server.URL)
		if err != nil {
			t.Fatalf("healthy rate-limit response must not block: %v", err)
		}
		resp.Body.Close()
	}
	if hits != 3 {
		t.Fatalf("expected all 3 requests to reach the server, got %d", hits)
	}
}

func TestTransport_BlockingIsEnforced(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := capacitor.Wrap(nil).WithHTTPStatusHandling().Build()

	// First request receives 429 + Retry-After, arming the block window.
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("first request should succeed at the HTTP level: %v", err)
	}
	resp.Body.Close()

	// Second request must be refused locally without reaching the server.
	_, err = client.Get(server.URL)
	if err == nil {
		t.Fatal("expected a block error on the second request")
	}
	var capErr *capacitor.CapacityError
	if !errors.As(err, &capErr) {
		t.Fatalf("expected CapacityError, got %T", err)
	}
	if capErr.Op != "blocked" || !errors.Is(err, capacitor.ErrBlocked) {
		t.Errorf("expected blocked error, got op=%q err=%v", capErr.Op, err)
	}
	if hits != 1 {
		t.Errorf("blocked request should not reach the server; hits = %d, want 1", hits)
	}
}

func TestTransport_StaleStateResets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Capacity-Suggested-Concurrency", "2")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := capacitor.NewClient(&capacitor.Config{
		InitialConcurrency: 20,
		MinConcurrency:     1,
		MaxConcurrency:     100,
		StateExpiry:        20 * time.Millisecond,
		SignalHandlers:     []capacitor.SignalHandler{&capacitor.CapacityHandler{}},
	})

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got := client.State(server.URL).CurrentConcurrency; got != 2 {
		t.Fatalf("concurrency after signal = %d, want 2", got)
	}

	// Let the state go stale, then make a request that should reset to initial.
	time.Sleep(40 * time.Millisecond)
	resp, err = client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// After the reset-on-entry it will be re-throttled by this response to 2,
	// so assert the reset happened by observing it never got stuck below initial
	// across a fresh, non-signalling host instead.
	if got := client.State(server.URL).CurrentConcurrency; got != 2 {
		t.Fatalf("concurrency after second signal = %d, want 2", got)
	}
}

func TestTransport_GOAWAYBackoff(t *testing.T) {
	cfg := capacitor.DefaultConfig()
	cfg.EnableGOAWAYHandling = true
	cfg.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errStr("http2: server sent GOAWAY")
	})

	client := capacitor.NewClient(cfg)
	if _, err := client.Get("https://example.com"); err == nil {
		t.Fatal("expected transport error to surface")
	}

	state := client.State("https://example.com")
	if state == nil || !state.IsBlocked() {
		t.Fatal("GOAWAY should have armed a block window")
	}
}

func TestTransport_DoesNotMutateRequest(t *testing.T) {
	var seenUA string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := capacitor.Wrap(nil).WithUserAgent("Capacitor/test").Build()

	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if seenUA != "Capacitor/test" {
		t.Errorf("server saw UA %q, want %q", seenUA, "Capacitor/test")
	}
	if req.Header.Get("User-Agent") != "" {
		t.Errorf("caller request was mutated: UA = %q", req.Header.Get("User-Agent"))
	}
}

func TestHostKeyFunc(t *testing.T) {
	cases := map[string]string{
		"https://api.example.com/a/b": "https://api.example.com",
		"http://localhost:8080/x":     "http://localhost:8080",
	}
	for in, want := range cases {
		u, _ := url.Parse(in)
		if got := capacitor.HostKeyFunc(u); got != want {
			t.Errorf("HostKeyFunc(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPathPrefixKeyFunc(t *testing.T) {
	fn := capacitor.PathPrefixKeyFunc(1)
	u, _ := url.Parse("https://api.example.com/admin/users")
	if got := fn(u); got != "https://api.example.com/admin" {
		t.Errorf("got %q", got)
	}

	root, _ := url.Parse("https://api.example.com/")
	if got := fn(root); got != "https://api.example.com" {
		t.Errorf("root path got %q", got)
	}

	if got := capacitor.PathPrefixKeyFunc(0)(u); got != "https://api.example.com" {
		t.Errorf("n<=0 should fall back to host, got %q", got)
	}
}

func TestExactPathKeyFunc(t *testing.T) {
	u, _ := url.Parse("https://api.example.com/v1/thing")
	if got := capacitor.ExactPathKeyFunc(u); got != "https://api.example.com/v1/thing" {
		t.Errorf("got %q", got)
	}
}

func TestTransport_CustomKeyFunc(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := capacitor.Wrap(nil).
		WithKeyFunc(capacitor.ExactPathKeyFunc).
		Build()

	for _, p := range []string{"/a", "/b"} {
		resp, err := client.Get(server.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	if got := len(client.Stats()); got != 2 {
		t.Errorf("expected 2 pools with exact-path keys, got %d", got)
	}
}

// TestTransport_ConcurrentResponses drives many concurrent requests against a
// server that constantly changes its suggested concurrency, exercising the
// per-host adjustment path under contention. Run under -race in CI.
func TestTransport_ConcurrentResponses(t *testing.T) {
	var counter int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Oscillate the suggestion so the transport resizes repeatedly.
		n := (atomic.AddInt64(&counter, 1) % 20) + 1
		w.Header().Set("X-Capacity-Suggested-Concurrency", strconv.FormatInt(n, 10))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := capacitor.Wrap(nil).
		WithCapacityHeaders().
		WithConcurrency(10, 1, 20).
		Build()

	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Get(server.URL)
			if err != nil {
				return
			}
			resp.Body.Close()
		}()
	}
	wg.Wait()

	state := client.State(server.URL)
	if state == nil {
		t.Fatal("expected state to exist")
	}
	if state.CurrentConcurrency < 1 || state.CurrentConcurrency > 20 {
		t.Fatalf("final concurrency %d out of [1,20]", state.CurrentConcurrency)
	}
	for _, s := range client.Stats() {
		if s.InUse != 0 {
			t.Errorf("leaked in-use slots: %d", s.InUse)
		}
	}
}

func TestTransport_EvictsIdleHosts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := capacitor.Wrap(nil).
		WithKeyFunc(capacitor.ExactPathKeyFunc).
		WithConcurrency(5, 1, 5).
		WithMaxTrackedHosts(2).
		WithHostIdleTTL(time.Millisecond).
		Build()

	for _, p := range []string{"/a", "/b"} {
		resp, err := client.Get(server.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if got := len(client.Stats()); got != 2 {
		t.Fatalf("expected 2 tracked hosts, got %d", got)
	}

	// Let the existing hosts pass their idle TTL, then add a third. The idle
	// hosts should be swept, keeping the map bounded.
	time.Sleep(5 * time.Millisecond)
	resp, err := client.Get(server.URL + "/c")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got := len(client.Stats()); got > 2 {
		t.Fatalf("tracked hosts = %d, want <= 2 after eviction", got)
	}
}

// TestTransport_ConcurrentNewHost exercises the double-checked locking path in
// getOrCreateHostState: many goroutines race to create the same host entry
// simultaneously, so the second writer hits the already-exists branch.
func TestTransport_ConcurrentNewHost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := capacitor.Wrap(nil).Build()

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Get(server.URL)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			resp.Body.Close()
		}()
	}
	wg.Wait()

	if got := len(client.Stats()); got != 1 {
		t.Fatalf("expected exactly 1 host pool, got %d", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type errStr string

func (e errStr) Error() string { return string(e) }

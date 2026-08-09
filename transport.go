package capacitor

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Transport is an http.RoundTripper that enforces capacity limits
// based on server-provided capacity signaling headers.
//
// It is safe for concurrent use by multiple goroutines.
type Transport struct {
	config *Config
	base   http.RoundTripper
	goaway *GOAWAYHandler

	mu    sync.RWMutex
	hosts map[string]*hostState
}

type hostState struct {
	// mu serializes signal-driven adjustments so the read-modify-write across
	// state and semaphore stays atomic under concurrent responses.
	mu        sync.Mutex
	state     *State
	semaphore *Semaphore
}

// NewTransport creates a new capacity-aware transport.
func NewTransport(config *Config) *Transport {
	cfg := config.withDefaults()

	base := cfg.Transport
	if base == nil {
		base = http.DefaultTransport
	}

	t := &Transport{
		config: cfg,
		base:   base,
		hosts:  make(map[string]*hostState),
	}
	if cfg.EnableGOAWAYHandling {
		t.goaway = &GOAWAYHandler{}
	}
	return t
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := t.hostKey(req.URL)
	hs := t.getOrCreateHostState(host)

	// Reset throttling if the cached state has gone stale.
	t.expireStaleState(host, hs)

	// Refuse the request while the host is in a server-signalled block window.
	if hs.state.IsBlocked() {
		return nil, &CapacityError{
			Op:    "blocked",
			Host:  host,
			Err:   ErrBlocked,
			State: hs.state.clone(),
		}
	}

	// Add the user agent without mutating the caller's request. The
	// http.RoundTripper contract forbids modifying the incoming request.
	req = t.withUserAgent(req)

	// Create a context with timeout for acquiring the semaphore
	ctx := req.Context()
	if t.config.AcquireTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.config.AcquireTimeout)
		defer cancel()
	}

	// Acquire a concurrency slot
	if err := hs.semaphore.Acquire(ctx); err != nil {
		return nil, &CapacityError{
			Op:    "acquire",
			Host:  host,
			Err:   err,
			State: hs.state.clone(),
		}
	}

	// Ensure we release the slot when done
	defer hs.semaphore.Release()

	// Make the actual request
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		t.handleTransportError(host, hs, err)
		return nil, err
	}

	// Update state from response headers
	t.updateState(host, hs, resp)

	return resp, nil
}

// expireStaleState resets a host back to its initial concurrency once its
// cached state has not been refreshed within StateExpiry. This prevents a host
// from staying throttled indefinitely after the server has recovered.
func (t *Transport) expireStaleState(host string, hs *hostState) {
	if t.config.StateExpiry <= 0 || !hs.state.IsStale(t.config.StateExpiry) {
		return
	}

	// An active block window is authoritative and expires on its own schedule;
	// don't let stale-state recovery cut it short.
	if hs.state.IsBlocked() {
		return
	}

	if t.adjustConcurrency(hs, t.config.InitialConcurrency) {
		hs.state.touch()
		if t.config.OnStateChange != nil {
			t.config.OnStateChange(host, hs.state.clone())
		}
	}
}

// adjustConcurrency clamps target to the configured bounds and atomically
// applies it to the host's state and semaphore. It reports whether the value
// changed so the caller can fire OnStateChange outside the lock.
func (t *Transport) adjustConcurrency(hs *hostState, target int) bool {
	clamped := target
	if clamped < t.config.MinConcurrency {
		clamped = t.config.MinConcurrency
	}
	if clamped > t.config.MaxConcurrency {
		clamped = t.config.MaxConcurrency
	}

	hs.mu.Lock()
	defer hs.mu.Unlock()
	if hs.state.getCurrentConcurrency() == clamped {
		return false
	}
	hs.state.setCurrentConcurrency(clamped)
	hs.semaphore.Resize(clamped)
	hs.state.setClamped(target != clamped)
	return true
}

// handleTransportError inspects transport-level errors for GOAWAY or connection
// reset signals and applies backoff to the host when GOAWAY handling is enabled.
func (t *Transport) handleTransportError(host string, hs *hostState, err error) {
	if t.goaway == nil {
		return
	}
	signal := t.goaway.ProcessError(err)
	if signal == nil {
		return
	}

	if t.config.OnSignal != nil {
		t.config.OnSignal(host, signal)
	}
	if !signal.BlockUntil.IsZero() {
		hs.state.setBlockedUntil(signal.BlockUntil)
	}
	hs.state.touch()
}

// getOrCreateHostState returns the state for a host, creating it if needed.
func (t *Transport) getOrCreateHostState(host string) *hostState {
	t.mu.RLock()
	hs, ok := t.hosts[host]
	t.mu.RUnlock()

	if ok {
		return hs
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Double-check after acquiring write lock
	if hs, ok := t.hosts[host]; ok {
		return hs
	}

	// Bound memory by evicting idle hosts before growing past the limit.
	if t.config.MaxTrackedHosts > 0 && len(t.hosts) >= t.config.MaxTrackedHosts {
		t.evictIdleLocked()
	}

	hs = &hostState{
		state:     newState(t.config.InitialConcurrency),
		semaphore: NewSemaphore(t.config.InitialConcurrency),
	}
	t.hosts[host] = hs

	return hs
}

// evictIdleLocked removes hosts that have no in-flight or queued requests, are
// not in a block window, and have been idle beyond HostIdleTTL. The caller must
// hold t.mu for writing. Evicting only fully idle hosts keeps the pointer that
// active requests already hold valid.
func (t *Transport) evictIdleLocked() {
	ttl := t.config.HostIdleTTL
	for key, hs := range t.hosts {
		if hs.semaphore.InUse() == 0 &&
			hs.semaphore.Waiting() == 0 &&
			!hs.state.IsBlocked() &&
			hs.state.IsStale(ttl) {
			delete(t.hosts, key)
		}
	}
}

// updateState updates the host state from response headers using signal handlers.
func (t *Transport) updateState(host string, hs *hostState, resp *http.Response) {
	// Record that we heard from this host so stale-state expiry works even for
	// responses that carry no capacity headers.
	hs.state.touch()

	// If no handlers configured, nothing to do
	if len(t.config.SignalHandlers) == 0 {
		return
	}

	// Process response through all registered signal handlers
	var signals []*Signal
	for _, handler := range t.config.SignalHandlers {
		if signal := handler.Process(resp); signal != nil {
			signals = append(signals, signal)

			// Notify signal callback if configured
			if t.config.OnSignal != nil {
				t.config.OnSignal(host, signal)
			}
		}
	}

	// If no signals detected, keep current concurrency (defaults are sane)
	if len(signals) == 0 {
		return
	}

	// Process signals to determine action
	action := t.processSignals(signals)

	// Handle blocking signals (rate limit exceeded, etc.)
	if action.Block {
		hs.state.setBlockedUntil(action.BlockUntil)
	}

	// Update concurrency if suggested
	if action.AdjustConcurrency {
		if t.adjustConcurrency(hs, action.NewConcurrency) && t.config.OnStateChange != nil {
			t.config.OnStateChange(host, hs.state.clone())
		}
	}

	// Update state metadata from capacity headers if present
	headers := make(map[string]string)
	for _, key := range capacityHeaders {
		if v := resp.Header.Get(key); v != "" {
			headers[key] = v
		}
	}
	if len(headers) > 0 {
		hs.state.update(headers)
	}
}

// processSignals aggregates signals into an action.
func (t *Transport) processSignals(signals []*Signal) *signalAction {
	action := &signalAction{}

	for _, signal := range signals {
		// Any signal carrying a block window (Retry-After, rate-limit reset,
		// explicit block) should pause requests to the host.
		if !signal.BlockUntil.IsZero() {
			action.Block = true
			if signal.BlockUntil.After(action.BlockUntil) {
				action.BlockUntil = signal.BlockUntil
			}
		}

		switch signal.Type {
		case SignalTypeBlock:
			action.Block = true

		case SignalTypeRateLimit, SignalTypeBackoff:
			// Use the most conservative (lowest) suggested concurrency
			if signal.SuggestedConcurrency >= 0 {
				if !action.AdjustConcurrency || signal.SuggestedConcurrency < action.NewConcurrency {
					action.AdjustConcurrency = true
					action.NewConcurrency = signal.SuggestedConcurrency
				}
			}

		case SignalTypeCapacity:
			// Capacity signals suggest concurrency adjustments
			if signal.SuggestedConcurrency >= 0 {
				if !action.AdjustConcurrency {
					action.AdjustConcurrency = true
					action.NewConcurrency = signal.SuggestedConcurrency
				}
			}
		}
	}

	return action
}

// withUserAgent returns a request with the configured User-Agent applied. When a
// change is required it shallow-copies the request and clones only the header
// map, honoring the http.RoundTripper contract that the incoming request (and
// its header map) must not be modified.
func (t *Transport) withUserAgent(req *http.Request) *http.Request {
	if t.config.UserAgent == "" {
		return req
	}
	ua := t.config.UserAgent
	if existing := req.Header.Get("User-Agent"); existing != "" {
		ua = t.config.UserAgent + " " + existing
	}
	clone := *req
	header := req.Header.Clone()
	if header == nil {
		header = make(http.Header)
	}
	clone.Header = header
	clone.Header.Set("User-Agent", ua)
	return &clone
}

// hostKey returns the key used for concurrency grouping.
// If KeyFunc is configured, it is used; otherwise defaults to scheme://host.
func (t *Transport) hostKey(u *url.URL) string {
	if t.config.KeyFunc != nil {
		return t.config.KeyFunc(u)
	}
	return HostKeyFunc(u)
}

// State returns the current state snapshot for a host key, or nil if unknown.
func (t *Transport) State(host string) *State {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if hs, ok := t.hosts[host]; ok {
		return hs.state.clone()
	}
	return nil
}

// Stats returns statistics for all known hosts.
func (t *Transport) Stats() map[string]Stats {
	t.mu.RLock()
	defer t.mu.RUnlock()

	stats := make(map[string]Stats, len(t.hosts))
	for host, hs := range t.hosts {
		snap := hs.state.clone()
		stats[host] = Stats{
			CurrentConcurrency: snap.CurrentConcurrency,
			InUse:              hs.semaphore.InUse(),
			Available:          hs.semaphore.Available(),
			Waiting:            hs.semaphore.Waiting(),
			Status:             snap.Status,
			LastUpdated:        snap.LastUpdated,
		}
	}
	return stats
}

// Stats represents statistics for a single host.
type Stats struct {
	CurrentConcurrency int
	InUse              int
	Available          int
	Waiting            int
	Status             Status
	LastUpdated        time.Time
}

// capacityHeaders is the list of headers to look for in responses.
var capacityHeaders = []string{
	"X-Capacity-Status",
	"X-Capacity-Tasks-Running",
	"X-Capacity-Tasks-Desired",
	"X-Capacity-Tasks-Pending",
	"X-Capacity-Cluster-Max-Concurrency",
	"X-Capacity-Suggested-Concurrency",
	"X-Capacity-State-Age",
	"X-Capacity-Worker-Active",
	"X-Capacity-Worker-Available",
	"X-Capacity-Worker-Load-Factor",
	"X-Capacity-Latency-P99",
	"X-Capacity-Latency-Health",
}

// HostKeyFunc returns a key based on scheme://host:port only.
// This is the default behavior and groups all paths on the same host together.
func HostKeyFunc(u *url.URL) string {
	if u.Port() != "" {
		return u.Scheme + "://" + u.Host
	}
	return u.Scheme + "://" + u.Hostname()
}

// PathPrefixKeyFunc returns a KeyFunc that groups requests by the first n
// path segments. This is useful when different path prefixes map to
// different backend deployments.
//
// For example, with n=1:
//   - api.example.com/admin/users -> https://api.example.com/admin
//   - api.example.com/admin/config -> https://api.example.com/admin
//   - api.example.com/sales/orders -> https://api.example.com/sales
//
// With n=2:
//   - api.example.com/v1/admin/users -> https://api.example.com/v1/admin
//   - api.example.com/v1/sales/orders -> https://api.example.com/v1/sales
func PathPrefixKeyFunc(n int) func(u *url.URL) string {
	return func(u *url.URL) string {
		base := HostKeyFunc(u)
		if n <= 0 {
			return base
		}

		path := u.Path
		if path == "" || path == "/" {
			return base
		}

		// Trim leading slash and split
		if path[0] == '/' {
			path = path[1:]
		}

		segments := strings.SplitN(path, "/", n+1)
		if len(segments) == 0 {
			return base
		}

		// Take up to n segments
		count := n
		if len(segments) < count {
			count = len(segments)
		}

		return base + "/" + strings.Join(segments[:count], "/")
	}
}

// ExactPathKeyFunc returns a KeyFunc that groups requests by the exact path.
// This gives the most granular control but may create many concurrency pools.
func ExactPathKeyFunc(u *url.URL) string {
	return HostKeyFunc(u) + u.Path
}

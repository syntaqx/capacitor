package capacitor

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// Transport is an http.RoundTripper that enforces capacity limits
// based on server-provided capacity signaling headers.
//
// It is safe for concurrent use by multiple goroutines.
type Transport struct {
	config *Config
	base   http.RoundTripper

	mu    sync.RWMutex
	hosts map[string]*hostState
}

type hostState struct {
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

	return &Transport{
		config: cfg,
		base:   base,
		hosts:  make(map[string]*hostState),
	}
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := t.hostKey(req.URL)
	hs := t.getOrCreateHostState(host)

	// Add user agent if configured
	t.addUserAgent(req)

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
			State: hs.state.Clone(),
		}
	}

	// Ensure we release the slot when done
	defer hs.semaphore.Release()

	// Make the actual request
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	// Update state from response headers
	t.updateState(host, hs, resp)

	return resp, nil
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

	hs = &hostState{
		state:     NewState(t.config.InitialConcurrency),
		semaphore: NewSemaphore(t.config.InitialConcurrency),
	}
	t.hosts[host] = hs

	return hs
}

// updateState updates the host state from response headers using signal handlers.
func (t *Transport) updateState(host string, hs *hostState, resp *http.Response) {
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
		hs.state.SetBlockedUntil(action.BlockUntil)
	}

	// Update concurrency if suggested
	if action.AdjustConcurrency && action.NewConcurrency > 0 {
		suggested := action.NewConcurrency
		if suggested < t.config.MinConcurrency {
			suggested = t.config.MinConcurrency
		}
		if suggested > t.config.MaxConcurrency {
			suggested = t.config.MaxConcurrency
		}

		current := hs.state.GetCurrentConcurrency()
		if suggested != current {
			hs.state.SetCurrentConcurrency(suggested)
			hs.semaphore.Resize(suggested)

			if t.config.OnStateChange != nil {
				t.config.OnStateChange(host, hs.state.Clone())
			}
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
		hs.state.Update(headers)
	}
}

// processSignals aggregates signals into an action.
func (t *Transport) processSignals(signals []*Signal) *SignalAction {
	action := &SignalAction{
		Signals: signals,
	}

	for _, signal := range signals {
		switch signal.Type {
		case SignalTypeBlock:
			action.Block = true
			if signal.BlockUntil.After(action.BlockUntil) {
				action.BlockUntil = signal.BlockUntil
			}
			if signal.RetryAfter > action.RetryAfter {
				action.RetryAfter = signal.RetryAfter
			}

		case SignalTypeRateLimit, SignalTypeBackoff:
			// Use the most conservative (lowest) suggested concurrency
			if signal.SuggestedConcurrency > 0 {
				if !action.AdjustConcurrency || signal.SuggestedConcurrency < action.NewConcurrency {
					action.AdjustConcurrency = true
					action.NewConcurrency = signal.SuggestedConcurrency
				}
			}
			if signal.Type == SignalTypeBackoff {
				action.Backoff = true
			}

		case SignalTypeCapacity:
			// Capacity signals suggest concurrency adjustments
			if signal.SuggestedConcurrency > 0 {
				if !action.AdjustConcurrency {
					action.AdjustConcurrency = true
					action.NewConcurrency = signal.SuggestedConcurrency
				}
			}
		}
	}

	return action
}

// addUserAgent adds or appends the configured user agent.
func (t *Transport) addUserAgent(req *http.Request) {
	if t.config.UserAgent == "" {
		return
	}
	existing := req.Header.Get("User-Agent")
	if existing == "" {
		req.Header.Set("User-Agent", t.config.UserAgent)
	} else {
		req.Header.Set("User-Agent", t.config.UserAgent+" "+existing)
	}
}

// hostKey returns the key used for concurrency grouping.
// If KeyFunc is configured, it is used; otherwise defaults to scheme://host.
func (t *Transport) hostKey(u *url.URL) string {
	if t.config.KeyFunc != nil {
		return t.config.KeyFunc(u)
	}
	return HostKeyFunc(u)
}

// GetState returns the current state for a host, or nil if unknown.
func (t *Transport) GetState(host string) *State {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if hs, ok := t.hosts[host]; ok {
		return hs.state.Clone()
	}
	return nil
}

// GetStats returns statistics for all known hosts.
func (t *Transport) GetStats() map[string]Stats {
	t.mu.RLock()
	defer t.mu.RUnlock()

	stats := make(map[string]Stats, len(t.hosts))
	for host, hs := range t.hosts {
		stats[host] = Stats{
			CurrentConcurrency: hs.state.GetCurrentConcurrency(),
			InUse:              hs.semaphore.InUse(),
			Available:          hs.semaphore.Available(),
			Waiting:            hs.semaphore.Waiting(),
			Status:             hs.state.Status,
			LastUpdated:        hs.state.LastUpdated,
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
	LastUpdated        interface{}
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

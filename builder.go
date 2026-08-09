package capacitor

import (
	"cmp"
	"net/http"
	"net/url"
	"slices"
	"time"
)

// Wrap wraps an existing http.Client with capacity-aware behavior.
// Pass nil to use http.DefaultClient.
//
// By default, the wrapped client behaves exactly like the original -
// no signal handlers are enabled. Use the builder methods to add handlers.
//
// Example:
//
//	// Wrap your existing client with rate limit handling
//	client := capacitor.Wrap(myClient).
//	    WithRateLimitHeaders().
//	    WithHTTPStatusHandling().
//	    Build()
//
//	// Or use the default http client
//	client := capacitor.Wrap(nil).
//	    WithRateLimitHeaders().
//	    Build()
func Wrap(client *http.Client) *Builder {
	b := &Builder{
		baseClient: client,
		config: &Config{
			UserAgent:          "Capacitor/1.0",
			InitialConcurrency: 100,
			MaxConcurrency:     100,
			MinConcurrency:     1,
			AcquireTimeout:     30 * time.Second,
			StateExpiry:        30 * time.Second,
		},
		handlers: []SignalHandler{},
	}

	// Extract transport from base client if provided
	if client != nil && client.Transport != nil {
		b.config.Transport = client.Transport
	}

	return b
}

// Builder creates a configured HTTP client with modular signal handling.
type Builder struct {
	baseClient *http.Client
	config     *Config
	handlers   []SignalHandler
}

// ----------------------------------------------------------------------------
// Configuration
// ----------------------------------------------------------------------------

// WithUserAgent sets a User-Agent prefix for requests.
func (b *Builder) WithUserAgent(ua string) *Builder {
	b.config.UserAgent = ua
	return b
}

// WithConcurrency sets the initial, minimum, and maximum concurrency limits.
func (b *Builder) WithConcurrency(initial, minimum, maximum int) *Builder {
	b.config.InitialConcurrency = initial
	b.config.MinConcurrency = minimum
	b.config.MaxConcurrency = maximum
	return b
}

// WithAcquireTimeout sets how long to wait for a concurrency slot before a
// request fails with a *CapacityError. This is distinct from the wrapped
// http.Client's request timeout.
func (b *Builder) WithAcquireTimeout(timeout time.Duration) *Builder {
	b.config.AcquireTimeout = timeout
	return b
}

// OnStateChange registers a callback for state changes.
func (b *Builder) OnStateChange(fn func(host string, state *State)) *Builder {
	b.config.OnStateChange = fn
	return b
}

// OnSignal registers a callback for detected signals.
func (b *Builder) OnSignal(fn func(host string, signal *Signal)) *Builder {
	b.config.OnSignal = fn
	return b
}

// WithKeyFunc sets a custom function for concurrency pool grouping.
// By default, requests are grouped by scheme://host:port.
// Use this to implement path-based or custom grouping strategies.
//
// Example - group by first path segment:
//
//	client := capacitor.Wrap(nil).
//	    WithKeyFunc(capacitor.PathPrefixKeyFunc(1)).
//	    Build()
func (b *Builder) WithKeyFunc(fn func(u *url.URL) string) *Builder {
	b.config.KeyFunc = fn
	return b
}

// WithMaxTrackedHosts bounds the number of per-host pools kept in memory.
// Once the limit is reached, idle hosts (no in-flight or queued requests, not
// blocked, unused beyond the idle TTL) are evicted to make room. Pass 0 for
// unlimited. Useful for long-running clients that touch many distinct hosts,
// such as web crawlers.
func (b *Builder) WithMaxTrackedHosts(n int) *Builder {
	b.config.MaxTrackedHosts = n
	return b
}

// WithHostIdleTTL sets how long a host must be idle before it can be evicted
// once MaxTrackedHosts is reached. Only takes effect when MaxTrackedHosts > 0.
func (b *Builder) WithHostIdleTTL(d time.Duration) *Builder {
	b.config.HostIdleTTL = d
	return b
}

// ----------------------------------------------------------------------------
// Signal Handler Registration
// ----------------------------------------------------------------------------

// WithHandler adds a custom signal handler.
func (b *Builder) WithHandler(handler SignalHandler) *Builder {
	b.handlers = append(b.handlers, handler)
	return b
}

// WithRateLimitHeaders enables X-RateLimit-*, RateLimit-*, and CF-RateLimit-* header processing.
// This single handler covers GitHub, Twitter, Cloudflare, and IETF standard headers
// since HTTP headers are case-insensitive.
func (b *Builder) WithRateLimitHeaders() *Builder {
	b.handlers = append(b.handlers, &RateLimitHandler{})
	return b
}

// WithHTTPStatusHandling enables handling of 429, 503, 420, and Retry-After.
func (b *Builder) WithHTTPStatusHandling() *Builder {
	b.handlers = append(b.handlers, &HTTPStatusHandler{})
	return b
}

// WithCapacityHeaders enables X-Capacity-* header processing.
// This handles application-level capacity signaling.
func (b *Builder) WithCapacityHeaders() *Builder {
	b.handlers = append(b.handlers, &CapacityHandler{})
	return b
}

// WithGOAWAY enables HTTP/2 GOAWAY frame tracking.
func (b *Builder) WithGOAWAY() *Builder {
	b.config.EnableGOAWAYHandling = true
	return b
}

// WithDefaults enables the most common handlers:
// HTTP status codes (429, 503) and rate limit headers.
func (b *Builder) WithDefaults() *Builder {
	return b.
		WithHTTPStatusHandling().
		WithRateLimitHeaders()
}

// WithAll enables all built-in signal handlers.
func (b *Builder) WithAll() *Builder {
	return b.
		WithHTTPStatusHandling().
		WithRateLimitHeaders().
		WithCapacityHeaders().
		WithGOAWAY()
}

// ----------------------------------------------------------------------------
// Build
// ----------------------------------------------------------------------------

// Build creates the configured HTTP client.
func (b *Builder) Build() *Client {
	// Sort handlers by priority, preserving registration order for ties.
	slices.SortStableFunc(b.handlers, func(a, c SignalHandler) int {
		return cmp.Compare(a.Priority(), c.Priority())
	})

	b.config.SignalHandlers = b.handlers

	transport := NewTransport(b.config)

	// Build the wrapped client
	wrapped := &http.Client{
		Transport: transport,
	}

	// Preserve settings from base client if provided
	if b.baseClient != nil {
		wrapped.CheckRedirect = b.baseClient.CheckRedirect
		wrapped.Jar = b.baseClient.Jar
		wrapped.Timeout = b.baseClient.Timeout
	}

	return &Client{
		Client:    wrapped,
		transport: transport,
	}
}

// Transport returns just the transport layer.
// Useful if you need to construct the http.Client yourself.
func (b *Builder) Transport() *Transport {
	slices.SortStableFunc(b.handlers, func(a, c SignalHandler) int {
		return cmp.Compare(a.Priority(), c.Priority())
	})

	b.config.SignalHandlers = b.handlers

	return NewTransport(b.config)
}

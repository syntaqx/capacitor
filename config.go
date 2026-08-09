package capacitor

import (
	"net/http"
	"net/url"
	"time"
)

// Config configures the capacity-aware HTTP client.
type Config struct {
	// UserAgent is prepended to requests.
	// Default: "Capacitor/1.0"
	UserAgent string

	// InitialConcurrency is the starting concurrency limit before
	// receiving any capacity signals from the server.
	// Default: 100
	InitialConcurrency int

	// MaxConcurrency is the absolute maximum concurrent requests allowed,
	// regardless of what the server suggests.
	// Default: 100
	MaxConcurrency int

	// MinConcurrency is the minimum concurrent requests allowed,
	// even if the server suggests lower.
	// Default: 1
	MinConcurrency int

	// AcquireTimeout is how long to wait to acquire a concurrency slot.
	// Default: 30s
	AcquireTimeout time.Duration

	// StateExpiry is how long cached capacity state is considered valid.
	// After this duration without updates, state is considered stale.
	// Default: 30s
	StateExpiry time.Duration

	// OnStateChange is called whenever capacity state changes.
	// Can be used for logging or metrics.
	OnStateChange func(host string, state *State)

	// OnSignal is called whenever a signal is detected.
	// Can be used for logging, metrics, or custom handling.
	OnSignal func(host string, signal *Signal)

	// SignalHandlers is the list of handlers to process responses.
	// If nil, no handlers run and the client behaves as a passthrough
	// (no throttling). Handlers are processed in priority order.
	SignalHandlers []SignalHandler

	// EnableGOAWAYHandling enables tracking of HTTP/2 GOAWAY frames.
	// When enabled, a GOAWAY on a request triggers a short backoff for the host.
	// Default: false
	EnableGOAWAYHandling bool

	// Transport is the underlying HTTP transport to use.
	// If nil, http.DefaultTransport is used.
	Transport http.RoundTripper

	// KeyFunc returns the key used for concurrency grouping.
	// By default, requests are grouped by scheme://host:port.
	// Use this to implement path-based or custom grouping.
	// For example, to group by first path segment:
	//   KeyFunc: capacitor.PathPrefixKeyFunc(1)
	// If nil, HostKeyFunc is used.
	KeyFunc func(u *url.URL) string

	// MaxTrackedHosts bounds the number of per-host pools kept in memory.
	// When adding a host would exceed this limit, idle hosts that have been
	// unused for longer than HostIdleTTL are evicted. Active hosts (with
	// in-flight or queued requests, or an active block window) are never
	// evicted. Zero means unlimited (no eviction).
	// Default: 0 (unlimited)
	MaxTrackedHosts int

	// HostIdleTTL is how long a host must be idle before it becomes eligible
	// for eviction once MaxTrackedHosts is reached. Only used when
	// MaxTrackedHosts > 0.
	// Default: 5m
	HostIdleTTL time.Duration
}

// DefaultConfig returns the default configuration.
func DefaultConfig() *Config {
	return &Config{
		UserAgent:            "Capacitor/1.0",
		InitialConcurrency:   100,
		MaxConcurrency:       100,
		MinConcurrency:       1,
		AcquireTimeout:       30 * time.Second,
		StateExpiry:          30 * time.Second,
		SignalHandlers:       nil, // No handlers = passthrough behavior
		EnableGOAWAYHandling: false,
		Transport:            nil,
	}
}

// withDefaults returns a new config with defaults applied for zero values.
func (c *Config) withDefaults() *Config {
	if c == nil {
		return DefaultConfig()
	}

	cfg := *c

	// Don't override empty UserAgent - it's intentionally optional
	if cfg.InitialConcurrency <= 0 {
		cfg.InitialConcurrency = 100
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 100
	}
	if cfg.MinConcurrency <= 0 {
		cfg.MinConcurrency = 1
	}
	if cfg.AcquireTimeout <= 0 {
		cfg.AcquireTimeout = 30 * time.Second
	}
	if cfg.StateExpiry <= 0 {
		cfg.StateExpiry = 30 * time.Second
	}
	if cfg.MaxTrackedHosts > 0 && cfg.HostIdleTTL <= 0 {
		cfg.HostIdleTTL = 5 * time.Minute
	}

	// Keep the bounds consistent and start within them, so MaxConcurrency is an
	// absolute ceiling even before any signal arrives.
	if cfg.MinConcurrency > cfg.MaxConcurrency {
		cfg.MinConcurrency = cfg.MaxConcurrency
	}
	if cfg.InitialConcurrency < cfg.MinConcurrency {
		cfg.InitialConcurrency = cfg.MinConcurrency
	}
	if cfg.InitialConcurrency > cfg.MaxConcurrency {
		cfg.InitialConcurrency = cfg.MaxConcurrency
	}
	// Don't set default handlers - nil means passthrough

	return &cfg
}

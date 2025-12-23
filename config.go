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
	// Default: 10
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
	// If nil, DefaultSignalHandlers() is used.
	// Handlers are processed in priority order.
	SignalHandlers []SignalHandler

	// EnableGOAWAYHandling enables tracking of HTTP/2 GOAWAY frames.
	// When enabled, GOAWAY frames trigger automatic backoff.
	// Default: true
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
	// Don't set default handlers - nil means passthrough

	return &cfg
}

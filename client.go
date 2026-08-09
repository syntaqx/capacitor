package capacitor

import (
	"net/http"
	"net/url"
)

// Client is an HTTP client that respects server capacity signaling.
// It embeds *http.Client, so it exposes the full http.Client interface
// (Do, Get, Head, Post, PostForm, and the CheckRedirect/Jar/Timeout fields)
// while routing every request through the capacity-aware transport.
//
// Client is safe for concurrent use by multiple goroutines.
type Client struct {
	*http.Client
	transport *Transport
}

// NewClient creates a new capacity-aware HTTP client from an explicit Config.
// If config is nil, default configuration is used.
//
// To wrap an existing *http.Client and preserve its settings (Timeout, Jar,
// CheckRedirect, Transport), use the Wrap builder instead.
func NewClient(config *Config) *Client {
	transport := NewTransport(config)

	return &Client{
		Client: &http.Client{
			Transport: transport,
		},
		transport: transport,
	}
}

// State returns the current capacity state for a URL or host key, or nil if
// the host is not yet tracked. A full URL is resolved through the configured
// KeyFunc, so it works even with custom (e.g. path-based) grouping.
func (c *Client) State(urlOrKey string) *State {
	key := urlOrKey
	if u, err := url.Parse(urlOrKey); err == nil && u.Scheme != "" && u.Host != "" {
		key = c.transport.hostKey(u)
	}
	return c.transport.State(key)
}

// Stats returns statistics for all known hosts.
func (c *Client) Stats() map[string]Stats {
	return c.transport.Stats()
}

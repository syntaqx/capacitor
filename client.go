package capacitor

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Client is an HTTP client that respects server capacity signaling.
// It wraps http.Client and provides the same interface.
//
// Client is safe for concurrent use by multiple goroutines.
type Client struct {
	*http.Client
	transport *Transport
}

// NewClient creates a new capacity-aware HTTP client.
// If config is nil, default configuration is used.
func NewClient(config *Config) *Client {
	transport := NewTransport(config)

	return &Client{
		Client: &http.Client{
			Transport: transport,
		},
		transport: transport,
	}
}

// WrapClient wraps an existing http.Client with capacity-aware transport.
// The existing client's transport is used as the base transport.
func WrapClient(client *http.Client, config *Config) *Client {
	if config == nil {
		config = DefaultConfig()
	}

	// Use existing transport as base
	config.Transport = client.Transport

	transport := NewTransport(config)

	// Clone client settings
	wrapped := &http.Client{
		Transport:     transport,
		CheckRedirect: client.CheckRedirect,
		Jar:           client.Jar,
		Timeout:       client.Timeout,
	}

	return &Client{
		Client:    wrapped,
		transport: transport,
	}
}

// GetState returns the current capacity state for a URL or host.
func (c *Client) GetState(urlOrHost string) *State {
	// Normalize to host key
	host := urlOrHost
	if strings.HasPrefix(urlOrHost, "http://") || strings.HasPrefix(urlOrHost, "https://") {
		if u, err := url.Parse(urlOrHost); err == nil {
			host = u.Scheme + "://" + u.Host
		}
	}
	return c.transport.GetState(host)
}

// GetStats returns statistics for all known hosts.
func (c *Client) GetStats() map[string]Stats {
	return c.transport.GetStats()
}

// Transport returns the underlying capacity-aware transport.
func (c *Client) Transport() *Transport {
	return c.transport
}

// Do sends an HTTP request and returns an HTTP response.
// This is the same as http.Client.Do but with capacity limiting.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	return c.Client.Do(req)
}

// Get issues a GET to the specified URL.
func (c *Client) Get(url string) (*http.Response, error) {
	return c.Client.Get(url)
}

// Head issues a HEAD to the specified URL.
func (c *Client) Head(url string) (*http.Response, error) {
	return c.Client.Head(url)
}

// Post issues a POST to the specified URL.
func (c *Client) Post(url, contentType string, body io.Reader) (*http.Response, error) {
	return c.Client.Post(url, contentType, body)
}

// PostForm issues a POST with form data.
func (c *Client) PostForm(url string, data url.Values) (*http.Response, error) {
	return c.Client.PostForm(url, data)
}

// DoWithContext sends an HTTP request with context.
func (c *Client) DoWithContext(ctx context.Context, req *http.Request) (*http.Response, error) {
	return c.Client.Do(req.WithContext(ctx))
}

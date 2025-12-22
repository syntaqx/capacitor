package capacitor

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Signal represents a rate limiting or capacity signal extracted from a response.
type Signal struct {
	// Source identifies where the signal came from (e.g., "capacity", "ratelimit", "http")
	Source string

	// Type categorizes the signal
	Type SignalType

	// SuggestedConcurrency is the recommended concurrency, if applicable
	SuggestedConcurrency int

	// RetryAfter indicates when to retry, if applicable
	RetryAfter time.Duration

	// BlockUntil indicates when requests should be blocked until
	BlockUntil time.Time

	// Remaining indicates remaining requests in the current window
	Remaining int

	// Limit indicates the total limit in the current window
	Limit int

	// Message provides additional context
	Message string

	// Raw contains the raw header values for debugging
	Raw map[string]string
}

// SignalType categorizes the type of signal received.
type SignalType string

const (
	// SignalTypeNone indicates no signal was detected
	SignalTypeNone SignalType = ""

	// SignalTypeCapacity indicates application-level capacity headers
	SignalTypeCapacity SignalType = "capacity"

	// SignalTypeRateLimit indicates rate limiting (429, Retry-After, etc.)
	SignalTypeRateLimit SignalType = "rate_limit"

	// SignalTypeBackoff indicates server is overloaded (503, GOAWAY)
	SignalTypeBackoff SignalType = "backoff"

	// SignalTypeBlock indicates requests should be blocked temporarily
	SignalTypeBlock SignalType = "block"
)

// SignalHandler processes HTTP responses and extracts signals.
type SignalHandler interface {
	// Name returns the handler name for logging/debugging
	Name() string

	// Priority returns the handler priority (lower = higher priority)
	Priority() int

	// Process examines the response and returns any detected signals.
	// Returns nil if no relevant signal was detected.
	Process(resp *http.Response) *Signal
}

// SignalAction represents what action to take based on signals.
type SignalAction struct {
	// AdjustConcurrency indicates concurrency should be changed
	AdjustConcurrency bool
	NewConcurrency    int

	// Block indicates requests should be blocked
	Block      bool
	BlockUntil time.Time
	RetryAfter time.Duration

	// Backoff indicates exponential backoff should be used
	Backoff bool

	// Signals contains all detected signals
	Signals []*Signal
}

// DefaultSignalHandlers returns the default set of signal handlers.
func DefaultSignalHandlers() []SignalHandler {
	return []SignalHandler{
		&HTTPStatusHandler{},
		&RateLimitHandler{},
		&CapacityHandler{},
	}
}

// ----------------------------------------------------------------------------
// HTTP Status Handler (429, 503, 420, Retry-After)
// ----------------------------------------------------------------------------

// HTTPStatusHandler handles HTTP status codes that indicate rate limiting or overload.
// This includes 429 Too Many Requests, 503 Service Unavailable, and 420 Enhance Your Calm.
type HTTPStatusHandler struct{}

func (h *HTTPStatusHandler) Name() string  { return "http_status" }
func (h *HTTPStatusHandler) Priority() int { return 10 }

func (h *HTTPStatusHandler) Process(resp *http.Response) *Signal {
	signal := &Signal{
		Source: "http",
		Raw:    make(map[string]string),
	}

	// Check for rate limit status codes
	switch resp.StatusCode {
	case http.StatusTooManyRequests: // 429
		signal.Type = SignalTypeRateLimit
		signal.Message = "Too Many Requests"
	case http.StatusServiceUnavailable: // 503
		signal.Type = SignalTypeBackoff
		signal.Message = "Service Unavailable"
	case 420: // "Enhance Your Calm" (used by Twitter and others)
		signal.Type = SignalTypeRateLimit
		signal.Message = "Enhance Your Calm"
	default:
		return nil
	}

	// Parse Retry-After header (case-insensitive via http.Header.Get)
	if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
		signal.Raw["Retry-After"] = retryAfter
		signal.RetryAfter = parseRetryAfter(retryAfter)
		if signal.RetryAfter > 0 {
			signal.BlockUntil = time.Now().Add(signal.RetryAfter)
		}
	}

	// Default retry after if not specified
	if signal.RetryAfter == 0 {
		switch resp.StatusCode {
		case http.StatusTooManyRequests, 420:
			signal.RetryAfter = 5 * time.Second
		case http.StatusServiceUnavailable:
			signal.RetryAfter = 10 * time.Second
		}
		signal.BlockUntil = time.Now().Add(signal.RetryAfter)
	}

	return signal
}

// ----------------------------------------------------------------------------
// RateLimit Handler (X-RateLimit-*, RateLimit-*, CF-RateLimit-*)
// ----------------------------------------------------------------------------

// RateLimitHandler handles common rate limit headers from various providers.
// HTTP headers are case-insensitive, so this single handler covers:
//   - X-RateLimit-Limit, X-RateLimit-Remaining, X-RateLimit-Reset (GitHub, Twitter, many others)
//   - RateLimit-Limit, RateLimit-Remaining, RateLimit-Reset (IETF draft standard)
//   - CF-RateLimit-* (Cloudflare)
//
// See:
//   - https://datatracker.ietf.org/doc/draft-ietf-httpapi-ratelimit-headers/
//   - https://docs.github.com/en/rest/overview/resources-in-the-rest-api#rate-limiting
type RateLimitHandler struct{}

func (h *RateLimitHandler) Name() string  { return "ratelimit" }
func (h *RateLimitHandler) Priority() int { return 20 }

func (h *RateLimitHandler) Process(resp *http.Response) *Signal {
	signal := &Signal{
		Source: "ratelimit",
		Raw:    make(map[string]string),
	}

	// Try each header variant - http.Header.Get is case-insensitive
	// We check multiple prefixes as different providers use different conventions

	// Check for limit header
	limit := h.getFirstHeader(resp, "X-RateLimit-Limit", "RateLimit-Limit", "CF-RateLimit-Limit")
	if limit != "" {
		signal.Raw["Limit"] = limit
		signal.Limit = parseRateLimitValue(limit)
	}

	// Check for remaining header
	remaining := h.getFirstHeader(resp, "X-RateLimit-Remaining", "RateLimit-Remaining", "CF-RateLimit-Remaining")
	if remaining != "" {
		signal.Raw["Remaining"] = remaining
		signal.Remaining, _ = strconv.Atoi(remaining)
	}

	// Check for reset header
	reset := h.getFirstHeader(resp, "X-RateLimit-Reset", "RateLimit-Reset", "CF-RateLimit-Reset")
	if reset != "" {
		signal.Raw["Reset"] = reset
		signal.BlockUntil, signal.RetryAfter = parseResetValue(reset)
	}

	// Check for additional headers (informational)
	if v := resp.Header.Get("X-RateLimit-Used"); v != "" {
		signal.Raw["Used"] = v
	}
	if v := resp.Header.Get("X-RateLimit-Resource"); v != "" {
		signal.Raw["Resource"] = v
	}
	if v := resp.Header.Get("RateLimit-Policy"); v != "" {
		signal.Raw["Policy"] = v
	}

	// If no rate limit headers found, return nil
	if len(signal.Raw) == 0 {
		return nil
	}

	// Determine signal type based on remaining quota
	if signal.Remaining <= 0 && signal.Limit > 0 {
		signal.Type = SignalTypeBlock
		signal.Message = "Rate limit exceeded"
	} else if signal.Limit > 0 && signal.Remaining < signal.Limit/10 {
		signal.Type = SignalTypeRateLimit
		signal.Message = "Rate limit approaching"
		signal.SuggestedConcurrency = max(1, signal.Remaining*10/signal.Limit)
	} else {
		signal.Type = SignalTypeCapacity
	}

	return signal
}

// getFirstHeader returns the first non-empty header value from the list of keys.
func (h *RateLimitHandler) getFirstHeader(resp *http.Response, keys ...string) string {
	for _, key := range keys {
		if v := resp.Header.Get(key); v != "" {
			return v
		}
	}
	return ""
}

// ----------------------------------------------------------------------------
// Capacity Handler (X-Capacity-* headers)
// ----------------------------------------------------------------------------

// CapacityHandler handles application-level X-Capacity-* headers.
// These are custom headers for fine-grained capacity signaling.
type CapacityHandler struct{}

func (h *CapacityHandler) Name() string  { return "capacity" }
func (h *CapacityHandler) Priority() int { return 100 }

func (h *CapacityHandler) Process(resp *http.Response) *Signal {
	signal := &Signal{
		Source: "capacity",
		Type:   SignalTypeCapacity,
		Raw:    make(map[string]string),
	}

	hasCapacityHeaders := false

	for _, key := range capacityHeaders {
		if v := resp.Header.Get(key); v != "" {
			signal.Raw[key] = v
			hasCapacityHeaders = true
		}
	}

	if !hasCapacityHeaders {
		return nil
	}

	// Extract suggested concurrency
	if v := signal.Raw["X-Capacity-Suggested-Concurrency"]; v != "" {
		signal.SuggestedConcurrency, _ = strconv.Atoi(v)
	}

	// Check status for potential rate limiting
	if status := signal.Raw["X-Capacity-Status"]; status != "" {
		signal.Message = status
		switch Status(status) {
		case StatusAtLimit:
			signal.Type = SignalTypeRateLimit
		case StatusDegraded:
			signal.Type = SignalTypeBackoff
		}
	}

	return signal
}

// ----------------------------------------------------------------------------
// GOAWAY Handler (HTTP/2 connection-level signal)
// ----------------------------------------------------------------------------

// GOAWAYHandler tracks HTTP/2 GOAWAY frames and connection resets.
// Note: GOAWAY is handled at the error level, not response level.
type GOAWAYHandler struct{}

func (h *GOAWAYHandler) Name() string  { return "goaway" }
func (h *GOAWAYHandler) Priority() int { return 5 }

func (h *GOAWAYHandler) Process(resp *http.Response) *Signal {
	// GOAWAY is handled via ProcessError, not response headers
	return nil
}

// ProcessError checks if an error indicates a GOAWAY or connection reset.
func (h *GOAWAYHandler) ProcessError(err error) *Signal {
	if err == nil {
		return nil
	}

	errStr := err.Error()

	// Check for GOAWAY indicators
	if strings.Contains(errStr, "GOAWAY") ||
		strings.Contains(errStr, "http2: server sent GOAWAY") {
		return &Signal{
			Source:     "http2",
			Type:       SignalTypeBackoff,
			Message:    "GOAWAY received",
			RetryAfter: 5 * time.Second,
			BlockUntil: time.Now().Add(5 * time.Second),
		}
	}

	// Check for connection reset (may indicate overload)
	if strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "ECONNRESET") {
		return &Signal{
			Source:     "connection",
			Type:       SignalTypeBackoff,
			Message:    "Connection reset",
			RetryAfter: 2 * time.Second,
		}
	}

	return nil
}

// ----------------------------------------------------------------------------
// Helper functions
// ----------------------------------------------------------------------------

// parseRetryAfter parses the Retry-After header value.
// It can be either a number of seconds or an HTTP-date.
func parseRetryAfter(value string) time.Duration {
	// Try parsing as seconds first
	if seconds, err := strconv.Atoi(value); err == nil {
		return time.Duration(seconds) * time.Second
	}

	// Try parsing as HTTP-date
	if t, err := time.Parse(time.RFC1123, value); err == nil {
		return time.Until(t)
	}

	return 0
}

// parseResetValue parses a reset header which can be:
//   - Unix timestamp (e.g., "1640000000")
//   - Seconds until reset (e.g., "60")
func parseResetValue(value string) (blockUntil time.Time, retryAfter time.Duration) {
	ts, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return
	}

	// Heuristic: if > 1 billion, it's a Unix timestamp; otherwise seconds
	if ts > 1000000000 {
		blockUntil = time.Unix(ts, 0)
		retryAfter = time.Until(blockUntil)
	} else {
		retryAfter = time.Duration(ts) * time.Second
		blockUntil = time.Now().Add(retryAfter)
	}

	return
}

// parseRateLimitValue parses a rate limit value which can be:
//   - Simple: "100"
//   - Complex: "100, 100;window=60" (IETF draft format)
func parseRateLimitValue(v string) int {
	// Take the first number before any comma, semicolon, or space
	for i, c := range v {
		if c == ',' || c == ';' || c == ' ' {
			v = v[:i]
			break
		}
	}
	n, _ := strconv.Atoi(v)
	return n
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

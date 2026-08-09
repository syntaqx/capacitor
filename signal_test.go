package capacitor

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func respWith(status int, headers map[string]string) *http.Response {
	h := make(http.Header)
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: status, Header: h}
}

func TestHTTPStatusHandler(t *testing.T) {
	h := &HTTPStatusHandler{}

	if got := h.Process(respWith(http.StatusOK, nil)); got != nil {
		t.Fatalf("200 should produce no signal, got %+v", got)
	}

	sig := h.Process(respWith(http.StatusTooManyRequests, map[string]string{"Retry-After": "7"}))
	if sig == nil || sig.Type != SignalTypeRateLimit {
		t.Fatalf("429 should be rate limit, got %+v", sig)
	}
	if sig.RetryAfter != 7*time.Second {
		t.Errorf("Retry-After = %v, want 7s", sig.RetryAfter)
	}
	if sig.SuggestedConcurrency != -1 {
		t.Errorf("SuggestedConcurrency = %d, want -1 (no suggestion)", sig.SuggestedConcurrency)
	}

	sig = h.Process(respWith(http.StatusServiceUnavailable, nil))
	if sig == nil || sig.Type != SignalTypeBackoff {
		t.Fatalf("503 should be backoff, got %+v", sig)
	}
	if sig.RetryAfter != 10*time.Second {
		t.Errorf("default 503 Retry-After = %v, want 10s", sig.RetryAfter)
	}

	sig = h.Process(respWith(420, nil))
	if sig == nil || sig.Type != SignalTypeRateLimit {
		t.Fatalf("420 should be rate limit, got %+v", sig)
	}
}

func TestRateLimitHandler_Healthy(t *testing.T) {
	h := &RateLimitHandler{}
	sig := h.Process(respWith(200, map[string]string{
		"X-RateLimit-Limit":     "5000",
		"X-RateLimit-Remaining": "4999",
		"X-RateLimit-Reset":     "9",
	}))
	if sig == nil {
		t.Fatal("expected a signal")
	}
	if sig.Type != SignalTypeCapacity {
		t.Errorf("healthy quota should be capacity type, got %s", sig.Type)
	}
	if sig.SuggestedConcurrency != -1 {
		t.Errorf("healthy quota must not suggest concurrency, got %d", sig.SuggestedConcurrency)
	}
	// The Reset header must NOT arm a block window while quota remains.
	if !sig.BlockUntil.IsZero() {
		t.Errorf("healthy quota with a Reset header must not block, got BlockUntil=%v", sig.BlockUntil)
	}
}

func TestRateLimitHandler_Approaching(t *testing.T) {
	h := &RateLimitHandler{}
	sig := h.Process(respWith(200, map[string]string{
		"X-RateLimit-Limit":     "100",
		"X-RateLimit-Remaining": "5",
	}))
	if sig == nil || sig.Type != SignalTypeRateLimit {
		t.Fatalf("expected rate limit signal, got %+v", sig)
	}
	if sig.SuggestedConcurrency < 1 {
		t.Errorf("expected a positive suggested concurrency, got %d", sig.SuggestedConcurrency)
	}
}

func TestRateLimitHandler_Exceeded(t *testing.T) {
	h := &RateLimitHandler{}
	sig := h.Process(respWith(200, map[string]string{
		"X-RateLimit-Limit":     "100",
		"X-RateLimit-Remaining": "0",
		"X-RateLimit-Reset":     "30",
	}))
	if sig == nil || sig.Type != SignalTypeBlock {
		t.Fatalf("expected block signal, got %+v", sig)
	}
	// Exhausted quota with a Reset should carry a block window.
	if sig.BlockUntil.IsZero() {
		t.Error("exhausted quota should arm a block window from Reset")
	}
}

func TestRateLimitHandler_None(t *testing.T) {
	h := &RateLimitHandler{}
	if sig := h.Process(respWith(200, nil)); sig != nil {
		t.Fatalf("no rate limit headers should yield nil, got %+v", sig)
	}
}

func TestCapacityHandler(t *testing.T) {
	h := &CapacityHandler{}
	if sig := h.Process(respWith(200, nil)); sig != nil {
		t.Fatalf("no capacity headers should yield nil, got %+v", sig)
	}

	sig := h.Process(respWith(200, map[string]string{
		"X-Capacity-Status":                "at_limit",
		"X-Capacity-Suggested-Concurrency": "3",
	}))
	if sig == nil || sig.Type != SignalTypeRateLimit {
		t.Fatalf("at_limit should map to rate limit, got %+v", sig)
	}
	if sig.SuggestedConcurrency != 3 {
		t.Errorf("SuggestedConcurrency = %d, want 3", sig.SuggestedConcurrency)
	}

	sig = h.Process(respWith(200, map[string]string{"X-Capacity-Status": "degraded"}))
	if sig == nil || sig.Type != SignalTypeBackoff {
		t.Fatalf("degraded should map to backoff, got %+v", sig)
	}
	if sig.SuggestedConcurrency != -1 {
		t.Errorf("no suggested header should leave -1, got %d", sig.SuggestedConcurrency)
	}
}

func TestGOAWAYHandler_ProcessError(t *testing.T) {
	h := &GOAWAYHandler{}
	if sig := h.ProcessError(nil); sig != nil {
		t.Fatal("nil error should yield nil signal")
	}
	if sig := h.Process(respWith(200, nil)); sig != nil {
		t.Fatal("Process should always yield nil")
	}

	sig := h.ProcessError(errString("http2: server sent GOAWAY and closed the connection"))
	if sig == nil || sig.Type != SignalTypeBackoff {
		t.Fatalf("GOAWAY should yield backoff, got %+v", sig)
	}

	sig = h.ProcessError(errString("read: connection reset by peer"))
	if sig == nil || sig.Type != SignalTypeBackoff {
		t.Fatalf("connection reset should yield backoff, got %+v", sig)
	}
	if sig.BlockUntil.IsZero() {
		t.Error("connection reset backoff should arm a block window")
	}

	if sig := h.ProcessError(errString("some other error")); sig != nil {
		t.Fatalf("unrelated error should yield nil, got %+v", sig)
	}
}

func TestParseRetryAfter(t *testing.T) {
	if d := parseRetryAfter("5"); d != 5*time.Second {
		t.Errorf("seconds parse = %v, want 5s", d)
	}
	future := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	if d := parseRetryAfter(future); d <= 0 {
		t.Errorf("http-date parse should be positive, got %v", d)
	}
	if d := parseRetryAfter("garbage"); d != 0 {
		t.Errorf("garbage should be 0, got %v", d)
	}
}

func TestParseResetValue(t *testing.T) {
	// Unix timestamp in the future
	ts := time.Now().Add(time.Hour).Unix()
	until, retry := parseResetValue(itoa(ts))
	if retry <= 0 || until.Before(time.Now()) {
		t.Errorf("timestamp reset parse failed: until=%v retry=%v", until, retry)
	}

	// Seconds-until form
	_, retry = parseResetValue("60")
	if retry != 60*time.Second {
		t.Errorf("seconds reset = %v, want 60s", retry)
	}

	if _, retry := parseResetValue("nope"); retry != 0 {
		t.Errorf("invalid reset should be 0, got %v", retry)
	}
}

func TestParseRateLimitValue(t *testing.T) {
	cases := map[string]int{
		"100":                100,
		"100, 100;window=60": 100,
		"250;window=60":      250,
		"":                   0,
	}
	for in, want := range cases {
		if got := parseRateLimitValue(in); got != want {
			t.Errorf("parseRateLimitValue(%q) = %d, want %d", in, got, want)
		}
	}
}

// errString is a tiny error whose message can be matched by the GOAWAY handler.
type errString string

func (e errString) Error() string { return string(e) }

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

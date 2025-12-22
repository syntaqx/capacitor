//go:build integration

package capacitor_test

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/syntaqx/capacitor"
)

// Integration tests for testing against a real API.
//
// Run with:
//   go test -tags=integration -v
//   CAPACITOR_TEST_URL=https://api.example.com go test -tags=integration -v
//
// Environment variables:
//   CAPACITOR_TEST_URL     - Optional. The base URL to test against (default: https://syntaqx.com)
//   CAPACITOR_TEST_TIMEOUT - Optional. Request timeout (default: 30s).

const defaultTestURL = "https://syntaqx.com"

func getTestURL(t *testing.T) string {
	if url := os.Getenv("CAPACITOR_TEST_URL"); url != "" {
		return url
	}
	return defaultTestURL
}

func getTestTimeout() time.Duration {
	if timeout := os.Getenv("CAPACITOR_TEST_TIMEOUT"); timeout != "" {
		if d, err := time.ParseDuration(timeout); err == nil {
			return d
		}
	}
	return 30 * time.Second
}

func getTestClient(t *testing.T) *capacitor.Client {
	// Create a base HTTP client with custom settings
	baseClient := &http.Client{
		Timeout: getTestTimeout(),
	}

	// Wrap it with capacitor and add handlers
	return capacitor.Wrap(baseClient).
		WithConcurrency(10, 1, 50).
		WithDefaults(). // HTTP status codes + rate limit headers
		OnSignal(func(host string, signal *capacitor.Signal) {
			t.Logf("Signal from %s [%s]: type=%s, remaining=%d, limit=%d, message=%s",
				host, signal.Source, signal.Type, signal.Remaining, signal.Limit, signal.Message)
		}).
		OnStateChange(func(host string, state *capacitor.State) {
			t.Logf("State change for %s: concurrency=%d, status=%s",
				host, state.CurrentConcurrency, state.Status)
		}).
		Build()
}

func TestIntegration_BasicRequest(t *testing.T) {
	url := getTestURL(t)
	client := getTestClient(t)

	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	t.Logf("Response status: %d", resp.StatusCode)

	// Log all rate limit related headers
	rateLimitHeaders := []string{
		// Standard
		"RateLimit-Limit", "RateLimit-Remaining", "RateLimit-Reset", "RateLimit-Policy",
		// GitHub style
		"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset", "X-RateLimit-Used", "X-RateLimit-Resource",
		// Twitter style
		"x-rate-limit-limit", "x-rate-limit-remaining", "x-rate-limit-reset",
		// Cloudflare
		"CF-RateLimit-Limit", "CF-RateLimit-Remaining", "CF-RateLimit-Reset",
		// Capacity headers
		"X-Capacity-Status", "X-Capacity-Suggested-Concurrency", "X-Capacity-Tasks-Running",
		"X-Capacity-Tasks-Desired", "X-Capacity-Worker-Load-Factor", "X-Capacity-Latency-P99",
		// Common
		"Retry-After",
	}

	for _, header := range rateLimitHeaders {
		if v := resp.Header.Get(header); v != "" {
			t.Logf("%s: %s", header, v)
		}
	}

	// Check state was captured
	state := client.GetState(url)
	if state != nil {
		t.Logf("Captured state - Status: %s, Concurrency: %d, Blocked: %v",
			state.Status, state.CurrentConcurrency, state.IsBlocked())
	}
}

func TestIntegration_ConcurrentRequests(t *testing.T) {
	url := getTestURL(t)
	client := getTestClient(t)

	// Make several concurrent requests
	const numRequests = 10
	results := make(chan error, numRequests)

	for i := 0; i < numRequests; i++ {
		go func(n int) {
			resp, err := client.Get(url)
			if err != nil {
				results <- err
				return
			}
			resp.Body.Close()
			t.Logf("Request %d completed with status %d", n, resp.StatusCode)
			results <- nil
		}(i)
	}

	// Collect results
	var errors int
	for i := 0; i < numRequests; i++ {
		if err := <-results; err != nil {
			t.Logf("Request error: %v", err)
			errors++
		}
	}

	t.Logf("Completed %d/%d requests successfully", numRequests-errors, numRequests)

	// Log final state
	state := client.GetState(url)
	if state != nil {
		t.Logf("Final state - Status: %s, Concurrency: %d, Blocked: %v",
			state.Status, state.CurrentConcurrency, state.IsBlocked())
		if state.IsBlocked() {
			t.Logf("  Blocked until: %v", state.BlockedUntil)
		}
	}

	stats := client.GetStats()
	for host, s := range stats {
		t.Logf("Stats for %s: InUse=%d, Available=%d, Waiting=%d",
			host, s.InUse, s.Available, s.Waiting)
	}
}

func TestIntegration_AdaptiveBehavior(t *testing.T) {
	url := getTestURL(t)
	client := getTestClient(t)

	// Make requests in waves to observe adaptation
	for wave := 0; wave < 3; wave++ {
		t.Logf("Wave %d starting", wave+1)

		for i := 0; i < 5; i++ {
			resp, err := client.Get(url)
			if err != nil {
				t.Logf("Request error: %v", err)
				continue
			}
			resp.Body.Close()
		}

		state := client.GetState(url)
		if state != nil {
			t.Logf("After wave %d - Concurrency: %d, Status: %s, Blocked: %v",
				wave+1, state.CurrentConcurrency, state.Status, state.IsBlocked())
		}

		time.Sleep(100 * time.Millisecond)
	}
}

func TestIntegration_RateLimitRecovery(t *testing.T) {
	url := getTestURL(t)
	client := getTestClient(t)

	t.Log("Making requests until rate limited or 20 requests...")

	for i := 0; i < 20; i++ {
		state := client.GetState(url)
		if state != nil && state.IsBlocked() {
			t.Logf("Request %d: Blocked until %v, waiting...", i, state.BlockedUntil)
			time.Sleep(time.Until(state.BlockedUntil) + 100*time.Millisecond)
		}

		resp, err := client.Get(url)
		if err != nil {
			t.Logf("Request %d error: %v", i, err)
			continue
		}

		t.Logf("Request %d: status=%d", i, resp.StatusCode)
		resp.Body.Close()

		// If we got rate limited, log it
		if resp.StatusCode == 429 {
			t.Logf("Rate limited at request %d", i)
			state := client.GetState(url)
			if state != nil {
				t.Logf("  Blocked until: %v", state.BlockedUntil)
			}
		}

		time.Sleep(50 * time.Millisecond)
	}
}

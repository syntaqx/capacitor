// Example demonstrates a fully configured capacitor client.
//
// Run with: go run ./example
package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/syntaqx/capacitor"
)

func main() {
	// Create a base HTTP client with your preferred settings
	baseClient := &http.Client{
		Timeout: 30 * time.Second,
	}

	// Wrap it with capacitor and configure handlers
	initialConcurrency := 10
	client := capacitor.Wrap(baseClient).
		WithConcurrency(initialConcurrency, 1, 100). // initial=10, min=1, max=100
		WithRateLimitHeaders().                      // X-RateLimit-*, RateLimit-*, CF-RateLimit-*
		WithHTTPStatusHandling().                    // 429, 503, 420 + Retry-After
		WithCapacityHeaders().                       // X-Capacity-* application headers
		OnStateChange(func(host string, state *capacitor.State) {
			log.Printf("[state] %s: status=%s concurrency=%d blocked_until=%v",
				host, state.Status, state.CurrentConcurrency, state.BlockedUntil)
		}).
		OnSignal(func(host string, signal *capacitor.Signal) {
			log.Printf("[signal] %s: source=%s remaining=%d block_until=%v",
				host, signal.Source, signal.Remaining, signal.BlockUntil)
		}).
		Build()

	// Target API (change this to your endpoint)
	healthURL := "https://api.example.com/health"

	fmt.Println("=== Capacitor Example ===")
	fmt.Printf("Target: %s\n", healthURL)
	fmt.Println()

	// Probe the health endpoint first to learn server capacity
	fmt.Println("Probing health endpoint to discover capacity...")
	resp, err := client.Get(healthURL)
	if err != nil {
		log.Printf("Health check failed: %v", err)
	} else {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		fmt.Printf("Health check: %d\n", resp.StatusCode)
		fmt.Printf("Body: %s\n", truncate(string(body), 200))
	}

	// Show capacity state learned from health check
	state := client.GetState(healthURL)
	fmt.Printf("Discovered capacity: status=%s, concurrency=%d\n",
		state.Status, state.CurrentConcurrency)

	// Check if we got real capacity from backend or using defaults
	concurrency := state.CurrentConcurrency
	if concurrency == 0 || concurrency == initialConcurrency {
		fmt.Println("⚠️  Using DEFAULT concurrency (no capacity headers received from backend)")
		if concurrency == 0 {
			concurrency = 5
		}
	} else {
		fmt.Printf("✅ Using BACKEND capacity: %d (received from X-Capacity-Suggested-Concurrency)\n", concurrency)
	}
	fmt.Println()

	// Make concurrent requests using the server-suggested concurrency
	fmt.Printf("Making %d concurrent requests...\n", concurrency)
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			reqStart := time.Now()
			resp, err := client.Get(healthURL)
			if err != nil {
				log.Printf("Request %d failed: %v", n, err)
				return
			}
			resp.Body.Close()
			fmt.Printf("Request %d: status=%d (took %v)\n", n, resp.StatusCode, time.Since(reqStart).Round(time.Millisecond))
		}(i)
	}
	wg.Wait()
	fmt.Printf("All %d requests completed in %v (parallel)\n", concurrency, time.Since(start).Round(time.Millisecond))
	fmt.Println()

	// Show final stats
	fmt.Println("=== Final Stats ===")
	for host, stats := range client.GetStats() {
		fmt.Printf("%s: in_use=%d, available=%d, waiting=%d\n",
			host, stats.InUse, stats.Available, stats.Waiting)
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

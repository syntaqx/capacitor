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
	client := capacitor.Wrap(baseClient).
		WithConcurrency(10, 1, 100). // initial=10, min=1, max=100
		WithRateLimitHeaders().      // X-RateLimit-*, RateLimit-*, CF-RateLimit-*
		WithHTTPStatusHandling().    // 429, 503, 420 + Retry-After
		WithCapacityHeaders().       // X-Capacity-* application headers
		OnStateChange(func(host string, state *capacitor.State) {
			log.Printf("[state] %s: status=%s concurrency=%d blocked_until=%v",
				host, state.Status, state.CurrentConcurrency, state.BlockedUntil)
		}).
		OnSignal(func(host string, signal *capacitor.Signal) {
			log.Printf("[signal] %s: source=%s remaining=%d block_until=%v",
				host, signal.Source, signal.Remaining, signal.BlockUntil)
		}).
		Build()

	// Target API
	apiURL := "https://api.syntaqx.com"

	fmt.Println("=== Capacitor Example ===")
	fmt.Printf("Target: %s\n", apiURL)
	fmt.Println()

	// Make a single request first
	fmt.Println("Making initial request...")
	resp, err := client.Get(apiURL)
	if err != nil {
		log.Printf("Request failed: %v", err)
	} else {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		fmt.Printf("Status: %d\n", resp.StatusCode)
		fmt.Printf("Body: %s\n", truncate(string(body), 200))
	}
	fmt.Println()

	// Show current state
	state := client.GetState(apiURL)
	fmt.Printf("Current state: status=%s, concurrency=%d, blocked_until=%v\n",
		state.Status, state.CurrentConcurrency, state.BlockedUntil)
	fmt.Println()

	// Make concurrent requests
	fmt.Println("Making 5 concurrent requests...")
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			resp, err := client.Get(apiURL)
			if err != nil {
				log.Printf("Request %d failed: %v", n, err)
				return
			}
			resp.Body.Close()
			fmt.Printf("Request %d: status=%d\n", n, resp.StatusCode)
		}(i)
	}
	wg.Wait()
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

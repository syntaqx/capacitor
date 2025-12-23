// Example demonstrates a fully configured capacitor client.
//
// Run with: go run ./example
// Run with custom duration: go run ./example -duration=10m
// Run with custom URL: go run ./example -url=https://api.example.com/health
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/syntaqx/capacitor"
)

func main() {
	// Parse command-line flags
	duration := flag.Duration("duration", 5*time.Minute, "How long to run the test")
	url := flag.String("url", "https://api.example.com/health", "Target URL to hit")
	interval := flag.Duration("interval", 1*time.Second, "Interval between request batches")
	auth := flag.String("auth", "", "Authorization header value (e.g., Bearer token or API key)")
	flag.Parse()

	// Create a base HTTP client with your preferred settings
	baseClient := &http.Client{
		Timeout: 30 * time.Second,
	}

	// Track capacity changes
	var lastConcurrency atomic.Int32
	var lastSuggested atomic.Int32
	var lastStatus atomic.Value
	lastStatus.Store("")

	// Wrap it with capacitor and configure handlers
	initialConcurrency := 10
	client := capacitor.Wrap(baseClient).
		WithUserAgent("Capacitor/1.0").               // Required for capacity headers
		WithConcurrency(initialConcurrency, 1, 2000). // initial=10, min=1, max=2000
		WithRateLimitHeaders().                       // X-RateLimit-*, RateLimit-*, CF-RateLimit-*
		WithHTTPStatusHandling().                     // 429, 503, 420 + Retry-After
		WithCapacityHeaders().                        // X-Capacity-* application headers
		OnStateChange(func(host string, state *capacitor.State) {
			oldConcurrency := lastConcurrency.Swap(int32(state.CurrentConcurrency))
			oldSuggested := lastSuggested.Swap(int32(state.SuggestedConcurrency))
			oldStatus := lastStatus.Swap(string(state.Status)).(string)

			// Log meaningful changes
			if oldSuggested != 0 && oldSuggested != int32(state.SuggestedConcurrency) {
				fmt.Printf("\n🔄 SUGGESTED CONCURRENCY CHANGED: %d → %d\n", oldSuggested, state.SuggestedConcurrency)
			}
			if oldConcurrency != 0 && oldConcurrency != int32(state.CurrentConcurrency) {
				fmt.Printf("   ACTUAL CONCURRENCY: %d → %d\n", oldConcurrency, state.CurrentConcurrency)
			}
			if oldStatus != "" && oldStatus != string(state.Status) {
				fmt.Printf("\n📊 STATUS CHANGED: %s → %s\n", oldStatus, state.Status)
			}
		}).
		Build()

	healthURL := *url
	authHeader := *auth

	// Helper to make requests with auth
	doRequest := func() (*http.Response, error) {
		req, err := http.NewRequest("GET", healthURL, nil)
		if err != nil {
			return nil, err
		}
		if authHeader != "" {
			req.Header.Set("Authorization", authHeader)
		}
		return client.Do(req)
	}

	// Print header
	fmt.Println()
	fmt.Println(strings.Repeat("═", 60))
	fmt.Println("  CAPACITOR - Adaptive HTTP Client Demo")
	fmt.Println(strings.Repeat("═", 60))
	fmt.Printf("  Target:   %s\n", healthURL)
	fmt.Printf("  Duration: %v\n", *duration)
	fmt.Printf("  Interval: %v between batches\n", *interval)
	fmt.Println(strings.Repeat("─", 60))
	fmt.Println()

	// Setup context with timeout and signal handling
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	// Handle Ctrl+C gracefully
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n\n⏹️  Gracefully stopping...")
		cancel()
	}()

	// Initial probe
	fmt.Print("🔍 Probing endpoint for initial capacity... ")
	resp, err := doRequest()
	if err != nil {
		fmt.Printf("❌ FAILED: %v\n", err)
		return
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	fmt.Printf("✓ %d\n", resp.StatusCode)

	state := client.GetState(healthURL)
	lastConcurrency.Store(int32(state.CurrentConcurrency))
	lastSuggested.Store(int32(state.SuggestedConcurrency))
	lastStatus.Store(string(state.Status))

	fmt.Println()
	fmt.Println(strings.Repeat("─", 75))
	if state.SuggestedConcurrency == 0 {
		fmt.Printf("  ⚠️  No capacity headers received - using default: %d\n", initialConcurrency)
	} else {
		fmt.Printf("  ✅ Backend suggested concurrency: %d\n", state.SuggestedConcurrency)
		fmt.Printf("  📈 Actual concurrency (clamped): %d\n", state.CurrentConcurrency)
	}
	if state.Status != "" {
		fmt.Printf("  📊 Server status: %s\n", state.Status)
	}
	fmt.Printf("  📦 Response: %s\n", truncate(string(body), 50))
	fmt.Println(strings.Repeat("─", 75))
	fmt.Println()

	// Stats tracking
	var totalRequests, totalSuccess, totalFail int64
	startTime := time.Now()
	batch := 0

	// Print header for batch output
	fmt.Println("  BATCH │ REQS │  OK  │ FAIL │ SUGGESTED │  ACTUAL │ STATUS")
	fmt.Println(strings.Repeat("─", 75))

	// Continuous loop
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			goto done
		case <-ticker.C:
			batch++
			state := client.GetState(healthURL)

			// Get available slots from the semaphore, not just the configured concurrency.
			// This is important: if capacity drops mid-run, we don't wastefully spawn
			// goroutines that will just block waiting for slots. The transport's semaphore
			// is the source of truth for actual available capacity.
			stats := client.GetStats()
			var available int
			for _, s := range stats {
				available = s.Available
				break // We only have one host in this example
			}
			if available == 0 {
				// No slots available yet (first request) or all in use
				available = state.CurrentConcurrency
				if available == 0 {
					available = 5
				}
			}

			// Make concurrent requests - only spawn as many as we have slots for
			var wg sync.WaitGroup
			var success, fail int64

			batchStart := time.Now()
			for i := 0; i < available; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					resp, err := doRequest()
					if err != nil {
						atomic.AddInt64(&fail, 1)
						return
					}
					resp.Body.Close()
					atomic.AddInt64(&success, 1)
				}()
			}
			wg.Wait()
			batchDuration := time.Since(batchStart)

			totalRequests += int64(available)
			totalSuccess += success
			totalFail += fail

			// Status indicator
			statusStr := string(state.Status)
			if statusStr == "" {
				statusStr = "-"
			}

			// Color-coded output based on success rate
			successRate := float64(success) / float64(available) * 100
			var indicator string
			switch {
			case successRate == 100:
				indicator = "✓"
			case successRate >= 80:
				indicator = "○"
			case successRate >= 50:
				indicator = "△"
			default:
				indicator = "✗"
			}

			fmt.Printf("  %s %3d │ %4d │ %4d │ %4d │ %9d │ %7d │ %s  (%v)\n",
				indicator, batch, available, success, fail, state.SuggestedConcurrency, state.CurrentConcurrency, statusStr, batchDuration.Round(time.Millisecond))
		}
	}

done:
	elapsed := time.Since(startTime)

	fmt.Println()
	fmt.Println(strings.Repeat("═", 60))
	fmt.Println("  FINAL SUMMARY")
	fmt.Println(strings.Repeat("═", 60))
	fmt.Printf("  Runtime:        %v\n", elapsed.Round(time.Second))
	fmt.Printf("  Total batches:  %d\n", batch)
	fmt.Printf("  Total requests: %d\n", totalRequests)
	fmt.Printf("  Succeeded:      %d (%.1f%%)\n", totalSuccess, float64(totalSuccess)/float64(totalRequests)*100)
	fmt.Printf("  Failed:         %d (%.1f%%)\n", totalFail, float64(totalFail)/float64(totalRequests)*100)
	fmt.Printf("  Throughput:     %.1f req/sec\n", float64(totalRequests)/elapsed.Seconds())
	fmt.Println(strings.Repeat("─", 60))

	for host, stats := range client.GetStats() {
		fmt.Printf("  %s\n", host)
		fmt.Printf("    In-use: %d | Available: %d | Waiting: %d\n",
			stats.InUse, stats.Available, stats.Waiting)
	}
	fmt.Println(strings.Repeat("═", 60))
	fmt.Println()
}

func truncate(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

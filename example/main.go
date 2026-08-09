// Command example demonstrates capacitor end to end: an adaptive client driving
// load against a fake backend that signals its capacity through HTTP headers.
//
// Run everything together (default) and watch the client track the server:
//
//	go run ./example
//
// Compare against a naive client that ignores the signals and overwhelms the
// server (note the 503/429 columns and the peak-concurrency summary):
//
//	go run ./example -baseline
//
// Or run the two sides separately:
//
//	go run ./example -mode=server -addr=:8080
//	go run ./example -mode=client -url=http://localhost:8080
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	mode := flag.String("mode", "both", "both | server | client")
	addr := flag.String("addr", ":8080", "listen address (server mode)")
	url := flag.String("url", "", "target URL (client mode)")
	duration := flag.Duration("duration", 24*time.Second, "how long the client runs")
	interval := flag.Duration("interval", 300*time.Millisecond, "batch interval")
	initial := flag.Int("initial", 8, "initial client concurrency")
	maxConc := flag.Int("max", 64, "max client concurrency (baseline fires this every tick)")
	baseline := flag.Bool("baseline", false, "use a naive fixed-concurrency client instead of capacitor")
	flag.Parse()

	cfg := demoConfig{
		url:      *url,
		duration: *duration,
		interval: *interval,
		initial:  *initial,
		maxConc:  *maxConc,
		baseline: *baseline,
	}

	switch *mode {
	case "server":
		runServerMode(*addr)
	case "client":
		if cfg.url == "" {
			fmt.Fprintln(os.Stderr, "client mode requires -url")
			os.Exit(2)
		}
		ctx, stop := signalContext()
		defer stop()
		t := runClient(ctx, cfg)
		printClientSummary(t)
	case "both":
		runBoth(cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q (want both|server|client)\n", *mode)
		os.Exit(2)
	}
}

func runBoth(cfg demoConfig) {
	srv := newDemoServer()
	ts := httptest.NewServer(http.HandlerFunc(srv.handler))
	defer ts.Close()
	cfg.url = ts.URL

	printIntro(cfg)

	ctx, stop := signalContext()
	defer stop()

	t := runClient(ctx, cfg)

	printClientSummary(t)
	printServerSummary(srv)
}

func runServerMode(addr string) {
	srv := newDemoServer()
	httpSrv := &http.Server{Addr: addr, Handler: http.HandlerFunc(srv.handler)}

	ctx, stop := signalContext()
	defer stop()

	go func() {
		fmt.Printf("capacitor demo server listening on %s\n", addr)
		fmt.Println("  point a client at it:  go run ./example -mode=client -url=http://localhost" + addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, "server error:", err)
			stop()
		}
	}()

	// Log the server's live view once a second so you can watch load and capacity.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = httpSrv.Shutdown(shutdownCtx)
			fmt.Println("\nserver stopped")
			printServerSummary(srv)
			return
		case <-ticker.C:
			capNow := srv.capacityAt(time.Since(srv.start))
			fmt.Printf("[server] capacity=%-3d inflight=%-3d peak=%-3d total=%-6d ok=%-6d shed=%d\n",
				capNow, srv.inFlight.Load(), srv.peakInFlight.Load(),
				srv.total.Load(), srv.ok.Load(), srv.shed.Load())
		}
	}
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func printIntro(cfg demoConfig) {
	kind := "capacitor (adaptive)"
	if cfg.baseline {
		kind = "baseline (naive, ignores signals)"
	}
	bar := strings.Repeat("═", 80)
	fmt.Println("\n" + bar)
	fmt.Println("  CAPACITOR - adaptive HTTP client demo")
	fmt.Println(bar)
	fmt.Printf("  client:   %s\n", kind)
	fmt.Printf("  duration: %s   interval: %s   max concurrency: %d\n", cfg.duration, cfg.interval, cfg.maxConc)
	fmt.Println("  The backend's true capacity changes over time and is advertised via")
	fmt.Println("  X-Capacity-* and X-RateLimit-* headers. Watch CLIENT-CONC track SUGGEST.")
	fmt.Println(strings.Repeat("─", 80))
}

func printClientSummary(t *tallies) {
	sent := t.sent.Load()
	ok := t.ok.Load()
	bad := t.http503.Load() + t.http429.Load() + t.blocked.Load() + t.errs.Load()
	rate := 0.0
	if sent > 0 {
		rate = float64(ok) / float64(sent) * 100
	}

	fmt.Println("\n" + strings.Repeat("─", 80))
	fmt.Println("  CLIENT SUMMARY")
	fmt.Printf("  requests sent: %d   ok: %d (%.1f%%)   throttled/failed: %d\n", sent, ok, rate, bad)
	fmt.Printf("  breakdown: 503=%d  429=%d  blocked-locally=%d  errors=%d\n",
		t.http503.Load(), t.http429.Load(), t.blocked.Load(), t.errs.Load())
}

func printServerSummary(s *demoServer) {
	total := s.total.Load()
	shed := s.shed.Load()
	shedRate := 0.0
	if total > 0 {
		shedRate = float64(shed) / float64(total) * 100
	}
	fmt.Println("\n  SERVER SUMMARY (the backend's point of view)")
	fmt.Printf("  requests handled: %d   served ok: %d   shed (429/503): %d (%.1f%%)\n",
		total, s.ok.Load(), shed, shedRate)
	fmt.Printf("  peak simultaneous in-flight the server had to absorb: %d\n", s.peakInFlight.Load())
	fmt.Println(strings.Repeat("═", 80))
}

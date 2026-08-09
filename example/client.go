package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/syntaqx/capacitor"
)

type demoConfig struct {
	url      string
	duration time.Duration
	interval time.Duration
	initial  int
	maxConc  int
	baseline bool
}

// serverSignals holds the most recent capacity signals observed from responses,
// so the dashboard can render the server's point of view.
type serverSignals struct {
	mu        sync.Mutex
	status    string
	suggested string
	load      string
}

func (s *serverSignals) update(h http.Header) {
	if h == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v := h.Get("X-Capacity-Status"); v != "" {
		s.status = v
	}
	if v := h.Get("X-Capacity-Suggested-Concurrency"); v != "" {
		s.suggested = v
	}
	if v := h.Get("X-Capacity-Worker-Load-Factor"); v != "" {
		s.load = v
	}
}

func (s *serverSignals) snapshot() (status, suggested, load string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return orDash(s.status), orDash(s.suggested), orDash(s.load)
}

type tallies struct {
	sent, ok, http503, http429, blocked, errs atomic.Int64
}

// runClient drives load against cfg.url and renders a live dashboard. When
// cfg.baseline is set it uses a naive fixed-concurrency http.Client; otherwise
// it uses an adaptive capacitor client that honors the server's signals.
func runClient(parent context.Context, cfg demoConfig) *tallies {
	ctx, cancel := context.WithTimeout(parent, cfg.duration)
	defer cancel()

	var sig serverSignals
	t := &tallies{}

	doReq, concFn := requestFuncs(cfg)

	fmt.Println()
	fmt.Println("  TIME │ SENT │  OK  │ 503 │ 429 │ BLK │ SRV-STATUS │ SUGGEST │ LOAD │ CLIENT-CONC")
	fmt.Println("  " + strings.Repeat("─", 78))

	start := time.Now()
	ticker := time.NewTicker(cfg.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return t
		case <-ticker.C:
			n := concFn()
			var batchOK, batch503, batch429, batchBlk, batchErr int64

			var wg sync.WaitGroup
			for range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					status, header, err := doReq()
					sig.update(header)
					t.sent.Add(1)
					switch {
					case errors.Is(err, capacitor.ErrBlocked):
						atomic.AddInt64(&batchBlk, 1)
						t.blocked.Add(1)
					case err != nil:
						atomic.AddInt64(&batchErr, 1)
						t.errs.Add(1)
					case status == http.StatusServiceUnavailable:
						atomic.AddInt64(&batch503, 1)
						t.http503.Add(1)
					case status == http.StatusTooManyRequests:
						atomic.AddInt64(&batch429, 1)
						t.http429.Add(1)
					default:
						atomic.AddInt64(&batchOK, 1)
						t.ok.Add(1)
					}
				}()
			}
			wg.Wait()

			status, suggested, load := sig.snapshot()
			fmt.Printf("  %4s │ %4d │ %4d │ %3d │ %3d │ %3d │ %-10s │ %7s │ %4s │ %d\n",
				time.Since(start).Round(time.Second),
				n, batchOK, batch503, batch429, batchBlk,
				status, suggested, load, n)
		}
	}
}

// requestFuncs returns a request executor and a per-tick concurrency function
// for either the adaptive or the baseline client.
func requestFuncs(cfg demoConfig) (func() (int, http.Header, error), func() int) {
	base := &http.Client{Timeout: 15 * time.Second}

	if cfg.baseline {
		do := func() (int, http.Header, error) {
			resp, err := base.Get(cfg.url)
			if err != nil {
				return 0, nil, err
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			return resp.StatusCode, resp.Header, nil
		}
		// A naive client just fires at its ceiling every tick, ignoring signals.
		return do, func() int { return cfg.maxConc }
	}

	client := capacitor.Wrap(base).
		WithUserAgent("capacitor-demo/1.0").
		WithConcurrency(cfg.initial, 1, cfg.maxConc).
		WithAll().
		Build()

	do := func() (int, http.Header, error) {
		resp, err := client.Get(cfg.url)
		if err != nil {
			return 0, nil, err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, resp.Header, nil
	}
	// Push exactly at the current client-side limit; capacitor adjusts it from
	// the server's signals, so we track capacity instead of overrunning it.
	conc := func() int {
		if st := client.State(cfg.url); st != nil && st.CurrentConcurrency > 0 {
			return st.CurrentConcurrency
		}
		return cfg.initial
	}
	return do, conc
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

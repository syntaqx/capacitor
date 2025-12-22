package capacitor_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/syntaqx/capacitor"
)

func TestClient_Basic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return capacity headers
		w.Header().Set("X-Capacity-Status", "healthy")
		w.Header().Set("X-Capacity-Tasks-Running", "10")
		w.Header().Set("X-Capacity-Suggested-Concurrency", "50")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := capacitor.Wrap(nil).WithCapacityHeaders().Build()
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	// Check state was updated
	state := client.GetState(server.URL)
	if state == nil {
		t.Fatal("expected state to be set")
	}
	if state.Status != capacitor.StatusHealthy {
		t.Errorf("expected status healthy, got %s", state.Status)
	}
	if state.SuggestedConcurrency != 50 {
		t.Errorf("expected suggested concurrency 50, got %d", state.SuggestedConcurrency)
	}
}

func TestClient_ConcurrencyLimit(t *testing.T) {
	var concurrent int64
	var maxConcurrent int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt64(&concurrent, 1)
		defer atomic.AddInt64(&concurrent, -1)

		// Track max concurrent
		for {
			old := atomic.LoadInt64(&maxConcurrent)
			if cur <= old || atomic.CompareAndSwapInt64(&maxConcurrent, old, cur) {
				break
			}
		}

		// Simulate work
		time.Sleep(50 * time.Millisecond)

		// Return low concurrency suggestion
		w.Header().Set("X-Capacity-Status", "healthy")
		w.Header().Set("X-Capacity-Suggested-Concurrency", "5")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := capacitor.NewClient(&capacitor.Config{
		InitialConcurrency: 5,
		MaxConcurrency:     10,
	})

	// Make many concurrent requests
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Get(server.URL)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			resp.Body.Close()
		}()
	}
	wg.Wait()

	max := atomic.LoadInt64(&maxConcurrent)
	if max > 5 {
		t.Errorf("expected max concurrent <= 5, got %d", max)
	}
}

func TestClient_DynamicConcurrencyAdjustment(t *testing.T) {
	requestCount := 0
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestCount++
		count := requestCount
		mu.Unlock()

		// Start with high concurrency, then reduce
		suggested := "100"
		if count > 5 {
			suggested = "2"
		}

		w.Header().Set("X-Capacity-Status", "healthy")
		w.Header().Set("X-Capacity-Suggested-Concurrency", suggested)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	stateChanges := 0
	client := capacitor.Wrap(nil).
		WithCapacityHeaders().
		WithConcurrency(10, 1, 100).
		OnStateChange(func(host string, state *capacitor.State) {
			stateChanges++
		}).
		Build()

	// First batch at high concurrency
	for i := 0; i < 5; i++ {
		resp, _ := client.Get(server.URL)
		resp.Body.Close()
	}

	state := client.GetState(server.URL)
	if state.CurrentConcurrency != 100 {
		t.Errorf("expected concurrency 100, got %d", state.CurrentConcurrency)
	}

	// Next requests should reduce concurrency
	for i := 0; i < 5; i++ {
		resp, _ := client.Get(server.URL)
		resp.Body.Close()
	}

	state = client.GetState(server.URL)
	if state.CurrentConcurrency != 2 {
		t.Errorf("expected concurrency 2, got %d", state.CurrentConcurrency)
	}

	if stateChanges == 0 {
		t.Error("expected state change callback to be called")
	}
}

func TestClient_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hold the request
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := capacitor.NewClient(&capacitor.Config{
		InitialConcurrency: 1, // Only 1 slot
		AcquireTimeout:     50 * time.Millisecond,
	})

	// First request will get the slot
	go func() {
		resp, _ := client.Get(server.URL)
		if resp != nil {
			resp.Body.Close()
		}
	}()

	// Give first request time to acquire slot
	time.Sleep(10 * time.Millisecond)

	// Second request should timeout waiting for slot
	_, err := client.Get(server.URL)
	if err == nil {
		t.Error("expected timeout error")
	}

	// Error may be wrapped in url.Error, so unwrap to check
	var capErr *capacitor.CapacityError
	if !errors.As(err, &capErr) {
		t.Errorf("expected CapacityError, got %T: %v", err, err)
	}
}

func TestClient_WrapExisting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Capacity-Status", "healthy")
		w.Header().Set("X-Capacity-Suggested-Concurrency", "25")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Create existing client with custom timeout
	existing := &http.Client{
		Timeout: 5 * time.Second,
	}

	// Wrap it with capacity headers
	client := capacitor.Wrap(existing).WithCapacityHeaders().Build()

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	// Verify wrapped client preserved timeout
	if client.Timeout != 5*time.Second {
		t.Errorf("expected timeout 5s, got %v", client.Timeout)
	}

	// Verify capacity tracking works
	state := client.GetState(server.URL)
	if state == nil || state.SuggestedConcurrency != 25 {
		t.Error("expected state to be tracked")
	}
}

func TestSemaphore_Resize(t *testing.T) {
	sem := capacitor.NewSemaphore(2)

	// Acquire both slots
	ctx := context.Background()
	sem.Acquire(ctx)
	sem.Acquire(ctx)

	if sem.Available() != 0 {
		t.Errorf("expected 0 available, got %d", sem.Available())
	}

	// Start a waiter
	waiting := make(chan struct{})
	acquired := make(chan struct{})
	go func() {
		close(waiting)
		sem.Acquire(ctx)
		close(acquired)
	}()

	<-waiting
	time.Sleep(10 * time.Millisecond) // Let it start waiting

	// Resize to allow more
	sem.Resize(3)

	// Waiter should now be able to acquire
	select {
	case <-acquired:
		// Success
	case <-time.After(100 * time.Millisecond):
		t.Error("waiter should have acquired after resize")
	}

	if sem.InUse() != 3 {
		t.Errorf("expected 3 in use, got %d", sem.InUse())
	}
}

func TestTransport_MultipleHosts(t *testing.T) {
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Capacity-Status", "healthy")
		w.Header().Set("X-Capacity-Suggested-Concurrency", "10")
		w.WriteHeader(http.StatusOK)
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Capacity-Status", "busy")
		w.Header().Set("X-Capacity-Suggested-Concurrency", "5")
		w.WriteHeader(http.StatusOK)
	}))
	defer server2.Close()

	client := capacitor.Wrap(nil).WithCapacityHeaders().Build()

	// Make requests to both servers
	resp1, _ := client.Get(server1.URL)
	resp1.Body.Close()

	resp2, _ := client.Get(server2.URL)
	resp2.Body.Close()

	// Check states are separate
	stats := client.GetStats()
	if len(stats) != 2 {
		t.Errorf("expected 2 hosts, got %d", len(stats))
	}

	state1 := client.GetState(server1.URL)
	state2 := client.GetState(server2.URL)

	if state1.Status != capacitor.StatusHealthy {
		t.Errorf("expected server1 healthy, got %s", state1.Status)
	}
	if state2.Status != capacitor.StatusBusy {
		t.Errorf("expected server2 busy, got %s", state2.Status)
	}
}

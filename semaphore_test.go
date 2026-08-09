package capacitor_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/syntaqx/capacitor"
)

func TestSemaphore_AcquireRelease(t *testing.T) {
	sem := capacitor.NewSemaphore(2)
	ctx := context.Background()

	if err := sem.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if err := sem.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if sem.Available() != 0 || sem.InUse() != 2 {
		t.Fatalf("available=%d inUse=%d, want 0/2", sem.Available(), sem.InUse())
	}
	if sem.Capacity() != 2 {
		t.Errorf("capacity = %d, want 2", sem.Capacity())
	}

	sem.Release()
	if sem.Available() != 1 {
		t.Errorf("available after release = %d, want 1", sem.Available())
	}
}

func TestSemaphore_TryAcquire(t *testing.T) {
	sem := capacitor.NewSemaphore(1)
	if !sem.TryAcquire() {
		t.Fatal("first TryAcquire should succeed")
	}
	if sem.TryAcquire() {
		t.Fatal("second TryAcquire should fail")
	}
	sem.Release()
	if !sem.TryAcquire() {
		t.Fatal("TryAcquire should succeed after release")
	}
}

func TestSemaphore_AcquireContextCancelled(t *testing.T) {
	sem := capacitor.NewSemaphore(1)
	if err := sem.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := sem.Acquire(ctx); err == nil {
		t.Fatal("expected context deadline error")
	}
	if sem.Waiting() != 0 {
		t.Errorf("waiting = %d, want 0 after timeout", sem.Waiting())
	}
}

func TestSemaphore_ShrinkDoesNotForceRelease(t *testing.T) {
	sem := capacitor.NewSemaphore(4)
	ctx := context.Background()
	for range 3 {
		if err := sem.Acquire(ctx); err != nil {
			t.Fatal(err)
		}
	}
	sem.Resize(1)
	if sem.InUse() != 3 {
		t.Errorf("in use = %d, want 3 (shrink must not evict holders)", sem.InUse())
	}
	// Available is clamped implicitly; no new acquire until enough releases.
	if sem.TryAcquire() {
		t.Error("should not acquire while over capacity")
	}
	if sem.Available() != 0 {
		t.Errorf("available = %d, want 0 (never negative after shrink)", sem.Available())
	}
}

func TestSemaphore_FIFOGrantOrder(t *testing.T) {
	sem := capacitor.NewSemaphore(1)
	if err := sem.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}

	const n = 5
	var mu sync.Mutex
	var order []int
	started := make(chan int, n)

	for i := range n {
		go func() {
			started <- i
			if err := sem.Acquire(context.Background()); err != nil {
				return
			}
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
			sem.Release()
		}()
		// Wait until this waiter has queued before starting the next, so the
		// FIFO order is deterministic.
		<-started
		waitFor(t, func() bool { return sem.Waiting() == i+1 })
	}

	sem.Release() // release the initial holder, draining the queue in order

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(order) == n
	})

	mu.Lock()
	defer mu.Unlock()
	for i := range n {
		if order[i] != i {
			t.Fatalf("grant order = %v, want ascending FIFO", order)
		}
	}
}

// TestSemaphore_Concurrent verifies mutual exclusion under heavy contention:
// the number of simultaneous holders must never exceed the capacity. This is
// the test that matters under `go test -race`.
func TestSemaphore_Concurrent(t *testing.T) {
	const capacity = 8
	sem := capacitor.NewSemaphore(capacity)

	var inUse, maxSeen int64
	var wg sync.WaitGroup
	for range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := sem.Acquire(ctx); err != nil {
				return
			}
			defer sem.Release()

			cur := atomic.AddInt64(&inUse, 1)
			for {
				m := atomic.LoadInt64(&maxSeen)
				if cur <= m || atomic.CompareAndSwapInt64(&maxSeen, m, cur) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			atomic.AddInt64(&inUse, -1)
		}()
	}
	wg.Wait()

	if maxSeen > capacity {
		t.Fatalf("observed %d simultaneous holders, capacity is %d", maxSeen, capacity)
	}
	if sem.InUse() != 0 || sem.Waiting() != 0 {
		t.Fatalf("leaked slots: inUse=%d waiting=%d", sem.InUse(), sem.Waiting())
	}
}

// TestSemaphore_ResizeUnderLoad hammers the semaphore while it is concurrently
// resized, asserting only that nothing deadlocks or panics and all slots drain.
func TestSemaphore_ResizeUnderLoad(t *testing.T) {
	sem := capacitor.NewSemaphore(4)

	done := make(chan struct{})
	go func() {
		sizes := []int{1, 16, 2, 32, 4}
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			default:
				sem.Resize(sizes[i%len(sizes)])
				time.Sleep(200 * time.Microsecond)
			}
		}
	}()

	var wg sync.WaitGroup
	for range 300 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := sem.Acquire(ctx); err != nil {
				return
			}
			time.Sleep(time.Millisecond)
			sem.Release()
		}()
	}
	wg.Wait()
	close(done)

	sem.Resize(4)
	waitFor(t, func() bool { return sem.InUse() == 0 && sem.Waiting() == 0 })
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

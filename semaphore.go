package capacitor

import (
	"context"
	"sync"
)

// Semaphore is a weighted semaphore that can be resized dynamically.
// It is safe for concurrent use by multiple goroutines.
type Semaphore struct {
	mu      sync.Mutex
	cond    *sync.Cond
	max     int
	current int
	waiters int
}

// NewSemaphore creates a new semaphore with the given capacity.
func NewSemaphore(n int) *Semaphore {
	s := &Semaphore{max: n}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// Acquire blocks until a slot is available or the context is cancelled.
// Returns nil on success, or the context error if cancelled.
func (s *Semaphore) Acquire(ctx context.Context) error {
	s.mu.Lock()

	// Fast path: slot available
	if s.current < s.max {
		s.current++
		s.mu.Unlock()
		return nil
	}

	// Slow path: need to wait
	s.waiters++

	// Create a channel to signal when we should wake up
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			s.cond.Broadcast()
			s.mu.Unlock()
		case <-done:
		}
	}()

	for s.current >= s.max {
		// Check context before waiting
		select {
		case <-ctx.Done():
			s.waiters--
			s.mu.Unlock()
			close(done)
			return ctx.Err()
		default:
		}

		s.cond.Wait()

		// Check context after waking
		select {
		case <-ctx.Done():
			s.waiters--
			s.mu.Unlock()
			close(done)
			return ctx.Err()
		default:
		}
	}

	s.current++
	s.waiters--
	s.mu.Unlock()
	close(done)
	return nil
}

// TryAcquire attempts to acquire a slot without blocking.
// Returns true if successful, false otherwise.
func (s *Semaphore) TryAcquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.current < s.max {
		s.current++
		return true
	}
	return false
}

// Release releases a slot back to the semaphore.
func (s *Semaphore) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.current > 0 {
		s.current--
		s.cond.Signal()
	}
}

// Resize changes the maximum capacity of the semaphore.
// If the new capacity is larger, waiting goroutines may be woken.
// If smaller, no active slots are forcibly released.
func (s *Semaphore) Resize(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	oldMax := s.max
	s.max = n

	// If we increased capacity, wake up waiters
	if n > oldMax {
		s.cond.Broadcast()
	}
}

// Available returns the number of available slots.
func (s *Semaphore) Available() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.max - s.current
}

// Capacity returns the current maximum capacity.
func (s *Semaphore) Capacity() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.max
}

// InUse returns the number of slots currently in use.
func (s *Semaphore) InUse() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// Waiting returns the number of goroutines waiting for a slot.
func (s *Semaphore) Waiting() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waiters
}

package capacitor

import (
	"container/list"
	"context"
	"sync"
)

// Semaphore is a resizable counting semaphore with context-aware acquisition.
//
// Unlike a fixed channel-based semaphore, its capacity can change at runtime via
// Resize, which the transport uses to react to capacity signals. Queued waiters
// are granted slots in FIFO order, and no slot is ever forcibly revoked from a
// caller that already holds one.
//
// It is safe for concurrent use by multiple goroutines.
type Semaphore struct {
	mu      sync.Mutex
	limit   int
	cur     int
	waiters list.List // FIFO queue of chan struct{}
}

// NewSemaphore creates a new semaphore with the given capacity.
func NewSemaphore(n int) *Semaphore {
	if n < 0 {
		n = 0
	}
	return &Semaphore{limit: n}
}

// Acquire blocks until a slot is available or the context is cancelled.
// It returns nil on success, or the context's error if cancelled first.
func (s *Semaphore) Acquire(ctx context.Context) error {
	s.mu.Lock()
	if s.cur < s.limit {
		s.cur++
		s.mu.Unlock()
		return nil
	}

	// Don't join the queue if the caller is already cancelled.
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return err
	}

	ready := make(chan struct{})
	elem := s.waiters.PushBack(ready)
	s.mu.Unlock()

	select {
	case <-ready:
		// A releaser transferred a slot to us.
		return nil
	case <-ctx.Done():
		s.mu.Lock()
		select {
		case <-ready:
			// Granted concurrently with cancellation; hand the slot back.
			s.cur--
			s.grantLocked()
		default:
			s.waiters.Remove(elem)
		}
		s.mu.Unlock()
		return ctx.Err()
	}
}

// TryAcquire attempts to acquire a slot without blocking.
// Returns true if successful, false otherwise.
func (s *Semaphore) TryAcquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cur < s.limit {
		s.cur++
		return true
	}
	return false
}

// Release releases a slot back to the semaphore, granting it to the next
// queued waiter if any.
func (s *Semaphore) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cur > 0 {
		s.cur--
	}
	s.grantLocked()
}

// Resize changes the maximum capacity of the semaphore.
// Growing wakes queued waiters up to the new capacity; shrinking never forcibly
// revokes slots already held, so InUse may temporarily exceed the new maximum
// until callers Release.
func (s *Semaphore) Resize(n int) {
	if n < 0 {
		n = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.limit = n
	s.grantLocked()
}

// grantLocked hands free slots to queued waiters in FIFO order.
// The caller must hold s.mu.
func (s *Semaphore) grantLocked() {
	for s.cur < s.limit {
		front := s.waiters.Front()
		if front == nil {
			return
		}
		s.waiters.Remove(front)
		s.cur++
		close(front.Value.(chan struct{}))
	}
}

// Available returns the number of available slots. It is never negative, even
// while in-use slots exceed the maximum after a shrink.
func (s *Semaphore) Available() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur >= s.limit {
		return 0
	}
	return s.limit - s.cur
}

// Capacity returns the current maximum capacity.
func (s *Semaphore) Capacity() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.limit
}

// InUse returns the number of slots currently in use.
func (s *Semaphore) InUse() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Waiting returns the number of goroutines waiting for a slot.
func (s *Semaphore) Waiting() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waiters.Len()
}

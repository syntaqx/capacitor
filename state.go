package capacitor

import (
	"strconv"
	"sync"
	"time"
)

// Status represents the server's reported capacity status.
type Status string

const (
	StatusUnknown     Status = ""
	StatusHealthy     Status = "healthy"
	StatusBusy        Status = "busy"
	StatusAtLimit     Status = "at_limit"
	StatusDegraded    Status = "degraded"
	StatusScalingUp   Status = "scaling_up"
	StatusScalingDown Status = "scaling_down"
)

// IsHealthy returns true if the status indicates normal operation.
func (s Status) IsHealthy() bool {
	return s == StatusHealthy || s == StatusScalingUp || s == StatusScalingDown
}

// State represents the capacity state for a single host.
type State struct {
	mu sync.RWMutex

	// Server-reported cluster state
	Status                Status
	TasksRunning          int
	TasksDesired          int
	TasksPending          int
	ClusterMaxConcurrency int
	SuggestedConcurrency  int
	StateAge              int // seconds, -1 if unknown

	// Server-reported worker metrics
	WorkerActive     int
	WorkerAvailable  int
	WorkerLoadFactor float64
	LatencyP99       float64
	LatencyHealth    float64

	// Client-side tracking
	LastUpdated        time.Time
	CurrentConcurrency int
	BlockedUntil       time.Time

	// Clamped indicates if CurrentConcurrency was clamped to MinConcurrency
	// because SuggestedConcurrency was below the configured minimum.
	// This helps users detect when backend suggests throttling below their floor.
	Clamped bool
}

// NewState creates a new state with initial concurrency.
func NewState(initialConcurrency int) *State {
	return &State{
		Status:             StatusUnknown,
		CurrentConcurrency: initialConcurrency,
		LastUpdated:        time.Now(),
	}
}

// Update updates the state from response headers.
func (s *State) Update(headers map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if status, ok := headers["X-Capacity-Status"]; ok {
		s.Status = Status(status)
	}
	if v, ok := headers["X-Capacity-Tasks-Running"]; ok {
		s.TasksRunning, _ = strconv.Atoi(v)
	}
	if v, ok := headers["X-Capacity-Tasks-Desired"]; ok {
		s.TasksDesired, _ = strconv.Atoi(v)
	}
	if v, ok := headers["X-Capacity-Tasks-Pending"]; ok {
		s.TasksPending, _ = strconv.Atoi(v)
	}
	if v, ok := headers["X-Capacity-Cluster-Max-Concurrency"]; ok {
		s.ClusterMaxConcurrency, _ = strconv.Atoi(v)
	}
	if v, ok := headers["X-Capacity-Suggested-Concurrency"]; ok {
		// Only store non-negative values; negative is invalid
		if suggested, _ := strconv.Atoi(v); suggested >= 0 {
			s.SuggestedConcurrency = suggested
		}
	}
	if v, ok := headers["X-Capacity-State-Age"]; ok {
		s.StateAge, _ = strconv.Atoi(v)
	}
	if v, ok := headers["X-Capacity-Worker-Active"]; ok {
		s.WorkerActive, _ = strconv.Atoi(v)
	}
	if v, ok := headers["X-Capacity-Worker-Available"]; ok {
		s.WorkerAvailable, _ = strconv.Atoi(v)
	}
	if v, ok := headers["X-Capacity-Worker-Load-Factor"]; ok {
		s.WorkerLoadFactor, _ = strconv.ParseFloat(v, 64)
	}
	if v, ok := headers["X-Capacity-Latency-P99"]; ok {
		s.LatencyP99, _ = strconv.ParseFloat(v, 64)
	}
	if v, ok := headers["X-Capacity-Latency-Health"]; ok {
		s.LatencyHealth, _ = strconv.ParseFloat(v, 64)
	}

	s.LastUpdated = time.Now()
}

// GetSuggestedConcurrency returns the server's suggested concurrency,
// clamped to the provided min/max bounds.
func (s *State) GetSuggestedConcurrency(min, max int) int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	suggested := s.SuggestedConcurrency
	if suggested <= 0 {
		suggested = s.CurrentConcurrency
	}

	if suggested < min {
		return min
	}
	if suggested > max {
		return max
	}
	return suggested
}

// SetCurrentConcurrency updates the current concurrency limit.
func (s *State) SetCurrentConcurrency(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.CurrentConcurrency = n
}

// SetClamped sets whether the concurrency was clamped by config limits.
func (s *State) SetClamped(clamped bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Clamped = clamped
}

// GetCurrentConcurrency returns the current concurrency limit.
func (s *State) GetCurrentConcurrency() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.CurrentConcurrency
}

// SetBlockedUntil sets when the host should be unblocked.
func (s *State) SetBlockedUntil(t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.BlockedUntil = t
}

// IsBlocked returns true if the host is currently blocked.
func (s *State) IsBlocked() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return time.Now().Before(s.BlockedUntil)
}

// GetBlockedUntil returns when the block expires.
func (s *State) GetBlockedUntil() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.BlockedUntil
}

// IsStale returns true if the state hasn't been updated recently.
func (s *State) IsStale(expiry time.Duration) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return time.Since(s.LastUpdated) > expiry
}

// Clone returns a copy of the current state.
func (s *State) Clone() *State {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return &State{
		Status:                s.Status,
		TasksRunning:          s.TasksRunning,
		TasksDesired:          s.TasksDesired,
		TasksPending:          s.TasksPending,
		ClusterMaxConcurrency: s.ClusterMaxConcurrency,
		SuggestedConcurrency:  s.SuggestedConcurrency,
		StateAge:              s.StateAge,
		WorkerActive:          s.WorkerActive,
		WorkerAvailable:       s.WorkerAvailable,
		WorkerLoadFactor:      s.WorkerLoadFactor,
		LatencyP99:            s.LatencyP99,
		LatencyHealth:         s.LatencyHealth,
		LastUpdated:           s.LastUpdated,
		CurrentConcurrency:    s.CurrentConcurrency,
		BlockedUntil:          s.BlockedUntil,
		Clamped:               s.Clamped,
	}
}

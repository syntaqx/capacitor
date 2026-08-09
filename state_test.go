package capacitor

import (
	"testing"
	"time"
)

func TestState_UpdateAndClone(t *testing.T) {
	s := newState(10)
	s.update(map[string]string{
		"X-Capacity-Status":                "busy",
		"X-Capacity-Tasks-Running":         "8",
		"X-Capacity-Tasks-Desired":         "10",
		"X-Capacity-Suggested-Concurrency": "42",
		"X-Capacity-Worker-Load-Factor":    "0.75",
	})

	if s.Status != StatusBusy {
		t.Errorf("Status = %s, want busy", s.Status)
	}
	if s.SuggestedConcurrency != 42 {
		t.Errorf("SuggestedConcurrency = %d, want 42", s.SuggestedConcurrency)
	}
	if s.WorkerLoadFactor != 0.75 {
		t.Errorf("WorkerLoadFactor = %v, want 0.75", s.WorkerLoadFactor)
	}

	c := s.clone()
	if c.Status != s.Status || c.SuggestedConcurrency != s.SuggestedConcurrency {
		t.Error("clone does not match original")
	}
}

func TestState_NegativeSuggestedResetsToZero(t *testing.T) {
	s := newState(10)
	s.update(map[string]string{"X-Capacity-Suggested-Concurrency": "-5"})
	if s.SuggestedConcurrency != 0 {
		t.Errorf("negative suggested should reset to 0, got %d", s.SuggestedConcurrency)
	}
}

func TestState_Blocking(t *testing.T) {
	s := newState(10)
	if s.IsBlocked() {
		t.Error("new state should not be blocked")
	}
	s.setBlockedUntil(time.Now().Add(time.Minute))
	if !s.IsBlocked() {
		t.Error("state should be blocked")
	}
	if s.BlockedUntil.IsZero() {
		t.Error("BlockedUntil should be set")
	}
	s.setBlockedUntil(time.Time{})
	if s.IsBlocked() {
		t.Error("state should no longer be blocked")
	}
}

func TestState_Stale(t *testing.T) {
	s := newState(10)
	if s.IsStale(time.Minute) {
		t.Error("fresh state should not be stale")
	}
	time.Sleep(15 * time.Millisecond)
	if !s.IsStale(5 * time.Millisecond) {
		t.Error("state should be stale")
	}
	s.touch()
	if s.IsStale(time.Minute) {
		t.Error("touched state should be fresh")
	}
}

func TestStatus_IsHealthy(t *testing.T) {
	healthy := []Status{StatusHealthy, StatusScalingUp, StatusScalingDown}
	for _, s := range healthy {
		if !s.IsHealthy() {
			t.Errorf("%s should be healthy", s)
		}
	}
	unhealthy := []Status{StatusAtLimit, StatusDegraded, StatusBusy}
	for _, s := range unhealthy {
		if s.IsHealthy() {
			t.Errorf("%s should not be healthy", s)
		}
	}
}

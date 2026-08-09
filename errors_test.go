package capacitor_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/syntaqx/capacitor"
)

func TestCapacityError(t *testing.T) {
	base := errors.New("boom")
	err := &capacitor.CapacityError{
		Op:    "acquire",
		Host:  "https://example.com",
		Err:   base,
		State: &capacitor.State{CurrentConcurrency: 7},
	}

	if !errors.Is(err, base) {
		t.Error("CapacityError should unwrap to its underlying error")
	}
	if err.Error() == "" {
		t.Error("Error() should be non-empty")
	}

	// Wrapped the way http.Client does (fmt.Errorf with %w).
	wrapped := fmt.Errorf("Get %q: %w", "https://example.com", err)
	if !capacitor.IsCapacityError(wrapped) {
		t.Error("IsCapacityError should see through wrapping")
	}
	if capacitor.IsCapacityError(base) {
		t.Error("plain error should not be a capacity error")
	}
}

func TestCapacityError_NoState(t *testing.T) {
	err := &capacitor.CapacityError{Op: "blocked", Host: "h", Err: capacitor.ErrBlocked}
	if err.Error() == "" {
		t.Error("Error() should render without a state")
	}
	if !errors.Is(err, capacitor.ErrBlocked) {
		t.Error("should unwrap to ErrBlocked")
	}
}

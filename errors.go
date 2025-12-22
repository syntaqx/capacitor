package capacitor

import (
	"fmt"
)

// CapacityError represents an error related to capacity limiting.
type CapacityError struct {
	Op    string // operation that failed (e.g., "acquire")
	Host  string // host that was being accessed
	Err   error  // underlying error
	State *State // current state at time of error
}

func (e *CapacityError) Error() string {
	if e.State != nil {
		return fmt.Sprintf("capacity %s for %s: %v (concurrency: %d, status: %s)",
			e.Op, e.Host, e.Err, e.State.CurrentConcurrency, e.State.Status)
	}
	return fmt.Sprintf("capacity %s for %s: %v", e.Op, e.Host, e.Err)
}

func (e *CapacityError) Unwrap() error {
	return e.Err
}

// IsCapacityError returns true if the error is a capacity-related error.
func IsCapacityError(err error) bool {
	_, ok := err.(*CapacityError)
	return ok
}

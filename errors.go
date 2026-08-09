package capacitor

import (
	"errors"
	"fmt"
)

// ErrBlocked is the underlying error when a request is refused because the host
// is within a server-signalled block window (e.g. after a 429 with Retry-After).
var ErrBlocked = errors.New("host is temporarily blocked")

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

// IsCapacityError returns true if err, or any error it wraps, is a
// *CapacityError. This unwraps errors returned by http.Client, which wraps
// transport errors in *url.Error.
func IsCapacityError(err error) bool {
	var capErr *CapacityError
	return errors.As(err, &capErr)
}

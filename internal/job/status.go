// Package job contains the PDF job lifecycle independent from transport and
// persistence implementations.
package job

import "fmt"

// Status is the durable lifecycle state of a PDF processing job.
type Status string

const (
	// StatusQueued means the job is durable and available for worker claim.
	StatusQueued Status = "queued"
	// StatusAnalyzing means a worker is validating and inspecting the source PDF.
	StatusAnalyzing Status = "analyzing"
	// StatusOptimizing means Ghostscript is producing an output candidate.
	StatusOptimizing Status = "optimizing"
	// StatusValidating means a worker is validating an optimized candidate.
	StatusValidating Status = "validating"
	// StatusCompleted means a valid best-effort result is available.
	StatusCompleted Status = "completed"
	// StatusFailed means processing ended with a stable failure.
	StatusFailed Status = "failed"
	// StatusCancelled means a requested cancellation reached a safe boundary.
	StatusCancelled Status = "cancelled"
	// StatusExpired means retained blobs and metadata are no longer available.
	StatusExpired Status = "expired"
)

// Valid reports whether the status belongs to the state machine.
func (s Status) Valid() bool {
	_, ok := transitions[s]
	return ok
}

// Terminal reports whether processing has stopped. Completed, failed, and
// cancelled jobs may still transition once more to expired.
func (s Status) Terminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusCancelled, StatusExpired:
		return true
	default:
		return false
	}
}

// CanTransitionTo reports whether next is an allowed direct successor.
func (s Status) CanTransitionTo(next Status) bool {
	for _, candidate := range transitions[s] {
		if candidate == next {
			return true
		}
	}
	return false
}

// ValidateTransition rejects unknown states and forbidden lifecycle edges.
func ValidateTransition(current, next Status) error {
	if !current.Valid() {
		return fmt.Errorf("invalid current job status %q", current)
	}
	if !next.Valid() {
		return fmt.Errorf("invalid next job status %q", next)
	}
	if !current.CanTransitionTo(next) {
		return fmt.Errorf("job status cannot transition from %q to %q", current, next)
	}
	return nil
}

var transitions = map[Status][]Status{
	StatusQueued:     {StatusAnalyzing, StatusFailed, StatusCancelled},
	StatusAnalyzing:  {StatusQueued, StatusOptimizing, StatusCompleted, StatusFailed, StatusCancelled},
	StatusOptimizing: {StatusQueued, StatusValidating, StatusFailed, StatusCancelled},
	StatusValidating: {StatusQueued, StatusOptimizing, StatusCompleted, StatusFailed, StatusCancelled},
	StatusCompleted:  {StatusExpired},
	StatusFailed:     {StatusExpired},
	StatusCancelled:  {StatusExpired},
	StatusExpired:    {},
}

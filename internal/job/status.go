// Package job contains the PDF job lifecycle independent from transport and
// persistence implementations.
package job

import "fmt"

type Status string

const (
	StatusQueued     Status = "queued"
	StatusAnalyzing  Status = "analyzing"
	StatusOptimizing Status = "optimizing"
	StatusValidating Status = "validating"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
	StatusCancelled  Status = "cancelled"
	StatusExpired    Status = "expired"
)

func (s Status) Valid() bool {
	_, ok := transitions[s]
	return ok
}

func (s Status) Terminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusCancelled, StatusExpired:
		return true
	default:
		return false
	}
}

func (s Status) CanTransitionTo(next Status) bool {
	for _, candidate := range transitions[s] {
		if candidate == next {
			return true
		}
	}
	return false
}

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
	StatusAnalyzing:  {StatusOptimizing, StatusCompleted, StatusFailed, StatusCancelled},
	StatusOptimizing: {StatusValidating, StatusFailed, StatusCancelled},
	StatusValidating: {StatusOptimizing, StatusCompleted, StatusFailed, StatusCancelled},
	StatusCompleted:  {StatusExpired},
	StatusFailed:     {StatusExpired},
	StatusCancelled:  {StatusExpired},
	StatusExpired:    {},
}

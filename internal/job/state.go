package job

import "fmt"

var allowedTransitions = map[State]map[State]struct{}{
	StateQueued:          {StateStarting: {}, StateCancelled: {}, StateInterrupted: {}},
	StateStarting:        {StateRunning: {}, StateFailed: {}, StateCancelRequested: {}, StateInterrupted: {}},
	StateRunning:         {StateSucceeded: {}, StateFailed: {}, StateCancelRequested: {}, StateInterrupted: {}},
	StateSucceeded:       {},
	StateFailed:          {},
	StateCancelRequested: {StateCancelled: {}, StateTimedOut: {}, StateInterrupted: {}},
	StateCancelled:       {},
	StateTimedOut:        {},
	StateInterrupted:     {},
}

func CanTransition(from, to State) bool {
	_, ok := allowedTransitions[from][to]
	return ok
}

func ValidateTransition(from, to State) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("invalid job state transition: %s -> %s", from, to)
	}
	return nil
}

package session

var allowedTransitions = map[State]map[State]struct{}{
	StateOpening: {StateActive: {}, StateFailed: {}},
	StateActive:  {StateClosing: {}, StateFailed: {}},
	StateClosing: {StateClosed: {}, StateFailed: {}},
	StateClosed:  {},
	StateFailed:  {},
}

func ValidateTransition(current, next State) error {
	allowed, ok := allowedTransitions[current]
	if !ok {
		return newError(CodeInvalidTransition, "unknown current session state", nil)
	}
	if _, ok := allowed[next]; !ok {
		return newError(CodeInvalidTransition, "invalid session state transition", nil)
	}
	return nil
}

func transition(record *Record, next State) error {
	if err := ValidateTransition(record.State, next); err != nil {
		return err
	}
	record.State = next
	return nil
}

func activeState(state State) bool {
	return state == StateOpening || state == StateActive || state == StateClosing
}

func knownState(state State) bool {
	_, ok := allowedTransitions[state]
	return ok
}

func AuthorizeMutation(record Record) error {
	if record.State == StateClosed {
		return newError(CodeClosed, "session is closed", nil)
	}
	if record.State == StateFailed {
		return newError(CodeFailed, "session is failed", nil)
	}
	if record.State != StateActive {
		return newError(CodeInvalidTransition, "session is not active", nil)
	}
	if record.IsolationMode == IsolationSharedReadonly {
		return newError(CodeWriteDenied, "shared-readonly session does not permit mutation tools", nil)
	}
	return nil
}

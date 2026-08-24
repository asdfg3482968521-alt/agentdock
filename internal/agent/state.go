package agent

func validAgentTransition(from, to AgentState) bool {
	switch from {
	case AgentStateQueued:
		return to == AgentStateStarting || to == AgentStateCancelled
	case AgentStateStarting:
		return to == AgentStateRunning || to == AgentStateFailed || to == AgentStateCancelled || to == AgentStateTimedOut || to == AgentStateInterrupted
	case AgentStateRunning:
		return to == AgentStateCancelRequested || to == AgentStateSucceeded || to == AgentStateFailed || to == AgentStateCancelled || to == AgentStateTimedOut || to == AgentStateInterrupted
	case AgentStateCancelRequested:
		return to == AgentStateCancelled || to == AgentStateFailed || to == AgentStateTimedOut || to == AgentStateInterrupted
	default:
		return false
	}
}

func recoveredAgentState(state AgentState) AgentState {
	switch state {
	case AgentStateQueued:
		return AgentStateQueued
	case AgentStateStarting, AgentStateRunning, AgentStateCancelRequested:
		return AgentStateInterrupted
	default:
		return state
	}
}

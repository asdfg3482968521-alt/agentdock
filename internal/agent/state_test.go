package agent

import "testing"

func TestRecoveredAgentStateMatchesD2Contract(t *testing.T) {
	cases := map[AgentState]AgentState{
		AgentStateQueued:          AgentStateQueued,
		AgentStateStarting:        AgentStateInterrupted,
		AgentStateRunning:         AgentStateInterrupted,
		AgentStateCancelRequested: AgentStateInterrupted,
		AgentStateSucceeded:       AgentStateSucceeded,
		AgentStateFailed:          AgentStateFailed,
		AgentStateCancelled:       AgentStateCancelled,
		AgentStateTimedOut:        AgentStateTimedOut,
		AgentStateInterrupted:     AgentStateInterrupted,
	}
	for from, want := range cases {
		if got := recoveredAgentState(from); got != want {
			t.Fatalf("recoveredAgentState(%s)=%s want=%s", from, got, want)
		}
	}
}

func TestAgentTerminalStates(t *testing.T) {
	for _, state := range []AgentState{AgentStateSucceeded, AgentStateFailed, AgentStateCancelled, AgentStateTimedOut, AgentStateInterrupted} {
		if !state.Terminal() {
			t.Fatalf("%s should be terminal", state)
		}
	}
	for _, state := range []AgentState{AgentStateQueued, AgentStateStarting, AgentStateRunning, AgentStateCancelRequested} {
		if state.Terminal() {
			t.Fatalf("%s should be nonterminal", state)
		}
	}
}

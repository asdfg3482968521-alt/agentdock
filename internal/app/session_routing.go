package app

import (
	"strings"

	sessionruntime "github.com/uvwt/agentdock/internal/session"
)

func d1RuntimeScope(sessionID string) string {
	return "session:" + strings.TrimSpace(sessionID)
}

func (r *Runtime) resolveD1SessionRoute(sessionID, requestedWorkspace string, mutating bool) (sessionruntime.Record, error) {
	if r == nil || r.sessions == nil {
		return sessionruntime.Record{}, toolError("SESSION_RUNTIME_UNAVAILABLE", "session runtime is unavailable", "session")
	}
	record, err := r.sessions.Resolve(strings.TrimSpace(sessionID), strings.TrimSpace(requestedWorkspace))
	if err != nil {
		return sessionruntime.Record{}, sessionToolError(err)
	}
	if mutating {
		if err := sessionruntime.AuthorizeMutation(record); err != nil {
			return sessionruntime.Record{}, sessionToolError(err)
		}
	}
	return record, nil
}

func openCodeToolMutates(tool string) bool {
	switch strings.TrimSpace(tool) {
	case "read", "glob", "grep", "lsp":
		return false
	default:
		return true
	}
}

func (r *Runtime) validateJobSessionOwnership(sessionID string, statusSessionID string) error {
	if _, err := r.resolveD1SessionRoute(sessionID, "", false); err != nil {
		return err
	}
	if strings.TrimSpace(statusSessionID) != strings.TrimSpace(sessionID) {
		return toolError(sessionruntime.CodeJobOwnership, "job does not belong to the requested session", "session")
	}
	return nil
}

func (r *Runtime) sessionRuntimeHasPeer(record sessionruntime.Record) bool {
	if r == nil || r.sessions == nil {
		return false
	}
	for _, peer := range r.sessions.List(sessionruntime.ListFilter{ActiveOnly: true}) {
		if peer.SessionID == record.SessionID {
			continue
		}
		if strings.EqualFold(peer.Workspace(), record.Workspace()) && peer.PermissionMode == record.PermissionMode {
			return true
		}
	}
	return false
}

package session

import (
	"errors"
	"fmt"
)

const (
	CodeNotFound          = "SESSION_NOT_FOUND"
	CodeClosed            = "SESSION_CLOSED"
	CodeFailed            = "SESSION_FAILED"
	CodeWriteDenied       = "SESSION_WRITE_DENIED"
	CodeWorkspaceMismatch = "SESSION_WORKSPACE_MISMATCH"
	CodeConflict          = "SESSION_CONFLICT"
	CodeJobOwnership      = "SESSION_JOB_OWNERSHIP_MISMATCH"
	CodeInvalidBaseRef    = "INVALID_BASE_REF"
	CodeInvalidRepoRoot   = "INVALID_REPO_ROOT"
	CodeInvalidTransition = "INVALID_SESSION_TRANSITION"
)

type Error struct {
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

func newError(code, message string, cause error) error {
	return &Error{Code: code, Message: message, Cause: cause}
}

func ErrorCode(err error) string {
	var sessionErr *Error
	if errors.As(err, &sessionErr) {
		return sessionErr.Code
	}
	return ""
}

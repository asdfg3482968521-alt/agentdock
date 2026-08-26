package opencodenative

type PermissionTool struct {
	MessageID string `json:"messageID"`
	CallID    string `json:"callID"`
}

type PermissionRequest struct {
	ID         string          `json:"id"`
	SessionID  string          `json:"sessionID"`
	Permission string          `json:"permission"`
	Patterns   []string        `json:"patterns"`
	Metadata   map[string]any  `json:"metadata"`
	Always     []string        `json:"always"`
	Tool       *PermissionTool `json:"tool,omitempty"`
}

type PermissionReply string

const (
	PermissionReplyOnce   PermissionReply = "once"
	PermissionReplyAlways PermissionReply = "always"
	PermissionReplyReject PermissionReply = "reject"
)

type PermissionReplyRequest struct {
	Reply   PermissionReply `json:"reply"`
	Message string          `json:"message,omitempty"`
}

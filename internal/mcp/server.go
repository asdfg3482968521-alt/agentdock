package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	sdkjsonrpc "github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uvwt/agentdock/internal/app"
	"github.com/uvwt/agentdock/internal/buildinfo"
	"github.com/uvwt/agentdock/internal/config"
)

type Server struct {
	runtime     *app.Runtime
	sdk         *mcpsdk.Server
	sdkNotion   *mcpsdk.Server // 配置了过滤环境变量时为 Notion 客户端单独构建的小目录实例；未配置时为 nil，所有客户端共用完整目录
	httpHandler http.Handler
	instanceID  string
	requestSeq  atomic.Uint64
}

func NewServer(runtime *app.Runtime, cfg config.Config) *Server {
	server := &Server{runtime: runtime, instanceID: fmt.Sprintf("%x", time.Now().UnixNano())}
	newServerOptions := func() *mcpsdk.ServerOptions {
		options := &mcpsdk.ServerOptions{Capabilities: &mcpsdk.ServerCapabilities{}}
		if cfg.Instructions != "" {
			// 可选说明文字随 initialize 响应下发，由支持 MCP instructions 的客户端注入模型上下文。
			options.Instructions = cfg.Instructions
		}
		return options
	}
	implementation := &mcpsdk.Implementation{Name: config.ServerName, Version: buildinfo.Version}
	server.sdk = mcpsdk.NewServer(implementation, newServerOptions())
	if runtime != nil {
		for _, name := range runtime.ToolNames() {
			server.registerTool(server.sdk, name, cfg)
		}
		// AGENTDOCK_MCP_TOOLS_INCLUDE/EXCLUDE 只对 Notion 客户端生效：
		// 为它单独注册过滤后的小目录，其余 MCP 客户端始终拿到完整目录。
		// Notion 无法消化过大的 tools/list（见 tool_filter.go）。
		if notionToolFilterConfigured() {
			server.sdkNotion = mcpsdk.NewServer(implementation, newServerOptions())
			for _, name := range runtime.ToolNames() {
				if !mcpToolExposed(name) {
					continue
				}
				server.registerTool(server.sdkNotion, name, cfg)
			}
		}
	}
	server.httpHandler = mcpsdk.NewStreamableHTTPHandler(
		server.sdkForRequest,
		&mcpsdk.StreamableHTTPOptions{
			// 仅在显式配置公网 URL 且启用认证时放宽 SDK 的 localhost Host 校验。
			// 反代或 Tunnel 会保留公网 Host，入口仍由静态 Token 或 OAuth resource 绑定保护。
			DisableLocalhostProtection: cfg.OAuthServerURL != "" && cfg.AuthRequired(),
			// /mcp 始终保持无会话传输：Notion 的 MCP 客户端不会稳定携带
			// Mcp-Session-Id（initialize 之后也会额外发起不带会话头的 GET SSE 流），
			// 有状态会话模式会把这些请求全部拒绝，直接导致 Notion 连接失败。
			Stateless:                    true,
			JSONResponse:                 true,
			MaxRequestBodyBytes:          1 << 20,
			PropagateRequestCancellation: true,
		},
	)
	return server
}

// sdkForRequest 按请求来源选择 MCP 目录：识别为 Notion 的请求拿到过滤后的
// 小目录，其余所有客户端拿到完整目录。未配置过滤器时一律走完整目录。
func (s *Server) sdkForRequest(r *http.Request) *mcpsdk.Server {
	if s.sdkNotion != nil && isNotionMCPClient(r) {
		return s.sdkNotion
	}
	return s.sdk
}

func (s *Server) AgentDockContext(ctx context.Context) (app.Result, error) {
	return s.runtime.AgentDockContext(ctx)
}

func (s *Server) HTTPHandler() http.Handler {
	return s.httpHandler
}

// ToolDescriptors exposes the currently available AgentDock tool catalog for
// non-MCP transports such as GPT Actions. It uses the same runtime availability
// filter and metadata as the MCP server, so there is no second tool registry.
func (s *Server) ToolDescriptors() []map[string]any {
	if s == nil || s.runtime == nil {
		return []map[string]any{}
	}
	return toolDescriptorsForNames(s.runtime.ToolNames())
}

// CallToolDirect dispatches a tool inside the AgentDock process without
// round-tripping through /mcp. GPT Actions therefore share the exact same
// Runtime.Call path, panic containment, logging and tool envelope as MCP.
func (s *Server) CallToolDirect(ctx context.Context, name string, arguments map[string]any) map[string]any {
	started := time.Now()
	requestID := fmt.Sprintf("%s-gpt-%d", s.instanceID, s.requestSeq.Add(1))
	plane := toolRequestPlane(name)
	if arguments == nil {
		arguments = map[string]any{}
	}
	slog.Info("GPT Action tool started", "request_id", requestID, "server_instance", s.instanceID, "tool", name, "plane", plane)
	result, err := s.invokeTool(ctx, name, arguments, requestID, plane)
	finishedAttrs := []any{"request_id", requestID, "server_instance", s.instanceID, "tool", name, "plane", plane, "duration_ms", time.Since(started).Milliseconds(), "ok", err == nil}
	if err != nil {
		finishedAttrs = append(finishedAttrs, "error", err)
	}
	slog.Info("GPT Action tool finished", finishedAttrs...)
	return toolEnvelope(name, result, err)
}

func (s *Server) ServeStdio(in io.Reader, out io.Writer) error {
	if s == nil || s.sdk == nil {
		return errors.New("MCP server is not initialized")
	}
	return s.sdk.Run(context.Background(), &mcpsdk.IOTransport{
		Reader: readCloser{Reader: in},
		Writer: writeCloser{Writer: out},
	})
}

func (s *Server) registerTool(target *mcpsdk.Server, name string, cfg config.Config) {
	def, ok := toolDefinition(name)
	if !ok {
		return
	}
	meta := toolMetadata(def)
	tool := &mcpsdk.Tool{
		Name:         name,
		Title:        def.Title,
		Description:  def.Description,
		InputSchema:  app.InputSchemaForConfig(name, cfg),
		OutputSchema: app.OutputSchemaForConfig(name, cfg),
	}
	if def.Annotations != nil {
		tool.Annotations = &mcpsdk.ToolAnnotations{
			Title:           def.Annotations.Title,
			ReadOnlyHint:    def.Annotations.ReadOnlyHint,
			DestructiveHint: cloneBoolPointer(def.Annotations.DestructiveHint),
			IdempotentHint:  def.Annotations.IdempotentHint,
			OpenWorldHint:   cloneBoolPointer(def.Annotations.OpenWorldHint),
		}
	}
	if len(meta) > 0 {
		tool.Meta = mcpsdk.Meta(meta)
	}
	// 使用低层 AddTool：AgentDock 的参数校验、权限错误和结构化输出都由
	// Runtime 统一处理，SDK 只负责协议、会话与传输语义。
	target.AddTool(tool, func(ctx context.Context, request *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return s.callTool(ctx, name, request)
	})
}

func (s *Server) callTool(ctx context.Context, name string, request *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	started := time.Now()
	requestID := fmt.Sprintf("%s-%d", s.instanceID, s.requestSeq.Add(1))
	plane := toolRequestPlane(name)
	arguments := map[string]any{}
	if request != nil && request.Params != nil && len(request.Params.Arguments) > 0 && string(request.Params.Arguments) != "null" {
		if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
			slog.Warn("tool params invalid", "request_id", requestID, "server_instance", s.instanceID, "tool", name, "plane", plane, "duration_ms", time.Since(started).Milliseconds())
			return nil, &sdkjsonrpc.Error{Code: sdkjsonrpc.CodeInvalidParams, Message: "tool arguments must be a JSON object"}
		}
	}
	slog.Info("tool started", "request_id", requestID, "server_instance", s.instanceID, "tool", name, "plane", plane)
	result, err := s.invokeTool(ctx, name, arguments, requestID, plane)
	finishedAttrs := []any{"request_id", requestID, "server_instance", s.instanceID, "tool", name, "plane", plane, "duration_ms", time.Since(started).Milliseconds(), "ok", err == nil}
	if err != nil {
		finishedAttrs = append(finishedAttrs, "error", err)
	}
	slog.Info("tool finished", finishedAttrs...)

	encoded, encodeErr := json.Marshal(toolEnvelope(name, result, err))
	if encodeErr != nil {
		return nil, fmt.Errorf("encode MCP tool result: %w", encodeErr)
	}
	var response mcpsdk.CallToolResult
	if decodeErr := json.Unmarshal(encoded, &response); decodeErr != nil {
		return nil, fmt.Errorf("decode MCP tool result: %w", decodeErr)
	}
	return &response, nil
}

func (s *Server) invokeTool(ctx context.Context, name string, arguments map[string]any, requestID, plane string) (result app.Result, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("tool panic contained", "request_id", requestID, "server_instance", s.instanceID, "tool", name, "plane", plane, "panic", recovered)
			err = &app.ToolError{
				Code: "MCP_TOOL_PANIC", Message: "tool request failed unexpectedly", Category: "runtime", Retryable: false,
				Details: map[string]any{"tool": name, "plane": plane},
			}
		}
	}()
	if s == nil || s.runtime == nil {
		return nil, &app.ToolError{
			Code: "MCP_RUNTIME_UNAVAILABLE", Message: "AgentDock runtime is unavailable", Category: "runtime", Retryable: true,
			Details: map[string]any{"tool": name, "plane": plane},
		}
	}
	return s.runtime.Call(ctx, name, arguments)
}

func toolRequestPlane(name string) string {
	switch name {
	case "server_info", "agentdock_context", "mcp_manage", "mcp_tool_search", "mcp_tool_inspect":
		return "CONTROL_PLANE"
	default:
		return "EXECUTION_PLANE"
	}
}

func toolMetadata(def ToolDefinition) map[string]any {
	meta := map[string]any{}
	if len(def.FileArgRewritePaths) > 0 {
		paths := append([]string(nil), def.FileArgRewritePaths...)
		meta["file_arg_rewrite_paths"] = paths
		meta["openai/fileParams"] = paths
	}
	if len(def.FileResultRewritePaths) > 0 {
		paths := append([]string(nil), def.FileResultRewritePaths...)
		meta["file_result_rewrite_paths"] = paths
		meta["openai/fileResultPaths"] = paths
		meta["openai/fileOutputs"] = paths
	}
	return meta
}

func cloneBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

type readCloser struct{ io.Reader }

func (readCloser) Close() error { return nil }

type writeCloser struct{ io.Writer }

func (writeCloser) Close() error { return nil }

func toolDescriptorsForNames(names []string) []map[string]any {
	descriptors := make([]map[string]any, 0, len(names))
	for _, name := range names {
		def, _ := toolDefinition(name)
		descriptor := map[string]any{
			"name":         name,
			"title":        def.Title,
			"description":  def.Description,
			"inputSchema":  inputSchema(name),
			"outputSchema": outputSchema(name),
		}
		meta := toolMetadata(def)
		if paths, ok := meta["file_arg_rewrite_paths"].([]string); ok {
			descriptor["file_arg_rewrite_paths"] = paths
		}
		if paths, ok := meta["file_result_rewrite_paths"].([]string); ok {
			descriptor["file_result_rewrite_paths"] = paths
		}
		if len(meta) > 0 {
			descriptor["_meta"] = meta
		}
		descriptors = append(descriptors, descriptor)
	}
	return descriptors
}

func toolEnvelope(name string, structured any, err error) map[string]any {
	if err != nil {
		payload := map[string]any{"tool": name, "error": err.Error()}
		var toolErr *app.ToolError
		if errors.As(err, &toolErr) {
			payload["code"] = toolErr.Code
			payload["category"] = toolErr.Category
			payload["retryable"] = toolErr.Retryable
			payload["details"] = toolErr.Details
			if toolErr.Code == "PERMISSION_REQUIRED" {
				payload["permission_request"] = map[string]any{
					"tool_name":  name,
					"permission": toolErr.Details["permission"],
					"status":     "required",
				}
			}
		}
		return map[string]any{"isError": true, "structuredContent": payload, "content": []map[string]any{{"type": "text", "text": pretty(payload)}}}
	}
	if name == "view_image" {
		payload := asMap(structured)
		if data, _ := payload["_mcp_image_base64"].(string); data != "" {
			mimeType, _ := payload["_mcp_image_mime_type"].(string)
			clean := cloneWithoutInternalImage(payload)
			return map[string]any{"isError": false, "structuredContent": clean, "content": []map[string]any{{"type": "image", "data": data, "mimeType": mimeType}}}
		}
	}
	if name == "mcp_tool_call" || strings.HasPrefix(name, "opencode_") {
		return dynamicMCPToolEnvelope(structured)
	}
	return map[string]any{"isError": false, "structuredContent": structured, "content": []map[string]any{{"type": "text", "text": pretty(structured)}}}
}

func dynamicMCPToolEnvelope(structured any) map[string]any {
	payload := asMap(structured)
	remote, _ := payload["result"].(map[string]any)
	isError, _ := remote["isError"].(bool)
	content, ok := remote["content"]
	if !ok {
		content = []map[string]any{{"type": "text", "text": pretty(payload)}}
	}
	return map[string]any{
		"isError":           isError,
		"structuredContent": payload,
		"content":           content,
	}
}

func cloneWithoutInternalImage(value map[string]any) map[string]any {
	clean := make(map[string]any, len(value))
	for key, item := range value {
		if key == "_mcp_image_base64" || key == "_mcp_image_mime_type" {
			continue
		}
		clean[key] = item
	}
	return clean
}

func asMap(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case app.Result:
		return map[string]any(typed)
	default:
		return map[string]any{}
	}
}

func pretty(value any) string {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}

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
	httpHandler http.Handler
	instanceID  string
	requestSeq  atomic.Uint64
}

func NewServer(runtime *app.Runtime, cfg config.Config) *Server {
	server := &Server{runtime: runtime, instanceID: fmt.Sprintf("%x", time.Now().UnixNano())}
	serverOptions := &mcpsdk.ServerOptions{Capabilities: &mcpsdk.ServerCapabilities{}}
	if cfg.Instructions != "" {
		// 可选说明文字随 initialize 响应下发，由支持 MCP instructions 的客户端注入模型上下文。
		serverOptions.Instructions = cfg.Instructions
	}
	server.sdk = mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: config.ServerName, Version: buildinfo.Version},
		serverOptions,
	)
	if runtime != nil {
		for _, name := range runtime.ToolNames() {
			server.registerTool(name, cfg)
		}
	}
	commonOptions := mcpsdk.StreamableHTTPOptions{
		// 仅在显式配置公网 URL 且启用认证时放宽 SDK 的 localhost Host 校验。
		// 反代或 Tunnel 会保留公网 Host，入口仍由静态 Token 或 OAuth resource 绑定保护。
		DisableLocalhostProtection:   cfg.OAuthServerURL != "" && cfg.AuthRequired(),
		JSONResponse:                 true,
		MaxRequestBodyBytes:          1 << 20,
		PropagateRequestCancellation: true,
	}
	statefulOptions := commonOptions
	statefulOptions.Stateless = false
	statelessOptions := commonOptions
	statelessOptions.Stateless = true
	server.httpHandler = &protocolAdaptiveHTTPHandler{
		stateful: mcpsdk.NewStreamableHTTPHandler(
			func(*http.Request) *mcpsdk.Server { return server.sdk },
			&statefulOptions,
		),
		stateless: mcpsdk.NewStreamableHTTPHandler(
			func(*http.Request) *mcpsdk.Server { return server.sdk },
			&statelessOptions,
		),
	}
	return server
}

const sessionlessMCPProtocolVersion = "2026-07-28"

type protocolAdaptiveHTTPHandler struct {
	stateful  http.Handler
	stateless http.Handler
}

func (h *protocolAdaptiveHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if useStatelessMCPTransport(r) {
		h.stateless.ServeHTTP(w, r)
		return
	}
	h.stateful.ServeHTTP(w, r)
}

func useStatelessMCPTransport(r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("Mcp-Session-Id")) != "" {
		return false
	}
	if version := strings.TrimSpace(r.Header.Get("Mcp-Protocol-Version")); version != "" {
		return version >= sessionlessMCPProtocolVersion
	}
	if r.Method != http.MethodPost || r.Body == nil {
		return false
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return false
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	r.ContentLength = int64(len(body))

	var message struct {
		Method string `json:"method"`
		Params struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &message); err != nil || message.Method != "initialize" {
		return false
	}
	return strings.TrimSpace(message.Params.ProtocolVersion) >= sessionlessMCPProtocolVersion
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

func (s *Server) registerTool(name string, cfg config.Config) {
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
	s.sdk.AddTool(tool, func(ctx context.Context, request *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
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
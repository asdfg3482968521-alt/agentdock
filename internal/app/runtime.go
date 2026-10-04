package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	acpruntime "github.com/uvwt/agentdock/internal/acp"
	agentruntime "github.com/uvwt/agentdock/internal/agent"
	"github.com/uvwt/agentdock/internal/buildinfo"
	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/credential"
	"github.com/uvwt/agentdock/internal/envstore"
	"github.com/uvwt/agentdock/internal/evolution"
	jobruntime "github.com/uvwt/agentdock/internal/job"
	mcpclient "github.com/uvwt/agentdock/internal/mcp/client"
	"github.com/uvwt/agentdock/internal/nativebridge"
	opencoderuntime "github.com/uvwt/agentdock/internal/opencode"
	"github.com/uvwt/agentdock/internal/opencodeautonomous"
	"github.com/uvwt/agentdock/internal/opencodenative"
	sessionruntime "github.com/uvwt/agentdock/internal/session"
	"github.com/uvwt/agentdock/internal/taskstate"
	toolacp "github.com/uvwt/agentdock/internal/tool/acp"
	toolbrowser "github.com/uvwt/agentdock/internal/tool/browser"
	toolcommand "github.com/uvwt/agentdock/internal/tool/command"
	toolcore "github.com/uvwt/agentdock/internal/tool/core"
	toolfile "github.com/uvwt/agentdock/internal/tool/file"
	toolmcp "github.com/uvwt/agentdock/internal/tool/mcp"
	toolmedia "github.com/uvwt/agentdock/internal/tool/media"
	toolrecall "github.com/uvwt/agentdock/internal/tool/recall"
	toolskill "github.com/uvwt/agentdock/internal/tool/skill"
	tooltask "github.com/uvwt/agentdock/internal/tool/task"
	"github.com/uvwt/agentdock/internal/workspace"
)

type Result = toolcore.Result

type StartupIssue struct {
	Subsystem string `json:"subsystem"`
	Error     string `json:"error"`
}

type Runtime struct {
	cfg              config.Config
	ws               *workspace.Workspace
	skills           *toolskill.Service
	mcpClients       *mcpclient.Manager
	mcpSessions      *MCPClientSessionStore
	tasks            *taskstate.Store
	command          *toolcommand.Service
	files            *toolfile.Service
	dynamicMCP       *toolmcp.Service
	openCode         *opencoderuntime.Supervisor
	nativeSupervisor d3Supervisor
	nativeAutonomous d3NativeAutonomousRunner
	jobs             *jobruntime.Manager
	sessions         *sessionruntime.Manager
	agents           *agentruntime.Manager
	credentialStore  agentCredentialStore
	media            *toolmedia.Service
	browser          *toolbrowser.Service
	recall           *toolrecall.Service
	evolution        *evolution.Service
	taskTools        *tooltask.Service
	acp              *toolacp.Service
	lifecycleMu      sync.RWMutex
	commandCtx       context.Context
	commandCancel    context.CancelFunc
	closing          bool
	closeOnce        sync.Once
	closeErr         error
	startupIssues    []StartupIssue
}

func NewRuntime(cfg config.Config) (*Runtime, error) {
	ws, err := workspace.New(cfg.AgentDockDefaultDir)
	if err != nil {
		return nil, err
	}
	envs, err := envstore.New(cfg.AgentDockHome)
	if err != nil {
		return nil, err
	}
	skills, err := toolskill.New(cfg, ws, envs)
	if err != nil {
		return nil, err
	}
	mcpClients, err := mcpclient.NewManager(cfg.AgentDockHome, envs)
	if err != nil {
		return nil, err
	}
	tasks, err := taskstate.New(filepath.Join(cfg.AgentDockHome, "tasks"))
	if err != nil {
		_ = mcpClients.Close()
		return nil, err
	}

	commandCtx, commandCancel := context.WithCancel(context.Background())
	commandSessions := toolcommand.NewSessionStore()
	runtime := &Runtime{
		cfg: cfg, ws: ws, skills: skills,
		mcpClients: mcpClients, tasks: tasks,
		commandCtx: commandCtx, commandCancel: commandCancel,
	}
	mcpSessions, mcpSessionErr := newMCPClientSessionStore(cfg.AgentDockHome)
	if mcpSessionErr != nil {
		runtime.recordStartupIssue("mcp_session_awareness", mcpSessionErr)
	} else {
		runtime.mcpSessions = mcpSessions
	}

	// Everything below this point is optional to the HTTP/MCP control plane.
	// A broken optional subsystem must never prevent AgentDock from listening:
	// record the failure, keep the subsystem nil, and surface the cause through
	// server_info / GPT status so the operator can diagnose it after startup.
	sessionManager, sessionErr := sessionruntime.NewManager(context.Background(), sessionruntime.Config{
		StorageRoot:  filepath.Join(cfg.AgentDockHome, "sessions", "metadata"),
		WorktreeRoot: filepath.Join(cfg.AgentDockHome, "sessions", "worktrees"),
	})
	if sessionErr != nil {
		runtime.recordStartupIssue("session_runtime", sessionErr)
	} else {
		runtime.sessions = sessionManager
	}

	if cfg.OpenCodeEnabled && cfg.OpenCodeNativeURL != "" {
		if runtime.sessions == nil {
			runtime.recordStartupIssue("opencode_native", errors.New("session runtime unavailable; D3 native bridge not initialized"))
		} else {
			location, locateErr := (opencoderuntime.ManifestLocator{Path: cfg.OpenCodeRuntimeManifest}).Locate(context.Background())
			if locateErr != nil {
				runtime.recordStartupIssue("opencode_native_identity", locateErr)
			} else {
				nativeClient, clientErr := opencodenative.NewClient(
					opencodenative.Config{BaseURL: cfg.OpenCodeNativeURL},
					&http.Client{Timeout: 3 * time.Minute},
				)
				if clientErr != nil {
					runtime.recordStartupIssue("opencode_native_client", clientErr)
				} else {
					bridge, bridgeErr := nativebridge.New(nativebridge.Options{
						Sessions:             runtime.sessions,
						Native:               nativeClient,
						NativeRuntimeSHA:     location.VerifiedSHA,
						NativeServerIdentity: cfg.OpenCodeNativeURL,
					})
					if bridgeErr != nil {
						runtime.recordStartupIssue("opencode_native_bridge", bridgeErr)
					} else {
						runtime.nativeSupervisor = bridge
						autonomousBackend, autonomousErr := opencodeautonomous.New(bridge)
						if autonomousErr != nil {
							runtime.recordStartupIssue("opencode_native_autonomous", autonomousErr)
						} else {
							runtime.nativeAutonomous = autonomousBackend
						}
					}
				}
			}
		}
	}

	if cfg.OpenCodeEnabled {
		supervisor, supervisorErr := opencoderuntime.NewSupervisor(context.Background(), opencoderuntime.Options{
			Locator: opencoderuntime.ManifestLocator{Path: cfg.OpenCodeRuntimeManifest},
			LogDir:  cfg.OpenCodeLogDir,
		})
		if supervisorErr != nil {
			runtime.recordStartupIssue("opencode_execution", supervisorErr)
		} else {
			runtime.openCode = supervisor
		}
	}

	jobStorageRoot := cfg.JobStorageRoot
	if jobStorageRoot == "" {
		jobStorageRoot = filepath.Join(cfg.AgentDockHome, "jobs")
	}
	jobManager, jobErr := jobruntime.NewManager(
		context.Background(),
		jobruntime.NewOpenCodeExecutor(runtime.openCode),
		jobruntime.Config{
			StorageRoot: jobStorageRoot,
			Limits:      persistentJobLimits(cfg),
		},
	)
	if jobErr != nil {
		runtime.recordStartupIssue("job_runtime", jobErr)
	} else {
		runtime.jobs = jobManager
	}

	credentialStore, credentialErr := credential.NewStore(cfg.AgentDockHome)
	if credentialErr != nil {
		runtime.recordStartupIssue("credential_store", credentialErr)
	} else {
		runtime.credentialStore = credentialStore
	}

	agentBackends := map[string]agentruntime.AgentBackend{}
	if runtime.openCode != nil && runtime.sessions != nil {
		opencodeBackend, backendErr := newOpenCodeAgentBackend(runtime.openCode, func(sessionID, runtimeScope string) error {
			_, bindErr := runtime.sessions.SetRuntime(sessionID, runtimeScope)
			return bindErr
		})
		if backendErr != nil {
			runtime.recordStartupIssue("agent_opencode_backend", backendErr)
		} else {
			agentBackends["opencode"] = opencodeBackend
		}
	}
	addD3NativeAutonomousAgentBackend(agentBackends, runtime.nativeAutonomous)

	if runtime.sessions == nil {
		runtime.recordStartupIssue("agent_runtime", errors.New("session runtime unavailable; Agent runtime not initialized"))
	} else if runtime.credentialStore == nil {
		runtime.recordStartupIssue("agent_runtime", errors.New("credential store unavailable; Agent runtime not initialized"))
	} else {
		agentManager, agentErr := agentruntime.NewManager(context.Background(), agentruntime.ManagerOptions{
			StorageRoot: filepath.Join(cfg.AgentDockHome, "agents"),
			Sessions:    newAgentSessionResolver(runtime),
			Credentials: runtime.credentialStore,
			Providers: map[string]agentruntime.ProviderAdapter{
				"anthropic": agentruntime.NewAnthropicProvider(agentruntime.ProviderHTTPOptions{}),
				"deepseek":  agentruntime.NewDeepSeekProvider(agentruntime.ProviderHTTPOptions{}),
			},
			Backends: agentBackends,
			Tools:    newAgentToolBridge(runtime),
			Limits:   d3G1AgentLimits(runtime.nativeAutonomous != nil),
		})
		if agentErr != nil {
			runtime.recordStartupIssue("agent_runtime", agentErr)
		} else {
			runtime.agents = agentManager
		}
	}

	runtime.command = toolcommand.New(
		func() config.Config { return runtime.cfg },
		ws,
		envs,
		commandSessions,
		skills.ResolveActive,
		runtime.commandExecutionContext,
	)
	runtime.files = toolfile.New(ws, skills.ResolveResource, runtime.command.CommandEnv)
	runtime.dynamicMCP = toolmcp.New(mcpClients, envs)
	runtime.media = toolmedia.New(cfg, ws, runtime.command.InternalCommandEnv)
	runtime.browser = toolbrowser.New(toolbrowser.Config{
		AgentDockHome:    cfg.AgentDockHome,
		ExecutablePath:   cfg.BrowserExecutablePath,
		CDPURL:           cfg.BrowserCDPURL,
		ReuseExistingCDP: cfg.BrowserReuseExistingCDP,
	})
	runtime.recall = toolrecall.New(func() config.Config { return runtime.cfg })
	runtime.evolution = evolution.New(func() config.Config { return runtime.cfg }, tasks)
	runtime.taskTools = tooltask.New(func() config.Config { return runtime.cfg }, tasks, runtime.evolution)

	if cfg.ACPEnabled {
		acpEnvironment := make(map[string]string, len(cfg.ACPEnvFromEnv))
		acpReady := true
		for childName, hostName := range cfg.ACPEnvFromEnv {
			value, exists := os.LookupEnv(hostName)
			if !exists {
				runtime.recordStartupIssue("acp_runtime", fmt.Errorf("required ACP environment variable %s is missing", hostName))
				acpReady = false
				break
			}
			acpEnvironment[childName] = value
		}
		if acpReady {
			manager, acpErr := acpruntime.NewManager(acpruntime.Options{
				Home:       cfg.AgentDockHome,
				DefaultCWD: cfg.AgentDockDefaultDir,
				Agent: acpruntime.AgentSpec{
					Name:        cfg.ACPAgentName,
					Command:     cfg.ACPCommand,
					Args:        append([]string(nil), cfg.ACPArgs...),
					Environment: acpEnvironment,
				},
				MaxConcurrentRuns:  cfg.ACPMaxPrompts,
				InteractionTimeout: time.Duration(cfg.ACPInteractionMS) * time.Millisecond,
			})
			if acpErr != nil {
				runtime.recordStartupIssue("acp_runtime", acpErr)
			} else {
				runtime.acp = toolacp.New(manager)
			}
		}
	}

	return runtime, nil
}

func (r *Runtime) recordStartupIssue(subsystem string, err error) {
	if r == nil || err == nil {
		return
	}
	issue := StartupIssue{
		Subsystem: strings.TrimSpace(subsystem),
		Error:     strings.TrimSpace(err.Error()),
	}
	r.startupIssues = append(r.startupIssues, issue)
	slog.Warn(
		"optional subsystem unavailable; AgentDock continues in degraded mode",
		"subsystem", issue.Subsystem,
		"error", issue.Error,
	)
}

func (r *Runtime) startupIssueList() []StartupIssue {
	if r == nil || len(r.startupIssues) == 0 {
		return []StartupIssue{}
	}
	out := make([]StartupIssue, len(r.startupIssues))
	copy(out, r.startupIssues)
	return out
}

func (r *Runtime) subsystemStatus() map[string]any {
	if r == nil {
		return map[string]any{}
	}
	return map[string]any{
		"session":               r.sessions != nil,
		"job":                   r.jobs != nil,
		"agent":                 r.agents != nil,
		"credential_store":      r.credentialStore != nil,
		"opencode_execution":    r.openCode != nil,
		"opencode_native":       r.nativeSupervisor != nil,
		"opencode_autonomous":   r.nativeAutonomous != nil,
		"acp":                   r.acp != nil,
		"dynamic_mcp":           r.dynamicMCP != nil,
		"browser":               r.browser != nil,
		"mcp_session_awareness": r.mcpSessions != nil,
	}
}

func persistentJobLimits(cfg config.Config) jobruntime.Limits {
	limits := jobruntime.DefaultLimits()
	if cfg.JobMaxConcurrent > 0 {
		limits.MaxConcurrentJobs = cfg.JobMaxConcurrent
	}
	if cfg.JobMaxConcurrentPerWorkspace > 0 {
		limits.MaxConcurrentPerWorkspace = cfg.JobMaxConcurrentPerWorkspace
	}
	if cfg.JobMaxConcurrentWritePerWorkspace > 0 {
		limits.MaxConcurrentWritePerWorkspace = cfg.JobMaxConcurrentWritePerWorkspace
	}
	if cfg.JobMemoryOutputBytes > 0 {
		limits.MemoryOutputBytesPerJob = int64(cfg.JobMemoryOutputBytes)
	}
	if cfg.JobResponseOutputBytes > 0 {
		limits.MaxOutputBytesPerResponse = cfg.JobResponseOutputBytes
	}
	if cfg.JobDiskSpoolBytes > 0 {
		limits.MaxDiskOutputBytesPerJob = int64(cfg.JobDiskSpoolBytes)
	}
	if cfg.JobRetentionDays > 0 {
		limits.Retention = time.Duration(cfg.JobRetentionDays) * 24 * time.Hour
	}
	if cfg.JobRetentionMaxCompleted > 0 {
		limits.RetentionMaxCompletedJobs = cfg.JobRetentionMaxCompleted
	}
	if cfg.JobMaxTimeoutMS > 0 {
		limits.MaxTimeout = time.Duration(cfg.JobMaxTimeoutMS) * time.Millisecond
	}
	return limits
}

func (r *Runtime) Config() config.Config           { return r.cfg }
func (r *Runtime) Workspace() *workspace.Workspace { return r.ws }

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		var closeErrors []error
		r.lifecycleMu.Lock()
		r.closing = true
		if r.commandCancel != nil {
			r.commandCancel()
		}
		r.lifecycleMu.Unlock()
		if r.acp != nil {
			if err := r.acp.Close(); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf("close ACP runtime: %w", err))
			}
		}
		if r.agents != nil {
			if err := r.agents.Close(); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf("close Agent runtime: %w", err))
			}
		}
		if r.openCode != nil {
			if err := r.openCode.Close(); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf("close OpenCode runtime supervisor: %w", err))
			}
		}
		if r.browser != nil {
			if err := r.browser.Close(); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf("close browser runtime: %w", err))
			}
		}
		if r.command != nil {
			if err := r.command.Close(); err != nil {
				closeErrors = append(closeErrors, err)
			}
		}
		if r.mcpClients != nil {
			if err := r.mcpClients.Close(); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf("close dynamic MCP clients: %w", err))
			}
		}
		r.closeErr = errors.Join(closeErrors...)
	})
	return r.closeErr
}

func (r *Runtime) commandExecutionContext() (context.Context, error) {
	r.lifecycleMu.RLock()
	defer r.lifecycleMu.RUnlock()
	if r.closing || r.commandCtx == nil {
		return nil, toolError("RUNTIME_CLOSING", "AgentDock runtime is shutting down", "runtime")
	}
	return r.commandCtx, nil
}

func (r *Runtime) ToolNames() []string {
	specs := r.availableToolSpecs()
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	return names
}

func (r *Runtime) Call(ctx context.Context, name string, args map[string]any) (Result, error) {
	if args == nil {
		args = map[string]any{}
	}
	spec, ok := toolSpecByName(name)
	if !ok || !spec.available(r.cfg) {
		return nil, toolErrorDetails("UNKNOWN_TOOL", "tool is not available", "validation", map[string]any{"tool": name})
	}
	if spec.Handler == nil {
		return nil, toolErrorDetails("UNKNOWN_TOOL", "tool has no handler", "validation", map[string]any{"tool": name})
	}
	if err := validateTopLevelArguments(spec, args); err != nil {
		return nil, err
	}
	return spec.Handler(ctx, r, args)
}

// validateTopLevelArguments 只兑现 schema 的顶层严格契约；字段类型和嵌套结构仍由各工具处理。
func validateTopLevelArguments(spec ToolSpec, args map[string]any) error {
	if spec.InputSchema == nil {
		return nil
	}
	schema := spec.InputSchema()
	allowsAdditionalProperties, ok := schema["additionalProperties"].(bool)
	if !ok || allowsAdditionalProperties {
		return nil
	}
	properties, _ := schema["properties"].(map[string]any)
	unknown := make([]string, 0)
	for key := range args {
		if _, allowed := properties[key]; !allowed {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	code := "INVALID_ARGUMENT"
	if strings.HasPrefix(spec.Name, "agent_") {
		code = "INVALID_ARGUMENTS"
	}
	return toolErrorDetails(
		code,
		"unknown tool arguments",
		"validation",
		map[string]any{"tool": spec.Name, "fields": unknown},
	)
}
func (r *Runtime) serverInfo() Result {
	names := r.ToolNames()

	// 严格校验 outputSchema 的客户端（如 Notion）会把 null 视为校验失败：
	// 所有声明为 array 的字段在为空时也必须序列化为 []，不能是 null。
	openCodeRuntimes := r.openCodeSummaries()
	if openCodeRuntimes == nil {
		openCodeRuntimes = []opencoderuntime.Summary{}
	}

	// server_info 是排障入口：这里按主题分组保留字段，避免新增运行能力时
	// 把自检输出重新堆成一行难以审查的 map。
	return Result{
		"server":           config.ServerName,
		"title":            "AgentDock",
		"version":          buildinfo.Version,
		"protocol_version": config.ProtocolVersion,

		"os":         runtime.GOOS,
		"arch":       runtime.GOARCH,
		"go_version": runtime.Version(),

		"agentdock_home":        r.cfg.AgentDockHome,
		"agentdock_default_dir": r.cfg.AgentDockDefaultDir,
		"default_cwd":           r.ws.DefaultDisplay(),
		"path_model":            config.PathModel,

		"recall_enabled":               r.cfg.NexusEndpoint != "",
		"nexus_endpoint":               r.cfg.NexusEndpoint,
		"recall_bootstrap_recommended": r.cfg.NexusEndpoint != "",
		"recall_bootstrap_tool":        "recall_bootstrap",
		"recall_bootstrap_args":        map[string]any{},

		"task_state_dir": r.tasks.Root(),
		"command_session_limits": map[string]any{
			"max_running":  toolcommand.MaxConcurrentSessions,
			"max_retained": toolcommand.MaxRetainedSessions,
		},

		"browser_enabled":     r.cfg.BrowserEnabled,
		"acp_enabled":         r.cfg.ACPEnabled,
		"acp_agent":           r.cfg.ACPAgentName,
		"opencode_enabled":    r.cfg.OpenCodeEnabled,
		"opencode_manifest":   r.cfg.OpenCodeRuntimeManifest,
		"opencode_runtimes":   openCodeRuntimes,
		"opencode_tools":      append([]string{}, opencoderuntime.RequiredTools...),
		"trusted_proxy_cidrs": append([]string{}, r.cfg.TrustedProxyCIDRs...),

		"degraded_startup": len(r.startupIssues) > 0,
		"startup_issues":   r.startupIssueList(),
		"subsystems":       r.subsystemStatus(),

		"auth_enabled":          r.authEnabled(),
		"endpoint_path":         "/mcp",
		"mcp_session_awareness": r.mcpSessions != nil,
		"mcp_session_state_file": func() string {
			if r.mcpSessions == nil {
				return ""
			}
			return r.mcpSessions.Path()
		}(),
		"mcp_call_log_file": func() string {
			if r.mcpSessions == nil {
				return ""
			}
			return r.mcpSessions.ActivityLogPath()
		}(),
		"tools":      names,
		"tool_count": len(names),
	}
}

func (r *Runtime) authEnabled() bool {
	return r.cfg.AuthRequired()
}

# MCP 会话引导与持续任务控制

AgentDock 0.7.8 将协议级会话识别、网页绑定与 Watchdog 控制接入同一运行时。
客户端协议身份来自 `_meta["openai/session"]`，普通参数不能替代该身份。

## 首次调用

共享 MCP 入口接受可选元数据：

- `agentdock/conversation_url`
- `agentdock/conversation_title`
- `agentdock/watchdog_task_id`

有已验证的完整对话 URL 时，任意普通工具的同一次调用先原子绑定，再执行业务。
缺少或无效元数据 URL 时仅跟踪匿名会话。同一绑定保持 bound_at；不同 URL 或不同非空 task ID 冲突会拒绝业务。
显式 bind 是有意更换绑定的入口。

客户端只能从可信提示获得 URL，或新增工具目录尚未同步时：

```json
{
  "action": "bootstrap",
  "conversation_url": "https://chatgpt.com/c/example",
  "conversation_title": "当前任务",
  "watchdog_task_id": "wd-example"
}
```

将上述参数传给原有 `agentdock_context`。读回 session 的 URL、task ID、bound_at 确认绑定。
不提供 URL 时如实返回当前匿名会话和上下文。不得按前台标签、标题、时间或 opaque session ID 猜测。

## 会话范围控制

`mcp_session_control` 支持 status、pause、interrupt、resume、abort；
原有 `agentdock_context` 支持同名动作，并保留 context 字符串以兼容缓存客户端。
`server_info.session_bootstrap_actions` 和 `mcp_session_control_actions` 可检测支持。

| 动作 | Watchdog 请求 | 生效条件 |
|---|---|---|
| status | GET overview | 只读取当前绑定和任务 |
| pause | pause | 当前任务观察到 PAUSED |
| interrupt | pause_immediate | 当前回复被停止，观察到 PAUSED_INTERRUPTED |
| resume | resume / resume_checkpoint | 活跃暂停任务或有可恢复 checkpoint 的已停止任务重新运行 |
| abort | abort | 观察到当前任务 ABORTED |

默认桥接 `http://127.0.0.1:8770/`，可用 `AGENTDOCK_WATCHDOG_DASHBOARD_URL` 配置。
仅允许 http/https loopback，拒绝重定向，限制响应大小；令牌只在桥接内部使用。
Dashboard API 的 task_id 必须属于精确绑定的 canonical target_url。
无绑定 task ID 时优先唯一运行任务，再选唯一可恢复任务；多个活动任务一律拒绝。
DONE/ABORTED 不恢复。已停止任务要求 dashboard.resumable=true；该标记来自实际 checkpoint 检查。

accepted=true 只表示服务接受请求。effective=true 依赖后续实际状态；
状态尚未变化或观察失败时保留 accepted=true、effective=false 和说明，避免盲目重试。
控制台重启导致明确的令牌拒绝时最多刷新一次令牌；不对不确定的传输失败重复控制。

Watchdog 控制网页回复与继续循环，不代表冻结独立命令、原生子代理或进程内存。
这些任务须通过各自当前会话所属的取消和恢复接口处理。

## 验证

SDK 测试覆盖首次普通调用、参数 fallback、匿名跟踪、稳定 bound_at、冲突阻止业务、并发隔离和身份伪造拒绝。
httptest dashboard 覆盖真实 Python overview 字段、恢复状态、目标归属、歧义、令牌更新、响应大小和 pending/effective。

# AgentDock 私有版：Windows 一键配置和可视化面板

本配置向导适用于本仓库的私有构建，不会下载或安装上游 uvwt/agentdock 二进制文件，只会启动用户指定的私有版 agentdock.exe。

## 最快上手

1. 从本仓库的 Windows 构建工件（或自行编译）获得 agentdock.exe，放到仓库根目录。也可以在面板内选择已有路径。
2. 双击仓库根目录的 **Start-AgentDock.cmd**，打开 Windows Forms 图形面板。
3. 设置工作目录、数据目录和监听端口（默认 8765）。
4. 选择连接方式：
   - **local**：只监听 127.0.0.1，不暴露公网，使用 Bearer 认证。
   - **ngrok-managed**：安装 ngrok v3，直接粘贴 ngrok Authtoken，并填写 ngrok.exe 路径（若在 PATH 可留空），可选粘贴预留 HTTPS URL。
   - **ngrok-existing**：已在外部启动 ngrok，粘贴其完整 HTTPS Origin（例如 https://example.ngrok.app，不能带 /mcp）。
5. 勾选“登录 Windows 时自动运行”，点保存。启动器会注册名为 **AgentDock Personal Launcher** 的 Windows 用户计划任务。
6. 点击“启动”，刷新状态。健康状态应该是 RUNNING；点击“复制 MCP 地址”获得公网 HTTPS + /mcp。
7. 公网 ChatGPT MCP 插件授权需要 OAuth，点击“复制 OAuth 密码”并在授权页面输入。切勿把密码粘贴到 GitHub 或公开聊天。

## 凭据与运行方式

- 设置文件：%LOCALAPPDATA%\AgentDock\launcher\settings.json。
- OAuth 密码、签名密钥、Bearer Token 和 ngrok Authtoken 均由 Windows DPAPI 按当前用户加密存储在 secrets.json，不是明文。切换 Windows 账户/机器需重新设置。
- 托管模式先启动 ngrok，从它的本地控制 API (127.0.0.1:4040/api/tunnels) 获取 HTTPS 入口，设置 AGENTDOCK_SERVER_URL 并强制 OAuth，再启动 AgentDock。
- AgentDock 始终监听回环地址。ngrok 提供公网 HTTPS 转发。ngrok 或 AgentDock 进程异常退出后，监督进程有限次数重试；连续失败五次则停止。
- 自动任务在**当前用户登录**后运行，不是系统无人登录时启动；使用交互用户权限，不保存用户 Windows 密码。
- 此向导的配置文件独立于已有图形安装器的 control-panel-settings.json；同一端口不要同时启动两份服务。
- ngrok Token 会经由子进程环境变量传递，避免出现在命令行参数中。
- 临时 ngrok URL 每次可能不同；变化后需要更新 ChatGPT MCP 连接，稳定地址应使用 ngrok 已保留域名。
- 日志：%LOCALAPPDATA%\AgentDock\launcher\supervisor.log、agentdock.stderr.log 和 ngrok.stderr.log。若显示 ERROR，请查看日志与面板的 detail 字段。
- 请勿使用旧 README 的官方自动更新来升级你的私有版本：源码的自更新 API 默认仍指向上游项目。

## 验证与管理

本机健康检查：

    curl.exe http://127.0.0.1:8765/healthz

MCP 地址为面板显示的 HTTPS Origin 加 /mcp，公网上必须启用 OAuth。停止服务可在面板按“停止”，或：

    powershell -NoProfile -ExecutionPolicy Bypass -File scripts\windows\AgentDock.Runtime.ps1 -Mode stop

取消勾选“登录 Windows 时自动运行”并保存，可以取消计划任务。再次点击“启动”重启服务。

## OpenCode 扩展

AgentDock 的 D2/D3 原生子 Agent 是可选集成，还需另外部署与当前版本兼容的 OpenCode runtime manifest 与原生 HTTP 服务，配置 AGENTDOCK_OPENCODE_ENABLED、AGENTDOCK_OPENCODE_RUNTIME_MANIFEST、AGENTDOCK_D3_NATIVE_URL。本一键向导不会声称自动安装了 OpenCode，也不会替换其独立配置。

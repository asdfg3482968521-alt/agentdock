# Windows Forms configuration panel for the personal AgentDock build.
# Secrets stay in DPAPI user-encrypted LocalAppData. Only the user can reveal the OAuth password.
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
[System.Windows.Forms.Application]::EnableVisualStyles()
$ErrorActionPreference = "Stop"
$RepoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$Supervisor = Join-Path $PSScriptRoot "AgentDock.Runtime.ps1"
$Root = Join-Path $env:LOCALAPPDATA "AgentDock\launcher"
$SettingsPath = Join-Path $Root "settings.json"
$SecretsPath = Join-Path $Root "secrets.json"
$StatePath = Join-Path $Root "state.json"
$TaskName = "AgentDock Personal Launcher"
New-Item -Path $Root -ItemType Directory -Force | Out-Null

function Random-Secret {
    $bytes = New-Object byte[] 32
    $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
    return [BitConverter]::ToString($bytes).Replace("-","").ToLowerInvariant()
}
function Encrypt-Secret([string]$Value) {
    return ConvertFrom-SecureString (ConvertTo-SecureString $Value -AsPlainText -Force)
}
function Decrypt-Secret([string]$Value) {
    if (-not $Value) { return "" }
    $secure = ConvertTo-SecureString $Value
    $ptr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
    try { return [Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr) }
    finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr) }
}
function Valid-Origin([string]$Url) {
    $uri = $null
    if (-not [Uri]::TryCreate($Url,[UriKind]::Absolute,[ref]$uri)) { return $false }
    return $uri.Scheme -eq "https" -and -not $uri.UserInfo -and $uri.AbsolutePath -eq "/" -and -not $uri.Query -and -not $uri.Fragment
}
function Alert([string]$Message) {
    [void][System.Windows.Forms.MessageBox]::Show($Message, "AgentDock", [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Information)
}
$cfg = @{
    mode = "local"; port = 8765
    agentdock_path = (Join-Path $RepoRoot "agentdock.exe")
    ngrok_path = ""
    public_url = ""
    reserved_url = ""
    home_path = (Join-Path $env:USERPROFILE ".agentdock")
    workspace_path = (Join-Path $env:USERPROFILE "AgentDock")
    opencode_manifest = ""
    native_url = ""
    opencode_project_root = ""
    watchdog_url = "http://127.0.0.1:8770/"
}
if (Test-Path $SettingsPath) {
    $saved = Get-Content $SettingsPath -Raw | ConvertFrom-Json
    foreach ($name in @($cfg.Keys)) { if ($null -ne $saved.$name) { $cfg[$name] = $saved.$name } }
}
if (-not (Test-Path $cfg.agentdock_path)) {
    $alternative = Join-Path $RepoRoot "bin\agentdock.exe"
    if (Test-Path $alternative) { $cfg.agentdock_path = $alternative }
}
$knownSecrets = @{}
if (Test-Path $SecretsPath) {
    $stored = Get-Content $SecretsPath -Raw | ConvertFrom-Json
    foreach ($key in @("bearer_token","oauth_password","oauth_signing_key","ngrok_authtoken")) {
        if ($stored.$key) { $knownSecrets[$key] = [string]$stored.$key }
    }
}
$form = New-Object System.Windows.Forms.Form
$form.Text = "AgentDock | 一键配置与运行面板"
$form.Size = New-Object System.Drawing.Size(860,975)
$form.AutoScroll = $true
$form.StartPosition = "CenterScreen"
$form.MinimumSize = New-Object System.Drawing.Size(850,720)
$form.Font = New-Object System.Drawing.Font("Microsoft YaHei UI",9)
$form.BackColor = [System.Drawing.Color]::FromArgb(247,249,252)

$header = New-Object System.Windows.Forms.Label
$header.Text = "AgentDock 私有版本配置"
$header.Location = New-Object System.Drawing.Point(22,16)
$header.Size = New-Object System.Drawing.Size(790,36)
$header.Font = New-Object System.Drawing.Font("Microsoft YaHei UI",16,[System.Drawing.FontStyle]::Bold)
$form.Controls.Add($header)
$sub = New-Object System.Windows.Forms.Label
$sub.Text = "配置一次即可启动，支持 ngrok 粘贴、OAuth、健康状态和 Windows 登录自动任务。"
$sub.Location = New-Object System.Drawing.Point(24,54)
$sub.Size = New-Object System.Drawing.Size(805,25)
$form.Controls.Add($sub)

function Field([string]$Label, [int]$Y, [string]$Value, [bool]$Masked=$false) {
    $lab = New-Object System.Windows.Forms.Label
    $lab.Text=$Label; $lab.Location=New-Object System.Drawing.Point(24,$Y)
    $lab.Size=New-Object System.Drawing.Size(213,25)
    $form.Controls.Add($lab)
    $box = New-Object System.Windows.Forms.TextBox
    $box.Location = New-Object System.Drawing.Point(240,($Y-3))
    $box.Size = New-Object System.Drawing.Size(575,27)
    $box.Text = $Value
    $box.UseSystemPasswordChar = $Masked
    $form.Controls.Add($box)
    return $box
}
$exe = Field "AgentDock 可执行文件" 100 ([string]$cfg.agentdock_path)
$ngrok = Field "ngrok.exe 路径（托管模式）" 140 ([string]$cfg.ngrok_path)
$modeLabel = New-Object System.Windows.Forms.Label
$modeLabel.Text = "连接方式"; $modeLabel.Location = New-Object System.Drawing.Point(24,180)
$modeLabel.Size=New-Object System.Drawing.Size(210,25)
$form.Controls.Add($modeLabel)
$mode = New-Object System.Windows.Forms.ComboBox
$mode.DropDownStyle = "DropDownList"
$mode.Items.AddRange(@("local","ngrok-managed","ngrok-existing"))
$mode.Location=New-Object System.Drawing.Point(240,177)
$mode.Size=New-Object System.Drawing.Size(575,29)
$mode.SelectedItem=[string]$cfg.mode
if ($mode.SelectedIndex -lt 0) { $mode.SelectedIndex=0 }
$form.Controls.Add($mode)
$token = Field "ngrok Authtoken（粘贴）" 220 "" $true
$url = Field "已运行的 ngrok HTTPS 地址" 260 ([string]$cfg.public_url)
$reserved = Field "ngrok 保留域名 URL（可选）" 300 ([string]$cfg.reserved_url)
$port = Field "本地监听端口" 340 ([string]$cfg.port)
$home = Field "AgentDock 数据目录" 380 ([string]$cfg.home_path)
$workspace = Field "默认工作目录" 420 ([string]$cfg.workspace_path)

$manifest = Field "OpenCode Manifest（可选）" 460 ([string]$cfg.opencode_manifest)
$native = Field "OpenCode 原生 HTTP URL" 500 ([string]$cfg.native_url)
$opencodeRoot = Field "OpenCode 项目根目录" 540 ([string]$cfg.opencode_project_root)
$watchdog = Field "Watchdog 本地面板 URL" 580 ([string]$cfg.watchdog_url)
$tip = New-Object System.Windows.Forms.Label
$tip.Location = New-Object System.Drawing.Point(24,623)
$tip.Size = New-Object System.Drawing.Size(795,46)
$tip.Text = "托管模式粘贴 Authtoken，并安装 ngrok v3。已有地址模式须先在外部启动 ngrok。公网使用 OAuth，临时 URL 变化后须重新配置 ChatGPT 连接。"
$form.Controls.Add($tip)
$auto = New-Object System.Windows.Forms.CheckBox
$auto.Location = New-Object System.Drawing.Point(24,677)
$auto.Size = New-Object System.Drawing.Size(770,27)
$auto.Text = "登录 Windows 时自动运行（当前用户计划任务，非 SYSTEM）"
$auto.Checked = [bool](Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue)
$form.Controls.Add($auto)

function ActionButton([string]$Text,[int]$X,[int]$Y,[int]$Width) {
    $btn = New-Object System.Windows.Forms.Button
    $btn.Text=$Text; $btn.Location=New-Object System.Drawing.Point($X,$Y)
    $btn.Size=New-Object System.Drawing.Size($Width,36)
    $form.Controls.Add($btn)
    return $btn
}
$saveButton=ActionButton "保存配置" 24 720 145
$startButton=ActionButton "启动" 180 720 112
$stopButton=ActionButton "停止" 303 720 112
$refreshButton=ActionButton "刷新状态" 426 720 117
$copyUrl=ActionButton "复制 MCP 地址" 554 720 130
$copyPassword=ActionButton "复制 OAuth 密码" 694 720 126
$stateText = New-Object System.Windows.Forms.TextBox
$stateText.Multiline = $true
$stateText.ScrollBars = "Vertical"
$stateText.ReadOnly = $true
$stateText.Location = New-Object System.Drawing.Point(24,770)
$stateText.Size = New-Object System.Drawing.Size(796,140)
$form.Controls.Add($stateText)

function Refresh-Status {
    if (Test-Path $StatePath) {
        try {
            $state = Get-Content $StatePath -Raw | ConvertFrom-Json
            $process = if ($state.agent_pid) { Get-Process -Id ([int]$state.agent_pid) -ErrorAction SilentlyContinue } else { $null }
            $health = if ($state.health -eq "RUNNING" -and -not $process) { "STALE / NOT RUNNING" } else { $state.health }
            $stateText.Text = "状态: $health" + [Environment]::NewLine + "MCP: $($state.mcp_url)" + [Environment]::NewLine + "AgentDock PID: $($state.agent_pid); ngrok PID: $($state.ngrok_pid)" + [Environment]::NewLine + "更新时间: $($state.updated_at)" + [Environment]::NewLine + "提示: $($state.detail)"
        } catch { $stateText.Text = "读取状态失败: $($_.Exception.Message)" }
    } else { $stateText.Text = "尚未运行。先配置 AgentDock 可执行文件并保存。" }
}
function Save-Settings {
    if (-not (Test-Path -LiteralPath $exe.Text -PathType Leaf)) { throw "找不到 agentdock.exe，请填写实际路径。" }
    $num=0
    if (-not [int]::TryParse($port.Text,[ref]$num) -or $num -lt 1 -or $num -gt 65535) { throw "端口必须是 1-65535。" }
    if ($mode.SelectedItem -eq "ngrok-existing" -and -not (Valid-Origin $url.Text.Trim())) { throw "请粘贴有效的 ngrok HTTPS Origin，不能带 /mcp。" }
    if ($reserved.Text.Trim() -and -not (Valid-Origin $reserved.Text.Trim())) { throw "保留域名必须是有效 HTTPS Origin。" }
    if (-not [IO.Path]::IsPathRooted($home.Text) -or -not [IO.Path]::IsPathRooted($workspace.Text)) { throw "数据目录和工作目录必须是绝对路径。" }
    if ($manifest.Text.Trim() -and -not (Test-Path $manifest.Text.Trim() -PathType Leaf)) { throw "OpenCode runtime manifest 文件不存在。" }
    if ($native.Text.Trim() -and $native.Text.Trim() -notmatch "^https?://") { throw "OpenCode URL 必须以 http(s):// 开头。" }
    if ($watchdog.Text.Trim() -and $watchdog.Text.Trim() -notmatch "^https?://(localhost|127\.0\.0\.1|\[::1\])(?::\d+)?/?$") { throw "Watchdog URL 只允许本机地址。" }
    $config = [ordered]@{
        agentdock_path=[IO.Path]::GetFullPath($exe.Text.Trim())
        ngrok_path=$ngrok.Text.Trim()
        mode=[string]$mode.SelectedItem
        public_url=$url.Text.Trim().TrimEnd("/")
        reserved_url=$reserved.Text.Trim().TrimEnd("/")
        port=$num
        home_path=[IO.Path]::GetFullPath($home.Text.Trim())
        workspace_path=[IO.Path]::GetFullPath($workspace.Text.Trim())
        opencode_manifest=$manifest.Text.Trim()
        native_url=$native.Text.Trim()
        opencode_project_root=$opencodeRoot.Text.Trim()
        watchdog_url=$watchdog.Text.Trim()
    }
    $secrets=@{}
    foreach ($name in @("bearer_token","oauth_password","oauth_signing_key","ngrok_authtoken")) {
        if ($knownSecrets.ContainsKey($name)) { $secrets[$name]=$knownSecrets[$name] }
    }
    foreach ($name in @("bearer_token","oauth_password","oauth_signing_key")) {
        if (-not $secrets[$name]) { $secrets[$name] = Encrypt-Secret (Random-Secret) }
    }
    if ($token.Text.Trim()) { $secrets["ngrok_authtoken"] = Encrypt-Secret $token.Text.Trim() }
    if ($config.mode -eq "ngrok-managed" -and -not $secrets["ngrok_authtoken"]) { throw "托管 ngrok 模式请先粘贴 Authtoken。" }
    $secrets | ConvertTo-Json | Set-Content -LiteralPath $SecretsPath -Encoding UTF8
    $config | ConvertTo-Json | Set-Content -LiteralPath $SettingsPath -Encoding UTF8
    $script:knownSecrets=$secrets
    $token.Text = ""
    $user=[Security.Principal.WindowsIdentity]::GetCurrent().Name
    if ($auto.Checked) {
        $action=New-ScheduledTaskAction -Execute (Join-Path $PSHOME "powershell.exe") -Argument ('-NoProfile -NonInteractive -ExecutionPolicy Bypass -WindowStyle Hidden -File "' + $Supervisor + '" -Mode run')
        $trigger=New-ScheduledTaskTrigger -AtLogOn -User $user
        $principal=New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel LeastPrivilege
        $settings=New-ScheduledTaskSettingsSet -ExecutionTimeLimit (New-TimeSpan -Seconds 0) -MultipleInstances IgnoreNew
        Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
    } else {
        Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
    }
}
$saveButton.Add_Click({
    try { Save-Settings; Alert "保存成功。凭据使用当前用户的 Windows DPAPI 加密；自动任务设置已同步。"; Refresh-Status }
    catch { Alert "配置失败：$($_.Exception.Message)" }
})
$startButton.Add_Click({
    try {
        Save-Settings
        $current=$null
        if (Test-Path $StatePath) { $current=Get-Content $StatePath -Raw | ConvertFrom-Json }
        if ($current -and $current.agent_pid -and (Get-Process -Id ([int]$current.agent_pid) -ErrorAction SilentlyContinue)) { Alert "已经在运行，先停止后再应用新配置。"; return }
        Start-Process -FilePath (Join-Path $PSHOME "powershell.exe") -ArgumentList @("-NoProfile","-NonInteractive","-ExecutionPolicy","Bypass","-WindowStyle","Hidden","-File",('"' + $Supervisor + '"'),"-Mode","run") -WindowStyle Hidden | Out-Null
        Alert "已提交启动请求；请刷新状态，查看健康检查结果。"
    } catch { Alert "启动失败：$($_.Exception.Message)" }
})
$stopButton.Add_Click({
    try {
        & $Supervisor -Mode stop
        Refresh-Status
    } catch { Alert "停止失败：$($_.Exception.Message)" }
})
$refreshButton.Add_Click({ Refresh-Status })
$copyUrl.Add_Click({
    try {
        $s=Get-Content $StatePath -Raw | ConvertFrom-Json
        if (-not $s.mcp_url) { throw "当前还没有 MCP 地址。" }
        [System.Windows.Forms.Clipboard]::SetText([string]$s.mcp_url)
        Alert "MCP 地址已复制到剪贴板。"
    } catch { Alert $_.Exception.Message }
})
$copyPassword.Add_Click({
    try {
        $secrets = Get-Content $SecretsPath -Raw | ConvertFrom-Json
        $password = Decrypt-Secret ([string]$secrets.oauth_password)
        if (-not $password) { throw "请先保存配置。" }
        [System.Windows.Forms.Clipboard]::SetText($password)
        Alert "OAuth 密码已复制；剪贴板含敏感信息，请使用后及时清除。"
    } catch { Alert $_.Exception.Message }
})
$timer=New-Object System.Windows.Forms.Timer
$timer.Interval=5000
$timer.Add_Tick({ Refresh-Status })
$timer.Start()
Refresh-Status
[void]$form.ShowDialog()
$timer.Stop()

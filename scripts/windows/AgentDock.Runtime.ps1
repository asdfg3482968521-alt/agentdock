# AgentDock Windows supervisor. Use via Start-AgentDock.cmd (same Windows user).
param([ValidateSet("run","stop","status")][string]$Mode = "run")
$ErrorActionPreference = "Stop"
$Root = Join-Path $env:LOCALAPPDATA "AgentDock\launcher"
$SettingsPath = Join-Path $Root "settings.json"
$SecretsPath = Join-Path $Root "secrets.json"
$StatePath = Join-Path $Root "state.json"
$StopPath = Join-Path $Root "stop.signal"
$LogPath = Join-Path $Root "supervisor.log"
New-Item -ItemType Directory -Force -Path $Root | Out-Null
function Log([string]$Message) { Add-Content -LiteralPath $LogPath -Value ("[{0}] {1}" -f (Get-Date -Format o), $Message) -Encoding UTF8 }
function Get-Secret([string]$Name) {
    if (-not (Test-Path $SecretsPath)) { return "" }
    $db = Get-Content $SecretsPath -Raw | ConvertFrom-Json
    $cipher = $db.$Name
    if (-not $cipher) { return "" }
    $secure = ConvertTo-SecureString $cipher
    $pointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
    try { return [Runtime.InteropServices.Marshal]::PtrToStringBSTR($pointer) }
    finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($pointer) }
}
function Read-State {
    if (-not (Test-Path $StatePath)) { return $null }
    try { return Get-Content $StatePath -Raw | ConvertFrom-Json } catch { return $null }
}
function Alive([int]$ProcessId, [string]$Expected) {
    if ($ProcessId -le 0) { return $false }
    $proc = Get-Process -Id $ProcessId -ErrorAction SilentlyContinue
    return [bool]($proc -and $proc.ProcessName -ieq $Expected)
}
function Kill-Owned([int]$ProcessId, [string]$Expected) {
    if (Alive $ProcessId $Expected) { Stop-Process -Id $ProcessId -Force -ErrorAction SilentlyContinue }
}
function Write-State([string]$Health, [string]$Origin, [int]$AgentPid, [int]$NgrokPid, [string]$Detail) {
    $port = if ($cfg) { [int]$cfg.port } else { 8765 }
    $mcp = if ($Origin) { "$Origin/mcp" } else { "http://127.0.0.1:$port/mcp" }
    $state = [ordered]@{ health=$Health; origin=$Origin; mcp_url=$mcp; agent_pid=$AgentPid; ngrok_pid=$NgrokPid; updated_at=(Get-Date).ToString("o"); detail=$Detail }
    $temp = "$StatePath.tmp.$PID"
    $state | ConvertTo-Json | Set-Content -LiteralPath $temp -Encoding UTF8
    Move-Item -LiteralPath $temp -Destination $StatePath -Force
}
function Require-Origin([string]$Url) {
    $uri = $null
    if (-not [Uri]::TryCreate($Url, [UriKind]::Absolute, [ref]$uri)) { throw "Invalid public URL." }
    if ($uri.Scheme -ne "https" -or $uri.UserInfo -or $uri.AbsolutePath -ne "/" -or $uri.Query -or $uri.Fragment) {
        throw "Public URL must be an HTTPS origin without paths or query."
    }
    return $uri.GetLeftPart([UriPartial]::Authority).TrimEnd("/")
}
if ($Mode -eq "status") {
    $state = Read-State
    if ($state) { $state | ConvertTo-Json } else { '{"health":"NOT_STARTED"}' }
    exit 0
}
if ($Mode -eq "stop") {
    New-Item -ItemType File -Path $StopPath -Force | Out-Null
    $state = Read-State
    if ($state) { Kill-Owned ([int]$state.agent_pid) "agentdock"; Kill-Owned ([int]$state.ngrok_pid) "ngrok" }
    try { Stop-ScheduledTask -TaskName "AgentDock Personal Launcher" -ErrorAction SilentlyContinue } catch {}
    Write-State "STOPPED" "" 0 0 "Stopped by user."
    Log "User requested stop."
    exit 0
}
if (-not (Test-Path $SettingsPath)) { throw "Settings missing; open Start-AgentDock.cmd and save settings first." }
$cfg = Get-Content $SettingsPath -Raw | ConvertFrom-Json
if ([int]$cfg.port -lt 1 -or [int]$cfg.port -gt 65535) { throw "Invalid port." }
if (-not (Test-Path -LiteralPath $cfg.agentdock_path -PathType Leaf)) { throw "agentdock.exe missing: $($cfg.agentdock_path)" }
$previous = Read-State
if ($previous -and (Alive ([int]$previous.agent_pid) "agentdock")) { Log "Already running, skip duplicate."; exit 0 }
Remove-Item $StopPath -Force -ErrorAction SilentlyContinue
$env:AGENTDOCK_AUTH_TOKEN = Get-Secret "bearer_token"
if (-not $env:AGENTDOCK_AUTH_TOKEN) { throw "Bearer token not configured." }
$oauthPassword = Get-Secret "oauth_password"
$oauthSigningKey = Get-Secret "oauth_signing_key"
$ngrokAuthtoken = Get-Secret "ngrok_authtoken"
$env:AGENTDOCK_HOST = "127.0.0.1"
$env:AGENTDOCK_PORT = [string]$cfg.port
$env:AGENTDOCK_HOME = [string]$cfg.home_path
$env:AGENTDOCK_DEFAULT_DIR = [string]$cfg.workspace_path
$env:AGENTDOCK_OPENCODE_ENABLED = if ($cfg.opencode_manifest) { "true" } else { "false" }
$env:AGENTDOCK_OPENCODE_RUNTIME_MANIFEST = [string]$cfg.opencode_manifest
$env:AGENTDOCK_D3_NATIVE_URL = [string]$cfg.native_url
$env:AGENTDOCK_OPENCODE_PROJECT_ROOT = [string]$cfg.opencode_project_root
$env:AGENTDOCK_WATCHDOG_DASHBOARD_URL = [string]$cfg.watchdog_url
$env:AGENTDOCK_OAUTH_ACCESS_TOKEN_TTL = "1h"
$env:AGENTDOCK_OAUTH_ENABLED = "false"
$env:AGENTDOCK_OAUTH_REQUIRED = "false"
$env:AGENTDOCK_SERVER_URL = ""
$modeValue = [string]$cfg.mode
if ($modeValue -notin @("local","ngrok-managed","ngrok-existing")) { throw "Unknown connection mode." }
if ($modeValue -ne "local" -and ($oauthPassword.Length -lt 12 -or $oauthSigningKey.Length -lt 32)) { throw "OAuth credentials invalid." }
New-Item -ItemType Directory -Force -Path $cfg.home_path,$cfg.workspace_path | Out-Null
$ngrokPath = [string]$cfg.ngrok_path
if (-not $ngrokPath) { $cmd = Get-Command ngrok.exe -ErrorAction SilentlyContinue; if ($cmd) { $ngrokPath = $cmd.Source } }
$failureCount = 0
while (-not (Test-Path $StopPath)) {
    $ngrokProcess = $null
    $agentProcess = $null
    try {
        $origin = ""
        if ($modeValue -eq "ngrok-existing") {
            $origin = Require-Origin ([string]$cfg.public_url)
        } elseif ($modeValue -eq "ngrok-managed") {
            if (-not $ngrokPath -or -not (Test-Path $ngrokPath -PathType Leaf)) { throw "ngrok.exe is not installed or path is incorrect." }
            if (-not $ngrokAuthtoken) { throw "Paste your ngrok Authtoken into the launcher." }
            $env:NGROK_AUTHTOKEN = $ngrokAuthtoken
            $ngrokArgs = @("http", [string]$cfg.port)
            if ($cfg.reserved_url) { $ngrokArgs += @("--url", (Require-Origin ([string]$cfg.reserved_url))) }
            $ngrokProcess = Start-Process -FilePath $ngrokPath -ArgumentList $ngrokArgs -PassThru -WindowStyle Hidden -RedirectStandardOutput (Join-Path $Root "ngrok.stdout.log") -RedirectStandardError (Join-Path $Root "ngrok.stderr.log")
            Remove-Item Env:NGROK_AUTHTOKEN -ErrorAction SilentlyContinue
            for ($i=0; $i -lt 45; $i++) {
                $ngrokProcess.Refresh()
                if ($ngrokProcess.HasExited) { throw "ngrok exited before tunnel was ready." }
                try {
                    $api = Invoke-RestMethod -Uri "http://127.0.0.1:4040/api/tunnels" -TimeoutSec 2
                    $endpoint = $api.tunnels | Where-Object { $_.public_url -like "https://*" } | Select-Object -First 1
                    if ($endpoint) { $origin = Require-Origin ([string]$endpoint.public_url); break }
                } catch {}
                Start-Sleep -Seconds 1
            }
            if (-not $origin) { throw "ngrok local API on 127.0.0.1:4040 did not return an HTTPS URL." }
            if ($cfg.reserved_url -and $origin -ne (Require-Origin ([string]$cfg.reserved_url))) { throw "Reserved ngrok URL mismatch." }
        }
        if ($modeValue -ne "local") {
            $env:AGENTDOCK_SERVER_URL = $origin
            $env:AGENTDOCK_OAUTH_ENABLED = "true"
            $env:AGENTDOCK_OAUTH_REQUIRED = "true"
            $env:AGENTDOCK_OAUTH_PASSWORD = $oauthPassword
            $env:AGENTDOCK_OAUTH_TOKEN_SECRET = $oauthSigningKey
        }
        $ngrokPid = if ($ngrokProcess) { $ngrokProcess.Id } else { 0 }
        Write-State "STARTING" $origin 0 $ngrokPid ""
        $agentProcess = Start-Process -FilePath $cfg.agentdock_path -PassThru -WindowStyle Hidden -WorkingDirectory (Split-Path -Parent $cfg.agentdock_path) -RedirectStandardOutput (Join-Path $Root "agentdock.stdout.log") -RedirectStandardError (Join-Path $Root "agentdock.stderr.log")
        $ready = $false
        for ($j=0; $j -lt 30; $j++) {
            $agentProcess.Refresh()
            if ($agentProcess.HasExited) { throw "AgentDock exited before health check." }
            try {
                $probe = Invoke-WebRequest -Uri "http://127.0.0.1:$($cfg.port)/healthz" -TimeoutSec 2 -UseBasicParsing
                if ($probe.StatusCode -eq 200) { $ready=$true; break }
            } catch {}
            Start-Sleep -Seconds 1
        }
        if (-not $ready) { throw "Health check timeout; see agentdock.stderr.log." }
        Write-State "RUNNING" $origin $agentProcess.Id $ngrokPid ""
        Log "Healthy mode=$modeValue origin=$origin"
        $failureCount = 0
        while (-not (Test-Path $StopPath)) {
            Start-Sleep -Seconds 3
            $agentProcess.Refresh()
            if ($agentProcess.HasExited) { throw "AgentDock unexpectedly exited." }
            if ($ngrokProcess) { $ngrokProcess.Refresh(); if ($ngrokProcess.HasExited) { throw "ngrok unexpectedly exited." } }
        }
    } catch {
        $failureCount++
        $reason = $_.Exception.Message
        Log "Supervisor failure: $reason"
        Write-State "ERROR" "" 0 0 $reason
        if ($failureCount -ge 5) { break }
        Start-Sleep -Seconds ([Math]::Min($failureCount*5,30))
    } finally {
        if ($agentProcess) { Kill-Owned $agentProcess.Id "agentdock" }
        if ($ngrokProcess) { Kill-Owned $ngrokProcess.Id "ngrok" }
    }
}
if (Test-Path $StopPath) { Write-State "STOPPED" "" 0 0 "Stopped by user." }
Log "Supervisor stopped."

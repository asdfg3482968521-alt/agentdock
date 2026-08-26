param(
    [string]$Root = 'E:/AI_Code/GPTTaskBoard',
    [string]$AgentDockSha = '',
    [string]$RuntimeSha = 'd5664f38002d8326eca179cf2efb5774c8ce1247',
    [int]$NativePort = 4096
)

# D3-I revised production startup wiring.
#
#  1. Start the OpenCode native autonomous server (loopback 127.0.0.1:<port>).
#  2. Export AGENTDOCK_D3_NATIVE_URL so provider=opencode-native routes to the
#     native autonomous backend.
#  3. Launch AgentDock service launch-core with the frozen OpenCode runtime
#     manifest and the normal (not locked) AgentDock service update path.
$ErrorActionPreference = 'Stop'

if ([string]::IsNullOrWhiteSpace($AgentDockSha)) {
    throw 'AgentDockSha is required (e.g. the D3_I_REVISED_FINAL_SHA)'
}

$nativeStart = Join-Path $PSScriptRoot 'start-d3-i-native-server.ps1'
& $nativeStart -Root $Root -Port $NativePort -RuntimeSha $RuntimeSha
if ($LASTEXITCODE -ne 0) { throw "native server did not become ready on port $NativePort" }

$env:AGENTDOCK_HOME = Join-Path $Root 'data/state'
$env:AGENTDOCK_DEFAULT_DIR = Join-Path $Root 'data/taskboard'
$env:AGENTDOCK_OPENCODE_ENABLED = '1'
$env:AGENTDOCK_OPENCODE_RUNTIME_MANIFEST = Join-Path $Root 'config/opencode/verified-runtime.json'
$env:AGENTDOCK_OPENCODE_LOG_DIR = Join-Path $Root 'logs/opencode'
$env:AGENTDOCK_JOB_STORAGE_ROOT = Join-Path $Root 'data/jobs'
$env:AGENTDOCK_D3_NATIVE_URL = "http://127.0.0.1:$NativePort"
$env:TEMP = Join-Path $Root 'temp'
$env:TMP = $env:TEMP

$agentDock = Join-Path $Root ("runtime/agentdock/versions/$AgentDockSha/agentdock.exe")
if (-not (Test-Path $agentDock)) { throw "revised AgentDock binary missing: $agentDock" }

& $agentDock service launch-core --runtime-root (Join-Path $Root 'config/agentdock/desktop-runtime')
exit $LASTEXITCODE

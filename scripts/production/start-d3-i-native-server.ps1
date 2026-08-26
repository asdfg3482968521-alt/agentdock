param(
    [string]$Root = 'E:/AI_Code/GPTTaskBoard',
    [int]$Port = 4096,
    [string]$RuntimeSha = 'd5664f38002d8326eca179cf2efb5774c8ce1247'
)

# D3-I native autonomous production server owner.
#
# Contract (traced from the frozen OpenCode runtime):
#   serving process  = <runtime>/bun/bin/bun.exe
#   command          = bun run --conditions=browser <workspace>/packages/opencode/src/index.ts
#                      --pure serve --hostname 127.0.0.1 --port <port>
#   host             = 127.0.0.1 (loopback)
#   port             = 4096 (frozen production allocation)
#   base URL         = http://127.0.0.1:4096
#   readiness        = GET /session/status?directory= -> 200 (plus GET /provider -> 200)
#   startup ownership = this script (started before AgentDock launch-core)
#   restart behavior  = stop the bun process tree, then re-run this script
#   persistence       = process stays alive for the lifetime of production;
#                       AgentDock consumes AGENTDOCK_D3_NATIVE_URL and never
#                       owns/restarts the native server process
$ErrorActionPreference = 'Stop'

$runtime = "$Root/runtime/opencode/versions/$RuntimeSha"
$bun = "$runtime/bun/bin/bun.exe"
$index = "$runtime/workspace/packages/opencode/src/index.ts"
$cwd = "$runtime/workspace/packages/opencode"

if (-not (Test-Path $bun)) { throw "frozen runtime bun missing: $bun" }
if (-not (Test-Path $index)) { throw "frozen runtime opencode entry missing: $index" }

$logDir = "$Root/logs/opencode/native"
New-Item -ItemType Directory -Force -Path $logDir | Out-Null
$outLog = "$logDir/native-serve.stdout.log"
$errLog = "$logDir/native-serve.stderr.log"

$url = "http://127.0.0.1:$Port"

try {
    $probe = Invoke-WebRequest -UseBasicParsing -TimeoutSec 2 ("$url/session/status?directory=")
    if ($probe.StatusCode -eq 200) {
        Write-Output "NATIVE_ALREADY_READY $url"
        exit 0
    }
} catch {
    # not ready yet; continue to start
}

$arguments = @('run', '--conditions=browser', $index, '--pure', 'serve', '--hostname', '127.0.0.1', '--port', [string]$Port)
$process = Start-Process -FilePath $bun -ArgumentList $arguments -WorkingDirectory $cwd -WindowStyle Hidden -RedirectStandardOutput $outLog -RedirectStandardError $errLog -PassThru
Write-Output "NATIVE_PID $($process.Id)"

for ($i = 0; $i -lt 60; $i++) {
    Start-Sleep -Seconds 1
    try {
        $probe = Invoke-WebRequest -UseBasicParsing -TimeoutSec 2 ("$url/session/status?directory=")
        if ($probe.StatusCode -eq 200) {
            Write-Output "NATIVE_READY $url"
            exit 0
        }
    } catch {
        # continue polling
    }
}

Write-Output "NATIVE_NOT_READY $url"
exit 1

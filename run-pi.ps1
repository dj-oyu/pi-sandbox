param(
    [Parameter(Mandatory = $false)]
    [string]$Workspace = (Get-Location).Path,

    [Parameter(Mandatory = $false)]
    [string]$RefsDir = "",

    [Parameter(Mandatory = $false)]
    [string]$GitHubToken = $env:GITHUB_TOKEN,

    [Parameter(Mandatory = $false)]
    [string]$Image = "pi-sandbox:latest",

    [Parameter(Mandatory = $false)]
    [string]$Model = "auto",

    [Parameter(Mandatory = $false)]
    [string]$ShmSize = "",

    [Parameter(Mandatory = $false)]
    [string]$Prompt = "",

    [Parameter(Mandatory = $false)]
    [string]$PromptFile = "",

    [Parameter(Mandatory = $false)]
    [switch]$NonInteractive,

    [Parameter(Mandatory = $false)]
    [switch]$WithAwsEmulator,

    [Parameter(Mandatory = $false)]
    [switch]$UpdateAwsEmulator,

    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$PiArgs
)

$ErrorActionPreference = "Stop"
$SandboxDir = $PSScriptRoot
if (-not $RefsDir) {
    $RefsDir = Join-Path $SandboxDir "refs"
}
$LogsDir = Join-Path $SandboxDir "logs"
$SessionsDir = Join-Path $LogsDir "sessions"
$PiConfigDir = Join-Path $SandboxDir "pi-config"

New-Item -ItemType Directory -Force -Path $Workspace | Out-Null
New-Item -ItemType Directory -Force -Path $RefsDir | Out-Null
New-Item -ItemType Directory -Force -Path $SessionsDir | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $PiConfigDir "skills") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $PiConfigDir "extensions") | Out-Null

# Ensure pi-proxy.exe is built
$proxyExe = Join-Path $SandboxDir "pi-proxy.exe"
if (-not (Test-Path $proxyExe)) {
    Write-Host "[pi-sandbox] Building pi-proxy.exe via wslc..." -ForegroundColor Cyan
    $goDir = Join-Path $SandboxDir "go-proxy"
    & wslc run --rm -u root -w /src --entrypoint sh -v "${goDir}:/src:rw" -v "${SandboxDir}:/out:rw" pi-sandbox:latest -c "GOOS=windows GOARCH=amd64 go build -o /out/pi-proxy.exe ."
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to build pi-proxy.exe"
    }
}

# Detect WSL vEthernet host IP dynamically so it survives Windows reboots
$wslIp = (Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue |
    Where-Object { $_.InterfaceAlias -like "*WSL*" } |
    Select-Object -First 1 -ExpandProperty IPAddress)
if (-not $wslIp) {
    $wslIp = "127.0.0.1"
}

# Resolve loaded Sync-Strata model ID automatically when -Model auto
if ($Model -eq "auto") {
    try {
        $modelsResp = Invoke-RestMethod -Uri "http://127.0.0.1:8080/v1/models" -TimeoutSec 2 -ErrorAction Stop
        if ($modelsResp.data -and $modelsResp.data[0].id) {
            $Model = $modelsResp.data[0].id
        }
    } catch {}
    if ($Model -eq "auto") {
        $Model = "qwen3.8-flash-next-sync-s"
    }
}

# Generate per-session token and runtime models.json
$token = [Convert]::ToBase64String((1..24 | ForEach-Object { Get-Random -Maximum 256 }))
$runtimeModels = Join-Path $SandboxDir ".runtime-models.json"
(Get-Content (Join-Path $SandboxDir "models.json") -Raw).Replace("__WSL_HOST_IP__", $wslIp).Replace("__STRATA_MODEL_ID__", $Model) |
    Set-Content -Path $runtimeModels -Encoding UTF8

# Optional AWS Official Emulator sidecar container
$awsEmuContainerName = $null
if ($WithAwsEmulator) {
    $emuExists = ((& wslc image ls 2>$null) | Out-String) -match "(?m)^aws-emulator\s+latest\b"
    if ($UpdateAwsEmulator -or -not $emuExists) {
        Write-Host "[pi-sandbox] Building/updating aws-emulator:latest from upstream LTS..." -ForegroundColor Cyan
        & wslc build --no-cache -f (Join-Path $SandboxDir "Dockerfile.aws-emulator") -t aws-emulator:latest $SandboxDir
    }
    $emuDir = Join-Path $SandboxDir ".aws-emulator"
    New-Item -ItemType Directory -Force -Path $emuDir | Out-Null
    $awsEmuContainerName = "pi-aws-emu-$PID"
    Write-Host "[pi-sandbox] Starting aws-emulator..." -ForegroundColor Cyan
    & wslc run -d --rm --name $awsEmuContainerName -m 512M `
        -p "127.0.0.1:9297:9297" `
        -v "${emuDir}:/home/emulator/.aws/emulator:rw" `
        aws-emulator:latest -b 0.0.0.0 -p 9297 -t off -a off | Out-Null

    $caPem = Join-Path $emuDir "9297\ca.pem"
    $ready = $false
    for ($i = 0; $i -lt 30; $i++) {
        if (Test-Path $caPem) {
            try {
                $tcp = [System.Net.Sockets.TcpClient]::new()
                $tcp.Connect("127.0.0.1", 9297)
                $tcp.Close()
                $ready = $true
                break
            } catch {}
        }
        Start-Sleep -Milliseconds 300
    }
    if (-not $ready) {
        & wslc container stop $awsEmuContainerName 2>$null | Out-Null
        throw "aws-emulator failed to become healthy."
    }
    $env:PI_AWS_EMU_PORT = "19297"
    $env:PI_AWS_EMU_CA = $caPem
    $env:PI_AWS_EMU_CONTAINER = $awsEmuContainerName
    Write-Host "[pi-sandbox] aws-emulator ready via http://${wslIp}:19297" -ForegroundColor Green
}

# Start host-side Go proxy (pi-proxy.exe)
$env:PI_PROXY_TOKEN = $token
$env:PI_LOG_DIR = $LogsDir
$env:PI_WORKSPACE = $Workspace
$env:PI_ALLOWLIST = Join-Path $SandboxDir "allowlist.json"
$proxyProc = Start-Process -FilePath $proxyExe -PassThru -NoNewWindow

Start-Sleep -Milliseconds 600
if ($proxyProc.HasExited) {
    throw "Host proxy failed to start."
}

Write-Host "[pi-sandbox] Host IP: $wslIp | Logs: $LogsDir" -ForegroundColor Cyan
Write-Host "[pi-sandbox] Workspace: $Workspace -> /workspace (rw)" -ForegroundColor Cyan
Write-Host "[pi-sandbox] Refs:      $RefsDir -> /refs (ro)" -ForegroundColor Cyan

try {
    if ($PromptFile) {
        if (-not (Test-Path $PromptFile)) {
            throw "PromptFile not found: $PromptFile"
        }
        $Prompt = Get-Content -Path $PromptFile -Raw -Encoding UTF8
    }

    $isBatch = [bool]$Prompt -or $NonInteractive
    $proxyUrl = "http://${wslIp}:18888"
    $wslcArgs = @("run", "--rm")
    if (-not $isBatch) {
        $wslcArgs += "-it"
    }
    $wslcArgs += @(
        "--cap-drop=ALL",
        "-u", "agent",
        "-w", "/workspace",
        "-e", "STRATA_PROXY_TOKEN=$token",
        "-e", "PI_PROXY_URL=http://${wslIp}:18080",
        "-e", "HTTP_PROXY=$proxyUrl",
        "-e", "HTTPS_PROXY=$proxyUrl",
        "-e", "http_proxy=$proxyUrl",
        "-e", "https_proxy=$proxyUrl",
        "-e", "NO_PROXY=localhost,127.0.0.1,$wslIp",
        "-e", "no_proxy=localhost,127.0.0.1,$wslIp",
        "-v", "${Workspace}:/workspace:rw",
        "-v", "${RefsDir}:/refs:ro",
        "-v", "${SessionsDir}:/home/agent/.pi/agent/sessions:rw",
        "-v", "${runtimeModels}:/home/agent/.pi/agent/models.json:ro",
        "-v", "$(Join-Path $PiConfigDir 'APPEND_SYSTEM.md'):/home/agent/.pi/agent/APPEND_SYSTEM.md:ro",
        "-v", "$(Join-Path $PiConfigDir 'extensions'):/home/agent/.pi/agent/extensions:ro",
        "-v", "$(Join-Path $PiConfigDir 'skills'):/home/agent/.pi/agent/skills:ro",
        "-v", "$(Join-Path $PiConfigDir 'agents'):/home/agent/.pi/agent/agents:ro",
        "-v", "$(Join-Path $PiConfigDir 'prompts'):/home/agent/.pi/agent/prompts:ro",
        "-v", "$(Join-Path $SandboxDir 'shims\idf.py'):/usr/local/bin/idf.py:ro",
        "-p", "127.0.0.1:3000:3000",
        "-p", "127.0.0.1:5173:5173",
        "-p", "127.0.0.1:8000:8000"
    )

    if ($ShmSize) {
        $wslcArgs += @("--shm-size", $ShmSize)
    }

    if ($GitHubToken) {
        $wslcArgs += @("-e", "GITHUB_TOKEN=$GitHubToken", "-e", "GH_TOKEN=$GitHubToken")
    }

    if ($WithAwsEmulator) {
        $emuEndpoint = "http://${wslIp}:19297"
        $wslcArgs += @(
            "-e", "AWS_ENDPOINT_URL=$emuEndpoint",
            "-e", "AWS_ACCESS_KEY_ID=test",
            "-e", "AWS_SECRET_ACCESS_KEY=test",
            "-e", "AWS_DEFAULT_REGION=us-east-1"
        )
    }

    $baseArgs = @($wslcArgs)
    $runArgs = @($baseArgs) + @($Image, "--provider", "sync-strata", "--model", $Model)
    if ($Prompt) {
        $runArgs += @("-p", $Prompt)
    }
    if ($PiArgs) {
        $runArgs += $PiArgs
    }

    & wslc @runArgs

    if ($isBatch) {
        $doneFile = Join-Path $Workspace ".pi\DONE.json"
        for ($retry = 1; $retry -le 2; $retry++) {
            if (-not (Test-Path $doneFile)) { break }
            try {
                $doneObj = Get-Content $doneFile -Raw | ConvertFrom-Json
                if ($doneObj.targetMet -eq $true) { break }
            } catch { break }
            Write-Host "[pi-sandbox] Target not met yet; auto-continuing (round $retry/2)..." -ForegroundColor Yellow
            $contArgs = @($baseArgs) + @(
                $Image, "--provider", "sync-strata", "--model", $Model,
                "--continue", "-p",
                "Target score is not met yet. Continue using tools, finish all remaining deliverables (tests, E2E, README, git commit), and run verify_metrics until TARGET MET."
            )
            & wslc @contArgs
        }
    }
}
finally {
    if ($proxyProc -and -not $proxyProc.HasExited) {
        Stop-Process -Id $proxyProc.Id -Force -ErrorAction SilentlyContinue
        Write-Host "[pi-sandbox] Host proxy stopped." -ForegroundColor Yellow
    }
    if ($awsEmuContainerName) {
        & wslc container stop $awsEmuContainerName 2>$null | Out-Null
        Write-Host "[pi-sandbox] aws-emulator ($awsEmuContainerName) stopped." -ForegroundColor Yellow
    }
}

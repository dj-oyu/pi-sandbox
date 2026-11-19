param(
    [switch]$WithWeb,
    [switch]$WithAws,
    [switch]$All,
    [switch]$NoCache
)

$ErrorActionPreference = "Stop"
$Root = $PSScriptRoot

if ($All) {
    $WithWeb = $true
    $WithAws = $true
}

Write-Host "[setup] Checking wslc.exe..." -ForegroundColor Cyan
$wslcVer = & wslc version 2>&1
if ($LASTEXITCODE -ne 0) {
    throw "wslc.exe is not available. Please update WSL to a version supporting WSL containers (wsl --update)."
}
Write-Host "[setup] $wslcVer" -ForegroundColor Green

$cacheFlag = @()
if ($NoCache) { $cacheFlag = @("--no-cache") }

Write-Host "[setup] Building base image (pi-sandbox:latest)..." -ForegroundColor Cyan
& wslc build @cacheFlag -t pi-sandbox:latest $Root
if ($LASTEXITCODE -ne 0) { throw "Failed to build pi-sandbox:latest" }

Write-Host "[setup] Building Go host proxy (pi-proxy.exe)..." -ForegroundColor Cyan
$goDir = Join-Path $Root "go-proxy"
& wslc run --rm -u root -w /src --entrypoint sh `
    -v "${goDir}:/src:rw" `
    -v "${Root}:/out:rw" `
    pi-sandbox:latest -c "go test ./... && GOOS=windows GOARCH=amd64 go build -o /out/pi-proxy.exe ."
if ($LASTEXITCODE -ne 0) { throw "Failed to build pi-proxy.exe" }

if ($WithWeb) {
    Write-Host "[setup] Building Playwright image (pi-sandbox-web:latest)..." -ForegroundColor Cyan
    & wslc build @cacheFlag -f (Join-Path $Root "Dockerfile.web") -t pi-sandbox-web:latest $Root
    if ($LASTEXITCODE -ne 0) { throw "Failed to build pi-sandbox-web:latest" }
}

if ($WithAws) {
    Write-Host "[setup] Building AWS Emulator & Lambda RIE images..." -ForegroundColor Cyan
    & wslc build @cacheFlag -f (Join-Path $Root "Dockerfile.aws-emulator") -t aws-emulator:latest $Root
    if ($LASTEXITCODE -ne 0) { throw "Failed to build aws-emulator:latest" }
    & wslc build @cacheFlag -f (Join-Path $Root "Dockerfile.lambda") -t pi-lambda-rie:latest $Root
    if ($LASTEXITCODE -ne 0) { throw "Failed to build pi-lambda-rie:latest" }
}

New-Item -ItemType Directory -Force -Path (Join-Path $Root "refs") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $Root "logs\sessions") | Out-Null

Write-Host "[setup] Complete! Start Sync-Strata on the host, then run .\run-pi.ps1 -Workspace <path>" -ForegroundColor Green

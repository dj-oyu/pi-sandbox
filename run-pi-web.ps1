param(
    [Parameter(Mandatory = $false)]
    [string]$Workspace = (Get-Location).Path,

    [Parameter(Mandatory = $false)]
    [string]$RefsDir = "",

    [Parameter(Mandatory = $false)]
    [string]$GitHubToken = $env:GITHUB_TOKEN,

    [Parameter(Mandatory = $false)]
    [string]$Prompt = "",

    [Parameter(Mandatory = $false)]
    [string]$PromptFile = "",

    [Parameter(Mandatory = $false)]
    [switch]$NonInteractive,

    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$PiArgs
)

$ErrorActionPreference = "Stop"
$SandboxDir = $PSScriptRoot
if (-not $RefsDir) { $RefsDir = Join-Path $SandboxDir "refs" }
$ImageName = "pi-sandbox-web:latest"

$existing = ((& wslc image ls 2>$null) | Out-String) -match "(?m)^pi-sandbox-web\s+latest\b"
if (-not $existing) {
    Write-Host "[pi-sandbox-web] Image '$ImageName' is not built yet." -ForegroundColor Yellow
    Write-Host "Run this command once to build it:" -ForegroundColor Cyan
    Write-Host "  wslc build -f $SandboxDir\Dockerfile.web -t $ImageName $SandboxDir" -ForegroundColor White
    exit 1
}

& (Join-Path $SandboxDir "run-pi.ps1") `
    -Workspace $Workspace `
    -RefsDir $RefsDir `
    -GitHubToken $GitHubToken `
    -Image $ImageName `
    -ShmSize "512m" `
    -Prompt $Prompt `
    -PromptFile $PromptFile `
    -NonInteractive:$NonInteractive `
    @PiArgs

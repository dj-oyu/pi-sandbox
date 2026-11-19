param(
    [Parameter(Mandatory = $false)]
    [string]$Workspace = (Get-Location).Path,

    [Parameter(Mandatory = $false)]
    [string]$RefsDir = "",

    [Parameter(Mandatory = $false)]
    [switch]$Update,

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

& (Join-Path $SandboxDir "run-pi.ps1") `
    -Workspace $Workspace `
    -RefsDir $RefsDir `
    -WithAwsEmulator `
    -UpdateAwsEmulator:$Update `
    -Prompt $Prompt `
    -PromptFile $PromptFile `
    -NonInteractive:$NonInteractive `
    @PiArgs

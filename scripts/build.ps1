param(
    [string]$Tag = 'local',
    [string]$Repository = $env:GITHUB_REPOSITORY,
    [switch]$Signed
)
$ErrorActionPreference = 'Stop'
Push-Location (Split-Path -Parent $PSScriptRoot)
try {
    $env:CGO_ENABLED = '0'
    if (-not $Repository) { $Repository = 'FluentLauncher/FluentLauncher-plugins' }
    $signedFlag = '-signed=' + $Signed.IsPresent.ToString().ToLowerInvariant()
    go run ./tools/package -tag $Tag -repository $Repository $signedFlag
    if ($LASTEXITCODE -ne 0) { throw 'Plugin packaging failed' }
} finally { Pop-Location }

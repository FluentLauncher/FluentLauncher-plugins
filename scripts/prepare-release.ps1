param(
    [Parameter(Mandatory)][ValidateSet('build', 'push', 'tag')][string]$Action,
    [string]$Tag = '',
    [string]$Repository = $env:GITHUB_REPOSITORY
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'release-functions.ps1')
$projectRoot = Split-Path -Parent $PSScriptRoot
Push-Location $projectRoot
try {
    $sha = git rev-parse HEAD
    if ($LASTEXITCODE -ne 0) { throw 'Cannot determine the build commit' }
    if ($Action -eq 'push') {
        if ($env:GITHUB_REF -ne 'refs/heads/main') { throw 'Automatic plugin releases must run from main' }
        $remoteHead = @(git ls-remote origin refs/heads/main)
        if ($LASTEXITCODE -ne 0 -or $remoteHead.Count -ne 1) { throw 'Cannot determine the current main commit' }
        if ($sha -ne ($remoteHead[0] -split '\s+')[0]) {
            Write-Output 'Skipping an outdated main build; a newer push will publish the plugins.'
            if ($env:GITHUB_OUTPUT) { Add-Content -LiteralPath $env:GITHUB_OUTPUT -Value 'skip=true' -Encoding utf8 }
            return
        }
    }
    $planArgs = @{ Action = $Action; Tag = $Tag }
    if ($Action -ne 'build') {
        $planArgs.Tags = @(git tag --list)
        if ($LASTEXITCODE -ne 0) { throw 'Cannot read collection tags' }
        $planArgs.HeadTags = @(git tag --points-at HEAD)
        if ($LASTEXITCODE -ne 0) { throw 'Cannot read tags at the build commit' }
        $planArgs.PublishedTags = @(gh api --paginate "repos/$Repository/releases?per_page=100" --jq '.[] | select(.draft == false and .prerelease == false) | .tag_name')
        if ($LASTEXITCODE -ne 0) { throw 'Cannot read published versions; refusing to guess a collection version' }
    }
    $plan = Get-PluginReleasePlan @planArgs
    $plan | Add-Member -NotePropertyName sha -NotePropertyValue $sha
    New-Item -ItemType Directory -Force -Path build | Out-Null
    [IO.File]::WriteAllText((Join-Path $projectRoot 'build/plugin-release-plan.json'), ($plan | ConvertTo-Json) + "`n", [Text.UTF8Encoding]::new($false))
    if ($env:GITHUB_OUTPUT) {
        @("publish=$($plan.publish.ToString().ToLowerInvariant())", "skip=$($plan.skip.ToString().ToLowerInvariant())") |
            Add-Content -LiteralPath $env:GITHUB_OUTPUT -Encoding utf8
    }
    Write-Output "Plugin release plan: $Action $($plan.tag) at $sha (skip: $($plan.skip))"
} finally { Pop-Location }

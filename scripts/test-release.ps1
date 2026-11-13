$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'release-functions.ps1')

function Assert-Equal($Actual, $Expected) {
    if ($Actual -cne $Expected) { throw "Expected '$Expected', got '$Actual'" }
}
function Assert-Throws([scriptblock]$Run, [string]$Message) {
    try { & $Run } catch {
        if ($_.Exception.Message -notlike "*$Message*") { throw }
        return
    }
    throw "Expected failure containing '$Message'"
}

$tags = @('v1.0.1', 'v1.9.9', 'v1.10.0', 'sdk/v9.0.0', 'v2.0.0-preview.1')
$versions = @{ PluginVersions = @{ 'official.echo' = '1.0.1' }; ReleasedVersions = @{ 'official.echo' = '1.0.0' } }
Assert-Equal (Get-PluginReleasePlan -Action push -Tags $tags @versions).tag 'v1.10.1'
Assert-Equal (Get-PluginReleasePlan -Action push -PluginVersions @{ 'official.echo' = '1.0.0' }).tag 'v1.0.0'
Assert-Equal (Get-PluginReleasePlan -Action build).publish $false
Assert-Equal (Get-PluginReleasePlan -Action build).tag 'local'
Assert-Equal (Get-PluginReleasePlan -Action tag -Tag v2.0.0 -PublishedTags $tags).tag 'v2.0.0'
Assert-Equal (Get-PluginReleasePlan -Action push -Tags $tags -HeadTags v1.10.0 @versions).tag 'v1.10.0'
Assert-Equal (Get-PluginReleasePlan -Action push -Tags $tags -HeadTags v1.10.0 -PublishedTags v1.10.0).skip $true
Assert-Throws { Get-PluginReleasePlan -Action tag -Tag v1.9.9 -PublishedTags $tags } 'newer tag'
Assert-Throws { Get-PluginReleasePlan -Action tag -Tag 'v1.2.3-nightly.1' } 'stable vX.Y.Z'
$unchanged = @{ PluginVersions = @{ 'official.echo' = '1.0.0' }; ReleasedVersions = @{ 'official.echo' = '1.0.0' } }
Assert-Equal (Get-PluginReleasePlan -Action push -Tags $tags @unchanged).publish $false
Assert-Equal (Get-PluginReleasePlan -Action push -Tags $tags @unchanged).skip $false
Assert-Equal (Get-PluginReleasePlan -Action push -Tags $tags @unchanged).tag 'local'
Assert-Equal (Get-PluginReleasePlan -Action release -Tags $tags @unchanged).publish $true
$newPlugin = @{ PluginVersions = @{ 'official.echo' = '1.0.0'; 'official.new' = '0.1.0' }; ReleasedVersions = @{ 'official.echo' = '1.0.0' } }
Assert-Equal (Get-PluginReleasePlan -Action push -Tags $tags @newPlugin).publish $true
$decreased = @{ PluginVersions = @{ 'official.echo' = '0.9.0' }; ReleasedVersions = @{ 'official.echo' = '1.0.0' } }
Assert-Throws { Get-PluginReleasePlan -Action push @decreased } 'version decreased'
Assert-Throws { Get-PluginReleasePlan -Action release @decreased } 'version decreased'

# Exercise the real scripts with fake GitHub/git commands and isolated assets.
# These tests never use credentials, the network, or the working tree's tags.
$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('fluent-plugin-release-test-' + [guid]::NewGuid().ToString('N'))
$scripts = Join-Path $testRoot 'scripts'
$assets = Join-Path $testRoot 'build/release'
New-Item -ItemType Directory -Force -Path $scripts, $assets | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $testRoot 'plugins/echo') | Out-Null
foreach ($name in @('prepare-release.ps1', 'publish-release.ps1', 'release-functions.ps1')) {
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot $name) -Destination $scripts
}
$utf8 = [Text.UTF8Encoding]::new($false)
[IO.File]::WriteAllText((Join-Path $testRoot 'catalog.source.json'), '[{"id":"official.echo"}]', $utf8)
$manifestPath = Join-Path $testRoot 'plugins/echo/plugin.json'
[IO.File]::WriteAllText($manifestPath, '{"id":"official.echo","version":"1.0.1"}', $utf8)
foreach ($name in @('catalog.json', 'catalog.sig', 'official.echo-1.0.1-windows-amd64.zip', 'official.echo-1.0.1-icon.png')) {
    [IO.File]::WriteAllText((Join-Path $assets $name), 'test', $utf8)
}
$sha = '1234567890123456789012345678901234567890'
$global:pluginReleaseTest = @{
    Calls = [Collections.Generic.List[string]]::new(); HeadSHA = $sha; MainSHA = $sha; TagSHA = $sha
    Tags = @('v1.0.0', 'v1.0.1', 'sdk/v0.1.0'); HeadTags = @(); PublishedTags = @('v1.0.0', 'v1.0.1')
    TagExists = $false; Existing = ''; FailAPI = $false; FailUpload = $false
    ReleasedVersion = '1.0.0'; FailReadBaseline = $false
}
function global:gh {
    $state = $global:pluginReleaseTest
    $state.Calls.Add('gh ' + ($args -join ' '))
    $global:LASTEXITCODE = 0
    if ($args[0] -eq 'api') {
        if ($state.FailAPI) { $global:LASTEXITCODE = 1; return }
        if ($args -contains '--method') { $state.TagExists = $true; return }
        if ($args[-1] -like '*.tag_name') { $state.PublishedTags }
        elseif ($state.Existing) { $state.Existing }
    }
    if ($args[0] -eq 'release' -and $args[1] -eq 'upload' -and $state.FailUpload) { $global:LASTEXITCODE = 1 }
}
function global:git {
    $state = $global:pluginReleaseTest
    $state.Calls.Add('git ' + ($args -join ' '))
    $global:LASTEXITCODE = 0
    switch ($args[0]) {
        'rev-parse' { $state.HeadSHA }
        'rev-list' { $state.TagSHA }
        'tag' { if ($args[1] -eq '--list') { $state.Tags } else { $state.HeadTags } }
        'ls-remote' {
            if ($args[-1] -eq 'refs/heads/main') { "$($state.MainSHA)`trefs/heads/main" }
            elseif ($state.TagExists) { "$($state.TagSHA)`t$($args[-1])" }
        }
        'show' {
            if ($state.FailReadBaseline) { $global:LASTEXITCODE = 1; return }
            if ($args[1] -like '*:catalog.source.json') { '[{"id":"official.echo"}]' }
            else { '{"id":"official.echo","version":"' + $state.ReleasedVersion + '"}' }
        }
    }
}
$originalEnv = @{}
foreach ($name in @('GITHUB_REF', 'GITHUB_OUTPUT')) { $originalEnv[$name] = [Environment]::GetEnvironmentVariable($name) }
try {
    $prepare = Join-Path $scripts 'prepare-release.ps1'
    $publish = Join-Path $scripts 'publish-release.ps1'
    $planPath = Join-Path $testRoot 'build/plugin-release-plan.json'
    $env:GITHUB_REF = 'refs/heads/main'
    $env:GITHUB_OUTPUT = Join-Path $testRoot 'outputs.txt'
    # Documentation/comment-only pushes have unchanged versions: they still
    # build and upload artifacts, without creating a release or a tag.
    [IO.File]::WriteAllText($manifestPath, '{"id":"official.echo","version":"1.0.0"}', $utf8)
    & $prepare -Action push -Repository example/plugins
    $plan = Get-Content -Raw $planPath | ConvertFrom-Json
    Assert-Equal $plan.publish $false
    Assert-Equal $plan.skip $false
    Assert-Equal $plan.tag 'local'
    Assert-Throws { & $publish -Repository example/plugins } 'does not authorize'
    & $prepare -Action release -Repository example/plugins
    Assert-Equal (Get-Content -Raw $planPath | ConvertFrom-Json).publish $true

    [IO.File]::WriteAllText($manifestPath, '{"id":"official.echo","version":"0.9.0"}', $utf8)
    Assert-Throws { & $prepare -Action push -Repository example/plugins } 'version decreased'
    [IO.File]::WriteAllText($manifestPath, '{"id":"official.echo","version":"1.0.1"}', $utf8)
    $global:pluginReleaseTest.FailReadBaseline = $true
    Assert-Throws { & $prepare -Action push -Repository example/plugins } 'Cannot read'
    $global:pluginReleaseTest.FailReadBaseline = $false
    & $prepare -Action push -Repository example/plugins
    $plan = Get-Content -Raw $planPath | ConvertFrom-Json
    Assert-Equal $plan.tag 'v1.0.2'
    Assert-Equal $plan.publish $true
    Assert-Equal $plan.sha $sha
    if ((Get-Content -Raw $env:GITHUB_OUTPUT) -notmatch 'publish=true') { throw 'No publish output' }

    & $publish -Repository example/plugins
    $calls = $global:pluginReleaseTest.Calls -join "`n"
    if ($calls -notmatch 'git/refs --method POST --raw-field ref=refs/tags/v1.0.2 --raw-field sha=1234567890' -or
        $calls -notmatch 'release create v1.0.2 .*--verify-tag --target 1234567890.*--draft' -or
        $calls -notmatch 'release upload .*catalog.sig.*official.echo-1.0.1-icon.png' -or
        $calls -notmatch 'release edit .*--draft=false --prerelease=false --latest') { throw 'Incorrect release publication sequence' }

    $global:pluginReleaseTest.Existing = '{"draft":false}'
    $global:pluginReleaseTest.Calls.Clear()
    Assert-Throws { & $publish -Repository example/plugins } 'immutable'
    if (($global:pluginReleaseTest.Calls -join "`n") -match 'release (upload|edit)') { throw 'Published assets were mutated' }

    $global:pluginReleaseTest.Existing = '{"draft":true}'
    $global:pluginReleaseTest.Calls.Clear()
    & $publish -Repository example/plugins
    if (($global:pluginReleaseTest.Calls -join "`n") -match 'release create|--method POST') { throw 'Draft resume created another release or tag' }

    $global:pluginReleaseTest.TagSHA = 'different-commit'
    Assert-Throws { & $publish -Repository example/plugins } 'different commit'
    $global:pluginReleaseTest.TagSHA = $sha
    $global:pluginReleaseTest.FailUpload = $true
    $global:pluginReleaseTest.Calls.Clear()
    Assert-Throws { & $publish -Repository example/plugins } 'Uploading'
    if (($global:pluginReleaseTest.Calls -join "`n") -match 'release edit') { throw 'A failed upload was published' }
    $global:pluginReleaseTest.FailUpload = $false
    $global:pluginReleaseTest.FailAPI = $true
    Assert-Throws { & $publish -Repository example/plugins } 'Cannot inspect'
    Assert-Throws { & $prepare -Action push -Repository example/plugins } 'Cannot read published versions'
    $global:pluginReleaseTest.FailAPI = $false

    $global:pluginReleaseTest.Tags += 'v1.0.2'
    $global:pluginReleaseTest.HeadTags = @('v1.0.2')
    & $prepare -Action push -Repository example/plugins
    Assert-Equal (Get-Content -Raw $planPath | ConvertFrom-Json).tag 'v1.0.2'
    $global:pluginReleaseTest.PublishedTags += 'v1.0.2'
    $global:pluginReleaseTest.ReleasedVersion = '1.0.1'
    & $prepare -Action push -Repository example/plugins
    Assert-Equal (Get-Content -Raw $planPath | ConvertFrom-Json).skip $true
    Assert-Throws { & $publish -Repository example/plugins } 'does not authorize'

    $env:GITHUB_REF = 'refs/heads/feature'
    Assert-Throws { & $prepare -Action push -Repository example/plugins } 'from main'
    & $prepare -Action build -Repository example/plugins
    Assert-Equal (Get-Content -Raw $planPath | ConvertFrom-Json).publish $false
    $env:GITHUB_REF = 'refs/heads/main'
    $global:pluginReleaseTest.MainSHA = 'newer-main'
    [IO.File]::WriteAllText($env:GITHUB_OUTPUT, '', $utf8)
    & $prepare -Action push -Repository example/plugins
    Assert-Equal (Get-Content -Raw $env:GITHUB_OUTPUT).Trim() 'skip=true'
} finally {
    foreach ($name in $originalEnv.Keys) { [Environment]::SetEnvironmentVariable($name, $originalEnv[$name]) }
    Remove-Item Function:\gh, Function:\git
    Remove-Variable pluginReleaseTest -Scope Global
    $resolved = [IO.Path]::GetFullPath($testRoot)
    $tempPrefix = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\fluent-plugin-release-test-'
    if (-not $resolved.StartsWith($tempPrefix, [StringComparison]::OrdinalIgnoreCase)) { throw 'Test cleanup path outside temporary workspace' }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
Write-Output 'Plugin release planning and publishing guard tests passed.'

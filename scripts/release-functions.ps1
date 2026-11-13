# Plugin collection tags are independent of the versions in plugin.json.
function Read-PluginVersions {
    param([string]$Ref = '')
    $readJSON = {
        param([string]$Path)
        if ($Ref) {
            $json = @(git show "${Ref}:$Path")
            if ($LASTEXITCODE -ne 0) { throw "Cannot read $Path at $Ref" }
            ($json -join "`n") | ConvertFrom-Json
        } else {
            Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json
        }
    }
    $versions = @{}
    foreach ($entry in (& $readJSON 'catalog.source.json')) {
        if ($entry.id -cnotmatch '^official\.[a-z0-9_-]+$' -or $versions.ContainsKey($entry.id)) { throw 'Invalid or duplicate catalogue plugin ID' }
        $manifest = & $readJSON ('plugins/' + $entry.id.Substring('official.'.Length) + '/plugin.json')
        if ($manifest.id -cne $entry.id -or $manifest.version -notmatch '^[0-9]+\.[0-9]+\.[0-9]+$') { throw "Invalid plugin manifest: $($entry.id)" }
        $versions[$entry.id] = $manifest.version
    }
    return $versions
}

function Get-PluginVersionChanges {
    param([hashtable]$Current = @{}, [hashtable]$Released = @{})
    foreach ($id in ($Current.Keys | Sort-Object)) {
        if (-not $Released.ContainsKey($id)) {
            $id
        } elseif ([version]$Current[$id] -lt [version]$Released[$id]) {
            throw "Plugin version decreased: $id ($($Released[$id]) -> $($Current[$id]))"
        } elseif ([version]$Current[$id] -gt [version]$Released[$id]) {
            $id
        }
    }
}

function Get-PluginReleasePlan {
    param(
        [ValidateSet('build', 'push', 'release', 'tag')][string]$Action,
        [string]$Tag = '',
        [string[]]$Tags = @(),
        [string[]]$HeadTags = @(),
        [string[]]$PublishedTags = @(),
        [hashtable]$PluginVersions = @{},
        [hashtable]$ReleasedVersions = @{}
    )
    $plan = [pscustomobject]@{ action = $Action; tag = 'local'; publish = $false; skip = $false }
    if ($Action -eq 'build') { return $plan }

    $pattern = '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
    $published = @($PublishedTags | Where-Object { $_ -cmatch $pattern })
    $latestPublished = $published | Sort-Object { [version]$_.Substring(1) } -Descending | Select-Object -First 1
    $changes = @(Get-PluginVersionChanges -Current $PluginVersions -Released $ReleasedVersions)
    if ($Action -eq 'tag') {
        if ($Tag -cnotmatch $pattern) { throw 'Plugin collection tags must be stable vX.Y.Z versions' }
        if ($latestPublished -and [version]$Tag.Substring(1) -le [version]$latestPublished.Substring(1)) {
            throw 'Published plugin releases are immutable; use a newer tag'
        }
    } else {
        # A completed push run is idempotent. A tag left before a failed
        # upload is reused, so rerunning can finish its draft at the same SHA.
        if (@($HeadTags | Where-Object { $_ -cin $published }).Count -gt 0) {
            $plan.skip = $true
            return $plan
        }
        if ($Action -eq 'push' -and $changes.Count -eq 0) { return $plan }
        $latest = @($Tags + $PublishedTags) | Where-Object { $_ -cmatch $pattern } |
            Sort-Object { [version]$_.Substring(1) } -Descending | Select-Object -First 1
        if ($latest -and $latest -cin $HeadTags) {
            $Tag = $latest
        } elseif ($latest) {
            $v = [version]$latest.Substring(1)
            $Tag = 'v{0}.{1}.{2}' -f $v.Major, $v.Minor, ($v.Build + 1)
        } else {
            $Tag = 'v1.0.0'
        }
    }
    $plan.tag = $Tag
    $plan.publish = $true
    return $plan
}

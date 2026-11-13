# Plugin collection tags are independent of the versions in plugin.json.
function Get-PluginReleasePlan {
    param(
        [ValidateSet('build', 'push', 'tag')][string]$Action,
        [string]$Tag = '',
        [string[]]$Tags = @(),
        [string[]]$HeadTags = @(),
        [string[]]$PublishedTags = @()
    )
    $plan = [pscustomobject]@{ action = $Action; tag = 'local'; publish = $false; skip = $false }
    if ($Action -eq 'build') { return $plan }

    $pattern = '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
    $published = @($PublishedTags | Where-Object { $_ -cmatch $pattern })
    $latestPublished = $published | Sort-Object { [version]$_.Substring(1) } -Descending | Select-Object -First 1
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

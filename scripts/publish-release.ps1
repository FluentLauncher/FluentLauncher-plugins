param([string]$Repository = $env:GITHUB_REPOSITORY)
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
Push-Location $projectRoot
try {
    $plan = Get-Content -Raw build/plugin-release-plan.json | ConvertFrom-Json
    if (-not $plan.publish -or $plan.skip) { throw 'The release plan does not authorize publishing' }
    $tag = $plan.tag
    foreach ($name in @('catalog.json', 'catalog.sig')) {
        if (-not (Test-Path -LiteralPath "build/release/$name" -PathType Leaf)) { throw "Missing release asset: $name" }
    }
    $archives = @(Get-ChildItem -LiteralPath build/release -Filter '*.zip' -File)
    if ($archives.Count -eq 0) { throw 'Missing plugin ZIPs' }
    # A list query distinguishes an absent release from a failed API call.
    $existing = @(gh api --paginate "repos/$Repository/releases?per_page=100" --jq ".[] | select(.tag_name == `"$tag`") | {draft}")
    if ($LASTEXITCODE -ne 0) { throw 'Cannot inspect existing releases' }
    if ($existing.Count -gt 0 -and -not (($existing -join "`n") | ConvertFrom-Json).draft) {
        throw 'Published plugin releases are immutable; use a new tag'
    }
    $remoteTag = @(git ls-remote origin "refs/tags/$tag")
    if ($LASTEXITCODE -ne 0) { throw 'Cannot inspect the release tag' }
    if ($remoteTag.Count -eq 0) {
        if ($plan.action -eq 'tag') { throw 'The manually pushed release tag is missing' }
        # GITHUB_TOKEN creates the tag without starting another workflow.
        gh api "repos/$Repository/git/refs" --method POST --raw-field "ref=refs/tags/$tag" --raw-field "sha=$($plan.sha)" --silent
        if ($LASTEXITCODE -ne 0) { throw 'Creating the release tag failed' }
    }
    git fetch origin "refs/tags/${tag}:refs/tags/${tag}"
    if ($LASTEXITCODE -ne 0) { throw 'Cannot resolve the release tag' }
    $tagSHA = git rev-list -n 1 $tag
    if ($LASTEXITCODE -ne 0 -or $tagSHA -ne $plan.sha) { throw 'Release tag points at a different commit' }
    if ($existing.Count -eq 0) {
        gh release create $tag --repo $Repository --verify-tag --target $plan.sha --draft --title "官方插件 $tag" --generate-notes
        if ($LASTEXITCODE -ne 0) { throw 'Creating draft plugin release failed' }
    }
    $assets = @(Get-ChildItem -LiteralPath build/release -File | ForEach-Object FullName)
    gh release upload $tag --repo $Repository --clobber @assets
    if ($LASTEXITCODE -ne 0) { throw 'Uploading packages failed' }
    gh release edit $tag --repo $Repository --draft=false --prerelease=false --latest
    if ($LASTEXITCODE -ne 0) { throw 'Publishing packages failed' }
} finally { Pop-Location }

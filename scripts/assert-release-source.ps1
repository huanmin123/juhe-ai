param(
  [Parameter(Mandatory = $true)][string]$RepoRoot,
  [string]$ExpectedCommit = ''
)

$ErrorActionPreference = 'Stop'

if (-not (Get-Command git -ErrorAction SilentlyContinue)) {
  throw 'git is required to validate the release source.'
}

$resolvedRepoRoot = (Resolve-Path -LiteralPath $RepoRoot).Path
$gitTopLevel = (& git -C $resolvedRepoRoot rev-parse --show-toplevel 2>&1)
if ($LASTEXITCODE -ne 0) {
  throw "Release source is not a Git worktree: $resolvedRepoRoot"
}
$resolvedGitTopLevel = (Resolve-Path -LiteralPath ($gitTopLevel | Select-Object -Last 1)).Path
if (-not [string]::Equals($resolvedRepoRoot, $resolvedGitTopLevel, [System.StringComparison]::OrdinalIgnoreCase)) {
  throw "Release source must be the Git worktree root: $resolvedGitTopLevel"
}

$commit = ((& git -C $resolvedRepoRoot rev-parse HEAD 2>&1) | Select-Object -Last 1).Trim()
if ($LASTEXITCODE -ne 0 -or $commit -notmatch '^[0-9a-fA-F]{40}$') {
  throw 'Unable to resolve the release source commit.'
}
if ($ExpectedCommit -and -not [string]::Equals($commit, $ExpectedCommit.Trim(), [System.StringComparison]::OrdinalIgnoreCase)) {
  throw "Release source commit $commit does not match expected commit $ExpectedCommit."
}

$status = @(& git -C $resolvedRepoRoot status --porcelain=v1 --untracked-files=all)
if ($LASTEXITCODE -ne 0) {
  throw 'Unable to inspect the release source worktree.'
}
if ($status.Count -gt 0) {
  $preview = ($status | Select-Object -First 20) -join "`n"
  throw "Release source is not clean. Build from a clean checkout of the fixed release SHA.`n$preview"
}

Write-Output "RELEASE_SOURCE_OK commit=$commit"

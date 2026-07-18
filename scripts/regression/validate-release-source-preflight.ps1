$ErrorActionPreference = 'Stop'

$repoRoot = Resolve-Path (Join-Path $PSScriptRoot '..\..')
$preflightScript = Join-Path $repoRoot 'scripts\assert-release-source.ps1'
$bashPreflightScript = (Join-Path $repoRoot 'scripts\assert-release-source.sh') -replace '\\', '/'
$tempRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("juhe-ai-release-source-{0}" -f [guid]::NewGuid().ToString('N'))
$resolvedTempBase = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())

function Invoke-Preflight {
  param(
    [Parameter(Mandatory = $true)][string]$WorkingTree,
    [Parameter(Mandatory = $true)][string]$ExpectedCommit
  )

  try {
    $output = @(& $preflightScript -RepoRoot $WorkingTree -ExpectedCommit $ExpectedCommit 2>&1)
    return [pscustomobject]@{
      ExitCode = 0
      Output = ($output -join "`n")
    }
  } catch {
    return [pscustomobject]@{
      ExitCode = 1
      Output = $_.ToString()
    }
  }
}

function Invoke-BashPreflight {
  param(
    [Parameter(Mandatory = $true)][string]$WorkingTree,
    [Parameter(Mandatory = $true)][string]$ExpectedCommit
  )

  $bashWorkingTree = $WorkingTree -replace '\\', '/'
  $output = & bash $bashPreflightScript $bashWorkingTree $ExpectedCommit 2>&1
  return [pscustomobject]@{
    ExitCode = $LASTEXITCODE
    Output = ($output -join "`n")
  }
}

try {
  New-Item -ItemType Directory -Path $tempRoot -Force | Out-Null
  git -C $tempRoot init --quiet
  git -C $tempRoot config user.email 'release-preflight@example.invalid'
  git -C $tempRoot config user.name 'Release Preflight Test'
  Set-Content -LiteralPath (Join-Path $tempRoot 'tracked.txt') -Value 'baseline' -NoNewline
  git -C $tempRoot add tracked.txt
  git -C $tempRoot commit --quiet -m 'baseline'
  $commit = (git -C $tempRoot rev-parse HEAD).Trim()

  $clean = Invoke-Preflight -WorkingTree $tempRoot -ExpectedCommit $commit
  if ($clean.ExitCode -ne 0 -or $clean.Output -notmatch 'RELEASE_SOURCE_OK') {
    throw "clean source should pass: $($clean.Output)"
  }
  if (-not $IsWindows) {
    $cleanBash = Invoke-BashPreflight -WorkingTree $tempRoot -ExpectedCommit $commit
    if ($cleanBash.ExitCode -ne 0 -or $cleanBash.Output -notmatch 'RELEASE_SOURCE_OK') {
      throw "clean source should pass bash preflight: $($cleanBash.Output)"
    }
  }

  Set-Content -LiteralPath (Join-Path $tempRoot 'tracked.txt') -Value 'dirty' -NoNewline
  $dirtyTracked = Invoke-Preflight -WorkingTree $tempRoot -ExpectedCommit $commit
  if ($dirtyTracked.ExitCode -eq 0 -or $dirtyTracked.Output -notmatch 'not clean') {
    throw "dirty tracked source should fail: $($dirtyTracked.Output)"
  }
  if (-not $IsWindows) {
    $dirtyTrackedBash = Invoke-BashPreflight -WorkingTree $tempRoot -ExpectedCommit $commit
    if ($dirtyTrackedBash.ExitCode -eq 0 -or $dirtyTrackedBash.Output -notmatch 'not clean') {
      throw "dirty tracked source should fail bash preflight: $($dirtyTrackedBash.Output)"
    }
  }

  Set-Content -LiteralPath (Join-Path $tempRoot 'tracked.txt') -Value 'baseline' -NoNewline
  Set-Content -LiteralPath (Join-Path $tempRoot 'untracked.txt') -Value 'untracked' -NoNewline
  $dirtyUntracked = Invoke-Preflight -WorkingTree $tempRoot -ExpectedCommit $commit
  if ($dirtyUntracked.ExitCode -eq 0 -or $dirtyUntracked.Output -notmatch 'not clean') {
    throw "untracked source should fail: $($dirtyUntracked.Output)"
  }
  if (-not $IsWindows) {
    $dirtyUntrackedBash = Invoke-BashPreflight -WorkingTree $tempRoot -ExpectedCommit $commit
    if ($dirtyUntrackedBash.ExitCode -eq 0 -or $dirtyUntrackedBash.Output -notmatch 'not clean') {
      throw "untracked source should fail bash preflight: $($dirtyUntrackedBash.Output)"
    }
  }

  Remove-Item -LiteralPath (Join-Path $tempRoot 'untracked.txt') -Force
  $wrongCommit = ('0' * 40)
  $mismatch = Invoke-Preflight -WorkingTree $tempRoot -ExpectedCommit $wrongCommit
  if ($mismatch.ExitCode -eq 0 -or $mismatch.Output -notmatch 'does not match expected commit') {
    throw "commit mismatch should fail: $($mismatch.Output)"
  }
  if (-not $IsWindows) {
    $mismatchBash = Invoke-BashPreflight -WorkingTree $tempRoot -ExpectedCommit $wrongCommit
    if ($mismatchBash.ExitCode -eq 0 -or $mismatchBash.Output -notmatch 'does not match expected commit') {
      throw "commit mismatch should fail bash preflight: $($mismatchBash.Output)"
    }
  }

  $packagePowerShell = Get-Content -LiteralPath (Join-Path $repoRoot 'scripts\package-release.ps1') -Raw
  foreach ($required in @('ExpectedCommit', 'assert-release-source.ps1', 'RELEASE_SOURCE_COMMIT')) {
    if ($packagePowerShell -notmatch [regex]::Escape($required)) {
      throw "PowerShell package script must include release source gate: $required"
    }
  }
  if ([regex]::Matches($packagePowerShell, 'assert-release-source\.ps1').Count -lt 2) {
    throw 'PowerShell package script must recheck the release source after the build'
  }
  $packageBash = Get-Content -LiteralPath (Join-Path $repoRoot 'scripts\package-release.sh') -Raw
  foreach ($required in @('--expected-commit', 'assert-release-source.sh', 'RELEASE_SOURCE_COMMIT')) {
    if ($packageBash -notmatch [regex]::Escape($required)) {
      throw "bash package script must include release source gate: $required"
    }
  }
  if ($packageBash -match 'BASH_SOURCE') {
    throw 'bash package script must resolve its directory from portable $0 semantics'
  }
  if ([regex]::Matches($packageBash, 'assert-release-source\.sh').Count -lt 2) {
    throw 'bash package script must recheck the release source after the build'
  }

  Write-Output 'release source preflight regression passed'
} finally {
  $resolvedTarget = [System.IO.Path]::GetFullPath($tempRoot)
  if (-not $resolvedTarget.StartsWith($resolvedTempBase, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "refusing to remove unexpected path: $resolvedTarget"
  }
  Remove-Item -LiteralPath $resolvedTarget -Recurse -Force -ErrorAction SilentlyContinue
}

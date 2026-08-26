param(
  [Parameter(Mandatory = $true)][string]$RepoRoot,
  [Parameter(Mandatory = $true)][string]$CompareBranch
)

$ErrorActionPreference = 'Stop'
$resolvedRepoRoot = (Resolve-Path -LiteralPath $RepoRoot).Path

function Get-MigrationMap {
  param([Parameter(Mandatory = $true)][string[]]$Paths)

  $map = @{}
  foreach ($path in $Paths) {
    $normalized = $path -replace '\\', '/'
    if ($normalized -notmatch '^backend-go/db/migrations/(\d{6})_(.+\.sql)$') { continue }
    $version = $Matches[1]
    if ($map.ContainsKey($version)) {
      throw "Duplicate Goose migration version ${version}: $($map[$version]) and $normalized"
    }
    $map[$version] = $normalized
  }
  return $map
}

$localPaths = @(Get-ChildItem -LiteralPath (Join-Path $resolvedRepoRoot 'backend-go\db\migrations') -File -Filter '*.sql' | ForEach-Object {
  "backend-go/db/migrations/$($_.Name)"
})
$localMap = Get-MigrationMap -Paths $localPaths

$branchPaths = @(git -C $resolvedRepoRoot ls-tree -r --name-only $CompareBranch -- backend-go/db/migrations 2>&1)
if ($LASTEXITCODE -ne 0) {
  throw "Unable to inspect comparison branch ${CompareBranch}: $($branchPaths -join "`n")"
}
$branchMap = Get-MigrationMap -Paths $branchPaths

$branchEntries = @($branchMap.GetEnumerator())
$branchHashArgs = @($branchEntries | ForEach-Object { "$CompareBranch`:$($_.Value)" })
$branchHashOutput = @(git -C $resolvedRepoRoot rev-parse @branchHashArgs 2>&1)
if ($LASTEXITCODE -ne 0 -or $branchHashOutput.Count -ne $branchEntries.Count) {
  throw "Unable to hash migrations on $CompareBranch"
}
$branchHashes = @{}
for ($index = 0; $index -lt $branchEntries.Count; $index++) {
  $hash = $branchHashOutput[$index].Trim()
  if ($hash -notmatch '^[0-9a-fA-F]{40}$') {
    throw "Unable to hash $CompareBranch`:$($branchEntries[$index].Value)"
  }
  $branchHashes[$branchEntries[$index].Value] = $hash
}

$localHashOutput = @(git -C $resolvedRepoRoot hash-object -- @localPaths 2>&1)
if ($LASTEXITCODE -ne 0 -or $localHashOutput.Count -ne $localPaths.Count) {
  throw 'Unable to hash local migrations'
}
$localHashes = @{}
for ($index = 0; $index -lt $localPaths.Count; $index++) {
  $hash = $localHashOutput[$index].Trim()
  if ($hash -notmatch '^[0-9a-fA-F]{40}$') {
    throw "Unable to hash local migration $($localPaths[$index])"
  }
  $localHashes[$localPaths[$index]] = $hash
}

$conflicts = @()
foreach ($version in $localMap.Keys) {
  $localPath = $localMap[$version]
  $localHash = $localHashes[$localPath]
  if ($branchMap.ContainsKey($version)) {
    $branchPath = $branchMap[$version]
    $branchHash = $branchHashes[$branchPath]
    if ($localHash -eq $branchHash) {
      continue
    }
    $equivalentBranchPath = $branchHashes.GetEnumerator() |
      Where-Object { $_.Value -eq $localHash } |
      Select-Object -First 1 -ExpandProperty Key
    if (-not $equivalentBranchPath) {
      $conflicts += "version ${version}: $localPath / $branchPath"
    }
    continue
  }

  $equivalentBranchPath = $branchHashes.GetEnumerator() |
    Where-Object { $_.Value -eq $localHash } |
    Select-Object -First 1 -ExpandProperty Key
  if ($equivalentBranchPath) {
    continue
  }
}

if ($conflicts.Count -gt 0) {
  $preview = ($conflicts | Select-Object -First 50) -join "`n"
  throw "Migration compatibility conflicts detected ($($conflicts.Count)):`n$preview"
}

Write-Output "MIGRATION_VERSION_COMPATIBLE branch=$CompareBranch local=$($localMap.Count) compared=$($branchMap.Count)"

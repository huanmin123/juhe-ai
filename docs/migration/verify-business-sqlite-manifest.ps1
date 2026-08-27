param(
    [string]$RepositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
)

$ErrorActionPreference = 'Stop'
$typesPath = Join-Path $RepositoryRoot 'backend\src\modules\db-service\db-service-types.ts'
$accessPath = Join-Path $RepositoryRoot 'backend\src\modules\db-service\db-service-operation-access-mode.ts'
$manifestPath = Join-Path $PSScriptRoot 'BusinessSQLite-owner-manifest.json'

$types = Get-Content -LiteralPath $typesPath -Raw
$start = $types.IndexOf('export type DbServiceOperation =')
$end = $types.IndexOf('export type DbServiceOperationResult', $start)
if ($start -lt 0 -or $end -le $start) { throw 'DbServiceOperation union not found' }
$union = $types.Substring($start, $end - $start)
$sourceOps = [regex]::Matches($union, "type:\s*'([^']+)'") | ForEach-Object { $_.Groups[1].Value } | Sort-Object -Unique

$access = Get-Content -LiteralPath $accessPath -Raw
$accessOps = [regex]::Matches($access, "(?m)^\s{2}([a-z0-9_]+):\s*'(?:read|write|maintenance|runtime)'") | ForEach-Object { $_.Groups[1].Value } | Sort-Object -Unique

$manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
$manifestOps = @($manifest.operations | ForEach-Object { $_.operation } | Sort-Object -Unique)

$missingInManifest = Compare-Object $sourceOps $manifestOps -PassThru | Where-Object { $_ -in $sourceOps }
$missingInAccess = Compare-Object $sourceOps $accessOps -PassThru | Where-Object { $_ -in $sourceOps }
$extraInManifest = Compare-Object $sourceOps $manifestOps -PassThru | Where-Object { $_ -in $manifestOps }
if ($missingInManifest -or $missingInAccess -or $extraInManifest) {
    if ($missingInManifest) { Write-Error ('Missing from manifest: ' + ($missingInManifest -join ', ')) }
    if ($missingInAccess) { Write-Error ('Missing from access mode: ' + ($missingInAccess -join ', ')) }
    if ($extraInManifest) { Write-Error ('Extra in manifest: ' + ($extraInManifest -join ', ')) }
    exit 1
}

$writes = @($manifest.operations | Where-Object { $_.access -eq 'write' }).Count
$reads = @($manifest.operations | Where-Object { $_.access -eq 'read' }).Count
[pscustomobject]@{
    sourceUniqueOperations = $sourceOps.Count
    accessModeUniqueOperations = $accessOps.Count
    manifestUniqueOperations = $manifestOps.Count
    manifestWrites = $writes
    manifestReads = $reads
    status = 'pass'
} | ConvertTo-Json -Compress


param(
  [string]$OutputDir = 'release',
  [string]$PackageName = 'juhe-ai-release',
  [ValidateSet('tar.gz', 'zip', 'both')][string]$ArchiveFormat = 'both',
  [string]$FrontendApiBaseUrl = '/__aisys__/api',
  [string]$FrontendGatewayBaseUrl = '',
  [string]$ExpectedCommit = ''
)

$ErrorActionPreference = 'Stop'

function Assert-SafePackageName {
  param([Parameter(Mandatory = $true)][string]$Name)

  if ($Name.Length -gt 80 -or $Name -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]*$') {
    throw 'PackageName must be 1-80 characters and contain only letters, numbers, dot, underscore, or hyphen; it must start with a letter or number.'
  }
}

function Resolve-SafeReleaseRoot {
  param(
    [Parameter(Mandatory = $true)][string]$BasePath,
    [Parameter(Mandatory = $true)][string]$OutputDirectory
  )

  if ([string]::IsNullOrWhiteSpace($OutputDirectory) -or [System.IO.Path]::IsPathRooted($OutputDirectory)) {
    throw 'OutputDir must be a non-empty relative path inside the repository.'
  }

  if (($OutputDirectory -split '[\\/]') -contains '..') {
    throw 'OutputDir must not contain parent-directory traversal segments (..).'
  }

  $resolvedBasePath = [System.IO.Path]::GetFullPath($BasePath).TrimEnd(
    [System.IO.Path]::DirectorySeparatorChar,
    [System.IO.Path]::AltDirectorySeparatorChar
  )
  $resolvedOutputPath = [System.IO.Path]::GetFullPath(
    [System.IO.Path]::Combine($resolvedBasePath, $OutputDirectory)
  ).TrimEnd(
    [System.IO.Path]::DirectorySeparatorChar,
    [System.IO.Path]::AltDirectorySeparatorChar
  )
  $basePrefix = "$resolvedBasePath$([System.IO.Path]::DirectorySeparatorChar)"

  if (-not $resolvedOutputPath.StartsWith($basePrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw 'OutputDir must resolve strictly inside the repository.'
  }

  return $resolvedOutputPath
}

function Assert-SafeOutputAncestors {
  param(
    [Parameter(Mandatory = $true)][string]$BasePath,
    [Parameter(Mandatory = $true)][string]$CandidatePath
  )

  $currentPath = $BasePath
  $relativePath = [System.IO.Path]::GetRelativePath($BasePath, $CandidatePath)

  foreach ($segment in ($relativePath -split '[\\/]')) {
    if ([string]::IsNullOrEmpty($segment) -or $segment -eq '.') {
      continue
    }

    $currentPath = [System.IO.Path]::Combine($currentPath, $segment)
    $item = Get-Item -LiteralPath $currentPath -Force -ErrorAction SilentlyContinue
    if ($null -eq $item) {
      continue
    }

    if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
      throw "OutputDir must not traverse a reparse point: $currentPath"
    }

    if (-not $item.PSIsContainer) {
      throw "OutputDir contains a non-directory path component: $currentPath"
    }
  }
}

function Assert-NoReparsePoints {
  param(
    [Parameter(Mandatory = $true)][string]$Path,
    [switch]$Recurse
  )

  $rootItem = Get-Item -LiteralPath $Path -Force
  $items = @($rootItem)

  if ($Recurse -and $rootItem.PSIsContainer) {
    $items += @(Get-ChildItem -LiteralPath $Path -Force -Recurse)
  }

  foreach ($item in $items) {
    if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
      throw "Symbolic links, junctions, and other reparse points are forbidden: $($item.FullName)"
    }
  }
}

function Assert-SafeRemovalTarget {
  param(
    [Parameter(Mandatory = $true)][string]$TargetPath,
    [Parameter(Mandatory = $true)][string]$ExpectedParent,
    [switch]$Recurse
  )

  $resolvedTargetPath = [System.IO.Path]::GetFullPath($TargetPath)
  $resolvedExpectedParent = [System.IO.Path]::GetFullPath($ExpectedParent).TrimEnd(
    [System.IO.Path]::DirectorySeparatorChar,
    [System.IO.Path]::AltDirectorySeparatorChar
  )
  $actualParent = [System.IO.Path]::GetDirectoryName($resolvedTargetPath)

  if (-not [string]::Equals($actualParent, $resolvedExpectedParent, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Refusing to remove a path outside the release directory: $resolvedTargetPath"
  }

  $targetItem = Get-Item -LiteralPath $resolvedTargetPath -Force -ErrorAction SilentlyContinue
  if ($null -ne $targetItem) {
    Assert-NoReparsePoints -Path $resolvedTargetPath -Recurse:$Recurse
  }
}

function Invoke-ReleasePackageValidator {
  param(
    [Parameter(Mandatory = $true)][string[]]$Paths,
    [switch]$LinksOnly
  )

  $validatorArgs = @('--quiet')
  if ($LinksOnly) {
    $validatorArgs += '--links-only'
  }
  $validatorArgs += $Paths

  & node $validatorPath @validatorArgs
  if ($LASTEXITCODE -ne 0) {
    throw 'Release package validation failed.'
  }
}

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
Assert-SafePackageName -Name $PackageName
$releaseRoot = Resolve-SafeReleaseRoot -BasePath $repoRoot -OutputDirectory $OutputDir
Assert-SafeOutputAncestors -BasePath $repoRoot -CandidatePath $releaseRoot
$packageRoot = [System.IO.Path]::Combine($releaseRoot, $PackageName)
$tarArchivePath = [System.IO.Path]::Combine($releaseRoot, "$PackageName.tar.gz")
$zipArchivePath = [System.IO.Path]::Combine($releaseRoot, "$PackageName.zip")
$validatorPath = Join-Path $PSScriptRoot 'validate-release-package.mjs'

if (-not (Test-Path -LiteralPath $validatorPath -PathType Leaf)) {
  throw "Release package validator not found: $validatorPath"
}
Assert-NoReparsePoints -Path $validatorPath

function Copy-RequiredItem {
  param(
    [Parameter(Mandatory = $true)][string]$Source,
    [Parameter(Mandatory = $true)][string]$Destination
  )

  if (-not (Test-Path -LiteralPath $Source)) {
    throw "Required path not found: $Source"
  }

  Assert-NoReparsePoints -Path $Source -Recurse
  Invoke-ReleasePackageValidator -Paths @($Source) -LinksOnly
  Copy-Item -LiteralPath $Source -Destination $Destination -Recurse -Force
}

function Write-Utf8NoBom {
  param(
    [Parameter(Mandatory = $true)][string]$Path,
    [Parameter(Mandatory = $true)][string]$Content
  )

  [System.IO.File]::WriteAllText($Path, $Content, [System.Text.UTF8Encoding]::new($false))
}

function Copy-ReleaseBackendPackageJson {
  param(
    [Parameter(Mandatory = $true)][string]$Source,
    [Parameter(Mandatory = $true)][string]$Destination
  )

  if (-not (Test-Path -LiteralPath $Source)) {
    throw "Required path not found: $Source"
  }

  Assert-NoReparsePoints -Path $Source
  Invoke-ReleasePackageValidator -Paths @($Source) -LinksOnly
  $packageJson = Get-Content -Raw -LiteralPath $Source | ConvertFrom-Json
  $packageJson.scripts = [ordered]@{
    'check:runtime' = 'node dist/scripts/preflight/check-node-sqlite.js'
    'maintenance:backfill-account-balance' = 'node dist/scripts/maintenance/run-account-balance-backfill.js'
    'ops:drain-redis-streams' = 'node dist/scripts/operations/drain-redis-streams.js'
    'ops:redis-queue-fence' = 'node dist/scripts/operations/manage-redis-queue-fence.js'
    'start' = 'node dist/scripts/preflight/check-node-sqlite.js && node dist/server.js'
  }
  Write-Utf8NoBom -Path $Destination -Content (($packageJson | ConvertTo-Json -Depth 20) + "`n")
}

Set-Location $repoRoot

$releaseSourceOutput = @(& (Join-Path $PSScriptRoot 'assert-release-source.ps1') -RepoRoot $repoRoot -ExpectedCommit $ExpectedCommit)
$releaseSourceCommit = ((& git -C $repoRoot rev-parse HEAD 2>&1) | Select-Object -Last 1).Trim()
if ($LASTEXITCODE -ne 0 -or $releaseSourceCommit -notmatch '^[0-9a-fA-F]{40}$') {
  throw 'Unable to resolve the release source commit.'
}
$releaseSourceOutput | Write-Host

if (-not (Get-Command node -ErrorAction SilentlyContinue)) {
  throw 'Node.js LTS is required for packaging. Install Node.js 22.x LTS (>=22.13.0) or 24.x LTS (>=24.11.0) first.'
}

if (-not (Get-Command pnpm -ErrorAction SilentlyContinue)) {
  if (Get-Command corepack -ErrorAction SilentlyContinue) {
    corepack enable
    corepack prepare pnpm@latest --activate
  } else {
    throw 'pnpm is required. Install pnpm or enable corepack first.'
  }
}

pnpm --filter juhe-ai-backend check:runtime
if ($LASTEXITCODE -ne 0) {
  throw 'Node.js runtime preflight failed.'
}

Write-Host '==> Building workspace'
$env:VITE_JUHE_AI_API_BASE_URL = $FrontendApiBaseUrl
$env:VITE_JUHE_AI_GATEWAY_BASE_URL = $FrontendGatewayBaseUrl
Write-Host "==> Frontend API base URL: $FrontendApiBaseUrl"
if ($FrontendGatewayBaseUrl) {
  Write-Host "==> Frontend gateway base URL: $FrontendGatewayBaseUrl"
} else {
  Write-Host '==> Frontend gateway base URL: inferred from browser origin'
}
pnpm build
if ($LASTEXITCODE -ne 0) {
  throw 'Workspace build failed.'
}

@(& (Join-Path $PSScriptRoot 'assert-release-source.ps1') -RepoRoot $repoRoot -ExpectedCommit $releaseSourceCommit) | Write-Host

Write-Host '==> Preparing release folder'
Assert-SafeOutputAncestors -BasePath $repoRoot -CandidatePath $releaseRoot
New-Item -ItemType Directory -Force $releaseRoot | Out-Null
Assert-SafeOutputAncestors -BasePath $repoRoot -CandidatePath $releaseRoot
Assert-SafeRemovalTarget -TargetPath $packageRoot -ExpectedParent $releaseRoot -Recurse
Remove-Item -LiteralPath $packageRoot -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force $packageRoot | Out-Null
New-Item -ItemType Directory -Force (Join-Path $packageRoot 'backend') | Out-Null
New-Item -ItemType Directory -Force (Join-Path $packageRoot 'frontend') | Out-Null
New-Item -ItemType Directory -Force (Join-Path $packageRoot 'docs') | Out-Null
New-Item -ItemType Directory -Force (Join-Path $packageRoot 'scripts') | Out-Null
New-Item -ItemType Directory -Force (Join-Path $packageRoot 'deploy') | Out-Null
Write-Utf8NoBom -Path (Join-Path $packageRoot 'RELEASE_SOURCE_COMMIT') -Content "$releaseSourceCommit`n"

Copy-RequiredItem (Join-Path $repoRoot 'package.json') (Join-Path $packageRoot 'package.json')
Copy-RequiredItem (Join-Path $repoRoot 'pnpm-lock.yaml') (Join-Path $packageRoot 'pnpm-lock.yaml')
Copy-RequiredItem (Join-Path $repoRoot 'pnpm-workspace.yaml') (Join-Path $packageRoot 'pnpm-workspace.yaml')
Copy-ReleaseBackendPackageJson (Join-Path $repoRoot 'backend/package.json') (Join-Path $packageRoot 'backend/package.json')
Copy-RequiredItem (Join-Path $repoRoot 'backend/.env.example') (Join-Path $packageRoot 'backend/.env.example')
Copy-RequiredItem (Join-Path $repoRoot 'backend/dist') (Join-Path $packageRoot 'backend/dist')
Copy-RequiredItem (Join-Path $repoRoot 'frontend/package.json') (Join-Path $packageRoot 'frontend/package.json')
Copy-RequiredItem (Join-Path $repoRoot 'frontend/.env.example') (Join-Path $packageRoot 'frontend/.env.example')
Copy-RequiredItem (Join-Path $repoRoot 'frontend/dist') (Join-Path $packageRoot 'frontend/dist')
Copy-RequiredItem (Join-Path $repoRoot 'deploy/start.sh') (Join-Path $packageRoot 'start.sh')
Copy-RequiredItem (Join-Path $repoRoot 'deploy/start.ps1') (Join-Path $packageRoot 'start.ps1')
Copy-RequiredItem (Join-Path $repoRoot 'scripts/run-with-owner-lock.mjs') (Join-Path $packageRoot 'scripts/run-with-owner-lock.mjs')
Copy-RequiredItem (Join-Path $repoRoot 'scripts/validate-owner-manifest.mjs') (Join-Path $packageRoot 'scripts/validate-owner-manifest.mjs')
Copy-RequiredItem (Join-Path $repoRoot 'deploy/owner-manifest.json') (Join-Path $packageRoot 'deploy/owner-manifest.json')
Copy-RequiredItem (Join-Path $repoRoot 'deploy/README.md') (Join-Path $packageRoot 'README.md')
Copy-RequiredItem (Join-Path $repoRoot 'docs/deploy') (Join-Path $packageRoot 'docs/deploy')

$startShellPath = Join-Path $packageRoot 'start.sh'
$startShellContent = (Get-Content -Raw -LiteralPath $startShellPath) -replace "`r`n", "`n"
Write-Utf8NoBom -Path $startShellPath -Content $startShellContent

$startPowerShellPath = Join-Path $packageRoot 'start.ps1'
$startPowerShellContent = Get-Content -Raw -LiteralPath $startPowerShellPath
Write-Utf8NoBom -Path $startPowerShellPath -Content $startPowerShellContent

Assert-NoReparsePoints -Path $packageRoot -Recurse
Invoke-ReleasePackageValidator -Paths @($packageRoot)

if ($ArchiveFormat -eq 'tar.gz' -or $ArchiveFormat -eq 'both') {
  Write-Host '==> Creating tar.gz archive'
  Assert-SafeRemovalTarget -TargetPath $tarArchivePath -ExpectedParent $releaseRoot
  Remove-Item -LiteralPath $tarArchivePath -Force -ErrorAction SilentlyContinue
  tar -czf $tarArchivePath -C $releaseRoot $PackageName
  Write-Host "==> Done: $tarArchivePath"
}

if ($ArchiveFormat -eq 'zip' -or $ArchiveFormat -eq 'both') {
  Write-Host '==> Creating zip archive'
  Assert-SafeRemovalTarget -TargetPath $zipArchivePath -ExpectedParent $releaseRoot
  Remove-Item -LiteralPath $zipArchivePath -Force -ErrorAction SilentlyContinue
  Compress-Archive -LiteralPath $packageRoot -DestinationPath $zipArchivePath -Force
  Write-Host "==> Done: $zipArchivePath"
}

Write-Host 'Upload the archive to the target server, extract it, then run start.sh on Linux/macOS or start.ps1 on Windows.'

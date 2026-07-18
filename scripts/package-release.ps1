param(
  [string]$OutputDir = 'release',
  [string]$PackageName = 'juhe-ai-release',
  [ValidateSet('tar.gz', 'zip', 'both')][string]$ArchiveFormat = 'both',
  [string]$FrontendApiBaseUrl = '/__aisys__/api',
  [string]$FrontendGatewayBaseUrl = '',
  [string]$ExpectedCommit = '',
  [switch]$IncludeLocalEnv
)

$ErrorActionPreference = 'Stop'

$repoRoot = Resolve-Path (Join-Path $PSScriptRoot '..')
$releaseRoot = Join-Path $repoRoot $OutputDir
$packageRoot = Join-Path $releaseRoot $PackageName
$tarArchivePath = Join-Path $releaseRoot "$PackageName.tar.gz"
$zipArchivePath = Join-Path $releaseRoot "$PackageName.zip"

function Copy-RequiredItem {
  param(
    [Parameter(Mandatory = $true)][string]$Source,
    [Parameter(Mandatory = $true)][string]$Destination
  )

  if (-not (Test-Path -LiteralPath $Source)) {
    throw "Required path not found: $Source"
  }

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

  $packageJson = Get-Content -Raw -LiteralPath $Source | ConvertFrom-Json
  $packageJson.scripts = [ordered]@{
    'check:runtime' = 'node dist/scripts/preflight/check-node-sqlite.js'
    'maintenance:backfill-account-balance' = 'node dist/scripts/maintenance/run-account-balance-backfill.js'
    'start' = 'node dist/scripts/preflight/check-node-sqlite.js && node dist/server.js'
  }
  Write-Utf8NoBom -Path $Destination -Content (($packageJson | ConvertTo-Json -Depth 20) + "`n")
}

Set-Location $repoRoot

& (Join-Path $PSScriptRoot 'assert-release-source.ps1') -RepoRoot $repoRoot -ExpectedCommit $ExpectedCommit
$releaseSourceCommit = ((& git -C $repoRoot rev-parse HEAD) | Select-Object -Last 1).Trim()

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
& (Join-Path $PSScriptRoot 'assert-release-source.ps1') -RepoRoot $repoRoot -ExpectedCommit $releaseSourceCommit

Write-Host '==> Preparing release folder'
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

if ($IncludeLocalEnv -and (Test-Path -LiteralPath (Join-Path $repoRoot 'backend/.env'))) {
  Copy-Item -LiteralPath (Join-Path $repoRoot 'backend/.env') -Destination (Join-Path $packageRoot 'backend/.env.example.local') -Force
  Write-Host '==> Copied backend/.env as backend/.env.example.local; review secrets before sharing'
}

if ($IncludeLocalEnv -and (Test-Path -LiteralPath (Join-Path $repoRoot 'frontend/.env'))) {
  Copy-Item -LiteralPath (Join-Path $repoRoot 'frontend/.env') -Destination (Join-Path $packageRoot 'frontend/.env.example.local') -Force
  Write-Host '==> Copied frontend/.env as frontend/.env.example.local; frontend dist is already built'
}

if ($ArchiveFormat -eq 'tar.gz' -or $ArchiveFormat -eq 'both') {
  Write-Host '==> Creating tar.gz archive'
  Remove-Item -LiteralPath $tarArchivePath -Force -ErrorAction SilentlyContinue
  tar -czf $tarArchivePath -C $releaseRoot $PackageName
  Write-Host "==> Done: $tarArchivePath"
}

if ($ArchiveFormat -eq 'zip' -or $ArchiveFormat -eq 'both') {
  Write-Host '==> Creating zip archive'
  Remove-Item -LiteralPath $zipArchivePath -Force -ErrorAction SilentlyContinue
  Compress-Archive -LiteralPath $packageRoot -DestinationPath $zipArchivePath -Force
  Write-Host "==> Done: $zipArchivePath"
}

Write-Host 'Upload the archive to the target server, extract it, then run start.sh on Linux/macOS or start.ps1 on Windows.'

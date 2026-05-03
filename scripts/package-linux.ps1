param(
  [string]$OutputDir = "release",
  [string]$PackageName = "juhe-ai-linux"
)

$ErrorActionPreference = "Stop"

$repoRoot = Resolve-Path (Join-Path $PSScriptRoot "..")
$releaseRoot = Join-Path $repoRoot $OutputDir
$packageRoot = Join-Path $releaseRoot $PackageName
$archivePath = Join-Path $releaseRoot "$PackageName.tar.gz"

function Copy-RequiredItem {
  param(
    [Parameter(Mandatory = $true)][string]$Source,
    [Parameter(Mandatory = $true)][string]$Destination
  )

  if (-not (Test-Path $Source)) {
    throw "Required path not found: $Source"
  }

  Copy-Item -LiteralPath $Source -Destination $Destination -Recurse -Force
}

Set-Location $repoRoot

Write-Host "==> Building workspace"
pnpm build

Write-Host "==> Preparing release folder"
Remove-Item -LiteralPath $packageRoot -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force $packageRoot | Out-Null
New-Item -ItemType Directory -Force (Join-Path $packageRoot "backend") | Out-Null
New-Item -ItemType Directory -Force (Join-Path $packageRoot "frontend") | Out-Null
New-Item -ItemType Directory -Force (Join-Path $packageRoot "deploy") | Out-Null

Copy-RequiredItem (Join-Path $repoRoot "package.json") (Join-Path $packageRoot "package.json")
Copy-RequiredItem (Join-Path $repoRoot "pnpm-lock.yaml") (Join-Path $packageRoot "pnpm-lock.yaml")
Copy-RequiredItem (Join-Path $repoRoot "pnpm-workspace.yaml") (Join-Path $packageRoot "pnpm-workspace.yaml")
Copy-RequiredItem (Join-Path $repoRoot "backend/package.json") (Join-Path $packageRoot "backend/package.json")
Copy-RequiredItem (Join-Path $repoRoot "backend/.env.example") (Join-Path $packageRoot "backend/.env.example")
Copy-RequiredItem (Join-Path $repoRoot "backend/dist") (Join-Path $packageRoot "backend/dist")
Copy-RequiredItem (Join-Path $repoRoot "frontend/.env.example") (Join-Path $packageRoot "frontend/.env.example")
Copy-RequiredItem (Join-Path $repoRoot "frontend/dist") (Join-Path $packageRoot "frontend/dist")
Copy-RequiredItem (Join-Path $repoRoot "deploy/start.sh") (Join-Path $packageRoot "start.sh")
Copy-RequiredItem (Join-Path $repoRoot "deploy/README.md") (Join-Path $packageRoot "README.md")

if (Test-Path (Join-Path $repoRoot "backend/.env")) {
  Copy-Item -LiteralPath (Join-Path $repoRoot "backend/.env") -Destination (Join-Path $packageRoot "backend/.env.example.local") -Force
  Write-Host "==> Copied backend/.env as backend/.env.example.local; review secrets before sharing"
}

if (Test-Path (Join-Path $repoRoot "frontend/.env")) {
  Copy-Item -LiteralPath (Join-Path $repoRoot "frontend/.env") -Destination (Join-Path $packageRoot "frontend/.env.example.local") -Force
  Write-Host "==> Copied frontend/.env as frontend/.env.example.local; frontend dist is already built"
}

Write-Host "==> Creating archive"
Remove-Item -LiteralPath $archivePath -Force -ErrorAction SilentlyContinue
tar -czf $archivePath -C $releaseRoot $PackageName

Write-Host "==> Done: $archivePath"
Write-Host "Upload it to Linux, extract it, then run: bash start.sh"

$ErrorActionPreference = 'Stop'

$appDir = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $appDir

function Test-CommandExists {
  param([Parameter(Mandatory = $true)][string]$Name)
  return [bool](Get-Command $Name -ErrorAction SilentlyContinue)
}

function Read-DotEnvValue {
  param(
    [Parameter(Mandatory = $true)][string]$Path,
    [Parameter(Mandatory = $true)][string]$Name,
    [Parameter(Mandatory = $true)][string]$Fallback
  )

  if (-not (Test-Path -LiteralPath $Path)) {
    return $Fallback
  }

  $pattern = '^\s*' + [regex]::Escape($Name) + '=(.*)$'
  $line = Get-Content -LiteralPath $Path | Where-Object { $_ -match $pattern } | Select-Object -Last 1
  if (-not $line) {
    return $Fallback
  }

  $value = ($line -replace $pattern, '$1').Trim().Trim('"').Trim("'")
  if ($value) { return $value }
  return $Fallback
}

if (-not (Test-CommandExists 'node')) {
  throw 'Node.js is required. Install Node.js 22.5+ before running this script.'
}

node --input-type=module -e "import 'node:sqlite'" *> $null
if ($LASTEXITCODE -ne 0) {
  $nodeVersion = (& node -v) -join ''
  throw "Node.js with node:sqlite support is required. Install Node.js 22.5+ or a newer LTS release. Current: $nodeVersion"
}

if (-not (Test-CommandExists 'pnpm')) {
  if (Test-CommandExists 'corepack') {
    corepack enable
    corepack prepare pnpm@latest --activate
  } else {
    throw 'pnpm is required. Install pnpm or enable corepack first.'
  }
}

if (-not (Test-Path -LiteralPath 'backend/.env')) {
  if (Test-Path -LiteralPath 'backend/.env.example.local') {
    Copy-Item -LiteralPath 'backend/.env.example.local' -Destination 'backend/.env'
    Write-Host 'Created backend/.env from backend/.env.example.local'
  } else {
    Copy-Item -LiteralPath 'backend/.env.example' -Destination 'backend/.env'
    Write-Host 'Created backend/.env from backend/.env.example'
  }
  Write-Host 'Please review backend/.env before production use, especially JUHE_AI_SECRET.'
}

New-Item -ItemType Directory -Force 'backend/data' | Out-Null

if (-not (Test-Path -LiteralPath 'node_modules') -or -not (Test-Path -LiteralPath 'backend/node_modules')) {
  Write-Host 'Installing production dependencies...'
  pnpm install --prod --frozen-lockfile --filter juhe-ai-backend...
} else {
  Write-Host 'Using existing node_modules. Remove node_modules and backend/node_modules to force reinstall.'
}

$hostValue = Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_HOST' -Fallback '127.0.0.1'
$portValue = Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_PORT' -Fallback '3000'

Write-Host "Starting juhe-ai at http://${hostValue}:${portValue}"
Write-Host 'The Web/API process will supervise a separate background worker process.'
node backend/dist/server.js

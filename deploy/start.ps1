$ErrorActionPreference = 'Stop'
if ($PSVersionTable.PSVersion.Major -ge 7) {
  $PSNativeCommandUseErrorActionPreference = $true
}

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

function Set-DotEnvValue {
  param(
    [Parameter(Mandatory = $true)][string]$Path,
    [Parameter(Mandatory = $true)][string]$Name,
    [Parameter(Mandatory = $true)][string]$Value
  )

  $lines = if (Test-Path -LiteralPath $Path) { @(Get-Content -LiteralPath $Path) } else { @() }
  $pattern = '^\s*' + [regex]::Escape($Name) + '='
  $updated = $false
  for ($index = 0; $index -lt $lines.Count; $index += 1) {
    if ($lines[$index] -match $pattern) {
      $lines[$index] = "$Name=$Value"
      $updated = $true
    }
  }
  if (-not $updated) {
    $lines += "$Name=$Value"
  }
  Set-Content -LiteralPath $Path -Value $lines -Encoding utf8
}

function New-JuheSecret {
  $bytes = [byte[]]::new(32)
  [System.Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
  return (($bytes | ForEach-Object { $_.ToString('x2') }) -join '')
}

function Ensure-DeploymentDefaults {
  $envPath = 'backend/.env'
  $fileSecret = Read-DotEnvValue -Path $envPath -Name 'JUHE_AI_SECRET' -Fallback ''
  if (-not $env:JUHE_AI_SECRET -and -not $fileSecret) {
    $generatedSecret = New-JuheSecret
    Set-DotEnvValue -Path $envPath -Name 'JUHE_AI_SECRET' -Value $generatedSecret
    $env:JUHE_AI_SECRET = $generatedSecret
    Write-Host 'Generated JUHE_AI_SECRET and saved it to backend/.env. Keep this value when migrating existing data.'
  }

  $fileOrigins = Read-DotEnvValue -Path $envPath -Name 'JUHE_AI_ALLOWED_ORIGINS' -Fallback ''
  if (-not $env:JUHE_AI_ALLOWED_ORIGINS -and -not $fileOrigins) {
    $publicOrigin = Read-DotEnvValue -Path $envPath -Name 'JUHE_AI_PUBLIC_ORIGIN' -Fallback ''
    if (-not $publicOrigin -and $env:JUHE_AI_PUBLIC_ORIGIN) {
      $publicOrigin = $env:JUHE_AI_PUBLIC_ORIGIN
    }
    $publicPort = if ($env:JUHE_AI_PUBLIC_PORT) {
      $env:JUHE_AI_PUBLIC_PORT
    } elseif ($env:JUHE_AI_PORT) {
      $env:JUHE_AI_PORT
    } else {
      Read-DotEnvValue -Path $envPath -Name 'JUHE_AI_PORT' -Fallback '3000'
    }
    $defaultOrigins = if ($publicOrigin) {
      $publicOrigin
    } else {
      "http://localhost:${publicPort},http://127.0.0.1:${publicPort}"
    }
    Set-DotEnvValue -Path $envPath -Name 'JUHE_AI_ALLOWED_ORIGINS' -Value $defaultOrigins
    $env:JUHE_AI_ALLOWED_ORIGINS = $defaultOrigins
    Write-Host "Set JUHE_AI_ALLOWED_ORIGINS to $defaultOrigins. Adjust backend/.env if using a public domain or reverse proxy."
  }
}

function Test-RipgrepDependency {
  Push-Location 'backend'
  try {
    node --input-type=module -e "import('@vscode/ripgrep').then(({ rgPath }) => import('node:fs').then(({ existsSync }) => process.exit(existsSync(rgPath) ? 0 : 1))).catch(() => process.exit(1))" *> $null
    return $LASTEXITCODE -eq 0
  } finally {
    Pop-Location
  }
}

if (-not (Test-CommandExists 'node')) {
  throw 'Node.js LTS is required. Install Node.js 22.x LTS (>=22.13.0) or 24.x LTS (>=24.11.0) before running this script.'
}

$runtimeCheckPath = 'backend/dist/scripts/preflight/check-node-sqlite.js'
if (-not (Test-Path -LiteralPath $runtimeCheckPath)) {
  throw "Runtime preflight script not found: $runtimeCheckPath. Please rebuild the release package."
}

node $runtimeCheckPath
if ($LASTEXITCODE -ne 0) {
  exit $LASTEXITCODE
}

if (-not (Test-CommandExists 'pnpm')) {
  if (Test-CommandExists 'corepack') {
    corepack enable
    corepack prepare pnpm@latest --activate
  } else {
    throw 'pnpm is required. Install pnpm or enable corepack first.'
  }
}

$env:NODE_ENV = if ($env:NODE_ENV) { $env:NODE_ENV } else { 'production' }

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

Ensure-DeploymentDefaults

New-Item -ItemType Directory -Force 'backend/data' | Out-Null

if (-not (Test-Path -LiteralPath 'node_modules') -or -not (Test-Path -LiteralPath 'backend/node_modules') -or -not (Test-RipgrepDependency)) {
  Write-Host 'Installing production dependencies...'
  pnpm install --prod --frozen-lockfile --filter juhe-ai-backend...
} else {
  Write-Host 'Using existing node_modules. Remove node_modules and backend/node_modules to force reinstall.'
}

$hostValue = Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_HOST' -Fallback '127.0.0.1'
$portValue = Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_PORT' -Fallback '3000'

Write-Host "Starting juhe-ai at http://${hostValue}:${portValue}"
Write-Host 'The Web/API process will supervise separate background worker and DB service processes.'
node backend/dist/server.js

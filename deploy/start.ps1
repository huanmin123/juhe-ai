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
    [AllowEmptyString()][string]$Fallback = ''
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

function Get-RuntimeLogIndexerNodeLauncher {
  return @'
import { appendFileSync, closeSync, existsSync, openSync, readFileSync } from 'node:fs'
import { randomUUID } from 'node:crypto'
import { isAbsolute, resolve } from 'node:path'
import { createRequire } from 'node:module'
import { spawn } from 'node:child_process'

const [binaryPath, backendRoot, logPath] = process.argv.slice(1)
const require = createRequire(resolve(backendRoot, 'package.json'))
const { parse } = require('dotenv')
const baseEnvPath = resolve(backendRoot, '.env')
const capacityEnvPath = resolve(backendRoot, '.env.capacity')

function readEnv(path) {
  return existsSync(path) ? parse(readFileSync(path)) : {}
}

function configured(name) {
  if (Object.hasOwn(process.env, name)) return { defined: true, value: process.env[name] ?? '' }
  if (Object.hasOwn(overlayEnv, name)) return { defined: true, value: overlayEnv[name] ?? '' }
  if (Object.hasOwn(capacityEnv, name)) return { defined: true, value: capacityEnv[name] ?? '' }
  if (Object.hasOwn(baseEnv, name)) return { defined: true, value: baseEnv[name] ?? '' }
  return { defined: false, value: '' }
}

function isCapacityEnvironmentVariable(name) {
  return name.startsWith('JUHE_AI_CONCURRENCY_')
    || name.startsWith('JUHE_AI_ACCOUNT_')
    || name.startsWith('JUHE_AI_BACKGROUND_')
    || name.startsWith('JUHE_AI_GATEWAY_')
    || name.startsWith('JUHE_AI_DB_')
    || name.startsWith('JUHE_AI_CHAT_DB_SERVICE_')
    || name.startsWith('JUHE_AI_REDIS_STREAM_')
    || name.startsWith('JUHE_AI_USAGE_SPOOL_')
    || name === 'JUHE_AI_SYSTEM_API_DB_SERVICE_MAX_IN_FLIGHT'
    || /^JUHE_AI_(GATEWAY|USAGE|LOG|STATS|OPS)_WORKER_REPLICAS$/.test(name)
}

const disableBaseEnv = String(process.env.JUHE_AI_DISABLE_BASE_ENV ?? '').trim().toLowerCase() === 'true'
const baseEnv = disableBaseEnv ? {} : readEnv(baseEnvPath)
const overlayName = (process.env.JUHE_AI_ENV_FILE ?? baseEnv.JUHE_AI_ENV_FILE ?? '').trim()
const overlayEnv = overlayName ? readEnv(isAbsolute(overlayName) ? overlayName : resolve(backendRoot, overlayName)) : {}
const capacityEnv = Object.fromEntries(Object.entries(readEnv(capacityEnvPath)).filter(([name]) => isCapacityEnvironmentVariable(name)))
const childEnv = { ...process.env }
const names = [
  'JUHE_AI_DATABASE_DRIVER',
  'JUHE_AI_RUNTIME_MODE',
  'JUHE_AI_RUNTIME_LOG_STORE',
  'JUHE_AI_RUNTIME_LOG_DATABASE_PATH',
  'JUHE_AI_TABLE_MONITOR_DATABASE_PATH',
  'JUHE_AI_RUNTIME_LOG_OWNER_LEASE',
  'JUHE_AI_RUNTIME_LOG_ONCE',
  'JUHE_AI_RUNTIME_LOG_POLL_INTERVAL',
  'JUHE_AI_RUNTIME_LOG_RETENTION_INTERVAL',
  'JUHE_AI_RUNTIME_LOG_RETENTION_DAYS',
  'JUHE_AI_RUNTIME_LOG_BATCH_SIZE',
  'JUHE_AI_DATABASE_PATH',
  'JUHE_AI_DATASET_DATABASE_PATH',
  'JUHE_AI_USAGE_CATALOG_DATABASE_PATH',
  'JUHE_AI_STATS_DATABASE_PATH',
  'JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT',
  'JUHE_AI_POSTGRES_URL',
  'JUHE_AI_LOG_DIR',
  'JUHE_AI_LOG_FILE_ENABLED',
  'JUHE_AI_LOG_RETENTION_DAYS',
  'JUHE_AI_LOG_MAX_FILES',
  'JUHE_AI_RG_PATH'
]
for (const name of names) {
  const value = configured(name)
  if (value.defined) childEnv[name] = value.value
}

const configuredInstance = configured('JUHE_AI_RUNTIME_LOG_INSTANCE_ID')
if (configuredInstance.defined) {
  if (!configuredInstance.value.trim()) throw new Error('JUHE_AI_RUNTIME_LOG_INSTANCE_ID is configured but empty.')
  childEnv.JUHE_AI_RUNTIME_LOG_INSTANCE_ID = configuredInstance.value
} else {
  if (disableBaseEnv) {
    throw new Error('JUHE_AI_RUNTIME_LOG_INSTANCE_ID must be set outside backend/.env when JUHE_AI_DISABLE_BASE_ENV=true.')
  }
  const instanceId = `runtime-log-indexer-${randomUUID()}`
  appendFileSync(baseEnvPath, `\nJUHE_AI_RUNTIME_LOG_INSTANCE_ID=${instanceId}\n`, 'utf8')
  childEnv.JUHE_AI_RUNTIME_LOG_INSTANCE_ID = instanceId
}

const runtimeMode = (childEnv.JUHE_AI_RUNTIME_MODE ?? '').trim().toLowerCase()
const hasPerformanceHints = ['JUHE_AI_POSTGRES_URL', 'JUHE_AI_REDIS_CACHE_URL', 'JUHE_AI_REDIS_STATE_URL', 'JUHE_AI_REDIS_QUEUE_URL']
  .some((name) => Boolean(configured(name).value.trim()))
if (!(childEnv.JUHE_AI_RUNTIME_LOG_STORE ?? '').trim() && !(childEnv.JUHE_AI_DATABASE_DRIVER ?? '').trim()) {
  childEnv.JUHE_AI_DATABASE_DRIVER = runtimeMode === 'performance' || (!runtimeMode && hasPerformanceHints) ? 'postgres' : 'sqlite'
}

function absoluteBackendPath(value, fallback) {
  const selected = (value ?? fallback).trim()
  return isAbsolute(selected) ? selected : resolve(backendRoot, selected)
}

childEnv.JUHE_AI_LOG_DIR = absoluteBackendPath(childEnv.JUHE_AI_LOG_DIR, './logs')
const runtimeLogStore = (childEnv.JUHE_AI_RUNTIME_LOG_STORE ?? '').trim() || (childEnv.JUHE_AI_DATABASE_DRIVER ?? '').trim()
if (runtimeLogStore.toLowerCase() === 'sqlite') {
  childEnv.JUHE_AI_DATABASE_PATH = absoluteBackendPath(childEnv.JUHE_AI_DATABASE_PATH, './data/juhe-ai.sqlite3')
  childEnv.JUHE_AI_DATASET_DATABASE_PATH = absoluteBackendPath(childEnv.JUHE_AI_DATASET_DATABASE_PATH, './data/juhe-ai-dataset.sqlite3')
  childEnv.JUHE_AI_RUNTIME_LOG_DATABASE_PATH = absoluteBackendPath(childEnv.JUHE_AI_RUNTIME_LOG_DATABASE_PATH, './data/juhe-ai-runtime-log.sqlite3')
  childEnv.JUHE_AI_TABLE_MONITOR_DATABASE_PATH = absoluteBackendPath(childEnv.JUHE_AI_TABLE_MONITOR_DATABASE_PATH, './data/juhe-ai-table-monitor.sqlite3')
  childEnv.JUHE_AI_USAGE_CATALOG_DATABASE_PATH = absoluteBackendPath(childEnv.JUHE_AI_USAGE_CATALOG_DATABASE_PATH, './data/juhe-ai-usage-catalog.sqlite3')
  childEnv.JUHE_AI_STATS_DATABASE_PATH = absoluteBackendPath(childEnv.JUHE_AI_STATS_DATABASE_PATH, './data/juhe-ai-stats.sqlite3')
  childEnv.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = absoluteBackendPath(childEnv.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT, './data/codex-context/state-shards')
}

const logFd = openSync(logPath, 'a')
try {
  const child = spawn(binaryPath, [], {
    cwd: process.cwd(),
    detached: true,
    env: childEnv,
    stdio: ['ignore', logFd, logFd],
    windowsHide: true
  })
  if (!child.pid) throw new Error('Unable to start juhe-ai-runtime-log-indexer.')
  child.unref()
  process.stdout.write(String(child.pid))
} finally {
  closeSync(logFd)
}
'@
}

function Get-TableMonitorNodeLauncher {
  return @'
import { closeSync, existsSync, openSync, readFileSync } from 'node:fs'
import { isAbsolute, resolve } from 'node:path'
import { createRequire } from 'node:module'
import { spawn } from 'node:child_process'

const [binaryPath, backendRoot, logPath] = process.argv.slice(1)
const require = createRequire(resolve(backendRoot, 'package.json'))
const { parse } = require('dotenv')
const baseEnvPath = resolve(backendRoot, '.env')
const capacityEnvPath = resolve(backendRoot, '.env.capacity')

function readEnv(path) {
  return existsSync(path) ? parse(readFileSync(path)) : {}
}

const disableBaseEnv = String(process.env.JUHE_AI_DISABLE_BASE_ENV ?? '').trim().toLowerCase() === 'true'
const baseEnv = disableBaseEnv ? {} : readEnv(baseEnvPath)
const overlayName = (process.env.JUHE_AI_ENV_FILE ?? baseEnv.JUHE_AI_ENV_FILE ?? '').trim()
const overlayEnv = overlayName ? readEnv(isAbsolute(overlayName) ? overlayName : resolve(backendRoot, overlayName)) : {}
const capacityEnv = Object.fromEntries(Object.entries(readEnv(capacityEnvPath)).filter(([name]) => isCapacityEnvironmentVariable(name)))

function configured(name) {
  if (Object.hasOwn(process.env, name)) return { defined: true, value: process.env[name] ?? '' }
  if (Object.hasOwn(overlayEnv, name)) return { defined: true, value: overlayEnv[name] ?? '' }
  if (Object.hasOwn(capacityEnv, name)) return { defined: true, value: capacityEnv[name] ?? '' }
  if (Object.hasOwn(baseEnv, name)) return { defined: true, value: baseEnv[name] ?? '' }
  return { defined: false, value: '' }
}

function isCapacityEnvironmentVariable(name) {
  return name.startsWith('JUHE_AI_CONCURRENCY_')
    || name.startsWith('JUHE_AI_ACCOUNT_')
    || name.startsWith('JUHE_AI_BACKGROUND_')
    || name.startsWith('JUHE_AI_GATEWAY_')
    || name.startsWith('JUHE_AI_DB_')
    || name.startsWith('JUHE_AI_CHAT_DB_SERVICE_')
    || name.startsWith('JUHE_AI_REDIS_STREAM_')
    || name.startsWith('JUHE_AI_USAGE_SPOOL_')
    || name === 'JUHE_AI_SYSTEM_API_DB_SERVICE_MAX_IN_FLIGHT'
    || /^JUHE_AI_(GATEWAY|USAGE|LOG|STATS|OPS)_WORKER_REPLICAS$/.test(name)
}

const childEnv = { ...process.env }
const names = [
  'JUHE_AI_DATABASE_DRIVER',
  'JUHE_AI_RUNTIME_MODE',
  'JUHE_AI_TABLE_MONITOR_STORE',
  'JUHE_AI_TABLE_MONITOR_INTERVAL',
  'JUHE_AI_TABLE_MONITOR_RUN_TIMEOUT',
  'JUHE_AI_TABLE_MONITOR_OWNER_LEASE',
  'JUHE_AI_TABLE_MONITOR_RETENTION_DAYS',
  'JUHE_AI_TABLE_MONITOR_MAX_TABLES',
  'JUHE_AI_TABLE_MONITOR_MAX_CONCURRENT_SOURCES',
  'JUHE_AI_TABLE_MONITOR_RETENTION_BATCH_SIZE',
  'JUHE_AI_TABLE_MONITOR_RETENTION_MAX_BATCHES',
  'JUHE_AI_TABLE_MONITOR_DATABASE_PATH',
  'JUHE_AI_RUNTIME_LOG_DATABASE_PATH',
  'JUHE_AI_DATABASE_PATH',
  'JUHE_AI_DATASET_DATABASE_PATH',
  'JUHE_AI_USAGE_CATALOG_DATABASE_PATH',
  'JUHE_AI_STATS_DATABASE_PATH',
  'JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT',
  'JUHE_AI_POSTGRES_URL'
]
for (const name of names) {
  const value = configured(name)
  if (value.defined) childEnv[name] = value.value
}

const configuredInstance = configured('JUHE_AI_TABLE_MONITOR_INSTANCE_ID')
if (configuredInstance.defined) {
  if (!configuredInstance.value.trim()) throw new Error('JUHE_AI_TABLE_MONITOR_INSTANCE_ID is configured but empty.')
  childEnv.JUHE_AI_TABLE_MONITOR_INSTANCE_ID = configuredInstance.value
} else {
  throw new Error('JUHE_AI_TABLE_MONITOR_INSTANCE_ID is required; release startup does not generate owner identities.')
}

const runtimeMode = (childEnv.JUHE_AI_RUNTIME_MODE ?? '').trim().toLowerCase()
const hasPerformanceHints = ['JUHE_AI_POSTGRES_URL', 'JUHE_AI_REDIS_CACHE_URL', 'JUHE_AI_REDIS_STATE_URL', 'JUHE_AI_REDIS_QUEUE_URL']
  .some((name) => Boolean(configured(name).value.trim()))
if (!(childEnv.JUHE_AI_TABLE_MONITOR_STORE ?? '').trim() && !(childEnv.JUHE_AI_DATABASE_DRIVER ?? '').trim()) {
  childEnv.JUHE_AI_DATABASE_DRIVER = runtimeMode === 'performance' || (!runtimeMode && hasPerformanceHints) ? 'postgres' : 'sqlite'
}

function absoluteBackendPath(value, fallback) {
  const selected = (value ?? fallback).trim()
  return isAbsolute(selected) ? selected : resolve(backendRoot, selected)
}

const tableMonitorStore = (childEnv.JUHE_AI_TABLE_MONITOR_STORE ?? '').trim() || (childEnv.JUHE_AI_DATABASE_DRIVER ?? '').trim() || 'sqlite'
if (tableMonitorStore.toLowerCase() === 'sqlite') {
  childEnv.JUHE_AI_TABLE_MONITOR_DATABASE_PATH = absoluteBackendPath(childEnv.JUHE_AI_TABLE_MONITOR_DATABASE_PATH, './data/juhe-ai-table-monitor.sqlite3')
  childEnv.JUHE_AI_RUNTIME_LOG_DATABASE_PATH = absoluteBackendPath(childEnv.JUHE_AI_RUNTIME_LOG_DATABASE_PATH, './data/juhe-ai-runtime-log.sqlite3')
  childEnv.JUHE_AI_DATABASE_PATH = absoluteBackendPath(childEnv.JUHE_AI_DATABASE_PATH, './data/juhe-ai.sqlite3')
  childEnv.JUHE_AI_DATASET_DATABASE_PATH = absoluteBackendPath(childEnv.JUHE_AI_DATASET_DATABASE_PATH, './data/juhe-ai-dataset.sqlite3')
  childEnv.JUHE_AI_USAGE_CATALOG_DATABASE_PATH = absoluteBackendPath(childEnv.JUHE_AI_USAGE_CATALOG_DATABASE_PATH, './data/juhe-ai-usage-catalog.sqlite3')
  childEnv.JUHE_AI_STATS_DATABASE_PATH = absoluteBackendPath(childEnv.JUHE_AI_STATS_DATABASE_PATH, './data/juhe-ai-stats.sqlite3')
  childEnv.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = absoluteBackendPath(childEnv.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT, './data/codex-context/state-shards')
}

const logFd = openSync(logPath, 'a')
try {
  const child = spawn(binaryPath, [], {
    cwd: process.cwd(),
    detached: true,
    env: childEnv,
    stdio: ['ignore', logFd, logFd],
    windowsHide: true
  })
  if (!child.pid) throw new Error('Unable to start juhe-ai-table-monitor.')
  child.unref()
  process.stdout.write(String(child.pid))
} finally {
  closeSync(logFd)
}
'@
}

function Get-RuntimeLogIndexerProcess {
  param(
    [Parameter(Mandatory = $true)][string]$PidPath,
    [switch]$RemoveStalePid
  )

  if (-not (Test-Path -LiteralPath $PidPath -PathType Leaf)) {
    return $null
  }

  $pidText = (Get-Content -LiteralPath $PidPath -Raw).Trim()
  if ($pidText -notmatch '^[1-9][0-9]*$') {
    if ($RemoveStalePid) { Remove-Item -LiteralPath $PidPath -Force }
    return $null
  }

  $indexerPid = [int]$pidText
  $process = Get-Process -Id $indexerPid -ErrorAction SilentlyContinue
  if ($null -eq $process) {
    if ($RemoveStalePid) { Remove-Item -LiteralPath $PidPath -Force }
    return $null
  }

  $processInfo = Get-CimInstance Win32_Process -Filter "ProcessId = $indexerPid" -ErrorAction SilentlyContinue
  if ($null -eq $processInfo -or $processInfo.CommandLine -notmatch 'juhe-ai-runtime-log-indexer(?:\.exe)?') {
    if ($RemoveStalePid) { Remove-Item -LiteralPath $PidPath -Force }
    return $null
  }

  return $process
}

function Stop-RuntimeLogIndexer {
  param(
    [Parameter(Mandatory = $true)][string]$PidPath
  )

  $process = Get-RuntimeLogIndexerProcess -PidPath $PidPath -RemoveStalePid
  if ($null -eq $process) {
    return
  }

  Stop-Process -Id $process.Id -ErrorAction Stop
  $deadline = [DateTime]::UtcNow.AddSeconds(10)
  while (-not $process.HasExited -and [DateTime]::UtcNow -lt $deadline) {
    Start-Sleep -Milliseconds 200
    $process.Refresh()
  }
  if (-not $process.HasExited) {
    throw "juhe-ai-runtime-log-indexer did not stop within 10 seconds (PID $($process.Id))."
  }
  Remove-Item -LiteralPath $PidPath -Force -ErrorAction SilentlyContinue
}

function Start-RuntimeLogIndexer {
  param(
    [Parameter(Mandatory = $true)][string]$AppDirectory
  )

  $binaryPath = Join-Path $AppDirectory 'backend-go/juhe-ai-runtime-log-indexer.exe'
  $runtimeDir = Join-Path $AppDirectory 'backend/runtime'
  $pidPath = Join-Path $runtimeDir 'juhe-ai-runtime-log-indexer.pid'
  $logPath = Join-Path $AppDirectory 'backend/logs/juhe-ai-runtime-log-indexer.log'
  if (-not (Test-Path -LiteralPath $binaryPath -PathType Leaf)) {
    throw "Go runtime-log indexer binary not found: $binaryPath. Rebuild the release package for Windows."
  }

  New-Item -ItemType Directory -Force $runtimeDir | Out-Null
  New-Item -ItemType Directory -Force (Split-Path -Parent $logPath) | Out-Null
  $existingProcess = Get-RuntimeLogIndexerProcess -PidPath $pidPath -RemoveStalePid
  if ($null -ne $existingProcess) {
    throw "juhe-ai-runtime-log-indexer is already running (PID $($existingProcess.Id)); stop the existing release before starting another one."
  }

  $launcher = Get-RuntimeLogIndexerNodeLauncher
  $previousNativeErrorPreference = $PSNativeCommandUseErrorActionPreference
  $PSNativeCommandUseErrorActionPreference = $false
  try {
    $launcherOutput = @(& node --input-type=module -e $launcher $binaryPath (Join-Path $AppDirectory 'backend') $logPath 2>&1)
    $launcherExitCode = $LASTEXITCODE
  } finally {
    $PSNativeCommandUseErrorActionPreference = $previousNativeErrorPreference
  }
  if ($launcherExitCode -ne 0) {
    $launcherOutput | Write-Error
    throw 'Unable to start juhe-ai-runtime-log-indexer.'
  }

  $indexerPidText = (($launcherOutput | ForEach-Object { $_.ToString() }) -join '').Trim()
  if ($indexerPidText -notmatch '^[1-9][0-9]*$') {
    throw "juhe-ai-runtime-log-indexer returned an invalid PID: $indexerPidText"
  }
  Set-Content -LiteralPath $pidPath -Value $indexerPidText -NoNewline -Encoding utf8
  Start-Sleep -Milliseconds 500
  $process = Get-RuntimeLogIndexerProcess -PidPath $pidPath -RemoveStalePid
  if ($null -eq $process) {
    $logTail = if (Test-Path -LiteralPath $logPath) { Get-Content -LiteralPath $logPath -Tail 20 } else { @('No indexer log was created.') }
    $logTail | Write-Error
    throw 'juhe-ai-runtime-log-indexer exited during startup.'
  }
  return [pscustomobject]@{ Process = $process; PidPath = $pidPath; LogPath = $logPath }
}

function Get-TableMonitorProcess {
  param(
    [Parameter(Mandatory = $true)][string]$PidPath,
    [switch]$RemoveStalePid
  )

  if (-not (Test-Path -LiteralPath $PidPath -PathType Leaf)) {
    return $null
  }

  $pidText = (Get-Content -LiteralPath $PidPath -Raw).Trim()
  if ($pidText -notmatch '^[1-9][0-9]*$') {
    if ($RemoveStalePid) { Remove-Item -LiteralPath $PidPath -Force }
    return $null
  }

  $monitorPid = [int]$pidText
  $process = Get-Process -Id $monitorPid -ErrorAction SilentlyContinue
  if ($null -eq $process) {
    if ($RemoveStalePid) { Remove-Item -LiteralPath $PidPath -Force }
    return $null
  }

  $processInfo = Get-CimInstance Win32_Process -Filter "ProcessId = $monitorPid" -ErrorAction SilentlyContinue
  if ($null -eq $processInfo -or $processInfo.CommandLine -notmatch 'juhe-ai-table-monitor(?:\.exe)?') {
    if ($RemoveStalePid) { Remove-Item -LiteralPath $PidPath -Force }
    return $null
  }

  return $process
}

function Stop-TableMonitor {
  param(
    [Parameter(Mandatory = $true)][string]$PidPath
  )

  $process = Get-TableMonitorProcess -PidPath $PidPath -RemoveStalePid
  if ($null -eq $process) {
    return
  }

  Stop-Process -Id $process.Id -ErrorAction Stop
  $deadline = [DateTime]::UtcNow.AddSeconds(10)
  while (-not $process.HasExited -and [DateTime]::UtcNow -lt $deadline) {
    Start-Sleep -Milliseconds 200
    $process.Refresh()
  }
  if (-not $process.HasExited) {
    throw "juhe-ai-table-monitor did not stop within 10 seconds (PID $($process.Id))."
  }
  Remove-Item -LiteralPath $PidPath -Force -ErrorAction SilentlyContinue
}

function Start-TableMonitor {
  param(
    [Parameter(Mandatory = $true)][string]$AppDirectory
  )

  $binaryPath = Join-Path $AppDirectory 'backend-go/juhe-ai-table-monitor.exe'
  $runtimeDir = Join-Path $AppDirectory 'backend/runtime'
  $pidPath = Join-Path $runtimeDir 'juhe-ai-table-monitor.pid'
  $logPath = Join-Path $AppDirectory 'backend/logs/juhe-ai-table-monitor.log'
  if (-not (Test-Path -LiteralPath $binaryPath -PathType Leaf)) {
    throw "Go table monitor binary not found: $binaryPath. Rebuild the release package for Windows."
  }

  New-Item -ItemType Directory -Force $runtimeDir | Out-Null
  New-Item -ItemType Directory -Force (Split-Path -Parent $logPath) | Out-Null
  $existingProcess = Get-TableMonitorProcess -PidPath $pidPath -RemoveStalePid
  if ($null -ne $existingProcess) {
    throw "juhe-ai-table-monitor is already running (PID $($existingProcess.Id)); stop the existing release before starting another one."
  }

  $launcher = Get-TableMonitorNodeLauncher
  $previousNativeErrorPreference = $PSNativeCommandUseErrorActionPreference
  $PSNativeCommandUseErrorActionPreference = $false
  try {
    $launcherOutput = @(& node --input-type=module -e $launcher $binaryPath (Join-Path $AppDirectory 'backend') $logPath 2>&1)
    $launcherExitCode = $LASTEXITCODE
  } finally {
    $PSNativeCommandUseErrorActionPreference = $previousNativeErrorPreference
  }
  if ($launcherExitCode -ne 0) {
    $launcherOutput | Write-Error
    throw 'Unable to start juhe-ai-table-monitor.'
  }

  $monitorPidText = (($launcherOutput | ForEach-Object { $_.ToString() }) -join '').Trim()
  if ($monitorPidText -notmatch '^[1-9][0-9]*$') {
    throw "juhe-ai-table-monitor returned an invalid PID: $monitorPidText"
  }
  Set-Content -LiteralPath $pidPath -Value $monitorPidText -NoNewline -Encoding utf8
  Start-Sleep -Milliseconds 500
  $process = Get-TableMonitorProcess -PidPath $pidPath -RemoveStalePid
  if ($null -eq $process) {
    $logTail = if (Test-Path -LiteralPath $logPath) { Get-Content -LiteralPath $logPath -Tail 20 } else { @('No table monitor log was created.') }
    $logTail | Write-Error
    throw 'juhe-ai-table-monitor exited during startup.'
  }
  return [pscustomobject]@{ Process = $process; PidPath = $pidPath; LogPath = $logPath }
}

function Get-AuditLogWriterNodeLauncher {
  return @'
import { closeSync, existsSync, openSync, readFileSync } from 'node:fs'
import { isAbsolute, resolve } from 'node:path'
import { createRequire } from 'node:module'
import { spawn } from 'node:child_process'

const [binaryPath, backendRoot, logPath] = process.argv.slice(1)
const require = createRequire(resolve(backendRoot, 'package.json'))
const { parse } = require('dotenv')
const baseEnvPath = resolve(backendRoot, '.env')
const capacityEnvPath = resolve(backendRoot, '.env.capacity')
const readEnv = (path) => existsSync(path) ? parse(readFileSync(path)) : {}
const baseEnv = readEnv(baseEnvPath)
const overlayName = (process.env.JUHE_AI_ENV_FILE ?? baseEnv.JUHE_AI_ENV_FILE ?? '').trim()
const overlayEnv = overlayName ? readEnv(isAbsolute(overlayName) ? overlayName : resolve(backendRoot, overlayName)) : {}
const capacityEnv = readEnv(capacityEnvPath)
const configured = (name) => {
  if (Object.hasOwn(process.env, name)) return process.env[name] ?? ''
  if (Object.hasOwn(overlayEnv, name)) return overlayEnv[name] ?? ''
  if (Object.hasOwn(capacityEnv, name)) return capacityEnv[name] ?? ''
  return baseEnv[name] ?? ''
}
const childEnv = { ...process.env }
const names = [
  'JUHE_AI_DATABASE_DRIVER', 'JUHE_AI_RUNTIME_MODE', 'JUHE_AI_AUDIT_LOG_STORE',
  'JUHE_AI_AUDIT_LOG_DATABASE_PATH', 'JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY',
  'JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY', 'JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH',
  'JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL', 'JUHE_AI_AUDIT_LOG_POSTGRES_SCHEMA', 'JUHE_AI_AUDIT_LOG_POSTGRES_URL',
  'JUHE_AI_AUDIT_LOG_OWNER_LEASE', 'JUHE_AI_AUDIT_LOG_RETENTION_INTERVAL',
  'JUHE_AI_AUDIT_LOG_RETENTION_BATCH_SIZE', 'JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS',
  'JUHE_AI_AUDIT_LOG_INPUT_SECRET', 'JUHE_AI_AUDIT_LOG_INPUT_URL', 'JUHE_AI_AUDIT_LOG_INPUT_TIMEOUT_MS',
  'JUHE_AI_AUDIT_LOG_INSTANCE_ID', 'JUHE_AI_POSTGRES_URL', 'JUHE_AI_DATABASE_PATH',
  'JUHE_AI_DATASET_DATABASE_PATH', 'JUHE_AI_USAGE_CATALOG_DATABASE_PATH',
  'JUHE_AI_STATS_DATABASE_PATH', 'JUHE_AI_RUNTIME_LOG_DATABASE_PATH',
  'JUHE_AI_TABLE_MONITOR_DATABASE_PATH', 'JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT',
  'JUHE_AI_USAGE_SHARD_ROOT'
]
for (const name of names) {
  const value = configured(name)
  if (value.trim()) childEnv[name] = value
}
const instanceId = configured('JUHE_AI_AUDIT_LOG_INSTANCE_ID').trim()
if (!instanceId) throw new Error('JUHE_AI_AUDIT_LOG_INSTANCE_ID is required; release startup does not generate owner identities.')
const secret = configured('JUHE_AI_AUDIT_LOG_INPUT_SECRET').trim()
if (!secret) throw new Error('JUHE_AI_AUDIT_LOG_INPUT_SECRET is required for the F3 input listener.')
childEnv.JUHE_AI_AUDIT_LOG_INSTANCE_ID = instanceId
childEnv.JUHE_AI_AUDIT_LOG_INPUT_SECRET = secret
childEnv.JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS = configured('JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS').trim() || '127.0.0.1:3303'
childEnv.JUHE_AI_AUDIT_LOG_INPUT_URL = configured('JUHE_AI_AUDIT_LOG_INPUT_URL').trim() || 'http://127.0.0.1:3303'
const store = (configured('JUHE_AI_AUDIT_LOG_STORE') || configured('JUHE_AI_DATABASE_DRIVER') || (configured('JUHE_AI_RUNTIME_MODE').trim().toLowerCase() === 'performance' ? 'postgres' : 'sqlite')).trim().toLowerCase()
childEnv.JUHE_AI_AUDIT_LOG_STORE = store
const absoluteBackendPath = (value, fallback) => {
  const selected = (value || fallback).trim()
  return isAbsolute(selected) ? selected : resolve(backendRoot, selected)
}
for (const [name, fallback] of [
  ['JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY', './data/audit-payload-blobs'],
  ['JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY', './data/audit-hot-search']
]) childEnv[name] = absoluteBackendPath(childEnv[name], fallback)
if (store === 'sqlite' || !store) {
  for (const [name, fallback] of [
    ['JUHE_AI_AUDIT_LOG_DATABASE_PATH', './data/juhe-ai-audit-log.sqlite3'],
    ['JUHE_AI_DATABASE_PATH', './data/juhe-ai.sqlite3'],
    ['JUHE_AI_DATASET_DATABASE_PATH', './data/juhe-ai-dataset.sqlite3'],
    ['JUHE_AI_USAGE_CATALOG_DATABASE_PATH', './data/juhe-ai-usage-catalog.sqlite3'],
    ['JUHE_AI_STATS_DATABASE_PATH', './data/juhe-ai-stats.sqlite3'],
    ['JUHE_AI_RUNTIME_LOG_DATABASE_PATH', './data/juhe-ai-runtime-log.sqlite3'],
    ['JUHE_AI_TABLE_MONITOR_DATABASE_PATH', './data/juhe-ai-table-monitor.sqlite3'],
    ['JUHE_AI_USAGE_SHARD_ROOT', './data/usage-shards'],
    ['JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT', './data/codex-context/state-shards']
  ]) childEnv[name] = absoluteBackendPath(childEnv[name], fallback)
  childEnv.JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH = absoluteBackendPath(childEnv.JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH, childEnv.JUHE_AI_DATABASE_PATH)
} else if (store === 'postgres' && !childEnv.JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL) {
  childEnv.JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL = childEnv.JUHE_AI_AUDIT_LOG_POSTGRES_URL ?? childEnv.JUHE_AI_POSTGRES_URL ?? ''
}
const logFd = openSync(logPath, 'a')
try {
  const child = spawn(binaryPath, [], { cwd: backendRoot, detached: true, env: childEnv, stdio: ['ignore', logFd, logFd], windowsHide: true })
  if (!child.pid) throw new Error('Unable to start juhe-ai-audit-log-writer.')
  child.unref()
  process.stdout.write(String(child.pid))
} finally {
  closeSync(logFd)
}
'@
}

function Get-AuditLogWriterProcess {
  param([Parameter(Mandatory = $true)][string]$PidPath, [switch]$RemoveStalePid)
  if (-not (Test-Path -LiteralPath $PidPath -PathType Leaf)) { return $null }
  $pidText = (Get-Content -LiteralPath $PidPath -Raw).Trim()
  if ($pidText -notmatch '^[1-9][0-9]*$') { if ($RemoveStalePid) { Remove-Item -LiteralPath $PidPath -Force }; return $null }
  $writerPid = [int]$pidText; $process = Get-Process -Id $writerPid -ErrorAction SilentlyContinue
  if ($null -eq $process) { if ($RemoveStalePid) { Remove-Item -LiteralPath $PidPath -Force }; return $null }
  $processInfo = Get-CimInstance Win32_Process -Filter "ProcessId = $writerPid" -ErrorAction SilentlyContinue
  if ($null -eq $processInfo -or $processInfo.CommandLine -notmatch 'juhe-ai-audit-log-writer(?:\.exe)?') { if ($RemoveStalePid) { Remove-Item -LiteralPath $PidPath -Force }; return $null }
  return $process
}

function Stop-AuditLogWriter { param([Parameter(Mandatory = $true)][string]$PidPath)
  $process = Get-AuditLogWriterProcess -PidPath $PidPath -RemoveStalePid; if ($null -eq $process) { return }
  Stop-Process -Id $process.Id -ErrorAction Stop; $deadline = [DateTime]::UtcNow.AddSeconds(10)
  while (-not $process.HasExited -and [DateTime]::UtcNow -lt $deadline) { Start-Sleep -Milliseconds 200; $process.Refresh() }
  if (-not $process.HasExited) { throw "juhe-ai-audit-log-writer did not stop within 10 seconds (PID $($process.Id))." }
  Remove-Item -LiteralPath $PidPath -Force -ErrorAction SilentlyContinue
}

function Start-AuditLogWriter { param([Parameter(Mandatory = $true)][string]$AppDirectory)
  $binaryPath = Join-Path $AppDirectory 'backend-go/juhe-ai-audit-log-writer.exe'; $runtimeDir = Join-Path $AppDirectory 'backend/runtime'; $pidPath = Join-Path $runtimeDir 'juhe-ai-audit-log-writer.pid'; $logPath = Join-Path $AppDirectory 'backend/logs/juhe-ai-audit-log-writer.log'
  if (-not (Test-Path -LiteralPath $binaryPath -PathType Leaf)) { throw "Go audit-log writer binary not found: $binaryPath. Rebuild the release package for Windows." }
  New-Item -ItemType Directory -Force $runtimeDir | Out-Null; New-Item -ItemType Directory -Force (Split-Path -Parent $logPath) | Out-Null
  $existingProcess = Get-AuditLogWriterProcess -PidPath $pidPath -RemoveStalePid; if ($null -ne $existingProcess) { throw "juhe-ai-audit-log-writer is already running (PID $($existingProcess.Id)); stop the existing release before starting another one." }
  $launcher = Get-AuditLogWriterNodeLauncher; $previous = $PSNativeCommandUseErrorActionPreference; $PSNativeCommandUseErrorActionPreference = $false
  try { $output = @(& node --input-type=module -e $launcher $binaryPath (Join-Path $AppDirectory 'backend') $logPath 2>&1); $code = $LASTEXITCODE } finally { $PSNativeCommandUseErrorActionPreference = $previous }
  if ($code -ne 0) { $output | Write-Error; throw 'Unable to start juhe-ai-audit-log-writer.' }
  $pidText = (($output | ForEach-Object { $_.ToString() }) -join '').Trim(); if ($pidText -notmatch '^[1-9][0-9]*$') { throw "juhe-ai-audit-log-writer returned an invalid PID: $pidText" }
  Set-Content -LiteralPath $pidPath -Value $pidText -NoNewline -Encoding utf8; Start-Sleep -Milliseconds 500
  $process = Get-AuditLogWriterProcess -PidPath $pidPath -RemoveStalePid; if ($null -eq $process) { $tail = if (Test-Path -LiteralPath $logPath) { Get-Content -LiteralPath $logPath -Tail 20 } else { @('No audit writer log was created.') }; $tail | Write-Error; throw 'juhe-ai-audit-log-writer exited during startup.' }
  return [pscustomobject]@{ Process = $process; PidPath = $pidPath; LogPath = $logPath }
}

function Start-ManagedNodeProcess {
  param(
    [Parameter(Mandatory = $true)][string]$WorkingDirectory,
    [Parameter(Mandatory = $true)][string[]]$Arguments
  )

  $nodeCommand = Get-Command node -ErrorAction Stop | Select-Object -First 1
  $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
  $startInfo.FileName = $nodeCommand.Source
  $startInfo.WorkingDirectory = $WorkingDirectory
  $startInfo.UseShellExecute = $false
  foreach ($argument in $Arguments) {
    [void]$startInfo.ArgumentList.Add($argument)
  }
  $process = [System.Diagnostics.Process]::new()
  $process.StartInfo = $startInfo
  if (-not $process.Start()) {
    throw 'Unable to start the juhe-ai Web/API process.'
  }
  return $process
}

function Stop-ManagedNodeProcess {
  param($Process)

  if ($null -eq $Process -or $Process.HasExited) {
    return
  }
  try {
    $Process.Kill($true)
  } catch {
    if (-not $Process.HasExited) {
      throw
    }
  }
  $deadline = [DateTime]::UtcNow.AddSeconds(10)
  while (-not $Process.HasExited -and [DateTime]::UtcNow -lt $deadline) {
    Start-Sleep -Milliseconds 200
    $Process.Refresh()
  }
  if (-not $Process.HasExited) {
    throw "juhe-ai Web/API process did not stop within 10 seconds (PID $($Process.Id))."
  }
}

function Wait-ManagedNodeReady {
  param(
    [Parameter(Mandatory = $true)][System.Diagnostics.Process]$Process,
    [Parameter(Mandatory = $true)][string]$BindAddress,
    [Parameter(Mandatory = $true)][string]$Port
  )
  $healthUrl = "http://$BindAddress`:$Port/__aisys__/api/health"
  $deadline = [DateTime]::UtcNow.AddSeconds(60)
  while (-not $Process.HasExited -and [DateTime]::UtcNow -lt $deadline) {
    try {
      $response = Invoke-WebRequest -Uri $healthUrl -UseBasicParsing -TimeoutSec 2 -ErrorAction Stop
      if ($response.StatusCode -ge 200 -and $response.StatusCode -lt 300) {
        return
      }
    } catch {
    }
    Start-Sleep -Seconds 1
    $Process.Refresh()
  }
  if ($Process.HasExited) {
    throw 'juhe-ai Web/API process exited before becoming ready.'
  }
  throw 'juhe-ai Web/API process did not become ready within 60 seconds.'
}

if (-not (Test-CommandExists 'node')) {
  throw 'Node.js LTS is required. Install Node.js 22.x LTS (>=22.13.0) or 24.x LTS (>=24.11.0) before running this script.'
}

$runtimeCheckPath = 'backend/dist/scripts/preflight/check-node-sqlite.js'
if (-not (Test-Path -LiteralPath $runtimeCheckPath)) {
  throw "Runtime preflight script not found: $runtimeCheckPath. Please rebuild the release package."
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
  Copy-Item -LiteralPath 'backend/.env.example' -Destination 'backend/.env'
  Write-Host 'Created backend/.env from backend/.env.example'
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

# The preflight imports dotenv and @vscode/ripgrep from production dependencies.
# Run it only after the release has installed those dependencies on a clean host.
node $runtimeCheckPath
if ($LASTEXITCODE -ne 0) {
  exit $LASTEXITCODE
}

$hostValue = Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_HOST' -Fallback '127.0.0.1'
$portValue = Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_PORT' -Fallback '3000'
$auditInputUrl = if ($env:JUHE_AI_AUDIT_LOG_INPUT_URL) { $env:JUHE_AI_AUDIT_LOG_INPUT_URL } else { Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_AUDIT_LOG_INPUT_URL' -Fallback 'http://127.0.0.1:3303' }
$env:JUHE_AI_AUDIT_LOG_INPUT_URL = $auditInputUrl

Write-Host "Starting juhe-ai at http://${hostValue}:${portValue}"
Write-Host 'The Web/API process will supervise separate background worker and DB service processes.'
$ownerLockEnabled = if ($env:JUHE_AI_OWNER_LOCK_ENABLED) { $env:JUHE_AI_OWNER_LOCK_ENABLED } else { Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_OWNER_LOCK_ENABLED' -Fallback 'false' }
$serverArguments = @('backend/dist/server.js')
if ($ownerLockEnabled.Trim().Equals('true', [System.StringComparison]::OrdinalIgnoreCase)) {
  $ownerManifestPath = [System.IO.Path]::GetFullPath((Join-Path $appDir 'deploy/owner-manifest.json'))
  $manifestEpoch = node -e "const fs=require('node:fs'); process.stdout.write(JSON.parse(fs.readFileSync('deploy/owner-manifest.json','utf8')).deploymentEpoch)"
  if ($LASTEXITCODE -ne 0 -or -not $manifestEpoch) { throw 'Unable to read deploy/owner-manifest.json deploymentEpoch.' }
  node scripts/validate-owner-manifest.mjs deploy/owner-manifest.json
  if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
  $ownerLockPath = if ($env:JUHE_AI_OWNER_LOCK_PATH) { $env:JUHE_AI_OWNER_LOCK_PATH } else { Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_OWNER_LOCK_PATH' -Fallback '' }
  if (-not [System.IO.Path]::IsPathRooted($ownerLockPath)) {
    throw 'JUHE_AI_OWNER_LOCK_PATH must be an absolute shared path outside the release directory.'
  }
  $ownerLockEpoch = if ($env:JUHE_AI_OWNER_LOCK_DEPLOYMENT_EPOCH) { $env:JUHE_AI_OWNER_LOCK_DEPLOYMENT_EPOCH } else { Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_OWNER_LOCK_DEPLOYMENT_EPOCH' -Fallback $manifestEpoch }
  if ($ownerLockEpoch -ne $manifestEpoch) { throw 'JUHE_AI_OWNER_LOCK_DEPLOYMENT_EPOCH does not match deploy/owner-manifest.json.' }
  $nodeVersion = node -p "require('./package.json').version"
  if ($LASTEXITCODE -ne 0 -or -not $nodeVersion) { throw 'Unable to read Node release version.' }
  node scripts/validate-owner-manifest.mjs --require-deployment-epoch=$ownerLockEpoch --require-node-version=$nodeVersion deploy/owner-manifest.json
  if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
  $env:JUHE_AI_OWNER_MANIFEST_PATH = $ownerManifestPath
  $env:JUHE_AI_OWNER_LOCK_DEPLOYMENT_EPOCH = $ownerLockEpoch
  $serverArguments = @(
    'scripts/run-with-owner-lock.mjs',
    '--lock-path', $ownerLockPath,
    '--release-root', $appDir,
    '--deployment-epoch', $ownerLockEpoch,
    '--role', 'server',
    '--version', $nodeVersion,
    '--', 'node', 'backend/dist/server.js'
  )
}

$runtimeLogIndexer = $null
$tableMonitor = $null
$auditLogWriter = $null
$serverProcess = $null
$serverExitCode = 1
try {
  $serverProcess = Start-ManagedNodeProcess -WorkingDirectory $appDir -Arguments $serverArguments
  Wait-ManagedNodeReady -Process $serverProcess -BindAddress $hostValue -Port $portValue
  $runtimeLogIndexer = Start-RuntimeLogIndexer -AppDirectory $appDir
  Write-Host "Started juhe-ai-runtime-log-indexer (PID $($runtimeLogIndexer.Process.Id))."
  $tableMonitor = Start-TableMonitor -AppDirectory $appDir
  Write-Host "Started juhe-ai-table-monitor (PID $($tableMonitor.Process.Id))."
  $auditLogWriter = Start-AuditLogWriter -AppDirectory $appDir
  Write-Host "Started juhe-ai-audit-log-writer (PID $($auditLogWriter.Process.Id))."
  while (-not $serverProcess.HasExited -and -not $runtimeLogIndexer.Process.HasExited -and -not $tableMonitor.Process.HasExited -and -not $auditLogWriter.Process.HasExited) {
    Start-Sleep -Seconds 1
    $serverProcess.Refresh()
    $runtimeLogIndexer.Process.Refresh()
    $tableMonitor.Process.Refresh()
    $auditLogWriter.Process.Refresh()
  }
  if ($runtimeLogIndexer.Process.HasExited -and -not $serverProcess.HasExited) {
    $logTail = if (Test-Path -LiteralPath $runtimeLogIndexer.LogPath) { Get-Content -LiteralPath $runtimeLogIndexer.LogPath -Tail 20 } else { @('No indexer log was created.') }
    $logTail | Write-Error
    throw "juhe-ai-runtime-log-indexer exited unexpectedly (PID $($runtimeLogIndexer.Process.Id))."
  }
  if ($tableMonitor.Process.HasExited -and -not $serverProcess.HasExited -and -not $runtimeLogIndexer.Process.HasExited) {
    $logTail = if (Test-Path -LiteralPath $tableMonitor.LogPath) { Get-Content -LiteralPath $tableMonitor.LogPath -Tail 20 } else { @('No table monitor log was created.') }
    $logTail | Write-Error
    throw "juhe-ai-table-monitor exited unexpectedly (PID $($tableMonitor.Process.Id))."
  }
  if ($serverProcess.HasExited -and -not $runtimeLogIndexer.Process.HasExited -and -not $tableMonitor.Process.HasExited) {
    throw "juhe-ai Web/API process exited unexpectedly (PID $($serverProcess.Id))."
  }
  if ($auditLogWriter.Process.HasExited -and -not $serverProcess.HasExited) {
    $logTail = if (Test-Path -LiteralPath $auditLogWriter.LogPath) { Get-Content -LiteralPath $auditLogWriter.LogPath -Tail 20 } else { @('No audit writer log was created.') }
    $logTail | Write-Error
    throw "juhe-ai-audit-log-writer exited unexpectedly (PID $($auditLogWriter.Process.Id))."
  }
  $serverProcess.WaitForExit()
  $serverExitCode = $serverProcess.ExitCode
  if ($serverExitCode -eq 0) { $serverExitCode = 1 }
} finally {
  if ($null -ne $serverProcess) {
    Stop-ManagedNodeProcess -Process $serverProcess
  }
  if ($null -ne $runtimeLogIndexer) {
    Stop-RuntimeLogIndexer -PidPath $runtimeLogIndexer.PidPath
  }
  if ($null -ne $tableMonitor) {
    Stop-TableMonitor -PidPath $tableMonitor.PidPath
  }
  if ($null -ne $auditLogWriter) {
    Stop-AuditLogWriter -PidPath $auditLogWriter.PidPath
  }
}
exit $serverExitCode

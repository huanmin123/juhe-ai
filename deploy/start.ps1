$ErrorActionPreference = 'Stop'
if ($PSVersionTable.PSVersion.Major -ge 7) {
  $PSNativeCommandUseErrorActionPreference = $true
}

# X01/X03 go-only 终态：本脚本是唯一的启动路径。Node Web/API 已物理归档到
# migration-backup/node/final-archive/（X02），legacybridge 反代已删除，
# 不再提供 hybrid / node 部署模式；历史值会被 fail-closed 拒绝。

$appDir = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $appDir
$env:TZ = 'UTC'

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
  if (-not (Test-Path -LiteralPath $Path)) { return $Fallback }
  $pattern = '^\s*' + [regex]::Escape($Name) + '=(.*)$'
  $line = Get-Content -LiteralPath $Path | Where-Object { $_ -match $pattern } | Select-Object -Last 1
  if (-not $line) { return $Fallback }
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
  if (-not $updated) { $lines += "$Name=$Value" }
  Set-Content -LiteralPath $Path -Value $lines -Encoding utf8
}

function New-JuheSecret {
  $bytes = [byte[]]::new(32)
  [System.Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
  return (($bytes | ForEach-Object { $_.ToString('x2') }) -join '')
}

function Get-DeployMode {
  $mode = if ($env:JUHE_AI_DEPLOY_MODE) { $env:JUHE_AI_DEPLOY_MODE } else { Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_DEPLOY_MODE' -Fallback 'go' }
  $normalized = $mode.Trim().ToLowerInvariant()
  if (-not $normalized) { $normalized = 'go' }
  if ($normalized -ne 'go') {
    throw "JUHE_AI_DEPLOY_MODE must be go (got: $mode). The hybrid and node deploy modes were retired with the archived Node backend (X01/X02); go-only is the only supported topology."
  }
  return $normalized
}

function Invoke-GoMaintenanceBootstrap {
  $binaryPath = Join-Path $appDir 'backend-go/juhe-ai-maintenance'
  if (-not (Test-Path -LiteralPath $binaryPath -PathType Leaf)) { throw "Go maintenance binary not found: $binaryPath. Rebuild the release package for Windows." }
  $databaseDriver = if ($env:JUHE_AI_DATABASE_DRIVER) { $env:JUHE_AI_DATABASE_DRIVER } else { Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_DATABASE_DRIVER' -Fallback 'sqlite' }
  $databaseDriver = $databaseDriver.Trim().ToLowerInvariant()
  $bootstrapArguments = [System.Collections.Generic.List[string]]::new()
  switch ($databaseDriver) {
    'sqlite' {
      $businessPath = Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_DATABASE_PATH' -Fallback './data/juhe-ai.sqlite3'
      $chatPath = Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_CHAT_DATABASE_PATH' -Fallback './data/juhe-ai-chat.sqlite3'
      $datasetPath = Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_DATASET_DATABASE_PATH' -Fallback './data/juhe-ai-dataset.sqlite3'
      $usageCatalogPath = Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_USAGE_CATALOG_DATABASE_PATH' -Fallback './data/juhe-ai-usage-catalog.sqlite3'
      $statsPath = Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_STATS_DATABASE_PATH' -Fallback './data/juhe-ai-stats.sqlite3'
      $codexShardRoot = Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT' -Fallback './data/codex-context/state-shards'
      $paths = "business=$businessPath,chat=$chatPath,dataset=$datasetPath,usage-catalog=$usageCatalogPath,stats=$statsPath,codex-context-shard-root=$codexShardRoot"
      $codexShardCount = Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT' -Fallback ''
      if ($codexShardCount) {
        $parsedShardCount = 0
        if (-not [int]::TryParse($codexShardCount, [ref]$parsedShardCount) -or $parsedShardCount -lt 1 -or $parsedShardCount -gt 256) {
          throw "JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT must be an integer between 1 and 256 (got: $codexShardCount)."
        }
        $paths = "$paths,codex-context-shard-count=$codexShardCount"
      }
      $bootstrapArguments.AddRange([string[]]@('--ensure-schema', '--driver', 'sqlite', '--paths', $paths))
    }
    'postgres' {
      $postgresDsn = if ($env:JUHE_AI_POSTGRES_URL) { $env:JUHE_AI_POSTGRES_URL } else { Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_POSTGRES_URL' -Fallback '' }
      if (-not $postgresDsn) { throw 'JUHE_AI_GO_MAINTENANCE_BOOTSTRAP=true with postgres driver requires JUHE_AI_POSTGRES_URL.' }
      $bootstrapArguments.AddRange([string[]]@('--ensure-schema', '--driver', 'postgres', '--dsn', $postgresDsn))
    }
    default { throw "Unsupported JUHE_AI_DATABASE_DRIVER for go maintenance bootstrap: $databaseDriver (expected sqlite or postgres)." }
  }
  $seedValue = if ($env:JUHE_AI_GO_MAINTENANCE_SEED) { $env:JUHE_AI_GO_MAINTENANCE_SEED } else { Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_GO_MAINTENANCE_SEED' -Fallback 'false' }
  if ($seedValue.Trim().ToLowerInvariant() -eq 'true') { $bootstrapArguments.Add('--seed') }
  if ($env:JUHE_AI_SECRET) { $bootstrapArguments.AddRange([string[]]@('--secret', $env:JUHE_AI_SECRET)) }
  Write-Host 'Running optional Go maintenance preflight (ensure-schema; seed when enabled)...'
  # Relative backend/.env storage paths resolve against backend/, matching the
  # historical per-file storage layout; the maintenance command is idempotent.
  $maintenanceProcess = Start-Process -FilePath $binaryPath -ArgumentList $bootstrapArguments -WorkingDirectory (Join-Path $appDir 'backend') -NoNewWindow -Wait -PassThru
  if ($maintenanceProcess.ExitCode -ne 0) { throw "juhe-ai-maintenance bootstrap failed with exit code $($maintenanceProcess.ExitCode)." }
}

function Ensure-DeploymentDefaults {
  $envPath = 'backend/.env'
  $fileSecret = Read-DotEnvValue -Path $envPath -Name 'JUHE_AI_SECRET' -Fallback ''
  if (-not $env:JUHE_AI_SECRET -and -not $fileSecret) {
    $generatedSecret = New-JuheSecret
    Set-DotEnvValue -Path $envPath -Name 'JUHE_AI_SECRET' -Value $generatedSecret
    $env:JUHE_AI_SECRET = $generatedSecret
    Write-Host 'Generated JUHE_AI_SECRET and saved it to backend/.env. Keep this value when migrating existing data.'
  } elseif (-not $env:JUHE_AI_SECRET) {
    $env:JUHE_AI_SECRET = $fileSecret
  }

  $fileOrigins = Read-DotEnvValue -Path $envPath -Name 'JUHE_AI_ALLOWED_ORIGINS' -Fallback ''
  if (-not $env:JUHE_AI_ALLOWED_ORIGINS -and -not $fileOrigins) {
    $publicOrigin = if ($env:JUHE_AI_PUBLIC_ORIGIN) { $env:JUHE_AI_PUBLIC_ORIGIN } else { Read-DotEnvValue -Path $envPath -Name 'JUHE_AI_PUBLIC_ORIGIN' -Fallback '' }
    $publicPort = if ($env:JUHE_AI_PUBLIC_PORT) { $env:JUHE_AI_PUBLIC_PORT } elseif ($env:JUHE_AI_PORT) { $env:JUHE_AI_PORT } else { Read-DotEnvValue -Path $envPath -Name 'JUHE_AI_PORT' -Fallback '3000' }
    $defaultOrigins = if ($publicOrigin) { $publicOrigin } else { "http://localhost:${publicPort},http://127.0.0.1:${publicPort}" }
    Set-DotEnvValue -Path $envPath -Name 'JUHE_AI_ALLOWED_ORIGINS' -Value $defaultOrigins
    $env:JUHE_AI_ALLOWED_ORIGINS = $defaultOrigins
    Write-Host "Set JUHE_AI_ALLOWED_ORIGINS to $defaultOrigins. Adjust backend/.env if using a public domain or reverse proxy."
  } elseif (-not $env:JUHE_AI_ALLOWED_ORIGINS) {
    $env:JUHE_AI_ALLOWED_ORIGINS = $fileOrigins
  }
}

function Get-GoProjectProcess {
  param([Parameter(Mandatory = $true)][string]$PidPath, [Parameter(Mandatory = $true)][string]$BinaryName, [switch]$RemoveStalePid)
  if (-not (Test-Path -LiteralPath $PidPath -PathType Leaf)) { return $null }
  $pidText = (Get-Content -LiteralPath $PidPath -Raw).Trim()
  if ($pidText -notmatch '^[1-9][0-9]*$') { if ($RemoveStalePid) { Remove-Item -LiteralPath $PidPath -Force }; return $null }
  $projectPid = [int]$pidText
  $process = Get-Process -Id $projectPid -ErrorAction SilentlyContinue
  if ($null -eq $process) { if ($RemoveStalePid) { Remove-Item -LiteralPath $PidPath -Force }; return $null }
  $processInfo = Get-CimInstance Win32_Process -Filter "ProcessId = $projectPid" -ErrorAction SilentlyContinue
  if ($null -eq $processInfo -or $processInfo.CommandLine -notmatch [regex]::Escape($BinaryName)) {
    if ($RemoveStalePid) { Remove-Item -LiteralPath $PidPath -Force }
    return $null
  }
  return $process
}

function Stop-GoProject {
  param([Parameter(Mandatory = $true)][string]$PidPath, [Parameter(Mandatory = $true)][string]$BinaryName)
  $process = Get-GoProjectProcess -PidPath $PidPath -BinaryName $BinaryName -RemoveStalePid
  if ($null -eq $process) { return }
  Stop-Process -Id $process.Id -ErrorAction Stop
  $deadline = [DateTime]::UtcNow.AddSeconds(10)
  while (-not $process.HasExited -and [DateTime]::UtcNow -lt $deadline) {
    Start-Sleep -Milliseconds 200
    $process.Refresh()
  }
  if (-not $process.HasExited) { throw "$BinaryName did not stop within 10 seconds (PID $($process.Id))." }
  Remove-Item -LiteralPath $PidPath -Force -ErrorAction SilentlyContinue
}

function Wait-HttpStatus {
  param(
    [Parameter(Mandatory = $true)][System.Diagnostics.Process]$Process,
    [Parameter(Mandatory = $true)][string]$Url,
    [Parameter(Mandatory = $true)][int]$ExpectedStatus,
    [Parameter(Mandatory = $true)][string]$Description
  )
  $deadline = [DateTime]::UtcNow.AddSeconds(60)
  while (-not $Process.HasExited -and [DateTime]::UtcNow -lt $deadline) {
    try {
      $response = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 2 -ErrorAction Stop
      if ($response.StatusCode -eq $ExpectedStatus) { return }
    } catch {
    }
    Start-Sleep -Seconds 1
    $Process.Refresh()
  }
  if ($Process.HasExited) { throw "$Description exited before becoming ready." }
  throw "$Description did not become ready within 60 seconds."
}

function Start-GoProject {
  param(
    [Parameter(Mandatory = $true)][string]$AppDirectory,
    [Parameter(Mandatory = $true)][ValidateSet('gateway', 'jobs')][string]$Project,
    [Parameter(Mandatory = $true)][string]$HealthUrl
  )
  $binaryName = "juhe-ai-$Project.exe"
  $binaryPath = Join-Path $AppDirectory "backend-go/$binaryName"
  $launcherPath = Join-Path $AppDirectory 'scripts/start-go-project.mjs'
  $runtimeDir = Join-Path $AppDirectory 'backend/runtime'
  $pidPath = Join-Path $runtimeDir "juhe-ai-go-$Project.pid"
  $logPath = Join-Path $AppDirectory "backend/logs/juhe-ai-go-$Project.log"
  if (-not (Test-Path -LiteralPath $binaryPath -PathType Leaf)) { throw "Go $Project binary not found: $binaryPath. Rebuild the release package for Windows." }
  if (-not (Test-Path -LiteralPath $launcherPath -PathType Leaf)) { throw "Go project launcher not found: $launcherPath. Rebuild the release package." }
  New-Item -ItemType Directory -Force $runtimeDir | Out-Null
  New-Item -ItemType Directory -Force (Split-Path -Parent $logPath) | Out-Null
  $existingProcess = Get-GoProjectProcess -PidPath $pidPath -BinaryName $binaryName -RemoveStalePid
  if ($null -ne $existingProcess) { throw "juhe-ai-go-$Project is already running (PID $($existingProcess.Id)); stop the existing release before starting another one." }
  $previousNativeErrorPreference = $PSNativeCommandUseErrorActionPreference
  $PSNativeCommandUseErrorActionPreference = $false
  try {
    $launcherOutput = @(& node $launcherPath $Project $binaryPath (Join-Path $AppDirectory 'backend') $logPath 2>&1)
    $launcherExitCode = $LASTEXITCODE
  } finally {
    $PSNativeCommandUseErrorActionPreference = $previousNativeErrorPreference
  }
  if ($launcherExitCode -ne 0) { $launcherOutput | Write-Error; throw "Unable to start juhe-ai-go-$Project." }
  $pidText = (($launcherOutput | ForEach-Object { $_.ToString() }) -join '').Trim()
  if ($pidText -notmatch '^[1-9][0-9]*$') { throw "juhe-ai-go-$Project returned an invalid PID: $pidText" }
  Set-Content -LiteralPath $pidPath -Value $pidText -NoNewline -Encoding utf8
  $process = Get-GoProjectProcess -PidPath $pidPath -BinaryName $binaryName -RemoveStalePid
  if ($null -eq $process) {
    $logTail = if (Test-Path -LiteralPath $logPath) { Get-Content -LiteralPath $logPath -Tail 20 } else { @("No Go $Project log was created.") }
    $logTail | Write-Error
    throw "juhe-ai-go-$Project exited during startup."
  }
  try {
    Wait-HttpStatus -Process $process -Url "$($HealthUrl.TrimEnd('/'))/health" -ExpectedStatus 200 -Description "juhe-ai-go-$Project"
  } catch {
    if (Test-Path -LiteralPath $logPath) { Get-Content -LiteralPath $logPath -Tail 20 | Write-Error }
    throw
  }
  return [pscustomobject]@{ Process = $process; PidPath = $pidPath; LogPath = $logPath }
}

if (-not (Test-CommandExists 'node')) { throw 'Node.js LTS is required. Install Node.js 22.x LTS (>=22.13.0) or 24.x LTS (>=24.11.0) before running this script.' }
$env:NODE_ENV = if ($env:NODE_ENV) { $env:NODE_ENV } else { 'production' }
# go-only release packages intentionally omit the archived backend/ tree.
# Create its runtime configuration root before the first .env write.
New-Item -ItemType Directory -Force 'backend' | Out-Null
if (-not (Test-Path -LiteralPath 'backend/.env')) {
  if (Test-Path -LiteralPath 'backend/.env.example') {
    Copy-Item -LiteralPath 'backend/.env.example' -Destination 'backend/.env'
    Write-Host 'Created backend/.env from backend/.env.example'
  } else {
    # go-only 发布包不再携带 backend/.env.example（X02 裁剪）：创建空文件，
    # 由 Ensure-DeploymentDefaults 写入生成的 JUHE_AI_SECRET / ALLOWED_ORIGINS。
    New-Item -ItemType File -Force 'backend/.env' | Out-Null
    Write-Host 'Created empty backend/.env (go-only release ships no backend/.env.example).'
  }
  Write-Host 'Configure all JUHE_AI_*_INSTANCE_ID values and F3/F4 input secrets before production use.'
}
Ensure-DeploymentDefaults
New-Item -ItemType Directory -Force 'backend/data' | Out-Null
$deployMode = Get-DeployMode

$hostValue = if ($env:JUHE_AI_HOST) { $env:JUHE_AI_HOST } else { Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_HOST' -Fallback '127.0.0.1' }
$portValue = if ($env:JUHE_AI_PORT) { $env:JUHE_AI_PORT } else { Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_PORT' -Fallback '3000' }
$env:JUHE_AI_AUDIT_LOG_INPUT_URL = if ($env:JUHE_AI_AUDIT_LOG_INPUT_URL) { $env:JUHE_AI_AUDIT_LOG_INPUT_URL } else { Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_AUDIT_LOG_INPUT_URL' -Fallback 'http://127.0.0.1:3303' }
$env:JUHE_AI_OPERATION_LOG_INPUT_URL = if ($env:JUHE_AI_OPERATION_LOG_INPUT_URL) { $env:JUHE_AI_OPERATION_LOG_INPUT_URL } else { Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_OPERATION_LOG_INPUT_URL' -Fallback 'http://127.0.0.1:3304' }

Write-Host "Starting juhe-ai at http://${hostValue}:${portValue} (deploy mode: ${deployMode}; go-only: the Go gateway owns the main HTTP entry, Go jobs owns F1/F2)"
$ownerLockEnabled = if ($env:JUHE_AI_OWNER_LOCK_ENABLED) { $env:JUHE_AI_OWNER_LOCK_ENABLED } else { Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_OWNER_LOCK_ENABLED' -Fallback 'false' }
if ($ownerLockEnabled.Trim().Equals('true', [System.StringComparison]::OrdinalIgnoreCase)) {
  throw 'JUHE_AI_OWNER_LOCK_ENABLED=true has no Go-mode server wrapper yet; go-only mode refuses to start without this deployment guard.'
}

$goGateway = $null
$goJobs = $null
try {
  $configuredSystemApi = if ($env:JUHE_AI_GATEWAY_SYSTEM_API_ENABLED) { $env:JUHE_AI_GATEWAY_SYSTEM_API_ENABLED } else { Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_GATEWAY_SYSTEM_API_ENABLED' -Fallback '' }
  $configuredSystemApi = $configuredSystemApi.Trim().ToLowerInvariant()
  if ($configuredSystemApi -eq '') {
    $env:JUHE_AI_GATEWAY_SYSTEM_API_ENABLED = 'true'
  } elseif ($configuredSystemApi -ne 'true') {
    throw "go-only deploy requires JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=true (got: $configuredSystemApi); the gateway must own the main HTTP entry."
  }
  $goMaintenanceBootstrap = if ($env:JUHE_AI_GO_MAINTENANCE_BOOTSTRAP) { $env:JUHE_AI_GO_MAINTENANCE_BOOTSTRAP } else { Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_GO_MAINTENANCE_BOOTSTRAP' -Fallback 'false' }
  if ($goMaintenanceBootstrap.Trim().ToLowerInvariant() -eq 'true') { Invoke-GoMaintenanceBootstrap }
  $gatewayHealthUrl = if ($env:JUHE_AI_GATEWAY_HEALTH_URL) { $env:JUHE_AI_GATEWAY_HEALTH_URL } else { Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_GATEWAY_HEALTH_URL' -Fallback 'http://127.0.0.1:3306' }
  $jobsHealthUrl = if ($env:JUHE_AI_JOBS_HEALTH_URL) { $env:JUHE_AI_JOBS_HEALTH_URL } else { Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_JOBS_HEALTH_URL' -Fallback 'http://127.0.0.1:3305' }
  $goGateway = Start-GoProject -AppDirectory $appDir -Project gateway -HealthUrl $gatewayHealthUrl
  $goJobs = Start-GoProject -AppDirectory $appDir -Project jobs -HealthUrl $jobsHealthUrl
  $auditInputUrl = (if ($env:JUHE_AI_AUDIT_LOG_INPUT_URL) { $env:JUHE_AI_AUDIT_LOG_INPUT_URL } else { Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_AUDIT_LOG_INPUT_URL' -Fallback 'http://127.0.0.1:3303' }).TrimEnd('/')
  $operationInputUrl = (if ($env:JUHE_AI_OPERATION_LOG_INPUT_URL) { $env:JUHE_AI_OPERATION_LOG_INPUT_URL } else { Read-DotEnvValue -Path 'backend/.env' -Name 'JUHE_AI_OPERATION_LOG_INPUT_URL' -Fallback 'http://127.0.0.1:3304' }).TrimEnd('/')
  Wait-HttpStatus -Process $goGateway.Process -Url "$auditInputUrl/__aiinternal__/health" -ExpectedStatus 204 -Description 'juhe-ai-go-gateway F3'
  Wait-HttpStatus -Process $goGateway.Process -Url "$operationInputUrl/__aiinternal__/v1/operation-logs/health" -ExpectedStatus 204 -Description 'juhe-ai-go-gateway F4'
  Wait-HttpStatus -Process $goGateway.Process -Url "http://${hostValue}:${portValue}/__aisys__/api/health" -ExpectedStatus 200 -Description 'juhe-ai-go-gateway system API'
  Write-Host "Started juhe-ai-go-gateway (PID $($goGateway.Process.Id)) and juhe-ai-go-jobs (PID $($goJobs.Process.Id))."
  while (-not $goGateway.Process.HasExited -and -not $goJobs.Process.HasExited) {
    Start-Sleep -Seconds 1
    $goGateway.Process.Refresh()
    $goJobs.Process.Refresh()
  }
  if ($goGateway.Process.HasExited) {
    if (Test-Path -LiteralPath $goGateway.LogPath) { Get-Content -LiteralPath $goGateway.LogPath -Tail 20 | Write-Error }
    throw "juhe-ai-go-gateway exited unexpectedly (PID $($goGateway.Process.Id))."
  }
  if (Test-Path -LiteralPath $goJobs.LogPath) { Get-Content -LiteralPath $goJobs.LogPath -Tail 20 | Write-Error }
  throw "juhe-ai-go-jobs exited unexpectedly (PID $($goJobs.Process.Id))."
} finally {
  if ($null -ne $goJobs) { Stop-GoProject -PidPath $goJobs.PidPath -BinaryName 'juhe-ai-jobs.exe' }
  if ($null -ne $goGateway) { Stop-GoProject -PidPath $goGateway.PidPath -BinaryName 'juhe-ai-gateway.exe' }
}
exit 0

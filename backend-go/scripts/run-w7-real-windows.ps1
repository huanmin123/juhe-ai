param(
  [Parameter(Mandatory = $true)][string]$PostgresBin,
  [Parameter(Mandatory = $true)][string]$RedisBin,
  [switch]$InjectPostgresStartupFailureAfterControllerExit
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$backendRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$tempRoot = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
$taskRoot = Join-Path $tempRoot ('juhe-ai-w7-real-' + [guid]::NewGuid().ToString('N'))
$postgresBinPath = [System.IO.Path]::GetFullPath($PostgresBin)
$redisBinPath = [System.IO.Path]::GetFullPath($RedisBin)
$initdbExe = Join-Path $postgresBinPath 'initdb.exe'
$pgCtlExe = Join-Path $postgresBinPath 'pg_ctl.exe'
$createdbExe = Join-Path $postgresBinPath 'createdb.exe'
$postgresExe = Join-Path $postgresBinPath 'postgres.exe'
$redisServerExe = Join-Path $redisBinPath 'redis-server.exe'
$redisCliExe = Join-Path $redisBinPath 'redis-cli.exe'
$goExe = (Get-Command go.exe -ErrorAction Stop | Select-Object -First 1).Source

foreach ($path in @($initdbExe, $pgCtlExe, $createdbExe, $postgresExe, $redisServerExe, $redisCliExe, $goExe)) {
  if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
    throw "Required executable is missing: $path"
  }
}
if (-not $taskRoot.StartsWith($tempRoot, [System.StringComparison]::OrdinalIgnoreCase) -or
    -not ([System.IO.Path]::GetFileName($taskRoot)).StartsWith('juhe-ai-w7-real-', [System.StringComparison]::Ordinal)) {
  throw "Unsafe W7 task root: $taskRoot"
}

function Invoke-NativeChecked {
  param(
    [Parameter(Mandatory = $true)][string]$Executable,
    [Parameter(Mandatory = $true)][string[]]$Arguments,
    [string]$WorkingDirectory = $backendRoot
  )
  Push-Location $WorkingDirectory
  try {
    $output = @(& $Executable @Arguments 2>&1)
    $exitCode = $LASTEXITCODE
  } finally {
    Pop-Location
  }
  if ($exitCode -ne 0) {
    throw "Native command failed with exit code ${exitCode}: $($output -join [Environment]::NewLine)"
  }
  return $output
}

function Test-CommandLineContainsPath {
  param(
    [Parameter(Mandatory = $true)][string]$CommandLine,
    [Parameter(Mandatory = $true)][string]$Path
  )
  $normalizedCommandLine = $CommandLine.Replace('\', '/')
  $normalizedPath = ([System.IO.Path]::GetFullPath($Path)).Replace('\', '/')
  return $normalizedCommandLine.Contains($normalizedPath, [System.StringComparison]::OrdinalIgnoreCase)
}

function Test-PostgresCommandLineDataDirectory {
  param(
    [Parameter(Mandatory = $true)][string]$CommandLine,
    [Parameter(Mandatory = $true)][string]$DataDirectory
  )
  $normalizedCommandLine = $CommandLine.Replace('\', '/')
  $normalizedDataDirectory = ([System.IO.Path]::GetFullPath($DataDirectory)).Replace('\', '/')
  $escapedDataDirectory = [System.Text.RegularExpressions.Regex]::Escape($normalizedDataDirectory)
  $pattern = '(?i)(?:^|\s)"?-D"?\s+(?:"' + $escapedDataDirectory + '"|' + $escapedDataDirectory + ')(?=$|\s)'
  return [System.Text.RegularExpressions.Regex]::IsMatch($normalizedCommandLine, $pattern)
}

function Start-IsolatedPostgres {
  param(
    [Parameter(Mandatory = $true)][string]$DataDirectory,
    [Parameter(Mandatory = $true)][string]$LogPath,
    [Parameter(Mandatory = $true)][int]$Port
  )
  $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
  $startInfo.FileName = $pgCtlExe
  $startInfo.WorkingDirectory = $backendRoot
  $startInfo.UseShellExecute = $false
  $startInfo.CreateNoWindow = $true
  foreach ($argument in @(
    'start', '-D', $DataDirectory, '-l', $LogPath,
    '-o', "-h 127.0.0.1 -p $Port", '-w'
  )) {
    $startInfo.ArgumentList.Add($argument)
  }

  # pg_ctl starts a long-lived postgres child. Capturing its inherited handles in
  # a PowerShell pipeline waits for EOF until postgres stops, deadlocking startup.
  $process = [System.Diagnostics.Process]::Start($startInfo)
  if ($null -eq $process) { throw 'Failed to start isolated PostgreSQL controller' }
  try {
    $process.WaitForExit()
    $exitCode = $process.ExitCode
  } finally {
    $process.Dispose()
  }
  if ($exitCode -ne 0) {
    $logTail = if (Test-Path -LiteralPath $LogPath -PathType Leaf) {
      (Get-Content -LiteralPath $LogPath -Tail 50) -join [Environment]::NewLine
    } else {
      '<postgres log unavailable>'
    }
    throw "PostgreSQL startup failed with exit code ${exitCode}: $logTail"
  }
  if ($InjectPostgresStartupFailureAfterControllerExit) {
    throw "Injected PostgreSQL startup failure after controller exit; task root: $taskRoot"
  }
}

function Get-FreeLoopbackPort {
  $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
  $listener.Start()
  try {
    return ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
  } finally {
    $listener.Stop()
  }
}

function Wait-LoopbackPort {
  param([Parameter(Mandatory = $true)][int]$Port)
  $deadline = [DateTime]::UtcNow.AddSeconds(15)
  do {
    $client = [System.Net.Sockets.TcpClient]::new()
    try {
      $connect = $client.ConnectAsync('127.0.0.1', $Port)
      if ($connect.Wait(250) -and $client.Connected) { return }
    } catch {
    } finally {
      $client.Dispose()
    }
    Start-Sleep -Milliseconds 100
  } while ([DateTime]::UtcNow -lt $deadline)
  throw "Timed out waiting for loopback port $Port"
}

function Start-IsolatedRedis {
  param(
    [Parameter(Mandatory = $true)][string]$Role,
    [Parameter(Mandatory = $true)][int]$Port,
    [Parameter(Mandatory = $true)][AllowEmptyCollection()][System.Collections.Generic.List[object]]$RuntimeRegistry
  )
  $dataDirectory = Join-Path $taskRoot ('redis-' + $Role)
  New-Item -ItemType Directory -Path $dataDirectory | Out-Null
  $logPath = Join-Path $taskRoot ('redis-' + $Role + '.log')
  $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
  $startInfo.FileName = $redisServerExe
  $startInfo.WorkingDirectory = $dataDirectory
  $startInfo.UseShellExecute = $false
  $startInfo.CreateNoWindow = $true
  foreach ($argument in @(
    '--bind', '127.0.0.1', '--port', [string]$Port,
    '--protected-mode', 'yes', '--dir', $dataDirectory,
    '--dbfilename', 'dump.rdb', '--appendonly', 'no',
    '--save', '', '--logfile', $logPath
  )) {
    $startInfo.ArgumentList.Add($argument)
  }
  $process = [System.Diagnostics.Process]::Start($startInfo)
  if ($null -eq $process) { throw "Failed to start isolated Redis $Role" }
  $runtime = [pscustomobject]@{ Role = $Role; Port = $Port; Process = $process; DataDirectory = $dataDirectory }
  $RuntimeRegistry.Add($runtime)
  Wait-LoopbackPort -Port $Port
  Invoke-NativeChecked -Executable $redisCliExe -Arguments @('-h', '127.0.0.1', '-p', [string]$Port, 'PING') | Out-Null
}

function Stop-IsolatedRedis {
  param([Parameter(Mandatory = $true)]$Runtime)
  $processId = [int]$Runtime.Process.Id
  $processInfo = Get-CimInstance Win32_Process -Filter "ProcessId = $processId" -ErrorAction SilentlyContinue
  if ($null -eq $processInfo) { return }
  $expectedExecutable = [System.IO.Path]::GetFullPath($redisServerExe)
  $actualExecutable = if ($processInfo.ExecutablePath) { [System.IO.Path]::GetFullPath($processInfo.ExecutablePath) } else { '' }
  $actualCommandLine = [string]$processInfo.CommandLine
  if (-not $actualExecutable.Equals($expectedExecutable, [System.StringComparison]::OrdinalIgnoreCase) -or
      [string]::IsNullOrWhiteSpace($actualCommandLine) -or
      -not (Test-CommandLineContainsPath -CommandLine $actualCommandLine -Path ([string]$Runtime.DataDirectory))) {
    throw "Refusing to stop unverified Redis PID $processId"
  }
  Stop-Process -Id $processId -ErrorAction Stop
  $Runtime.Process.WaitForExit(5000) | Out-Null
}

function Stop-IsolatedPostgres {
  param([Parameter(Mandatory = $true)][string]$DataDirectory)
  $expectedDataDirectory = [System.IO.Path]::GetFullPath((Join-Path $taskRoot 'pg-data'))
  $actualDataDirectory = [System.IO.Path]::GetFullPath($DataDirectory)
  if (-not $actualDataDirectory.Equals($expectedDataDirectory, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Refusing to stop PostgreSQL outside the isolated W7 data directory: $actualDataDirectory"
  }

  $pidPath = Join-Path $actualDataDirectory 'postmaster.pid'
  if (-not (Test-Path -LiteralPath $pidPath -PathType Leaf)) {
    throw "Cannot prove isolated PostgreSQL stopped because postmaster.pid is missing: $pidPath"
  }
  $pidLines = @(Get-Content -LiteralPath $pidPath -TotalCount 3)
  $processId = 0
  $postmasterStartEpoch = [long]0
  if ($pidLines.Count -lt 3 -or
      -not [int]::TryParse([string]$pidLines[0], [ref]$processId) -or
      $processId -le 0 -or
      -not [long]::TryParse([string]$pidLines[2], [ref]$postmasterStartEpoch) -or
      $postmasterStartEpoch -le 0) {
    throw "Refusing to stop PostgreSQL with invalid postmaster.pid: $pidPath"
  }
  $pidDataDirectory = [System.IO.Path]::GetFullPath([string]$pidLines[1])
  if (-not $pidDataDirectory.Equals($actualDataDirectory, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Refusing to stop PostgreSQL with mismatched postmaster.pid data directory: $pidDataDirectory"
  }

  $processInfo = Get-CimInstance Win32_Process -Filter "ProcessId = $processId" -ErrorAction Stop
  if ($null -eq $processInfo) { return }
  $expectedExecutable = [System.IO.Path]::GetFullPath($postgresExe)
  $actualExecutable = if ($processInfo.ExecutablePath) { [System.IO.Path]::GetFullPath($processInfo.ExecutablePath) } else { '' }
  $actualCommandLine = [string]$processInfo.CommandLine
  $processStartEpoch = ([DateTimeOffset]([DateTime]$processInfo.CreationDate)).ToUnixTimeSeconds()
  if (-not $actualExecutable.Equals($expectedExecutable, [System.StringComparison]::OrdinalIgnoreCase) -or
      [string]::IsNullOrWhiteSpace($actualCommandLine) -or
      -not (Test-PostgresCommandLineDataDirectory -CommandLine $actualCommandLine -DataDirectory $actualDataDirectory) -or
      [Math]::Abs($processStartEpoch - $postmasterStartEpoch) -gt 5) {
    throw "Refusing to stop unverified PostgreSQL PID $processId"
  }
  Invoke-NativeChecked -Executable $pgCtlExe -Arguments @('stop', '-D', $actualDataDirectory, '-m', 'fast', '-w') | Out-Null
}

$redisRuntimes = [System.Collections.Generic.List[object]]::new()
$postgresStartAttempted = $false
$evidence = $null
$cleanupErrors = [System.Collections.Generic.List[string]]::new()
New-Item -ItemType Directory -Path $taskRoot | Out-Null
$postgresData = Join-Path $taskRoot 'pg-data'
$postgresLog = Join-Path $taskRoot 'postgres.log'
$postgresPort = Get-FreeLoopbackPort
$cachePort = Get-FreeLoopbackPort
$statePort = Get-FreeLoopbackPort
$queuePort = Get-FreeLoopbackPort
if (@(@($postgresPort, $cachePort, $statePort, $queuePort) | Sort-Object -Unique).Count -ne 4) {
  throw 'Dynamic port allocation returned a duplicate port'
}

try {
  Invoke-NativeChecked -Executable $initdbExe -Arguments @(
    '-D', $postgresData, '-U', 'postgres', '-A', 'trust', '--encoding=UTF8', '--locale=C'
  ) | Out-Null
  $postgresStartAttempted = $true
  Start-IsolatedPostgres -DataDirectory $postgresData -LogPath $postgresLog -Port $postgresPort
  Wait-LoopbackPort -Port $postgresPort
  Invoke-NativeChecked -Executable $createdbExe -Arguments @(
    '-h', '127.0.0.1', '-p', [string]$postgresPort, '-U', 'postgres', 'juhe_ai_w7'
  ) | Out-Null

  Start-IsolatedRedis -Role 'cache' -Port $cachePort -RuntimeRegistry $redisRuntimes
  Start-IsolatedRedis -Role 'state' -Port $statePort -RuntimeRegistry $redisRuntimes
  Start-IsolatedRedis -Role 'queue' -Port $queuePort -RuntimeRegistry $redisRuntimes

  $namespace = 'w7-real-' + [guid]::NewGuid().ToString('N')
  $env:JUHE_AI_ENV = 'test'
  $env:JUHE_AI_SECRET = 'w7-real-isolated-secret-32-chars-minimum'
  $env:JUHE_AI_POSTGRES_URL = "postgres://postgres@127.0.0.1:$postgresPort/juhe_ai_w7?sslmode=disable"
  $env:JUHE_AI_REDIS_CACHE_URL = "redis://127.0.0.1:$cachePort/0"
  $env:JUHE_AI_REDIS_STATE_URL = "redis://127.0.0.1:$statePort/0"
  $env:JUHE_AI_REDIS_QUEUE_URL = "redis://127.0.0.1:$queuePort/0"
  $env:JUHE_AI_REDIS_NAMESPACE = $namespace
  $env:JUHE_AI_W7_REAL = '1'
  $env:JUHE_AI_W7_REAL_POSTGRES_URL = $env:JUHE_AI_POSTGRES_URL
  $env:JUHE_AI_W7_REAL_REDIS_STATE_URL = $env:JUHE_AI_REDIS_STATE_URL
  $env:JUHE_AI_W7_REAL_REDIS_QUEUE_URL = $env:JUHE_AI_REDIS_QUEUE_URL
  $env:JUHE_AI_W7_REAL_NAMESPACE = $namespace

  $schemaOutput = Invoke-NativeChecked -Executable $goExe -Arguments @(
    'run', './cmd/juhe-ai-maintenance', 'schema-up', '--dir', 'db/migrations'
  )
  $w0Output = Invoke-NativeChecked -Executable $goExe -Arguments @(
    'run', './cmd/juhe-ai-maintenance', 'w0-smoke'
  )
  $testOutput = Invoke-NativeChecked -Executable $goExe -Arguments @(
    'test', './internal/testkit/w7real', '-v', '-count=1'
  )
  $raceOutput = Invoke-NativeChecked -Executable $goExe -Arguments @(
    'test', '-race', './internal/testkit/w7real', '-count=1'
  )

  $evidence = [ordered]@{
    status = 'passed'
    taskRoot = $taskRoot
    postgresVersion = ((Invoke-NativeChecked -Executable $postgresExe -Arguments @('--version')) -join ' ')
    redisVersion = ((Invoke-NativeChecked -Executable $redisServerExe -Arguments @('--version')) -join ' ')
    ports = [ordered]@{ postgres = $postgresPort; redisCache = $cachePort; redisState = $statePort; redisQueue = $queuePort }
    schema = ($schemaOutput -join [Environment]::NewLine)
    w0Smoke = ($w0Output -join [Environment]::NewLine)
    w7Real = ($testOutput -join [Environment]::NewLine)
    w7RealRace = ($raceOutput -join [Environment]::NewLine)
  }
} finally {
  foreach ($runtime in @($redisRuntimes | Sort-Object { $_.Role } -Descending)) {
    try {
      Stop-IsolatedRedis -Runtime $runtime
    } catch {
      $cleanupErrors.Add("Redis $($runtime.Role): $($_.Exception.Message)")
    }
  }
  if ($postgresStartAttempted) {
    try {
      Stop-IsolatedPostgres -DataDirectory $postgresData
    } catch {
      $cleanupErrors.Add("PostgreSQL: $($_.Exception.Message)")
    }
  }
  if ($cleanupErrors.Count -eq 0 -and (Test-Path -LiteralPath $taskRoot)) {
    $resolvedTaskRoot = [System.IO.Path]::GetFullPath((Resolve-Path -LiteralPath $taskRoot))
    if (-not $resolvedTaskRoot.StartsWith($tempRoot, [System.StringComparison]::OrdinalIgnoreCase) -or
        -not ([System.IO.Path]::GetFileName($resolvedTaskRoot)).StartsWith('juhe-ai-w7-real-', [System.StringComparison]::Ordinal)) {
      throw "Refusing to remove unsafe W7 task root: $resolvedTaskRoot"
    }
    [System.IO.Directory]::Delete($resolvedTaskRoot, $true)
  }
  if ($cleanupErrors.Count -gt 0) {
    throw "W7 isolated runtime cleanup failed; task root retained at ${taskRoot}: $($cleanupErrors -join '; ')"
  }
}

$evidence | ConvertTo-Json -Depth 5

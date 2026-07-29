param(
  [string]$ScriptPath = (Join-Path $PSScriptRoot 'run-w7-real-windows.ps1'),
  [string]$PostgresBin,
  [string]$RedisBin
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$tokens = $null
$parseErrors = $null
$scriptAst = [System.Management.Automation.Language.Parser]::ParseFile(
  $ScriptPath,
  [ref]$tokens,
  [ref]$parseErrors
)
if ($parseErrors.Count -gt 0) {
  throw "W7 Windows harness has PowerShell parse errors: $($parseErrors.Message -join '; ')"
}

$cleanupTry = @($scriptAst.FindAll({
  param($node)
  $node -is [System.Management.Automation.Language.TryStatementAst] -and
    $null -ne $node.Finally -and
    $node.Finally.Extent.Text.Contains(
      'Stop-IsolatedPostgres -DataDirectory $postgresData',
      [System.StringComparison]::Ordinal
    )
}, $true))
if ($cleanupTry.Count -ne 1) {
  throw 'W7 harness must call Stop-IsolatedPostgres from exactly one finally block'
}

$source = Get-Content -Raw -LiteralPath $ScriptPath
$attemptOffset = $source.IndexOf('$postgresStartAttempted = $true', [System.StringComparison]::Ordinal)
$startOffset = $source.IndexOf('Start-IsolatedPostgres -DataDirectory $postgresData', [System.StringComparison]::Ordinal)
if ($attemptOffset -lt 0 -or $startOffset -lt 0 -or $attemptOffset -ge $startOffset) {
  throw 'PostgreSQL cleanup ownership must be recorded before pg_ctl can launch the postmaster'
}

$requiredGuards = @(
  'if ($postgresStartAttempted)',
  'Stop-IsolatedPostgres -DataDirectory $postgresData',
  "Join-Path `$taskRoot 'pg-data'",
  '[int]::TryParse([string]$pidLines[0], [ref]$processId)',
  '[long]::TryParse([string]$pidLines[2], [ref]$postmasterStartEpoch)',
  '$pidDataDirectory.Equals($actualDataDirectory',
  'Get-CimInstance Win32_Process -Filter "ProcessId = $processId" -ErrorAction Stop',
  'Test-PostgresCommandLineDataDirectory -CommandLine $actualCommandLine -DataDirectory $actualDataDirectory',
  '[Math]::Abs($processStartEpoch - $postmasterStartEpoch) -gt 5',
  '$actualExecutable.Equals($expectedExecutable',
  '[string]::IsNullOrWhiteSpace($actualCommandLine)',
  'Cannot prove isolated PostgreSQL stopped because postmaster.pid is missing',
  'if ($InjectPostgresStartupFailureAfterControllerExit)'
)
foreach ($guard in $requiredGuards) {
  if (-not $source.Contains($guard, [System.StringComparison]::Ordinal)) {
    throw "W7 PostgreSQL cleanup guard is missing: $guard"
  }
}
if ($source.Contains('$postgresStarted', [System.StringComparison]::Ordinal)) {
  throw 'W7 PostgreSQL cleanup must not depend on successful return from Start-IsolatedPostgres'
}

if ([string]::IsNullOrWhiteSpace($PostgresBin) -and [string]::IsNullOrWhiteSpace($RedisBin)) {
  Write-Output 'W7 PostgreSQL startup-failure cleanup static regression: PASS'
  exit 0
}
if ([string]::IsNullOrWhiteSpace($PostgresBin) -or [string]::IsNullOrWhiteSpace($RedisBin)) {
  throw 'PostgresBin and RedisBin must be provided together for the real failure-injection regression'
}

$output = @(& pwsh.exe -NoProfile -File $ScriptPath `
  -PostgresBin $PostgresBin `
  -RedisBin $RedisBin `
  -InjectPostgresStartupFailureAfterControllerExit 2>&1)
$exitCode = $LASTEXITCODE
if ($exitCode -eq 0) {
  throw 'Injected PostgreSQL startup failure unexpectedly succeeded'
}
$outputText = $output -join [Environment]::NewLine
if (-not $outputText.Contains('Injected PostgreSQL startup failure after controller exit', [System.StringComparison]::Ordinal)) {
  throw "Harness failed before the PostgreSQL fault-injection point: $outputText"
}
$taskRootMatch = [System.Text.RegularExpressions.Regex]::Match(
  $outputText.Replace('\', '/'),
  '(?i)(?<path>[A-Z]:/[^\r\n]*?/juhe-ai-w7-real-[0-9a-f]{32})'
)
if (-not $taskRootMatch.Success) {
  throw "Injected failure did not report its isolated task root: $outputText"
}
$injectedTaskRoot = $taskRootMatch.Groups['path'].Value.Replace('/', '\')
if (Test-Path -LiteralPath $injectedTaskRoot) {
  throw "Injected PostgreSQL startup failure retained task root: $injectedTaskRoot"
}
$escapedTaskRoot = [System.Text.RegularExpressions.Regex]::Escape($injectedTaskRoot.Replace('\', '/'))
$orphanedProcesses = @(Get-CimInstance Win32_Process -ErrorAction Stop | Where-Object {
  $commandLine = [string]$_.CommandLine
  -not [string]::IsNullOrWhiteSpace($commandLine) -and
    [System.Text.RegularExpressions.Regex]::IsMatch($commandLine.Replace('\', '/'), $escapedTaskRoot)
})
if ($orphanedProcesses.Count -gt 0) {
  throw "Injected PostgreSQL startup failure left orphaned process IDs: $($orphanedProcesses.ProcessId -join ', ')"
}

Write-Output 'W7 PostgreSQL startup-failure cleanup real regression: PASS'

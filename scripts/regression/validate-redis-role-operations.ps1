$ErrorActionPreference = 'Stop'

$root = Resolve-Path (Join-Path $PSScriptRoot '..\..')
$operations = Join-Path $root 'docs\deploy\macos\operations'
$verifyPath = Join-Path $operations 'verify-redis-role-isolation.sh'
$installPath = Join-Path $operations 'install-redis-role-services.sh'

foreach ($path in @($verifyPath, $installPath)) {
  if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
    throw "missing Redis role operation script: $path"
  }
}

$verify = Get-Content -LiteralPath $verifyPath -Raw
$install = Get-Content -LiteralPath $installPath -Raw

foreach ($token in @('JUHE_AI_REDIS_CACHE_URL', 'JUHE_AI_REDIS_STATE_URL', 'JUHE_AI_REDIS_QUEUE_URL')) {
  if ($verify -notmatch [regex]::Escape($token)) { throw "read-only gate does not inspect $token" }
}
foreach ($port in @(6379, 6380, 6381, 16379, 16380, 16381)) {
  if ($verify -notmatch "\b$port\b" -and $install -notmatch "\b$port\b") { throw "Redis role port $port is missing" }
}
foreach ($command in @('PING', 'CONFIG GET', 'INFO persistence', 'INFO server', 'launchctl print')) {
  if (($verify + $install) -notmatch [regex]::Escape($command)) { throw "Redis role gate is missing $command" }
}
foreach ($forbidden in @('CONFIG SET', 'FLUSHDB', 'FLUSHALL')) {
  if (($verify + $install) -match [regex]::Escape($forbidden)) { throw "Redis role scripts contain forbidden command $forbidden" }
}
foreach ($setting in @('appendonly no', 'save ""', 'allkeys-lru', 'noeviction', 'appendonly yes', 'appendfsync everysec', 'auto-aof-rewrite-min-size 1gb')) {
  if ($install -notmatch [regex]::Escape($setting)) { throw "installer is missing role setting: $setting" }
}
foreach ($step in @('launchctl bootout', 'launchctl bootstrap', 'launchctl kickstart', 'rollback_service')) {
  if ($install -notmatch [regex]::Escape($step)) { throw "installer is missing atomic service step: $step" }
}
if ($install -notmatch 'MODE=dry-run' -or $install -notmatch '--apply') { throw 'installer must default to dry-run and require --apply' }
if ($verify -notmatch 'new Set\(physicalEndpoints\)\.size') { throw 'read-only gate must reject shared physical Redis endpoints' }
if ($verify -notmatch 'processId' -or $verify -notmatch 'new Set\(processIds\)\.size') { throw 'read-only gate must reject shared Redis PIDs' }
if ($verify -notmatch 'Object\.entries\(config\)') { throw 'read-only gate must support node-redis object CONFIG GET replies' }

$bash = Get-Command bash -ErrorAction SilentlyContinue
if ($bash) {
  & $bash.Source -n ($verifyPath -replace '\\', '/')
  if ($LASTEXITCODE -ne 0) { throw 'verify-redis-role-isolation.sh syntax check failed' }
  & $bash.Source -n ($installPath -replace '\\', '/')
  if ($LASTEXITCODE -ne 0) { throw 'install-redis-role-services.sh syntax check failed' }
} else {
  Write-Warning 'bash unavailable; Redis role scripts received static validation only'
}

Write-Host 'Redis role operations static validation passed'

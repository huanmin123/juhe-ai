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
foreach ($port in @(6379, 6380, 6381, 16379, 16380, 16381, 16382)) {
  if ($verify -notmatch "\b$port\b" -and $install -notmatch "\b$port\b") { throw "Redis role port $port is missing" }
}
foreach ($token in @('scope <main|temporary|migration>', 'migration-scratch', 'migration/state-scratch', 'migration scope only supports the state scratch role')) {
  if ($install -notmatch [regex]::Escape($token)) { throw "installer lacks isolated migration scratch gate: $token" }
}
foreach ($token in @('main/cache)', 'main/state|main/queue)', 'temporary/cache|temporary/state|temporary/queue)', 'migration/state)', 'ROLE_ROOT="$BASE_DIR/redis/main/$ROLE"', 'ROLE_ROOT="$BASE_DIR/redis/temporary/$ROLE"', 'ROLE_ROOT="$BASE_DIR/redis/migration/state-scratch"')) {
  if ($install -notmatch [regex]::Escape($token)) { throw "installer lacks disjoint main/migration roots: $token" }
}
if ([regex]::Matches($install, 'ROLE_ROOT="\$BASE_DIR/redis/migration/state-scratch"').Count -ne 1) { throw 'migration scratch root must appear in exactly one path branch' }
if ($install -match 'elif \[ "\$SCOPE" = main \]') { throw 'Redis role mapping must use one explicit case without duplicate main branches' }
if ($install -notmatch 'if \[ "\$SCOPE" = temporary \] \|\| \[ "\$SCOPE" = migration \]; then rm -rf -- "\$ROLE_ROOT"; fi') { throw 'remove may recursively delete only the already mapped temporary/migration role root' }
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
  $actualStep = if ($step -eq 'rollback_service') { 'restore_previous_service' } else { $step }
  if ($install -notmatch [regex]::Escape($actualStep)) { throw "installer is missing atomic service step: $actualStep" }
}
if ($install -notmatch 'MODE=dry-run' -or $install -notmatch '--apply') { throw 'installer must default to dry-run and require --apply' }
if ($install -notmatch '\[ "\$\(id -u\)" = 0 \]') { throw 'installer apply must fail closed unless deployment controller runs it as root' }
if ($install -match '(?m)^\s*sudo\b') { throw 'installer must not own sudo authentication' }
if ($install -match 'no-appendfsync-on-rewrite yes') { throw 'queue must preserve everysec fsync during AOF rewrite' }
foreach ($token in @('EXPECTED_BASE_DIR=/Users/huanmin/juhe-ai-lite', 'main/cache', 'shared/redis-cache', 'shared/redis-cache.conf', 'redis-cache.log', 'recovery-required-', 'REDIS_ROLE_ROLLBACK_FAILED', 'launchd/port owner mismatch', 'INFO server')) {
  if ($install -notmatch [regex]::Escape($token)) { throw "installer lacks rollback/ownership gate: $token" }
}
foreach ($token in @('BACKUP_RETENTION=6', 'rotate_successful_backups', 'backup_is_recovery_referenced', 'chmod 600 "$BACKUP_ROOT/previous-redis.conf"')) {
  if ($install -notmatch [regex]::Escape($token)) { throw "installer lacks protected backup retention: $token" }
}
if ($verify -notmatch 'new Set\(physicalEndpoints\)\.size') { throw 'read-only gate must reject shared physical Redis endpoints' }
if ($verify -notmatch '\$\{url\.hostname\}:\$\{url\.port') { throw 'read-only gate physical endpoint identity must ignore redis/rediss protocol and DB' }
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

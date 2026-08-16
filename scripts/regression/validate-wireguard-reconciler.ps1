$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$operationsRoot = Join-Path $repoRoot 'docs\deploy\macos\operations'
$files = @(
  'wireguard-reconciler.sh',
  'migrate-wireguard-root-wrappers.sh',
  'install-wireguard-reconciler.sh'
)

foreach ($file in $files) {
  $path = Join-Path $operationsRoot $file
  if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Missing WireGuard recovery artifact: $file" }
}

$reconciler = Get-Content -Raw -LiteralPath (Join-Path $operationsRoot 'wireguard-reconciler.sh')
$migrator = Get-Content -Raw -LiteralPath (Join-Path $operationsRoot 'migrate-wireguard-root-wrappers.sh')
$installer = Get-Content -Raw -LiteralPath (Join-Path $operationsRoot 'install-wireguard-reconciler.sh')
$all = "$reconciler`n$migrator`n$installer"

foreach ($contract in @(
  "INSTALL_DIR='/usr/local/libexec/juhe-ai'",
  'STATE_DIR="$INSTALL_DIR/wireguard-reconciler-state"',
  'STALE_CONFIRMATIONS=2',
  'network-settling',
  'sleep-wake-grace',
  'maintenance-or-release-lock',
  'global-window-budget',
  'probe=unknown',
  'probe=healthy-disagrees',
  'new-mapping-handshake-or-transfer-timeout',
  'independent-probe',
  'run_bounded',
  'canary_done=0',
  'result=canary-failed edge=',
  'result=non-canary-failed edge=',
  'STALE_SAMPLE_MAX_GAP_SECONDS',
  'LEASE_STALE_SECONDS',
  'launchctl kickstart -k',
  'PersistentKeepalive',
  'events.log'
)) {
  if (-not $reconciler.Contains($contract, [StringComparison]::Ordinal)) {
    throw "WireGuard reconciler contract missing: $contract"
  }
}
foreach ($contract in @(
  'the root WireGuard allowlist must contain exactly eight edges',
  'ProgramArguments',
  'root WireGuard wrapper migration failed; restoring changed jobs',
  'config hash mismatch',
  'wrapper hash mismatch',
  'PersistentKeepalive = 25',
  'forbidden WireGuard shell hook',
  'write_fixed_wrapper',
  'source wrapper is hash-checked only',
  'peer_public_key',
  'show "$candidate" peers',
  'grep -Fxq "$peer_public_key"',
  'plist_has_exact_program_arguments',
  'rewrite_program_arguments',
  '/usr/libexec/PlistBuddy',
  'Delete :ProgramArguments',
  'Add :ProgramArguments array',
  'must contain exactly four ProgramArguments',
  'plist_binds_exact_pair',
  'wg-quick',
  '--wg-bin',
  '--wg-quick-bin',
  '--runtime-path',
  'wireguard-bin/wg',
  'wireguard-bin/wg-quick',
  'runtime_path_ok',
  'root_wheel_mode_755_regular',
  '%Su:%Sg',
  '= 755',
  'rewrite_runtime_path',
  'EnvironmentVariables.PATH',
  'root WireGuard wrapper migration installed for eight exact jobs'
)) {
  if (-not $migrator.Contains($contract, [StringComparison]::Ordinal)) {
    throw "WireGuard root-wrapper migration contract missing: $contract"
  }
}
if ($migrator.Contains('/var/run/wireguard/${interface_name}.name', [StringComparison]::Ordinal) -or
    $migrator.Contains('/var/run/wireguard/$logical.name', [StringComparison]::Ordinal)) {
  throw 'WireGuard root wrapper must not identify interfaces through the retired .name mapping'
}
if ($migrator.Contains('plutil -replace ProgramArguments.', [StringComparison]::Ordinal)) {
  throw 'WireGuard migration must rebuild ProgramArguments instead of indexed plutil replacement'
}
foreach ($contract in @(
  "CANONICAL_CONFIG_DIR='/usr/local/libexec/juhe-ai/wireguard-config'",
  '/usr/local/libexec/juhe-ai/wireguard-config/${interface_name}.conf',
  '$CANONICAL_CONFIG_DIR/$logical.conf',
  'chmod 700 "$CANONICAL_CONFIG_DIR"'
)) {
  if (-not "$migrator`n$installer".Contains($contract, [StringComparison]::Ordinal)) {
    throw "WireGuard canonical config contract missing: $contract"
  }
}
foreach ($retiredTarget in @('/usr/local/etc/wireguard')) {
  if ("$migrator`n$installer".Contains($retiredTarget, [StringComparison]::Ordinal)) {
    throw "WireGuard scripts still use the retired runtime config target: $retiredTarget"
  }
}

foreach ($contract in @(
  '--dry-run',
  '--apply',
  '--remove',
  '--probe-helper',
  '--wg-quick-bin',
  '--runtime-path',
  'wireguard-bin/wg',
  'wireguard-bin/wg-quick',
  'root_wheel_mode_755_regular',
  'EnvironmentVariables</key><dict>',
  '<key>PATH</key><string>$RUNTIME_PATH</string>',
  'STATE_DIR="$INSTALL_DIR/wireguard-reconciler-state"',
  '<install-dir>/wireguard-reconciler-state',
  '--script-sha256',
  '--migrator-sha256',
  'MIGRATOR_TMP',
  'migrator changed while copying into root-only directory',
  'wireguard-reconciler.manifest',
  'migrate-wireguard-root-wrappers.sh',
  'WireGuard reconciler removed'
)) {
  if (-not $installer.Contains($contract, [StringComparison]::Ordinal)) {
    throw "WireGuard installer contract missing: $contract"
  }
}
foreach ($artifact in @($reconciler, $installer)) {
  if ($artifact.Contains('/var/db/juhe-ai/wireguard-reconciler', [StringComparison]::Ordinal)) {
    throw 'WireGuard reconciler default state path must not traverse macOS /var'
  }
}

foreach ($forbidden in @(
  'systemctl restart',
  'nginx -s reload',
  'pm2 restart',
  'docker compose restart',
  'eval ',
  'aijh.huanmin.top',
  '192.168.1.',
  '/Users/huanmin'
)) {
  if ($all.Contains($forbidden, [StringComparison]::OrdinalIgnoreCase)) {
    throw "WireGuard recovery artifacts contain forbidden unrelated or private material: $forbidden"
  }
}

$bash = Get-Command bash -ErrorAction SilentlyContinue | Select-Object -First 1
if ($bash) {
  foreach ($file in $files) {
    & $bash.Source -n ((Join-Path $operationsRoot $file) -replace '\\', '/')
    if ($LASTEXITCODE -ne 0) { throw "Shell syntax failed: $file" }
  }
  & $bash.Source ((Join-Path $operationsRoot 'install-wireguard-reconciler.sh') -replace '\\', '/') --remove --dry-run
  if ($LASTEXITCODE -ne 0) { throw 'WireGuard reconciler remove dry-run failed' }
  $installerDefaultStateOutput = @(& $bash.Source ((Join-Path $operationsRoot 'install-wireguard-reconciler.sh') -replace '\\', '/') --dry-run --manifest '/tmp/manifest' --probe-helper '/tmp/probe' --install-dir '/tmp/juhe-ai-root')
  if ($LASTEXITCODE -ne 0) { throw 'WireGuard installer derived-state dry-run failed' }
  if (-not (($installerDefaultStateOutput -join "`n").Contains('state=/tmp/juhe-ai-root/wireguard-reconciler-state', [StringComparison]::Ordinal))) {
    throw 'WireGuard installer did not derive the state directory from install-dir'
  }
  & $bash.Source ((Join-Path $operationsRoot 'migrate-wireguard-root-wrappers.sh') -replace '\\', '/') --dry-run --manifest 'relative-manifest' 2>$null
  if ($LASTEXITCODE -eq 0) { throw 'WireGuard migrator accepted a relative manifest path' }
  & $bash.Source ((Join-Path $operationsRoot 'migrate-wireguard-root-wrappers.sh') -replace '\\', '/') --dry-run --manifest '/tmp/manifest' --runtime-path 'relative-runtime-path' 2>$null
  if ($LASTEXITCODE -eq 0) { throw 'WireGuard migrator accepted a relative runtime PATH segment' }
  & $bash.Source ((Join-Path $operationsRoot 'migrate-wireguard-root-wrappers.sh') -replace '\\', '/') --dry-run --manifest '/tmp/manifest' --runtime-path '/tmp::/usr/bin' 2>$null
  if ($LASTEXITCODE -eq 0) { throw 'WireGuard migrator accepted an empty runtime PATH segment' }
  & $bash.Source ((Join-Path $operationsRoot 'install-wireguard-reconciler.sh') -replace '\\', '/') --dry-run --manifest 'relative-manifest' --probe-helper '/tmp/probe' 2>$null
  if ($LASTEXITCODE -eq 0) { throw 'WireGuard installer accepted a relative manifest path' }
  & $bash.Source ((Join-Path $operationsRoot 'install-wireguard-reconciler.sh') -replace '\\', '/') --dry-run --manifest '/tmp/manifest' --probe-helper '/tmp/probe' --runtime-path 'relative-runtime-path' 2>$null
  if ($LASTEXITCODE -eq 0) { throw 'WireGuard installer accepted a relative runtime PATH segment' }
  & $bash.Source ((Join-Path $operationsRoot 'install-wireguard-reconciler.sh') -replace '\\', '/') --dry-run --manifest '/tmp/manifest' --probe-helper '/tmp/probe' --runtime-path '/tmp::/usr/bin' 2>$null
  if ($LASTEXITCODE -eq 0) { throw 'WireGuard installer accepted an empty runtime PATH segment' }
  & $bash.Source ((Join-Path $operationsRoot 'install-wireguard-reconciler.sh') -replace '\\', '/') --remove --dry-run --install-dir '/tmp/unsafe path' 2>$null
  if ($LASTEXITCODE -eq 0) { throw 'WireGuard installer accepted an unsafe remove path' }
  & $bash.Source ((Join-Path $operationsRoot 'install-wireguard-reconciler.sh') -replace '\\', '/') --remove --dry-run --install-dir '/' 2>$null
  if ($LASTEXITCODE -eq 0) { throw 'WireGuard installer accepted filesystem root as install-dir' }
  $unixName = (& $bash.Source -c 'uname -s').Trim()
  if ($unixName -in @('Darwin', 'Linux')) {
    & $bash.Source ((Join-Path $repoRoot 'scripts/regression/validate-wireguard-reconciler-harness.sh') -replace '\\', '/') ($repoRoot -replace '\\', '/')
    if ($LASTEXITCODE -ne 0) { throw 'WireGuard reconciler fake-command harness failed' }
  } else {
    Write-Warning "WireGuard fake-command harness requires Darwin/Linux bash; current shell reports $unixName."
  }
} else {
  Write-Warning 'bash is unavailable; skipped WireGuard shell syntax validation'
}

Write-Host 'WireGuard reconciler static and dry-run validation passed'

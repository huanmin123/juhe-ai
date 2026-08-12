$ErrorActionPreference = 'Stop'

$releaseStartScript = Get-Content -LiteralPath (Join-Path $PSScriptRoot '../../deploy/start.sh') -Raw
if ($releaseStartScript -match '\$\{[^}]+,,\}') {
  throw 'deploy/start.sh must remain compatible with macOS system Bash 3.2'
}
Set-StrictMode -Version Latest

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$operationsRoot = Join-Path $repoRoot 'docs\deploy\macos\operations'

function Get-ShellFunctionBlock {
  param(
    [string]$Content,
    [string]$FunctionName
  )

  $start = $Content.IndexOf("$FunctionName() {", [StringComparison]::Ordinal)
  if ($start -lt 0) { throw "Shell function not found: $FunctionName" }
  $openingBrace = $Content.IndexOf('{', $start)
  $depth = 0
  for ($index = $openingBrace; $index -lt $Content.Length; $index += 1) {
    $character = $Content[$index]
    if ($character -eq '{') { $depth += 1 }
    elseif ($character -eq '}') {
      $depth -= 1
      if ($depth -eq 0) { return $Content.Substring($start, $index - $start + 1) }
    }
  }
  throw "Shell function has unbalanced braces: $FunctionName"
}

$requiredFiles = @(
  'README.md',
  'legacy-node-postgres-index-bridge.catalog.json',
  'legacy-node-postgres-index-bridge.mjs',
  '遗留NodePostgreSQL索引桥接说明.md',
  'install-launchd-service.sh',
  'install-performance-topology.sh',
  'cleanup-production-artifacts.sh',
  'performance-handover-controller.sh',
  'manage-sing-box.sh',
  'migrate-wireguard-root-wrappers.sh',
  'wireguard-reconciler.sh',
  'install-wireguard-reconciler.sh',
  'wireguard-203-tls-nonce-probe-adapter.sh',
  'install-wireguard-203-tls-nonce-probe-adapter.sh',
  'diagnose-proxy-dns.sh',
  'temporary-cutover.sh',
  'templates\com.juhe-ai.plist.tpl',
  'templates\com.juhe-ai.sing-box.plist.tpl'
)

foreach ($relativePath in $requiredFiles) {
  $path = Join-Path $operationsRoot $relativePath
  if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
    throw "Missing macOS operation artifact: $relativePath"
  }
}

$allContent = ($requiredFiles | ForEach-Object { Get-Content -Raw -LiteralPath (Join-Path $operationsRoot $_) }) -join "`n"
foreach ($forbidden in @('aijh.huanmin.top', '/Users/huanmin', 'top.huanmin.juhe-ai-lite', 'JUHE_AI_SECRET=', 'postgresql://', 'redis://')) {
  if ($allContent.Contains($forbidden, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Public macOS operations contain private production material: $forbidden"
  }
}

$mainPlist = Get-Content -Raw -LiteralPath (Join-Path $operationsRoot 'templates\com.juhe-ai.plist.tpl')
if ($mainPlist -notmatch '<key>KeepAlive</key>\s*<true/>') { throw 'Main launchd template must use KeepAlive=true' }
if ($mainPlist -match 'worker\.js|db-service\.js|watchdog') { throw 'Main launchd template must guard only the main service' }

$launchdInstaller = Get-Content -Raw -LiteralPath (Join-Path $operationsRoot 'install-launchd-service.sh')
foreach ($contract in @('PLIST_BACKUP', 'HAD_LOADED_SERVICE', 'rollback_install', 'on_install_exit', '--health-port', 'wait_for_main_health')) {
  if (-not $launchdInstaller.Contains($contract, [StringComparison]::Ordinal)) { throw "Launchd installer rollback contract missing: $contract" }
}

$wireGuardReconciler = Get-Content -Raw -LiteralPath (Join-Path $operationsRoot 'wireguard-reconciler.sh')
$wireGuardMigrator = Get-Content -Raw -LiteralPath (Join-Path $operationsRoot 'migrate-wireguard-root-wrappers.sh')
$wireGuardInstaller = Get-Content -Raw -LiteralPath (Join-Path $operationsRoot 'install-wireguard-reconciler.sh')
foreach ($contract in @('STALE_CONFIRMATIONS=2', 'probe=unknown', 'sleep-wake-grace', 'global-window-budget', 'launchctl kickstart -k', 'independent-probe', 'root_path_chain')) {
  if (-not $wireGuardReconciler.Contains($contract, [StringComparison]::Ordinal)) { throw "WireGuard reconciler contract missing: $contract" }
}
foreach ($contract in @('exactly eight edges', 'ProgramArguments', 'root WireGuard wrapper migration failed', 'config hash mismatch', 'wrapper hash mismatch', 'peer_public_key', 'show "$candidate" peers', 'plist_has_exact_program_arguments', 'rewrite_program_arguments', '/usr/libexec/PlistBuddy', 'Delete :ProgramArguments', 'Add :ProgramArguments array')) {
  if (-not $wireGuardMigrator.Contains($contract, [StringComparison]::Ordinal)) { throw "WireGuard migration contract missing: $contract" }
}
if ($wireGuardMigrator.Contains('/var/run/wireguard/${interface_name}.name', [StringComparison]::Ordinal) -or
    $wireGuardMigrator.Contains('/var/run/wireguard/$logical.name', [StringComparison]::Ordinal)) {
  throw 'WireGuard migration wrapper must not identify interfaces through the retired .name mapping'
}
if ($wireGuardMigrator.Contains('plutil -replace ProgramArguments.', [StringComparison]::Ordinal)) {
  throw 'WireGuard migration must rebuild ProgramArguments instead of indexed plutil replacement'
}
foreach ($contract in @('--probe-helper', '--script-sha256', '--migrator-sha256', '--remove', 'wireguard-reconciler.manifest')) {
  if (-not $wireGuardInstaller.Contains($contract, [StringComparison]::Ordinal)) { throw "WireGuard installer contract missing: $contract" }
}
foreach ($forbidden in @('aijh.huanmin.top', '192.168.1.', '/Users/huanmin', 'systemctl restart', 'nginx -s reload', 'pm2 restart')) {
  if (("$wireGuardReconciler`n$wireGuardMigrator`n$wireGuardInstaller").Contains($forbidden, [StringComparison]::OrdinalIgnoreCase)) {
    throw "WireGuard operation artifacts contain forbidden private or unrelated action: $forbidden"
  }
}
$healthCheckIndex = $launchdInstaller.LastIndexOf('wait_for_main_health', [StringComparison]::Ordinal)
$healthStableIndex = $launchdInstaller.LastIndexOf('INSTALL_MUTATED=0', [StringComparison]::Ordinal)
if ($healthCheckIndex -lt 0 -or $healthStableIndex -lt 0 -or $healthCheckIndex -gt $healthStableIndex) {
  throw 'Launchd installer must verify stable local health before marking installation complete'
}

$performanceInstaller = Get-Content -Raw -LiteralPath (Join-Path $operationsRoot 'install-performance-topology.sh')
foreach ($contract in @('--dry-run', '--apply', '--service-user', '--release-dir', '--nginx-bin', '--nginx-main-config', 'system scope requires an explicit --nginx-main-config', '--nginx-config must be an included slot file, not the nginx main config', 'nginx slot config must already be an included regular file', 'nginx slot config is not included with matching contents by the active main config', 'NGINX_EXPANDED_CONFIG', '--runtime-dir', '--nginx-upstream-suffix', '--runtime-dir and --nginx-upstream-suffix must be provided together', '--go-sidecar-mode owner|reuse', 'GO_SIDECAR_MODE=owner', 'isolated candidate topology must use --go-sidecar-mode reuse', 'assert_reuse_has_no_candidate_go_sidecar', 'candidate reuse refuses a residual Go sidecar owner', 'GATEWAY_COUNT=3', 'USAGE_WORKERS=2', 'LOG_WORKERS=2', 'least_conn', 'GATEWAY_UPSTREAM', 'CONTROL_UPSTREAM', 'JUHE_AI_PERFORMANCE_NODE_ROLE', 'JUHE_AI_ACCOUNT_HEALTH_CHECK_DISPATCH_URL', 'JUHE_AI_DATASET_DATABASE_PATH', 'GO_SIDECAR_DATA_DIR', 'assert_audit_payload_blob_write_preflight', 'location ^~ /__aiinternal__/', 'proxy_next_upstream off;', 'X-Juhe-Topology-Install', 'INSTALL_TOKEN', 'activation_service_names', 'wait_for_health', 'wait_for_go_sidecar', 'wait_for_shared_go_sidecar', 'go-sidecar', 'juhe-ai-go-sidecar', '--node-path', 'NODE_BIN="$(PATH="$NODE_PATH" command -v node || true)"', "await import('pino')", 'Node runtime dependencies are unavailable in the candidate release', 'JUHE_AI_RUNTIME_LOG_STORE=postgres', 'JUHE_AI_RUNTIME_LOG_POSTGRES_URL', 'JUHE_AI_RUNTIME_LOG_INSTANCE_ID', 'JUHE_AI_TABLE_MONITOR_STORE=postgres', 'JUHE_AI_TABLE_MONITOR_POSTGRES_URL', 'JUHE_AI_TABLE_MONITOR_INSTANCE_ID', 'JUHE_AI_TABLE_MONITOR_INTERVAL', 'JUHE_AI_AUDIT_LOG_STORE=postgres', 'JUHE_AI_AUDIT_LOG_INSTANCE_ID', 'JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS', 'JUHE_AI_AUDIT_LOG_INPUT_SECRET', 'JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY', 'JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY', 'JUHE_AI_POSTGRES_URL', 'JUHE_AI_LOG_FILE_ENABLED=true', 'wait_for_metrics_registry', 'performance_metrics_registry_time_ms', 'metrics_registry_role_pids', 'VERIFIED_HEALTH_JSON', 'VERIFIED_GATEWAY_METRICS_ROLE_PIDS', 'health.processPid', 'health.dbServicePid', 'worker.replicaIndex + 1', '--print-redis-time-ms', '--observed-after-ms', '--role-pid', 'check-performance-process-metrics-registry.js', 'health_identity_matches', '/__aisys__/api/health', 'nginx_test', 'nginx_reload', '<key>UserName</key>', '--service-user must resolve to a non-root uid', 'SUDO_BIN', 'TEST_BIN', '/bin/test', '/bin/bash -s -- "$CURRENT_DIR"', 'assert_runtime_directory', 'assert_isolated_runtime_parent', 'runtime_managed_paths', 'migrate_runtime_ownership', 'assert_release_read_only', 'RESOLVED_BASE_DIR', 'chown -h "$SERVICE_USER"', 'system base directory must not be writable by the service user', 'release directory must not be writable by the service user', 'release entry must not be writable by the service user', 'required release file must not be writable by the service user', 'Go sidecar must be a regular file', 'service user cannot execute Go sidecar', 'rollback')) {
  if (-not $performanceInstaller.Contains($contract, [StringComparison]::Ordinal)) { throw "Performance topology installer contract missing: $contract" }
}
$releaseInputGateIndex = $performanceInstaller.IndexOf('[ -d "$CURRENT_DIR" ] || { echo "missing release directory: $CURRENT_DIR" >&2; exit 1; }', [StringComparison]::Ordinal)
$dryRunExitIndex = $performanceInstaller.IndexOf('[ "$MODE" = apply ] || exit 0', [StringComparison]::Ordinal)
$nodeLookupIndex = $performanceInstaller.IndexOf('NODE_BIN="$(PATH="$NODE_PATH" command -v node || true)"', [StringComparison]::Ordinal)
$runtimeCreateIndex = $performanceInstaller.IndexOf('mkdir -p "$BIN_DIR" "$LOG_DIR" "$RUNTIME_LOG_DIR" "$SPOOL_DIR" "$PLIST_DIR"', [StringComparison]::Ordinal)
if ($releaseInputGateIndex -lt 0 -or $dryRunExitIndex -lt $releaseInputGateIndex -or $nodeLookupIndex -lt $dryRunExitIndex -or $runtimeCreateIndex -lt $dryRunExitIndex) {
  throw 'Performance topology dry-run must gate immutable release inputs before exiting, while platform and mutable runtime work remains apply-only'
}
foreach ($contract in @('--instance-id-prefix', 'instance_id_for', 'instance_id_prefix=%s', '--instance-id-prefix is required when isolated runtime and upstream suffix are enabled', '--audit-input-port is required when isolated runtime and upstream suffix are enabled', '--go-sidecar-mode')) {
  if (-not $performanceInstaller.Contains($contract, [StringComparison]::Ordinal)) { throw "Performance topology instance identity contract missing: $contract" }
}
$productionCleanup = Get-Content -Raw -LiteralPath (Join-Path $operationsRoot 'cleanup-production-artifacts.sh')
foreach ($contract in @('--dry-run', '--apply', '--prune-releases', '--keep-release-count', '--prune-stale-links', '--stale-link-min-age-hours', '--prune-audit-hot', '--audit-success-hot-retention-hours', '--audit-success-sample-rate', 'current.next.*', 'protected-link', 'active-process', 'LaunchDaemons', 'RUNTIME_CONTAMINATION', 'CLEANUP_COMPLETE')) {
  if (-not $productionCleanup.Contains($contract, [StringComparison]::Ordinal)) { throw "Production cleanup contract missing: $contract" }
}
foreach ($forbidden in @('rm -rf "$BASE_DIR"', 'postgresql://', 'redis://')) {
  if ($productionCleanup.Contains($forbidden, [StringComparison]::OrdinalIgnoreCase)) { throw "Production cleanup script contains forbidden broad or private target: $forbidden" }
}
if ($performanceInstaller -match 'proxy_next_upstream_tries') {
  throw 'Performance topology must not retry streamed or non-idempotent gateway requests'
}
foreach ($contract in @(
  '"$NGINX_BIN" -t -c "$NGINX_MAIN_CONFIG"',
  '"$NGINX_BIN" -s reload -c "$NGINX_MAIN_CONFIG"',
  'service_user_xml="<key>UserName</key><string>$(xml_escape "$SERVICE_USER")</string>"',
  'if ! nginx_test >/dev/null 2>&1 || ! nginx_reload >/dev/null 2>&1'
)) {
  if (-not $performanceInstaller.Contains($contract, [StringComparison]::Ordinal)) {
    throw "Performance topology installer implementation missing: $contract"
  }
}
$serverSource = Get-Content -Raw -LiteralPath (Join-Path $repoRoot 'backend\src\server.ts')
foreach ($contract in @('processPid: process.pid', 'dbServicePid: dbService.pid', 'workerProcesses')) {
  if (-not $serverSource.Contains($contract, [StringComparison]::Ordinal)) {
    throw "Server health topology identity contract missing: $contract"
  }
}
foreach ($contract in @(
  'proxy_set_header X-Real-IP $http_x_real_ip;',
  'proxy_set_header X-Forwarded-For $http_x_forwarded_for;',
  'proxy_set_header X-Forwarded-Proto $http_x_forwarded_proto;'
)) {
  $count = ([regex]::Matches($performanceInstaller, [regex]::Escape($contract))).Count
  if ($count -ne 4) { throw "Performance topology must preserve the trusted proxy header in all four routes: $contract (found $count)" }
}
foreach ($forbidden in @(
  'proxy_set_header X-Real-IP $remote_addr;',
  'proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;',
  'proxy_set_header X-Forwarded-Proto $scheme;'
)) {
  if ($performanceInstaller.Contains($forbidden, [StringComparison]::Ordinal)) {
    throw "Performance topology must not rewrite or append the trusted proxy chain: $forbidden"
  }
}
$activationFunctionStart = $performanceInstaller.IndexOf('activation_service_names() {', [StringComparison]::Ordinal)
$activationLoopStart = $performanceInstaller.IndexOf('for name in $(activation_service_names); do', [StringComparison]::Ordinal)
$activationFunctionEnd = $performanceInstaller.IndexOf("`n}", $activationFunctionStart, [StringComparison]::Ordinal)
$activationFunctionBlock = $performanceInstaller.Substring($activationFunctionStart, $activationFunctionEnd - $activationFunctionStart)
$activationGatewayLoop = $activationFunctionBlock.IndexOf('while [ "$index" -le "$GATEWAY_COUNT" ]; do', [StringComparison]::Ordinal)
$activationGatewayLoopEnd = $activationFunctionBlock.IndexOf('  done', $activationGatewayLoop, [StringComparison]::Ordinal)
$activationControlWithinFunction = $activationFunctionBlock.IndexOf("  printf '%s\n' control-1", [StringComparison]::Ordinal)
$activationGoSidecarWithinFunction = $activationFunctionBlock.IndexOf("  printf '%s\n' go-sidecar", [StringComparison]::Ordinal)
$activationGoSidecarLast = $performanceInstaller.IndexOf("  printf '%s\n' go-sidecar", $activationFunctionStart, [StringComparison]::Ordinal)
$activationHealthCheck = $performanceInstaller.IndexOf('  wait_for_health "$name"', $activationLoopStart, [StringComparison]::Ordinal)
$activationRegistryCheck = $performanceInstaller.IndexOf('  wait_for_metrics_registry "$name"', $activationHealthCheck, [StringComparison]::Ordinal)
$activationLoopEnd = $performanceInstaller.IndexOf("`ndone", $activationRegistryCheck, [StringComparison]::Ordinal)
$activationFence = $performanceInstaller.IndexOf('  metrics_fence_ms="$(performance_metrics_registry_time_ms)"', $activationLoopStart, [StringComparison]::Ordinal)
$activationBootout = $performanceInstaller.IndexOf('  launchctl bootout "$DOMAIN" "$plist"', $activationLoopStart, [StringComparison]::Ordinal)
$activationBootstrap = $performanceInstaller.IndexOf('  launchctl bootstrap "$DOMAIN" "$plist"', $activationLoopStart, [StringComparison]::Ordinal)
$activationKickstart = $performanceInstaller.IndexOf('  launchctl kickstart -k "$DOMAIN/$(service_label "$name")"', $activationBootstrap, [StringComparison]::Ordinal)
if ($activationFunctionStart -lt 0 -or $activationFunctionEnd -lt 0 -or $activationLoopStart -lt 0 -or $activationGatewayLoop -lt 0 -or $activationGatewayLoopEnd -lt $activationGatewayLoop -or $activationControlWithinFunction -lt $activationGatewayLoopEnd -or $activationGoSidecarWithinFunction -lt $activationControlWithinFunction -or $activationBootout -lt $activationLoopStart -or $activationFence -lt $activationBootout -or $activationBootstrap -lt $activationFence -or $activationKickstart -lt $activationBootstrap -or $activationHealthCheck -lt $activationKickstart -or $activationRegistryCheck -lt $activationHealthCheck -or $activationLoopEnd -lt $activationRegistryCheck -or $activationGoSidecarLast -lt $activationFunctionStart -or $activationGoSidecarLast -gt $activationLoopStart) {
  throw 'Performance topology must activate Node gateway/control and verify DB readiness before starting the single Go sidecar'
}
if (-not $performanceInstaller.Contains('wait_for_metrics_registry "$name" "$metrics_fence_ms"', [StringComparison]::Ordinal)) {
  throw 'Performance topology registry gate must require a Redis-time freshness fence captured after bootout'
}
if (-not $performanceInstaller.Contains('role_pid_lines="$(metrics_registry_role_pids "$VERIFIED_HEALTH_JSON")"', [StringComparison]::Ordinal) -or -not $performanceInstaller.Contains('set -- "$@" --role-pid "$role_pid"', [StringComparison]::Ordinal)) {
  throw 'Performance topology registry gate must bind every expected role to the PID in the verified health topology'
}
if ($performanceInstaller.Contains('for role_pid in $(metrics_registry_role_pids', [StringComparison]::Ordinal)) {
  throw 'Performance topology must not swallow PID mapping failures inside a for command substitution'
}
$goSidecarRunScriptStart = $performanceInstaller.IndexOf('if [ "$name" = go-sidecar ]; then', [StringComparison]::Ordinal)
$goSidecarRunScriptEnd = $performanceInstaller.IndexOf("    else`n", $goSidecarRunScriptStart, [StringComparison]::Ordinal)
$goSidecarRunScript = $performanceInstaller.Substring($goSidecarRunScriptStart, $goSidecarRunScriptEnd - $goSidecarRunScriptStart)
foreach ($contract in @('JUHE_AI_RUNTIME_LOG_STORE=postgres', 'JUHE_AI_TABLE_MONITOR_STORE=postgres', 'JUHE_AI_AUDIT_LOG_STORE=postgres', 'JUHE_AI_RUNTIME_LOG_INSTANCE_ID', 'JUHE_AI_TABLE_MONITOR_INSTANCE_ID', 'JUHE_AI_AUDIT_LOG_INSTANCE_ID', 'JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY', 'JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY', 'JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS', 'JUHE_AI_AUDIT_LOG_INPUT_SECRET', 'JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL', 'JUHE_AI_AUDIT_LOG_POSTGRES_URL', 'JUHE_AI_POSTGRES_URL', 'juhe-ai-go-sidecar')) {
  if (-not $goSidecarRunScript.Contains($contract, [StringComparison]::Ordinal)) {
    throw "Performance topology Go sidecar run script missing: $contract"
  }
}
foreach ($forbidden in @('JUHE_AI_RUNTIME_LOG_STORE=sqlite', 'JUHE_AI_TABLE_MONITOR_STORE=sqlite', 'JUHE_AI_AUDIT_LOG_STORE=sqlite', 'JUHE_AI_SECRET', 'JUHE_AI_REDIS_', 'node backend/dist/server.js')) {
  if ($goSidecarRunScript.Contains($forbidden, [StringComparison]::Ordinal)) {
    throw "Performance topology Go sidecar run script must not introduce fallback or Node/Redis coupling: $forbidden"
  }
}
$goSidecarWaitFunction = Get-ShellFunctionBlock -Content $performanceInstaller -FunctionName 'wait_for_go_sidecar'
foreach ($contract in @('launchctl print "$DOMAIN/$label"', 'curl -fsS --max-time 2 -o /dev/null', '/__aiinternal__/health', 'Node -> F3 -> Node audit readback')) {
  if (-not $goSidecarWaitFunction.Contains($contract, [StringComparison]::Ordinal)) {
    throw "Performance topology Go sidecar liveness contract missing: $contract"
  }
}
if ($goSidecarWaitFunction -match 'psql|postgres|sqlite|POST ') {
  throw 'Performance topology must not claim Go sidecar input or Node readback readiness with a direct database or synthetic-write probe'
}
$sharedGoSidecarWaitFunction = Get-ShellFunctionBlock -Content $performanceInstaller -FunctionName 'wait_for_shared_go_sidecar'
if (-not $sharedGoSidecarWaitFunction.Contains('/__aiinternal__/health', [StringComparison]::Ordinal) -or $sharedGoSidecarWaitFunction.Contains('launchctl', [StringComparison]::Ordinal)) {
  throw 'Candidate topology must verify the shared Go sidecar health without managing a second launchd owner'
}
$reuseResidualGuard = Get-ShellFunctionBlock -Content $performanceInstaller -FunctionName 'assert_reuse_has_no_candidate_go_sidecar'
foreach ($contract in @('launchctl print', 'residual_plist', 'residual_run_script', 'candidate reuse refuses a residual Go sidecar owner')) {
  if (-not $reuseResidualGuard.Contains($contract, [StringComparison]::Ordinal)) {
    throw "Candidate reuse residual Go sidecar guard missing: $contract"
  }
}
$performanceServiceNamesFunction = Get-ShellFunctionBlock -Content $performanceInstaller -FunctionName 'service_names'
$rollbackFunctionStart = $performanceInstaller.IndexOf('rollback() {', [StringComparison]::Ordinal)
$onExitFunctionStart = $performanceInstaller.IndexOf('on_exit() {', $rollbackFunctionStart, [StringComparison]::Ordinal)
$rollbackFunction = $performanceInstaller.Substring($rollbackFunctionStart, $onExitFunctionStart - $rollbackFunctionStart)
if (-not $performanceServiceNamesFunction.Contains('if [ "$GO_SIDECAR_MODE" = owner ]; then', [StringComparison]::Ordinal) -or
    -not $performanceServiceNamesFunction.Contains("printf '%s\n' go-sidecar", [StringComparison]::Ordinal)) {
  throw 'Performance topology service lifecycle must manage exactly one Go sidecar only in owner mode'
}
foreach ($forbidden in @("printf '%s\n' runtime-log-indexer", "printf '%s\n' table-monitor", "printf '%s\n' audit-log-writer")) {
  if ($performanceServiceNamesFunction.Contains($forbidden, [StringComparison]::Ordinal)) {
    throw "Performance topology service lifecycle retained a standalone Go program: $forbidden"
  }
}
if (-not $rollbackFunction.Contains('for name in $(service_names); do', [StringComparison]::Ordinal) -or -not $rollbackFunction.Contains('$STAGE_DIR/$name.was-loaded', [StringComparison]::Ordinal)) {
  throw 'Performance topology rollback must restore every managed Node service and the single Go sidecar with prior loaded state'
}
$metricsGateFunctionStart = $performanceInstaller.IndexOf('wait_for_metrics_registry() {', [StringComparison]::Ordinal)
$metricsRolePidFunctionStart = $performanceInstaller.IndexOf('metrics_registry_role_pids() {', $metricsGateFunctionStart, [StringComparison]::Ordinal)
$metricsGateFunction = $performanceInstaller.Substring($metricsGateFunctionStart, $metricsRolePidFunctionStart - $metricsGateFunctionStart)
$metricsTimeFunction = Get-ShellFunctionBlock -Content $performanceInstaller -FunctionName 'performance_metrics_registry_time_ms'
if ($metricsGateFunction -notmatch '(?m)^  current_role_pid_lines="\$\(metrics_registry_role_pids "\$VERIFIED_HEALTH_JSON"\)"$') {
  throw 'Performance topology must propagate PID mapping helper failures without a fallback suffix'
}
if (-not $metricsGateFunction.Contains('role_pid_lines="$VERIFIED_GATEWAY_METRICS_ROLE_PIDS', [StringComparison]::Ordinal) -or $metricsGateFunction -notmatch 'VERIFIED_GATEWAY_METRICS_ROLE_PIDS="\$VERIFIED_GATEWAY_METRICS_ROLE_PIDS\r?\n\$current_role_pid_lines"') {
  throw 'Performance topology final control gate must reuse the verified PID mappings from every gateway activation'
}
$performanceHealthIndex = $performanceInstaller.LastIndexOf('wait_for_health "$name"', [StringComparison]::Ordinal)
$performanceNginxIndex = $performanceInstaller.LastIndexOf('nginx_reload', [StringComparison]::Ordinal)
$performanceIngressIndex = $performanceInstaller.LastIndexOf("wait_for_ingress`n", [StringComparison]::Ordinal)
if ($performanceHealthIndex -lt 0 -or $performanceNginxIndex -lt 0 -or $performanceIngressIndex -lt 0 -or $performanceHealthIndex -gt $performanceNginxIndex -or $performanceNginxIndex -gt $performanceIngressIndex) {
  throw 'Performance topology must verify every Node service before switching nginx'
}
$runtimeDirectoryFunction = Get-ShellFunctionBlock -Content $performanceInstaller -FunctionName 'assert_runtime_directory'
$isolatedRuntimeParentFunction = Get-ShellFunctionBlock -Content $performanceInstaller -FunctionName 'assert_isolated_runtime_parent'
$runtimeDirectoryComponentFunction = Get-ShellFunctionBlock -Content $performanceInstaller -FunctionName 'assert_runtime_directory_component'
$auditBlobDirectoryFunction = Get-ShellFunctionBlock -Content $performanceInstaller -FunctionName 'ensure_audit_payload_blob_directory'
$runtimeManagedPathsFunction = Get-ShellFunctionBlock -Content $performanceInstaller -FunctionName 'runtime_managed_paths'
$runtimeOwnershipFunctionStart = $performanceInstaller.IndexOf('migrate_runtime_ownership() {', [StringComparison]::Ordinal)
$runtimeOwnershipFunctionEnd = $performanceInstaller.IndexOf("`n}", $runtimeOwnershipFunctionStart, [StringComparison]::Ordinal) + 3
$runtimeOwnershipFunction = $performanceInstaller.Substring($runtimeOwnershipFunctionStart, $runtimeOwnershipFunctionEnd - $runtimeOwnershipFunctionStart)
$auditBlobWritePreflightFunction = Get-ShellFunctionBlock -Content $performanceInstaller -FunctionName 'assert_audit_payload_blob_write_preflight'
$instanceIdForFunction = Get-ShellFunctionBlock -Content $performanceInstaller -FunctionName 'instance_id_for'
$renderRunScriptFunctionStart = $performanceInstaller.IndexOf('render_run_script() {', [StringComparison]::Ordinal)
$renderRunScriptFunctionEnd = $performanceInstaller.IndexOf("`n}", $renderRunScriptFunctionStart, [StringComparison]::Ordinal) + 3
$renderRunScriptFunction = $performanceInstaller.Substring($renderRunScriptFunctionStart, $renderRunScriptFunctionEnd - $renderRunScriptFunctionStart)
$renderNginxFunctionStart = $performanceInstaller.IndexOf('render_nginx() {', [StringComparison]::Ordinal)
$renderNginxFunctionEnd = $performanceInstaller.IndexOf("`n}", $renderNginxFunctionStart, [StringComparison]::Ordinal) + 3
$renderNginxFunction = $performanceInstaller.Substring($renderNginxFunctionStart, $renderNginxFunctionEnd - $renderNginxFunctionStart)
$nginxIncludeFunction = Get-ShellFunctionBlock -Content $performanceInstaller -FunctionName 'assert_nginx_slot_included'
if (-not $runtimeManagedPathsFunction.Contains('"$DATA_DIR"', [StringComparison]::Ordinal) -or -not $runtimeOwnershipFunction.Contains('"$DATA_DIR"', [StringComparison]::Ordinal)) {
  throw 'Performance topology must manage and transfer ownership of the release-external data directory'
}
if (-not $auditBlobDirectoryFunction.Contains('mkdir "$next_path"', [StringComparison]::Ordinal) -or $auditBlobDirectoryFunction.Contains('mkdir -p', [StringComparison]::Ordinal)) {
  throw 'Performance topology must create audit payload directories one level at a time'
}
if ($performanceInstaller -match 'mkdir -p[^\r\n]*\$DATA_DIR/audit/blobs') {
  throw 'Performance topology must not follow data-directory symbolic links with mkdir -p'
}
foreach ($contract in @('audit_blob_dir="$DATA_DIR/audit/blobs"', ': > "$temporary_path"', 'mv "$temporary_path" "$renamed_path"', 'rm -f "$renamed_path"', '"$SUDO_BIN" -n -u "$SERVICE_USER" /bin/bash -s -- "$audit_blob_dir"')) {
  if (-not $auditBlobWritePreflightFunction.Contains($contract, [StringComparison]::Ordinal)) {
    throw "Performance topology audit payload write preflight missing: $contract"
  }
}
$auditBlobWritePreflightCall = $performanceInstaller.LastIndexOf('assert_audit_payload_blob_write_preflight', [StringComparison]::Ordinal)
$stageDirectoryCreation = $performanceInstaller.IndexOf('STAGE_DIR=', [StringComparison]::Ordinal)
if ($auditBlobWritePreflightCall -lt 0 -or $stageDirectoryCreation -lt 0 -or $auditBlobWritePreflightCall -gt $stageDirectoryCreation) {
  throw 'Performance topology must verify audit payload writes before staging launchd or nginx changes'
}
if (-not $renderRunScriptFunction.Contains('export JUHE_AI_DATASET_DATABASE_PATH="%s/juhe-ai-dataset.sqlite3"', [StringComparison]::Ordinal)) {
  throw 'Performance topology run scripts must keep the dataset store outside the read-only release'
}

$cutover = Get-Content -Raw -LiteralPath (Join-Path $operationsRoot 'temporary-cutover.sh')
foreach ($contract in @('assert_pid_cwd_port_health', 'API_HEALTH_PATH', 'rollback_target', "trap 'on_exit", '--dry-run', '--apply')) {
  if (-not $cutover.Contains($contract, [StringComparison]::Ordinal)) { throw "Temporary cutover contract missing: $contract" }
}
$attemptMarker = $cutover.IndexOf('SWITCH_ATTEMPTED=1', [StringComparison]::Ordinal)
$adapterInvocation = $cutover.IndexOf('"$SWITCH_SCRIPT" "$TARGET"', [StringComparison]::Ordinal)
if ($attemptMarker -lt 0 -or $adapterInvocation -lt 0 -or $attemptMarker -gt $adapterInvocation) {
  throw 'Temporary cutover must arm reverse rollback before invoking the switch adapter'
}
$rollbackProof = $cutover.LastIndexOf('verify_ingress "$rollback_target"', [StringComparison]::Ordinal)
if ($rollbackProof -lt 0 -or $rollbackProof -gt $attemptMarker) {
  throw 'Temporary cutover must prove the real rollback ingress before attempting a switch'
}

$performanceHandover = Get-Content -Raw -LiteralPath (Join-Path $operationsRoot 'performance-handover-controller.sh')
foreach ($contract in @('rollback-armed', 'route-staged', 'reload-requested', 'rollback-unproven', 'ROLLBACK_UNPROVEN', 'verify_route_and_slots_stable', 'verify_target_and_ingress_stable', 'verify_degraded_source_preflight', 'verify_gateway_ingress_once', 'preflight receipt expired', 'preflight file fingerprint changed', '--preflight-max-age-seconds', '--degraded-source', 'gateway health URLs must map to', 'main_gateway_instance_prefix', 'temporary_gateway_instance_prefix', 'gateway_instance_prefix_for', 'main_gateway_ingress_url', 'temporary_gateway_ingress_url', 'main and temporary slots share process or database-service PIDs', 'route-before-switch.conf', '.route-target.', 'require_preflight', 'preflight-cancelled', '--action <status|preflight|takeover|switchback|recover>', 'secret-like plan key is forbidden')) {
  if (-not $performanceHandover.Contains($contract, [StringComparison]::Ordinal)) { throw "Performance handover contract missing: $contract" }
}
foreach ($contract in @('main and temporary control instance IDs must differ', 'main and temporary gateway instance prefixes must differ', 'slot topology identities must differ')) {
  if (-not $performanceHandover.Contains($contract, [StringComparison]::Ordinal)) { throw "Performance handover identity isolation contract missing: $contract" }
}
if ($performanceHandover -match '\\beval\\b') { throw 'Performance handover must not evaluate plan values as shell code' }

$diagnostic = Get-Content -Raw -LiteralPath (Join-Path $operationsRoot 'diagnose-proxy-dns.sh')
foreach ($mutation in @('kill ', 'launchctl bootstrap', 'launchctl bootout', 'networksetup -set', 'scutil --set', 'rm -rf')) {
  if ($diagnostic.Contains($mutation, [StringComparison]::OrdinalIgnoreCase)) { throw "Diagnostic script is not read-only: $mutation" }
}

$singBox = Get-Content -Raw -LiteralPath (Join-Path $operationsRoot 'manage-sing-box.sh')
foreach ($contract in @('existing', 'brew', 'launchd', '--dry-run', '--apply', 'PROBE_URL', 'assert_loopback_sing_box_proxy', 'rollback_launchd', 'HAD_LOADED_SERVICE')) {
  if (-not $singBox.Contains($contract, [StringComparison]::OrdinalIgnoreCase)) { throw "sing-box management contract missing: $contract" }
}
$singboxMutationIndex = $singBox.IndexOf('LAUNCHD_MUTATED=1', [StringComparison]::Ordinal)
$singboxWaitIndex = $singBox.LastIndexOf('wait_for_listener', [StringComparison]::Ordinal)
if ($singboxMutationIndex -lt 0 -or $singboxWaitIndex -lt 0 -or $singboxMutationIndex -gt $singboxWaitIndex) {
  throw 'sing-box launchd must arm rollback before waiting for the verified listener'
}

$bash = Get-Command bash -ErrorAction SilentlyContinue
if ($bash) {
  Push-Location $repoRoot
  try {
    foreach ($script in Get-ChildItem -LiteralPath $operationsRoot -Filter '*.sh') {
      & $bash.Source -n ($script.FullName -replace '\\', '/')
      if ($LASTEXITCODE -ne 0) { throw "bash -n failed: $($script.Name)" }
    }
    & $bash.Source ((Join-Path $operationsRoot 'install-launchd-service.sh') -replace '\\', '/') --dry-run --scope user --base-dir '/tmp/juhe-ai-ops-test' --label 'com.example.juhe-ai'
    if ($LASTEXITCODE -ne 0) { throw 'launchd installer dry-run failed' }
    $performanceDryRunHarness = @'
set -euo pipefail
installer="$1"
fixture_executable="$2"
root="/tmp/juhe-ai-performance-dry-run.$$"
mkdir -p "$root"
trap 'rm -rf -- "$root"' EXIT
base="$root/base"
release="$root/release"
mkdir -p "$base" "$release/backend/dist/scripts/preflight" "$release/backend-go"
: > "$release/backend/dist/server.js"
: > "$release/backend/dist/scripts/preflight/check-node-sqlite.js"
  : > "$release/backend/dist/scripts/preflight/check-performance-process-metrics-registry.js"
  : > "$release/backend/.env"
  cp "$fixture_executable" "$release/backend-go/juhe-ai-go-sidecar"

default_output="$(bash "$installer" --dry-run --scope user --base-dir "$base" --release-dir "$release" --label-prefix com.example.juhe-ai.performance --nginx-config "$base/nginx.conf" --nginx-bin /not-a-real-nginx --nginx-main-config "$root/not-a-real-nginx.conf")"
printf '%s\n' "$default_output" | rg -Fq "data=$base/shared/data"
[ ! -e "$base/bin" ] && [ ! -e "$base/logs" ] && [ ! -e "$base/shared" ]

isolated_output="$(bash "$installer" --dry-run --scope user --base-dir "$base" --release-dir "$release" --label-prefix com.example.juhe-ai.temporary --runtime-dir "$base/runtime-temporary" --nginx-upstream-suffix temporary_20260730 --instance-id-prefix temporary --audit-input-port 3303 --go-sidecar-mode reuse --nginx-config "$base/temporary.conf" --nginx-bin /not-a-real-nginx --nginx-main-config "$root/not-a-real-nginx.conf")"
printf '%s\n' "$isolated_output" | rg -Fq "runtime=$base/runtime-temporary data=$base/runtime-temporary/data upstream_suffix=temporary_20260730 instance_id_prefix=temporary go_sidecar_mode=reuse"
[ ! -e "$base/runtime-temporary" ]

apply_fixture="$root/apply-fixture"
mkdir -p "$apply_fixture/bin" "$apply_fixture/Library/LaunchAgents" "$apply_fixture/release/backend/dist/scripts/preflight" "$apply_fixture/release/backend-go" "$apply_fixture/shared/data/audit/blobs"
: > "$apply_fixture/release/backend/dist/server.js"
: > "$apply_fixture/release/backend/dist/scripts/preflight/check-node-sqlite.js"
: > "$apply_fixture/release/backend/dist/scripts/preflight/check-performance-process-metrics-registry.js"
: > "$apply_fixture/release/backend/.env"
cp "$fixture_executable" "$apply_fixture/release/backend-go/juhe-ai-go-sidecar"
cat > "$apply_fixture/bin/launchctl" <<'EOF'
#!/bin/sh
exit 1
EOF
chmod 755 "$apply_fixture/bin/launchctl"
: > "$apply_fixture/Library/LaunchAgents/com.example.juhe-ai.temporary.go-sidecar.plist"
if PATH="$apply_fixture/bin:$PATH" HOME="$apply_fixture" bash "$installer" --apply --scope user --base-dir "$apply_fixture" --release-dir "$apply_fixture/release" --label-prefix com.example.juhe-ai.temporary --runtime-dir "$apply_fixture/runtime-temporary" --nginx-upstream-suffix temporary_20260730 --instance-id-prefix temporary --audit-input-port 3303 --go-sidecar-mode reuse --nginx-config "$apply_fixture/temporary.conf" --nginx-bin /not-a-real-nginx --nginx-main-config "$root/not-a-real-nginx.conf" >"$root/residual-go-sidecar.out" 2>&1; then
  echo 'performance topology apply accepted a residual candidate Go sidecar artifact' >&2
  exit 1
fi
grep -Fq 'candidate reuse refuses a residual Go sidecar owner' "$root/residual-go-sidecar.out"

if bash "$installer" --dry-run --scope user --base-dir "$base" --release-dir "$release" --label-prefix com.example.juhe-ai.temporary --runtime-dir "$base/runtime-temporary" --nginx-upstream-suffix temporary_20260730 --nginx-config "$base/temporary.conf" --nginx-bin /not-a-real-nginx --nginx-main-config "$root/not-a-real-nginx.conf" >"$root/missing-instance-prefix.out" 2>&1; then
  echo 'performance topology dry-run accepted isolated mode without an instance ID prefix' >&2
  exit 1
fi
grep -Fq -- '--instance-id-prefix is required when isolated runtime and upstream suffix are enabled' "$root/missing-instance-prefix.out"

if bash "$installer" --dry-run --scope user --base-dir "$base" --release-dir "$release" --label-prefix com.example.juhe-ai.temporary --runtime-dir "$base/runtime-temporary" --nginx-upstream-suffix temporary_20260730 --instance-id-prefix temporary --audit-input-port 3303 --nginx-config "$base/temporary.conf" --nginx-bin /not-a-real-nginx --nginx-main-config "$root/not-a-real-nginx.conf" >"$root/missing-reuse-mode.out" 2>&1; then
  echo 'performance topology dry-run accepted an isolated candidate that would start a second Go data owner' >&2
  exit 1
fi
grep -Fq 'isolated candidate topology must use --go-sidecar-mode reuse' "$root/missing-reuse-mode.out"

rm -f -- "$release/backend-go/juhe-ai-go-sidecar"
if bash "$installer" --dry-run --scope user --base-dir "$base" --release-dir "$release" >"$root/missing-go-sidecar.out" 2>&1; then
  echo 'performance topology dry-run accepted a release without the single Go sidecar' >&2
  exit 1
fi
grep -Fq 'missing Go sidecar' "$root/missing-go-sidecar.out"
'@
    & $bash.Source -c $performanceDryRunHarness bash ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/') ($bash.Source -replace '\\', '/')
    if ($LASTEXITCODE -ne 0) { throw 'performance topology immutable release dry-run harness failed' }
    $cleanupUnixName = (& $bash.Source -c 'uname -s').Trim()
    if ($cleanupUnixName -in @('Darwin', 'Linux')) {
      & $bash.Source ((Join-Path $repoRoot 'scripts\regression\production-cleanup-artifacts-harness.sh') -replace '\\', '/') ((Join-Path $operationsRoot 'cleanup-production-artifacts.sh') -replace '\\', '/')
      if ($LASTEXITCODE -ne 0) { throw 'production cleanup artifact harness failed' }
    } else {
      Write-Verbose "Production cleanup artifact harness requires Darwin/Linux bash; current shell reports $cleanupUnixName."
    }
$performanceHandoverHarness = @'
set -euo pipefail
root="${HOME}/.juhe-ai-handover-$$"
NODE_BIN="$(command -v node)"
mkdir -p "$root"
trap 'rm -rf -- "$root"' EXIT
mkdir -p "$root/plan" "$root/fakebin"
printf 'main\n' > "$root/route"
printf 'main\n' > "$root/main"
printf 'temporary\n' > "$root/temporary"
: > "$root/nginx.conf"
: > "$root/access.log"
cat > "$root/plan/handover.conf" <<EOF
route_file=$root/route
main_fragment=$root/main
temporary_fragment=$root/temporary
nginx_bin=$root/fakebin/nginx
node_bin=/bin/true
nginx_main_config=$root/nginx.conf
ingress_health_url=http://127.0.0.1:3099/__aisys__/health
access_log=$root/access.log
main_label=main
temporary_label=temporary
main_instance_id=control-1
temporary_instance_id=temporary-control-1
main_gateway_instance_prefix=gateway
temporary_gateway_instance_prefix=temporary-gateway
main_topology_identity=main-identity
temporary_topology_identity=temporary-identity
main_control_health_url=http://127.0.0.1:3399/__aisys__/health
temporary_control_health_url=http://127.0.0.1:3599/__aisys__/health
main_gateway_health_urls=http://127.0.0.1:3301/main-gateway-1/health,http://127.0.0.1:3302/main-gateway-2/health,http://127.0.0.1:3303/main-gateway-3/health
temporary_gateway_health_urls=http://127.0.0.1:3501/temporary-gateway-1/health,http://127.0.0.1:3502/temporary-gateway-2/health,http://127.0.0.1:3503/temporary-gateway-3/health
main_gateway_ingress_url=http://127.0.0.1:3399/v1/models
temporary_gateway_ingress_url=http://127.0.0.1:3599/v1/models
EOF
chmod 600 "$root/plan/handover.conf"

cat > "$root/fakebin/stat" <<'EOF'
#!/usr/bin/env bash
case "$1:$2" in
  -f:%z) wc -c < "$3" | tr -d ' '; exit 0 ;;
  -f:%Lp) printf '600\n'; exit 0 ;;
  -f:%u) id -u; exit 0 ;;
  -f:%u:%Lp) printf '%s:600\n' "$(id -u)"; exit 0 ;;
esac
exit 64
EOF
cat > "$root/fakebin/sleep" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
cat > "$root/fakebin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
headers= output= url= write_out= gateway_instance= gateway_pid=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -D) headers="$2"; shift 2 ;;
    -o) output="$2"; shift 2 ;;
    -w|--write-out) write_out="$2"; shift 2 ;;
    --max-time) shift 2 ;;
    -f|-s|-S|-fsS) shift ;;
    *) url="$1"; shift ;;
  esac
done
[ -z "${HANDOVER_MUTATE_DURING_PROBE_FILE:-}" ] || [ -e "${HANDOVER_MUTATE_DURING_PROBE_MARKER:-}" ] || {
  printf '\nmutated-during-probe\n' >> "$HANDOVER_MUTATE_DURING_PROBE_FILE"
  touch "$HANDOVER_MUTATE_DURING_PROBE_MARKER"
}
[ -z "${HANDOVER_EXPIRE_DURING_PROBE_JOURNAL:-}" ] || [ -e "${HANDOVER_EXPIRE_DURING_PROBE_MARKER:-}" ] || {
  sed 's/^preflight_epoch=.*/preflight_epoch=1/' "$HANDOVER_EXPIRE_DURING_PROBE_JOURNAL" > "$HANDOVER_EXPIRE_DURING_PROBE_JOURNAL.next"
  mv "$HANDOVER_EXPIRE_DURING_PROBE_JOURNAL.next" "$HANDOVER_EXPIRE_DURING_PROBE_JOURNAL"
  chmod 600 "$HANDOVER_EXPIRE_DURING_PROBE_JOURNAL"
  touch "$HANDOVER_EXPIRE_DURING_PROBE_MARKER"
}
temporary_down="${HANDOVER_TEMPORARY_DOWN:-0}"
[ -z "${HANDOVER_TEMPORARY_DOWN_FILE:-}" ] || [ ! -e "$HANDOVER_TEMPORARY_DOWN_FILE" ] || temporary_down=1
main_down="${HANDOVER_MAIN_DOWN:-0}"
[ -z "${HANDOVER_MAIN_DOWN_FILE:-}" ] || [ ! -e "$HANDOVER_MAIN_DOWN_FILE" ] || main_down=1
case "$url" in
  http://127.0.0.1:3599/*|http://127.0.0.1:3501/*|http://127.0.0.1:3502/*|http://127.0.0.1:3503/*) [ "$temporary_down" = 0 ] || exit 28 ;;
  http://127.0.0.1:3399/*|http://127.0.0.1:3301/*|http://127.0.0.1:3302/*|http://127.0.0.1:3303/*) [ "$main_down" = 0 ] || exit 29 ;;
  http://127.0.0.1:3099/*)
    active_label="$(tr -d '\n' < "$HANDOVER_ROUTE_FILE")"
    [ "$active_label" != temporary ] || [ "$temporary_down" = 0 ] || exit 28
    [ "$active_label" != main ] || [ "$main_down" = 0 ] || exit 29
    ;;
esac
case "$url" in
  http://127.0.0.1:3399/__aisys__/health) label=main; topology=main-identity; control_pid=101 ;;
  http://127.0.0.1:3599/__aisys__/health) label=temporary; topology=temporary-identity; control_pid=201 ;;
  http://127.0.0.1:3099/__aisys__/health)
    label="$(tr -d '\n' < "$HANDOVER_ROUTE_FILE")"
    case "$label" in main) topology=main-identity ;; temporary) topology=temporary-identity ;; *) exit 23 ;; esac
    case "$label" in main) control_pid=101 ;; temporary) control_pid=201 ;; esac
    ;;
  */main-gateway-1/health) gateway_instance=gateway-1; gateway_pid=301 ;;
  */main-gateway-2/health) gateway_instance=gateway-2; gateway_pid=302 ;;
  */main-gateway-3/health) gateway_instance=gateway-3; gateway_pid=303 ;;
  */temporary-gateway-1/health) gateway_instance=temporary-gateway-1; gateway_pid=501 ;;
  */temporary-gateway-2/health) gateway_instance=temporary-gateway-2; gateway_pid=502 ;;
  */temporary-gateway-3/health) gateway_instance=temporary-gateway-3; gateway_pid=503 ;;
  http://127.0.0.1:3399/v1/models) label=main; topology=main-identity; gateway_status="${HANDOVER_GATEWAY_INGRESS_STATUS:-401}" ;;
  http://127.0.0.1:3599/v1/models) label=temporary; topology=temporary-identity; gateway_status="${HANDOVER_GATEWAY_INGRESS_STATUS:-401}" ;;
  */__aisys__/api/health) ;;
  *) exit 22 ;;
esac
[ "${HANDOVER_BAD_TOPOLOGY:-0}" = 0 ] || topology=wrong-identity
case "${label:-}" in
  main) control_instance=control-1 ;;
  temporary) control_instance=temporary-control-1 ;;
esac
if [ "${HANDOVER_BAD_GATEWAY_ORDER:-0}" = 1 ] && [ "$url" = 'http://127.0.0.1:3301/main-gateway-1/health' ]; then gateway_instance=gateway-2; fi
if [ "${HANDOVER_OVERLAP_PID:-0}" = 1 ] && [ "$url" = 'http://127.0.0.1:3501/temporary-gateway-1/health' ]; then gateway_pid=301; fi
case "$url" in
  http://127.0.0.1:3399/__aisys__/health|http://127.0.0.1:3599/__aisys__/health|http://127.0.0.1:3099/__aisys__/health)
    printf '%s: %s\nX-Juhe-Topology-Install: %s\n' "$HANDOVER_HEADER" "$label" "$topology" > "$headers"
    printf '{"status":"ok","runtimeMode":"performance","nodeRole":"control","instanceId":"%s","processPid":%s,"dbServicePid":%s,"workerProcesses":[{"role":"usage-worker","replicaIndex":0,"pid":%s,"ready":true}],"workerTopologyReady":true}' "$control_instance" "$control_pid" "$((control_pid + 1))" "$((control_pid + 2))" > "$output"
    printf '%s\n' "$label" >> "$HANDOVER_ACCESS_LOG"
    ;;
  */main-gateway-*/health|*/temporary-gateway-*/health)
    printf '{"status":"ok","runtimeMode":"performance","nodeRole":"gateway","instanceId":"%s","processPid":%s,"dbServicePid":%s,"workerProcesses":[],"workerTopologyReady":true}' "$gateway_instance" "$gateway_pid" "$((gateway_pid + 100))" > "$output"
    ;;
  http://127.0.0.1:3399/v1/models|http://127.0.0.1:3599/v1/models)
    [ "${HANDOVER_BAD_GATEWAY_INGRESS:-0}" = 0 ] || topology=wrong-identity
    printf 'X-Juhe-Topology-Install: %s\n' "$topology" > "$headers"
    [ "$write_out" = '%{http_code}' ] && printf '%s' "$gateway_status"
    ;;
esac
EOF
cat > "$root/fakebin/nginx" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "$1" = -t ]; then exit 0; fi
if [ "$1" = -s ]; then
  if [ -n "${HANDOVER_FAIL_ONCE_FILE:-}" ] && [ ! -e "$HANDOVER_FAIL_ONCE_FILE" ]; then touch "$HANDOVER_FAIL_ONCE_FILE"; exit 1; fi
  [ -z "${HANDOVER_TEMPORARY_DOWN_AFTER_RELOAD_FILE:-}" ] || touch "$HANDOVER_TEMPORARY_DOWN_AFTER_RELOAD_FILE"
  [ -z "${HANDOVER_MAIN_DOWN_AFTER_RELOAD_FILE:-}" ] || touch "$HANDOVER_MAIN_DOWN_AFTER_RELOAD_FILE"
  exit 0
fi
exit 64
EOF
export HANDOVER_REAL_MV="$(command -v mv)"
cat > "$root/fakebin/mv" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ -n "${HANDOVER_FAIL_COMMITTED_JOURNAL_ONCE_FILE:-}" ] \
  && [ ! -e "$HANDOVER_FAIL_COMMITTED_JOURNAL_ONCE_FILE" ] \
  && [ -f "${1:-}" ] \
  && grep -qx 'state=committed' "${1:-}"; then
  touch "$HANDOVER_FAIL_COMMITTED_JOURNAL_ONCE_FILE"
  exit 74
fi
exec "$HANDOVER_REAL_MV" "$@"
EOF
chmod 700 "$root/fakebin"/*
export PATH="$root/fakebin:$PATH"
export HANDOVER_ROUTE_FILE="$root/route" HANDOVER_ACCESS_LOG="$root/access.log" HANDOVER_HEADER='X-Juhe-Active-Upstream'

for action in status preflight takeover switchback recover; do
  bash '__CONTROLLER__' --dry-run --action "$action" --plan-dir "$root/plan" >/dev/null
done
printf 'redis_password=forbidden\n' >> "$root/plan/handover.conf"
if bash '__CONTROLLER__' --dry-run --action status --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover accepted a secret-like plan key' >&2
  exit 69
fi
sed '$d' "$root/plan/handover.conf" > "$root/plan/handover.conf.next"
mv "$root/plan/handover.conf.next" "$root/plan/handover.conf"
chmod 600 "$root/plan/handover.conf"

sed "s#^node_bin=.*#node_bin=$NODE_BIN#" "$root/plan/handover.conf" > "$root/plan/handover.conf.next"
mv "$root/plan/handover.conf.next" "$root/plan/handover.conf"
chmod 600 "$root/plan/handover.conf"
cp -p "$root/plan/handover.conf" "$root/plan/handover.conf.clean"

if bash '__CONTROLLER__' --apply --action takeover --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover accepted takeover without preflight' >&2
  exit 70
fi
mkdir "$root/plan/handover.lock"
if bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover ignored an existing lock' >&2
  exit 71
fi
rmdir "$root/plan/handover.lock"

temporary_gateway_urls="$(awk -F= '$1 == "temporary_gateway_health_urls" { print substr($0, index($0, "=") + 1); exit }' "$root/plan/handover.conf")"
sed "s#^main_gateway_health_urls=.*#main_gateway_health_urls=$temporary_gateway_urls#" "$root/plan/handover.conf" > "$root/plan/handover.conf.next"
mv "$root/plan/handover.conf.next" "$root/plan/handover.conf"
chmod 600 "$root/plan/handover.conf"
if bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover accepted gateway URLs reused from the temporary slot' >&2
  exit 76
fi
cp -p "$root/plan/handover.conf.clean" "$root/plan/handover.conf"
chmod 600 "$root/plan/handover.conf"

sed 's#^main_gateway_ingress_url=.*#main_gateway_ingress_url=http://127.0.0.1:3599/v1/models#' "$root/plan/handover.conf" > "$root/plan/handover.conf.next"
mv "$root/plan/handover.conf.next" "$root/plan/handover.conf"
chmod 600 "$root/plan/handover.conf"
if bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover accepted a gateway ingress URL reused from the temporary slot' >&2
  exit 77
fi
cp -p "$root/plan/handover.conf.clean" "$root/plan/handover.conf"
chmod 600 "$root/plan/handover.conf"

sed 's#^main_gateway_health_urls=.*#main_gateway_health_urls=http://127.0.0.1:3301/main-gateway-1/health,http://127.0.0.1:3301/main-gateway-2/health,http://127.0.0.1:3303/main-gateway-3/health#' "$root/plan/handover.conf" > "$root/plan/handover.conf.next"
mv "$root/plan/handover.conf.next" "$root/plan/handover.conf"
chmod 600 "$root/plan/handover.conf"
if bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover accepted two direct gateway health URLs on one listener' >&2
  exit 84
fi
cp -p "$root/plan/handover.conf.clean" "$root/plan/handover.conf"
chmod 600 "$root/plan/handover.conf"

sed 's#^main_gateway_health_urls=.*#main_gateway_health_urls=http://127.0.0.1:3399/main-gateway-1/health,http://127.0.0.1:3302/main-gateway-2/health,http://127.0.0.1:3303/main-gateway-3/health#' "$root/plan/handover.conf" > "$root/plan/handover.conf.next"
mv "$root/plan/handover.conf.next" "$root/plan/handover.conf"
chmod 600 "$root/plan/handover.conf"
if bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover accepted a direct gateway health URL on the inner Nginx listener' >&2
  exit 85
fi
cp -p "$root/plan/handover.conf.clean" "$root/plan/handover.conf"
chmod 600 "$root/plan/handover.conf"

sed 's#^main_gateway_ingress_url=.*#main_gateway_ingress_url=http://localhost:3399/v1/models#' "$root/plan/handover.conf" > "$root/plan/handover.conf.next"
mv "$root/plan/handover.conf.next" "$root/plan/handover.conf"
chmod 600 "$root/plan/handover.conf"
if bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover accepted localhost as a distinct loopback listener spelling' >&2
  exit 82
fi
cp -p "$root/plan/handover.conf.clean" "$root/plan/handover.conf"
chmod 600 "$root/plan/handover.conf"

sed 's#^main_gateway_ingress_url=.*#main_gateway_ingress_url=http://127.0.0.1:03399/v1/models#' "$root/plan/handover.conf" > "$root/plan/handover.conf.next"
mv "$root/plan/handover.conf.next" "$root/plan/handover.conf"
chmod 600 "$root/plan/handover.conf"
if bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover accepted a noncanonical loopback port spelling' >&2
  exit 83
fi
cp -p "$root/plan/handover.conf.clean" "$root/plan/handover.conf"
chmod 600 "$root/plan/handover.conf"

export HANDOVER_BAD_GATEWAY_ORDER=1
if bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover accepted a gateway health response from the wrong gateway instance' >&2
  exit 78
fi
unset HANDOVER_BAD_GATEWAY_ORDER

sed 's#^temporary_gateway_instance_prefix=.*#temporary_gateway_instance_prefix=gateway#' "$root/plan/handover.conf" > "$root/plan/handover.conf.next"
mv "$root/plan/handover.conf.next" "$root/plan/handover.conf"
chmod 600 "$root/plan/handover.conf"
if bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover accepted a temporary gateway identity that collides with the main gateway prefix' >&2
  exit 86
fi
cp -p "$root/plan/handover.conf.clean" "$root/plan/handover.conf"
chmod 600 "$root/plan/handover.conf"

sed 's#^temporary_instance_id=.*#temporary_instance_id=control-1#' "$root/plan/handover.conf" > "$root/plan/handover.conf.next"
mv "$root/plan/handover.conf.next" "$root/plan/handover.conf"
chmod 600 "$root/plan/handover.conf"
if bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover accepted equal main and temporary control instance IDs' >&2
  exit 87
fi
cp -p "$root/plan/handover.conf.clean" "$root/plan/handover.conf"
chmod 600 "$root/plan/handover.conf"

sed 's#^temporary_topology_identity=.*#temporary_topology_identity=main-identity#' "$root/plan/handover.conf" > "$root/plan/handover.conf.next"
mv "$root/plan/handover.conf.next" "$root/plan/handover.conf"
chmod 600 "$root/plan/handover.conf"
if bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover accepted equal main and temporary topology identities' >&2
  exit 88
fi
cp -p "$root/plan/handover.conf.clean" "$root/plan/handover.conf"
chmod 600 "$root/plan/handover.conf"

export HANDOVER_OVERLAP_PID=1
if bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover accepted a PID shared by main and temporary gateway pools' >&2
  exit 79
fi
unset HANDOVER_OVERLAP_PID

export HANDOVER_BAD_GATEWAY_INGRESS=1
if bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover accepted an inner gateway ingress without the expected topology header' >&2
  exit 80
fi
unset HANDOVER_BAD_GATEWAY_INGRESS

export HANDOVER_GATEWAY_INGRESS_STATUS=404
if bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover accepted a non-gateway inner ingress HTTP status' >&2
  exit 81
fi
unset HANDOVER_GATEWAY_INGRESS_STATUS

export HANDOVER_BAD_TOPOLOGY=1
if bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover accepted a wrong topology identity' >&2
  exit 72
fi
unset HANDOVER_BAD_TOPOLOGY
bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null

cp -p "$root/temporary" "$root/temporary.clean"
printf 'changed-after-preflight\n' >> "$root/temporary"
if bash '__CONTROLLER__' --apply --action takeover --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover accepted a route fragment changed after preflight' >&2
  exit 89
fi
cp -p "$root/temporary.clean" "$root/temporary"
bash '__CONTROLLER__' --apply --action recover --plan-dir "$root/plan" >/dev/null
bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null
sed 's/^preflight_epoch=.*/preflight_epoch=1/' "$root/plan/handover.journal" > "$root/plan/handover.journal.next"
mv "$root/plan/handover.journal.next" "$root/plan/handover.journal"
chmod 600 "$root/plan/handover.journal"
if bash '__CONTROLLER__' --apply --action takeover --plan-dir "$root/plan" --preflight-max-age-seconds 60 >/dev/null 2>&1; then
  echo 'handover accepted an expired preflight receipt' >&2
  exit 90
fi
bash '__CONTROLLER__' --apply --action recover --plan-dir "$root/plan" >/dev/null
bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null
receipt_epoch="$(($(date +%s) - 61))"
sed -e "s/^preflight_epoch=.*/preflight_epoch=$receipt_epoch/" -e 's/^preflight_max_age_seconds=.*/preflight_max_age_seconds=60/' "$root/plan/handover.journal" > "$root/plan/handover.journal.next"
mv "$root/plan/handover.journal.next" "$root/plan/handover.journal"
chmod 600 "$root/plan/handover.journal"
if bash '__CONTROLLER__' --apply --action takeover --plan-dir "$root/plan" --preflight-max-age-seconds 900 >/dev/null 2>&1; then
  echo 'handover allowed the takeover command to extend the receipt max age' >&2
  exit 91
fi
bash '__CONTROLLER__' --apply --action recover --plan-dir "$root/plan" >/dev/null
bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null
sed 's/^route_sha256=\([0-9a-f]\{63\}\)[0-9a-f]$/route_sha256=\1/' "$root/plan/handover.journal" > "$root/plan/handover.journal.next"
mv "$root/plan/handover.journal.next" "$root/plan/handover.journal"
chmod 600 "$root/plan/handover.journal"
if bash '__CONTROLLER__' --apply --action takeover --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover accepted a truncated preflight fingerprint' >&2
  exit 92
fi
bash '__CONTROLLER__' --apply --action recover --plan-dir "$root/plan" >/dev/null
bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null

expect_probe_mutation_rejected() {
  mutation_file="$1" description="$2" backup="$mutation_file.probe-clean" marker="$root/probe-mutation-marker"
  cp -p "$mutation_file" "$backup"
  rm -f "$marker"
  export HANDOVER_MUTATE_DURING_PROBE_FILE="$mutation_file" HANDOVER_MUTATE_DURING_PROBE_MARKER="$marker"
  if bash '__CONTROLLER__' --apply --action takeover --plan-dir "$root/plan" >/dev/null 2>&1; then
    echo "handover accepted $description changed during the real-time probe" >&2
    exit 93
  fi
  unset HANDOVER_MUTATE_DURING_PROBE_FILE HANDOVER_MUTATE_DURING_PROBE_MARKER
  cp -p "$backup" "$mutation_file"
  rm -f "$backup" "$marker"
  chmod 600 "$root/plan/handover.conf"
  bash '__CONTROLLER__' --apply --action recover --plan-dir "$root/plan" >/dev/null
  bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null
}

expect_probe_mutation_rejected "$root/route" 'active route'
expect_probe_mutation_rejected "$root/temporary" 'target fragment'
expect_probe_mutation_rejected "$root/plan/handover.conf" 'handover plan'
expect_probe_mutation_rejected "$root/nginx.conf" 'Nginx main config'

export HANDOVER_EXPIRE_DURING_PROBE_JOURNAL="$root/plan/handover.journal" HANDOVER_EXPIRE_DURING_PROBE_MARKER="$root/probe-expiry-marker"
if bash '__CONTROLLER__' --apply --action takeover --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover accepted a receipt that expired during the real-time probe' >&2
  exit 94
fi
unset HANDOVER_EXPIRE_DURING_PROBE_JOURNAL HANDOVER_EXPIRE_DURING_PROBE_MARKER
rm -f "$root/probe-expiry-marker"
bash '__CONTROLLER__' --apply --action recover --plan-dir "$root/plan" >/dev/null
bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null

export HANDOVER_TEMPORARY_DOWN_AFTER_RELOAD_FILE="$root/temporary-down-after-reload"
if bash '__CONTROLLER__' --apply --action takeover --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover unexpectedly committed after the candidate failed following reload' >&2
  exit 95
fi
cmp "$root/main" "$root/route"
grep -qx 'state=rollback-proven' "$root/plan/handover.journal"
unset HANDOVER_TEMPORARY_DOWN_AFTER_RELOAD_FILE
rm -f "$root/temporary-down-after-reload"
bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null

bash '__CONTROLLER__' --apply --action takeover --plan-dir "$root/plan" >/dev/null
cmp "$root/temporary" "$root/route"
grep -qx 'state=committed' "$root/plan/handover.journal"

export HANDOVER_TEMPORARY_DOWN=1
if bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'normal reverse preflight accepted a failed active candidate' >&2
  exit 96
fi
export HANDOVER_MAIN_DOWN=1
if bash '__CONTROLLER__' --apply --action preflight --degraded-source --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'degraded-source preflight accepted an unhealthy rollback target' >&2
  exit 97
fi
unset HANDOVER_MAIN_DOWN
cp -p "$root/main" "$root/route"
if bash '__CONTROLLER__' --apply --action preflight --degraded-source --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'degraded-source preflight accepted a route that no longer points to the committed candidate' >&2
  exit 98
fi
cp -p "$root/temporary" "$root/route"
bash '__CONTROLLER__' --apply --action preflight --degraded-source --plan-dir "$root/plan" >/dev/null
bash '__CONTROLLER__' --apply --action switchback --plan-dir "$root/plan" >/dev/null
cmp "$root/main" "$root/route"
unset HANDOVER_TEMPORARY_DOWN
if bash '__CONTROLLER__' --apply --action recover --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover accepted recover from committed state' >&2
  exit 74
fi

bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null
bash '__CONTROLLER__' --apply --action recover --plan-dir "$root/plan" >/dev/null
grep -qx 'state=preflight-cancelled' "$root/plan/handover.journal"
if bash '__CONTROLLER__' --apply --action recover --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover accepted repeated recover after a cancelled preflight' >&2
  exit 75
fi
bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null
export HANDOVER_FAIL_ONCE_FILE="$root/fail-once"
if bash '__CONTROLLER__' --apply --action takeover --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover unexpectedly succeeded after injected reload failure' >&2
  exit 73
fi
cmp "$root/main" "$root/route"
grep -qx 'state=rollback-proven' "$root/plan/handover.journal"
unset HANDOVER_FAIL_ONCE_FILE
bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null
grep -qx 'state=preflight' "$root/plan/handover.journal"

export HANDOVER_FAIL_COMMITTED_JOURNAL_ONCE_FILE="$root/fail-committed-journal-once"
if bash '__CONTROLLER__' --apply --action takeover --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'handover unexpectedly succeeded after the committed journal write failed' >&2
  exit 101
fi
cmp "$root/main" "$root/route"
grep -qx 'state=rollback-proven' "$root/plan/handover.journal"
unset HANDOVER_FAIL_COMMITTED_JOURNAL_ONCE_FILE

# A failed degraded-source switch must never restore the known-dead source slot.
bash '__CONTROLLER__' --apply --action preflight --plan-dir "$root/plan" >/dev/null
bash '__CONTROLLER__' --apply --action takeover --plan-dir "$root/plan" >/dev/null
export HANDOVER_TEMPORARY_DOWN=1 HANDOVER_MAIN_DOWN_AFTER_RELOAD_FILE="$root/main-down-after-reload" HANDOVER_MAIN_DOWN_FILE="$root/main-down-after-reload"
bash '__CONTROLLER__' --apply --action preflight --degraded-source --plan-dir "$root/plan" >/dev/null
if bash '__CONTROLLER__' --apply --action switchback --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'degraded-source switchback unexpectedly committed with an unhealthy target' >&2
  exit 99
fi
cmp "$root/main" "$root/route"
grep -qx 'state=rollback-unproven' "$root/plan/handover.journal"
if bash '__CONTROLLER__' --apply --action recover --plan-dir "$root/plan" >/dev/null 2>&1; then
  echo 'degraded-source recover accepted an unhealthy target' >&2
  exit 100
fi
cmp "$root/main" "$root/route"
rm -f "$root/main-down-after-reload"
bash '__CONTROLLER__' --apply --action recover --plan-dir "$root/plan" >/dev/null
cmp "$root/main" "$root/route"
grep -qx 'state=committed' "$root/plan/handover.journal"
unset HANDOVER_TEMPORARY_DOWN HANDOVER_MAIN_DOWN_AFTER_RELOAD_FILE HANDOVER_MAIN_DOWN_FILE
'@
    $handoverUnixName = (& $bash.Source -c 'uname -s').Trim()
    if ($handoverUnixName -match '^(Darwin|Linux|MINGW|MSYS|CYGWIN)') {
      & $bash.Source -c ($performanceHandoverHarness.Replace('__CONTROLLER__', ((Join-Path $operationsRoot 'performance-handover-controller.sh') -replace '\\', '/')))
      if ($LASTEXITCODE -ne 0) { throw 'performance handover controller apply/rollback harness failed' }
    } else {
      Write-Verbose "Performance handover apply harness requires a POSIX-compatible bash; current shell reports $handoverUnixName."
    }
    & $bash.Source ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/') --dry-run --scope system --base-dir '/tmp/juhe-ai-performance-test' --nginx-config '/tmp/juhe-ai-performance-test/nginx.conf' 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'performance topology system scope accepted a missing service user' }
    $systemMainConfigError = (& $bash.Source ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/') --dry-run --scope system --service-user 'juhe-runtime' --base-dir '/tmp/juhe-ai-performance-test' --nginx-config '/tmp/juhe-ai-performance-test/slot.conf' 2>&1) | Out-String
    if ($LASTEXITCODE -eq 0 -or -not $systemMainConfigError.Contains('system scope requires an explicit --nginx-main-config', [StringComparison]::Ordinal)) {
      throw 'performance topology system scope did not reject a missing nginx main config'
    }
    $sameNginxConfigError = (& $bash.Source ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/') --dry-run --scope user --base-dir '/tmp/juhe-ai-performance-test' --nginx-config '/tmp/juhe-ai-performance-test/nginx.conf' --nginx-main-config '/tmp/juhe-ai-performance-test/nginx.conf' 2>&1) | Out-String
    if ($LASTEXITCODE -eq 0 -or -not $sameNginxConfigError.Contains('--nginx-config must be an included slot file, not the nginx main config', [StringComparison]::Ordinal)) {
      throw 'performance topology accepted the nginx main config as its included slot file'
    }
    $aliasedNginxConfigError = (& $bash.Source ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/') --dry-run --scope user --base-dir '/tmp/juhe-ai-performance-test' --nginx-config '/tmp/juhe-ai-performance-test/./nginx.conf' --nginx-main-config '/tmp/juhe-ai-performance-test/nginx.conf' 2>&1) | Out-String
    if ($LASTEXITCODE -eq 0 -or -not $aliasedNginxConfigError.Contains('--nginx-config must be an included slot file, not the nginx main config', [StringComparison]::Ordinal)) {
      throw 'performance topology accepted a dot-segment alias of the nginx main config'
    }
    $nginxIncludeMarkerHarness = @'
set -euo pipefail
slot_config=/tmp/juhe-ai-nginx-slot.conf
marker="# configuration file $slot_config:"
filler="$(printf '%1048576s' '')"
slot_contents='server { listen 127.0.0.1:4400; }'
printf '%s\n' "$slot_contents" > "$slot_config"
expanded_config="$marker
$slot_contents
# configuration file /tmp/juhe-ai-next.conf:
$filler"
__NGINX_INCLUDE_FUNCTION__
assert_nginx_slot_included "$expanded_config" "$slot_config"
if assert_nginx_slot_included "$filler" "$slot_config" >/dev/null 2>&1; then
  echo 'performance topology accepted an nginx expanded config without the slot include marker' >&2
  exit 1
fi
forged_config="$marker
server { listen 127.0.0.1:4499; }"
if assert_nginx_slot_included "$forged_config" "$slot_config" >/dev/null 2>&1; then
  echo 'performance topology accepted a forged nginx include marker with mismatched contents' >&2
  exit 1
fi
rm -f -- "$slot_config"
'@.Replace('__NGINX_INCLUDE_FUNCTION__', $nginxIncludeFunction)
    & $bash.Source -c $nginxIncludeMarkerHarness
    if ($LASTEXITCODE -ne 0) { throw 'performance topology nginx include marker large-config harness failed' }
    $nginxAliasHarness = @'
set -euo pipefail
installer="$1"
root="/tmp/juhe-ai-nginx-alias.$$"
mkdir -p "$root"
trap 'rm -rf -- "$root"' EXIT
printf 'events {}\n' > "$root/nginx.conf"
mkdir "$root/sub"
if bash "$installer" --dry-run --scope user --base-dir "$root" --nginx-config "$root/sub/../nginx.conf" --nginx-main-config "$root/nginx.conf" > "$root/output" 2>&1; then
  echo 'performance topology accepted a parent-segment alias of the nginx main config' >&2
  exit 1
fi
grep -Fq -- '--nginx-config must be an included slot file, not the nginx main config' "$root/output"
ln "$root/nginx.conf" "$root/slot.conf"
if bash "$installer" --dry-run --scope user --base-dir "$root" --nginx-config "$root/slot.conf" --nginx-main-config "$root/nginx.conf" > "$root/output" 2>&1; then
  echo 'performance topology accepted a hard-link alias of the nginx main config' >&2
  exit 1
fi
grep -Fq -- '--nginx-config must not resolve to the nginx main config' "$root/output"
ln -s "$root/nginx.conf" "$root/symlink-slot.conf"
if bash "$installer" --dry-run --scope user --base-dir "$root" --nginx-config "$root/symlink-slot.conf" --nginx-main-config "$root/nginx.conf" > "$root/output" 2>&1; then
  echo 'performance topology accepted a symbolic-link slot config' >&2
  exit 1
fi
grep -Fq 'nginx slot config must not be a symbolic link' "$root/output"
printf 'events {}\n' > "$root/Nginx-Case.conf"
if [ -e "$root/nginx-case.conf" ]; then
  if bash "$installer" --dry-run --scope user --base-dir "$root" --nginx-config "$root/nginx-case.conf" --nginx-main-config "$root/Nginx-Case.conf" > "$root/output" 2>&1; then
    echo 'performance topology accepted a case alias of the nginx main config' >&2
    exit 1
  fi
  grep -Fq -- '--nginx-config must not resolve to the nginx main config' "$root/output"
fi
'@
    $nginxAliasUnixName = (& $bash.Source -c 'uname -s').Trim()
    if ($nginxAliasUnixName -in @('Darwin', 'Linux')) {
      & $bash.Source -c $nginxAliasHarness bash ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/')
      if ($LASTEXITCODE -ne 0) { throw 'performance topology nginx physical-alias harness failed' }
    } else {
      Write-Verbose "Nginx physical-alias harness requires Darwin/Linux bash; current shell reports $nginxAliasUnixName."
    }
    & $bash.Source ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/') --dry-run --scope user --service-user 'juhe-runtime' --base-dir '/tmp/juhe-ai-performance-test' --nginx-config '/tmp/juhe-ai-performance-test/nginx.conf' 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'performance topology user scope accepted a service user' }
    & $bash.Source ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/') --dry-run --scope system --service-user root --base-dir '/tmp/juhe-ai-performance-test' --nginx-config '/tmp/juhe-ai-performance-test/nginx.conf' 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'performance topology system scope accepted root as its service user' }
    & $bash.Source ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/') --dry-run --scope user --base-dir '/tmp/juhe-ai-performance-test' --release-dir 'relative/release' 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'performance topology installer accepted a relative release directory' }
    & $bash.Source ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/') --dry-run --scope user --base-dir '/tmp/juhe-ai-performance-test' --runtime-dir 'relative/runtime' --nginx-upstream-suffix 'temporary_20260730' 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'performance topology installer accepted a relative isolated runtime directory' }
    & $bash.Source ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/') --dry-run --scope user --base-dir '/tmp/juhe-ai-performance-test' --runtime-dir '/tmp/juhe-ai-performance-test/runtime' --nginx-upstream-suffix 'temporary-slot' 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'performance topology installer accepted an invalid nginx upstream suffix' }
    & $bash.Source ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/') --dry-run --scope user --base-dir '/tmp/juhe-ai-performance-test' --runtime-dir '/tmp/juhe-ai-performance-test/runtime' 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'performance topology installer accepted a runtime directory without an upstream suffix' }
    & $bash.Source ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/') --dry-run --scope user --base-dir '/tmp/juhe-ai-performance-test' --nginx-upstream-suffix 'temporary_20260730' 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'performance topology installer accepted an upstream suffix without a runtime directory' }
    & $bash.Source ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/') --dry-run --scope user --base-dir '/tmp/juhe-ai-performance-test' --instance-id-prefix 'temporary slot' 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'performance topology installer accepted an unsafe instance ID prefix' }
    $resolvedReleaseHarness = @'
set -euo pipefail
installer="$1"
root="/tmp/juhe-ai-resolved-release.$$"
mkdir -p "$root"
trap 'rm -rf -- "$root"' EXIT
unsafe_release="$root/release\$unsafe"
mkdir -p "$unsafe_release"
ln -s "$unsafe_release" "$root/current"
if bash "$installer" --dry-run --scope user --base-dir "$root/base" --release-dir "$root/current" > "$root/output" 2>&1; then
  echo 'performance topology accepted an unsafe resolved release path' >&2
  exit 1
fi
grep -Fq 'resolved release path contains unsafe shell characters' "$root/output"
'@
    & $bash.Source -c $resolvedReleaseHarness bash ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/')
    if ($LASTEXITCODE -ne 0) { throw 'performance topology resolved release path safety harness failed' }
    & $bash.Source ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/') --dry-run --scope user --base-dir '/tmp/juhe-ai-performance-test' --nginx-bin 'relative/nginx' 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'performance topology installer accepted a relative nginx binary' }
    & $bash.Source ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/') --dry-run --scope user --base-dir '/tmp/juhe-ai-performance-test' --nginx-main-config 'relative/nginx.conf' 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'performance topology installer accepted a relative nginx main config' }
    & $bash.Source ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/') --dry-run --scope user --base-dir '/tmp/juhe-ai-performance-test' --control-port 3102 --gateway-base-port 3101 --gateway-count 3 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'performance topology installer accepted overlapping control and gateway ports' }

    $runtimeDirectoryHarness = @'
set -euo pipefail
root="$(mktemp -d)"
trap 'rm -rf -- "$root"' EXIT
mkdir -p "$root/base/inside" "$root/outside/nested"
RESOLVED_BASE_DIR="$(cd "$root/base" && pwd -P)"
BASE_DIR="$root/base"
__RUNTIME_DIRECTORY_FUNCTION__
__ISOLATED_RUNTIME_PARENT_FUNCTION__
__RUNTIME_DIRECTORY_COMPONENT_FUNCTION__
__AUDIT_BLOB_DIRECTORY_FUNCTION__
assert_runtime_directory "$root/base/inside"
assert_isolated_runtime_parent "$root/base/isolated/nested"
DATA_DIR="$root/base/new-data"
ensure_audit_payload_blob_directory
assert_runtime_directory "$DATA_DIR/audit/blobs"
ln -s "$root/outside" "$root/base/direct-link"
if assert_runtime_directory "$root/base/direct-link" 2>/dev/null; then
  echo 'runtime directory guard accepted a symbolic link' >&2
  exit 61
fi
ln -s "$root/outside" "$root/base/isolated-link"
if assert_isolated_runtime_parent "$root/base/isolated-link" 2>/dev/null; then
  echo 'isolated runtime parent guard accepted a symbolic-link ancestor' >&2
  exit 63
fi
ln -s "$root/outside" "$root/base/data-link"
DATA_DIR="$root/base/data-link"
if ensure_audit_payload_blob_directory 2>/dev/null; then
  echo 'audit payload directory creation accepted a DATA_DIR symbolic link' >&2
  exit 65
fi
[ ! -e "$root/outside/audit" ] || { echo 'DATA_DIR symbolic link caused a path to be created outside the base' >&2; exit 66; }
mkdir "$root/base/audit-link-data"
ln -s "$root/outside" "$root/base/audit-link-data/audit"
DATA_DIR="$root/base/audit-link-data"
if ensure_audit_payload_blob_directory 2>/dev/null; then
  echo 'audit payload directory creation accepted an audit symbolic link' >&2
  exit 67
fi
[ ! -e "$root/outside/blobs" ] || { echo 'audit symbolic link caused a path to be created outside the base' >&2; exit 68; }
mkdir -p "$root/base/blobs-link-data/audit"
ln -s "$root/outside" "$root/base/blobs-link-data/audit/blobs"
DATA_DIR="$root/base/blobs-link-data"
if ensure_audit_payload_blob_directory 2>/dev/null; then
  echo 'audit payload directory creation accepted a blobs symbolic link' >&2
  exit 69
fi
RESOLVED_BASE_DIR="$(cd "$root/outside" && pwd -P)"
if assert_runtime_directory "$root/base/inside" 2>/dev/null; then
  echo 'runtime directory guard accepted a physical path outside the base' >&2
  exit 62
fi
if assert_isolated_runtime_parent "$root/base/isolated/nested" 2>/dev/null; then
  echo 'isolated runtime parent guard accepted a physical path outside the base' >&2
  exit 64
fi
'@.Replace('__RUNTIME_DIRECTORY_FUNCTION__', $runtimeDirectoryFunction).Replace('__ISOLATED_RUNTIME_PARENT_FUNCTION__', $isolatedRuntimeParentFunction).Replace('__RUNTIME_DIRECTORY_COMPONENT_FUNCTION__', $runtimeDirectoryComponentFunction).Replace('__AUDIT_BLOB_DIRECTORY_FUNCTION__', $auditBlobDirectoryFunction)
    & $bash.Source -c $runtimeDirectoryHarness
    if ($LASTEXITCODE -ne 0) { throw 'Performance topology runtime directory containment harness failed' }

    $runtimeOwnershipHarness = @'
set -euo pipefail
root="$(mktemp -d)"
trap 'rm -rf -- "$root"' EXIT
LOG_DIR="$root/logs"
SPOOL_DIR="$root/spool"
DATA_DIR="$root/data"
SERVICE_USER=juhe-runtime
outside="$root/outside"
mkdir -p "$LOG_DIR/runtime" "$SPOOL_DIR/gateway-1" "$DATA_DIR/audit/blobs" "$outside" "$root/bin"
printf 'log\n' > "$LOG_DIR/runtime/root-owned.log"
printf 'spool\n' > "$SPOOL_DIR/gateway-1/root-owned.json"
printf 'blob\n' > "$DATA_DIR/audit/blobs/root-owned.blob"
printf 'outside\n' > "$outside/untouched"
chmod 600 "$LOG_DIR/runtime/root-owned.log" "$SPOOL_DIR/gateway-1/root-owned.json" "$DATA_DIR/audit/blobs/root-owned.blob" "$outside/untouched"
ln -s "$outside" "$SPOOL_DIR/external-link"
export CHOWN_LOG="$root/chown.log"
cat > "$root/bin/chown" <<'EOF'
#!/bin/sh
[ "$1" = -h ] || exit 91
[ "$2" = juhe-runtime ] || exit 92
shift 2
for path do printf '%s\n' "$path" >> "$CHOWN_LOG"; done
EOF
chmod +x "$root/bin/chown"
PATH="$root/bin:$PATH"
export PATH
__RUNTIME_OWNERSHIP_FUNCTION__
migrate_runtime_ownership
grep -Fxq "$LOG_DIR/runtime/root-owned.log" "$CHOWN_LOG"
grep -Fxq "$SPOOL_DIR/gateway-1/root-owned.json" "$CHOWN_LOG"
grep -Fxq "$DATA_DIR/audit/blobs" "$CHOWN_LOG"
grep -Fxq "$SPOOL_DIR/external-link" "$CHOWN_LOG"
if grep -Fxq "$DATA_DIR/audit/blobs/root-owned.blob" "$CHOWN_LOG"; then
  echo 'runtime ownership migration scanned append-only audit payloads' >&2
  exit 94
fi
if grep -Fxq "$outside/untouched" "$CHOWN_LOG"; then
  echo 'runtime ownership migration followed a symbolic link outside the managed trees' >&2
  exit 93
fi
'@.Replace('__RUNTIME_OWNERSHIP_FUNCTION__', $runtimeOwnershipFunction)
    & $bash.Source -c $runtimeOwnershipHarness
    if ($LASTEXITCODE -ne 0) { throw 'Performance topology root-to-nonroot runtime ownership harness failed' }

    $isolatedRenderHarness = @'
set -euo pipefail
root="$(mktemp -d)"
trap 'rm -rf -- "$root"' EXIT
RUNTIME_DIR="$root/base/runtime-temporary"
BIN_DIR="$RUNTIME_DIR/bin"
LOG_DIR="$RUNTIME_DIR/logs"
RUNTIME_LOG_DIR="$LOG_DIR/runtime"
SPOOL_DIR="$RUNTIME_DIR/usage-spool"
DATA_DIR="$RUNTIME_DIR/data"
CURRENT_DIR="$root/release"
NODE_PATH=/usr/local/opt/node@22/bin:/usr/bin:/bin
GATEWAY_COUNT=3
USAGE_WORKERS=2
LOG_WORKERS=2
GATEWAY_BASE_PORT=3501
CONTROL_PORT=3600
INGRESS_PORT=3599
AUDIT_INPUT_PORT=3303
GATEWAY_UPSTREAM=juhe_ai_gateway_pool_temporary_20260730
CONTROL_UPSTREAM=juhe_ai_control_temporary_20260730
INSTALL_TOKEN=temporary-install-token
INSTANCE_ID_PREFIX=temporary
GO_SIDECAR_MODE=reuse
BASE_DIR="$root/base"
GO_SIDECAR_DATA_DIR="$BASE_DIR/shared/data"
mkdir -p "$CURRENT_DIR/backend" "$CURRENT_DIR/backend-go"
cat > "$CURRENT_DIR/backend/.env" <<'EOF'
JUHE_AI_POSTGRES_URL=postgres://temporary
JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS=127.0.0.1:3303
JUHE_AI_AUDIT_LOG_INPUT_URL=http://127.0.0.1:3303
JUHE_AI_AUDIT_LOG_INPUT_SECRET=temporary-f3-input-secret-with-32-bytes
EOF
cat > "$CURRENT_DIR/backend-go/juhe-ai-go-sidecar" <<'EOF'
#!/bin/sh
printf '%s\n' "$NODE_ENV|$JUHE_AI_RUNTIME_LOG_STORE|$JUHE_AI_RUNTIME_LOG_INSTANCE_ID|$JUHE_AI_TABLE_MONITOR_STORE|$JUHE_AI_TABLE_MONITOR_INSTANCE_ID|$JUHE_AI_AUDIT_LOG_STORE|$JUHE_AI_AUDIT_LOG_INSTANCE_ID|$JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY|$JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY|$JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS|$JUHE_AI_AUDIT_LOG_INPUT_SECRET"
EOF
chmod 755 "$CURRENT_DIR/backend-go/juhe-ai-go-sidecar"
service_role() { printf gateway; }
service_port() { printf 3501; }
__INSTANCE_ID_FOR_FUNCTION__
__RENDER_RUN_SCRIPT_FUNCTION__
__RENDER_NGINX_FUNCTION__
render_run_script gateway-1 "$root/gateway-1.sh"
render_run_script go-sidecar "$root/go-sidecar.sh"
render_nginx "$root/nginx.conf"
rg -Fqx 'export JUHE_AI_INSTANCE_ID=temporary-gateway-1' "$root/gateway-1.sh"
rg -Fqx "export JUHE_AI_LOG_DIR=\"$RUNTIME_LOG_DIR\"" "$root/gateway-1.sh"
rg -Fqx "export JUHE_AI_USAGE_SPOOL_DIR=\"$SPOOL_DIR\"" "$root/gateway-1.sh"
rg -Fqx "export JUHE_AI_DATASET_DATABASE_PATH=\"$DATA_DIR/juhe-ai-dataset.sqlite3\"" "$root/gateway-1.sh"
rg -Fqx 'export JUHE_AI_RUNTIME_LOG_STORE=postgres' "$root/go-sidecar.sh"
rg -Fqx 'export JUHE_AI_RUNTIME_LOG_INSTANCE_ID="temporary-runtime-log"' "$root/go-sidecar.sh"
rg -Fqx 'export JUHE_AI_TABLE_MONITOR_STORE=postgres' "$root/go-sidecar.sh"
rg -Fqx 'export JUHE_AI_TABLE_MONITOR_INSTANCE_ID="temporary-table-monitor"' "$root/go-sidecar.sh"
rg -Fqx 'export JUHE_AI_AUDIT_LOG_STORE=postgres' "$root/go-sidecar.sh"
rg -Fqx 'export JUHE_AI_AUDIT_LOG_POSTGRES_URL="$audit_log_url"' "$root/go-sidecar.sh"
rg -Fqx 'export JUHE_AI_AUDIT_LOG_INSTANCE_ID="temporary-audit-log"' "$root/go-sidecar.sh"
rg -Fqx "export JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY=\"$GO_SIDECAR_DATA_DIR/audit/blobs\"" "$root/go-sidecar.sh"
rg -Fqx "export JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY=\"$GO_SIDECAR_DATA_DIR/audit/hot-search\"" "$root/go-sidecar.sh"
rg -Fqx 'export JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS="$input_address"' "$root/go-sidecar.sh"
rg -Fqx 'export JUHE_AI_AUDIT_LOG_INPUT_SECRET="$input_secret"' "$root/go-sidecar.sh"
rg -Fqx "exec \"$CURRENT_DIR/backend-go/juhe-ai-go-sidecar\"" "$root/go-sidecar.sh"
if rg -Fq 'JUHE_AI_RUNTIME_LOG_STORE=sqlite' "$root/go-sidecar.sh" || rg -Fq 'JUHE_AI_TABLE_MONITOR_STORE=sqlite' "$root/go-sidecar.sh" || rg -Fq 'JUHE_AI_AUDIT_LOG_STORE=sqlite' "$root/go-sidecar.sh" || rg -Fq 'JUHE_AI_SECRET' "$root/go-sidecar.sh" || rg -Fq 'JUHE_AI_REDIS_' "$root/go-sidecar.sh"; then
  echo 'Go sidecar run script introduced a forbidden fallback or Redis coupling' >&2
  exit 69
fi
(cd "$CURRENT_DIR" && "$root/go-sidecar.sh") > "$root/go-sidecar.env"
rg -Fqx "production|postgres|temporary-runtime-log|postgres|temporary-table-monitor|postgres|temporary-audit-log|$GO_SIDECAR_DATA_DIR/audit/blobs|$GO_SIDECAR_DATA_DIR/audit/hot-search|127.0.0.1:3303|temporary-f3-input-secret-with-32-bytes" "$root/go-sidecar.env"
rg -Fq "upstream $GATEWAY_UPSTREAM {" "$root/nginx.conf"
rg -Fq "upstream $CONTROL_UPSTREAM {" "$root/nginx.conf"
rg -Fq "proxy_pass http://$CONTROL_UPSTREAM;" "$root/nginx.conf"
rg -Fq "proxy_pass http://$GATEWAY_UPSTREAM;" "$root/nginx.conf"
if rg -Fq "$root/base/bin/performance" "$root/gateway-1.sh" "$root/nginx.conf"; then
  echo 'isolated topology retained the fixed performance run-script directory' >&2
  exit 65
fi
if rg -Fq "$root/base/logs" "$root/gateway-1.sh" "$root/nginx.conf"; then
  echo 'isolated topology retained the fixed shared log directory' >&2
  exit 66
fi
if rg -Fq "$root/base/shared/usage-spool" "$root/gateway-1.sh" "$root/nginx.conf"; then
  echo 'isolated topology retained the fixed shared usage spool directory' >&2
  exit 67
fi
if rg -Fq 'proxy_pass http://juhe_ai_control;' "$root/nginx.conf" || rg -Fq 'proxy_pass http://juhe_ai_gateway_pool;' "$root/nginx.conf"; then
  echo 'isolated topology retained an unsuffixed nginx upstream reference' >&2
  exit 68
fi
'@.Replace('__INSTANCE_ID_FOR_FUNCTION__', $instanceIdForFunction).Replace('__RENDER_RUN_SCRIPT_FUNCTION__', $renderRunScriptFunction).Replace('__RENDER_NGINX_FUNCTION__', $renderNginxFunction)
    & $bash.Source -c $isolatedRenderHarness
    if ($LASTEXITCODE -ne 0) { throw 'Performance topology isolated runtime and nginx rendering harness failed' }

    $rollbackHarness = @'
set -euo pipefail
root="$(mktemp -d)"
trap 'rm -rf -- "$root"' EXIT
NGINX_CONFIG="$root/nginx.conf"
NGINX_BACKUP="$root/nginx.backup"
STAGE_DIR="$root/stage"
mkdir -p "$STAGE_DIR"
events="$root/events"
service_names() { printf '%s\n' control-1; }
service_plist_path() { printf '%s/%s.plist' "$root" "$1"; }
service_run_path() { printf '%s/%s.sh' "$root" "$1"; }
service_label() { printf 'com.example.%s' "$1"; }
launchctl() {
  printf '%s\n' "$*" >> "$events"
  if [ "${FAIL_BOOTSTRAP:-0}" = 1 ] && [ "$1" = bootstrap ]; then return 1; fi
}
DOMAIN=system
nginx_test() { return 0; }
nginx_reload() { [ "${FAIL_NGINX_RELOAD:-1}" = 0 ]; }
__ROLLBACK_FUNCTION__
printf 'CANDIDATE\n' > "$NGINX_CONFIG"
printf 'OLD\n' > "$NGINX_BACKUP"
: > "$events"
if rollback; then echo 'rollback ignored nginx reload failure' >&2; exit 71; fi
grep -qx OLD "$NGINX_CONFIG" || exit 72
[ ! -s "$events" ] || { echo 'rollback stopped services after nginx reload failure' >&2; exit 73; }
printf 'CANDIDATE\n' > "$NGINX_CONFIG"
rm -f -- "$NGINX_BACKUP"
: > "$events"
if rollback; then echo 'rollback accepted a missing prior nginx config' >&2; exit 74; fi
grep -qx CANDIDATE "$NGINX_CONFIG" || exit 75
[ ! -s "$events" ] || { echo 'rollback stopped services without a prior nginx config' >&2; exit 76; }
printf 'CANDIDATE\n' > "$NGINX_CONFIG"
rm -f -- "$NGINX_BACKUP"
printf 'CANDIDATE PLIST\n' > "$root/control-1.plist"
printf 'CANDIDATE RUN\n' > "$root/control-1.sh"
: > "$events"
FAIL_NGINX_RELOAD=0
if ! rollback; then echo 'rollback failed to remove a first-install candidate topology' >&2; exit 81; fi
[ ! -e "$NGINX_CONFIG" ] || { echo 'rollback retained first-install nginx candidate config' >&2; exit 82; }
[ ! -e "$root/control-1.plist" ] || exit 83
[ ! -e "$root/control-1.sh" ] || exit 84
rg -q -- '^bootout ' "$events" || exit 85
printf 'CANDIDATE\n' > "$NGINX_CONFIG"
printf 'OLD\n' > "$NGINX_BACKUP"
printf 'CANDIDATE PLIST\n' > "$root/control-1.plist"
printf 'CANDIDATE RUN\n' > "$root/control-1.sh"
printf 'OLD PLIST\n' > "$root/control-1.plist.performance-backup.$$"
printf 'OLD RUN\n' > "$root/control-1.sh.performance-backup.$$"
touch "$STAGE_DIR/control-1.was-loaded"
: > "$events"
FAIL_NGINX_RELOAD=0
FAIL_BOOTSTRAP=1
if rollback; then echo 'rollback swallowed launchd bootstrap failure' >&2; exit 77; fi
grep -qx 'OLD PLIST' "$root/control-1.plist" || exit 78
grep -qx 'OLD RUN' "$root/control-1.sh" || exit 79
rg -q -- '^bootstrap ' "$events" || exit 80
'@.Replace('__ROLLBACK_FUNCTION__', $rollbackFunction)
    & $bash.Source -c $rollbackHarness
    if ($LASTEXITCODE -ne 0) { throw 'Performance topology fail-closed rollback harness failed' }

    $metricsGateHarness = @'
set -euo pipefail
CURRENT_DIR=/tmp/juhe-ai-performance-harness
INGRESS_PORT=3599
GATEWAY_COUNT=3
USAGE_WORKERS=2
LOG_WORKERS=2
INSTANCE_ID_PREFIX=
VERIFIED_HEALTH_JSON=
VERIFIED_GATEWAY_METRICS_ROLE_PIDS=
HARNESS_ROOT="$(mktemp -d)"
trap 'rm -rf -- "$HARNESS_ROOT"' EXIT
__METRICS_GATE_FUNCTION__
__INSTANCE_ID_FOR_FUNCTION__
metrics_registry_role_pids() {
  case "$1" in
    gateway-1) printf '%s\n' 'gateway:gateway-1=101' 'db-service:gateway-1=201' ;;
    gateway-2) printf '%s\n' 'gateway:gateway-2=102' 'db-service:gateway-2=202' ;;
    gateway-3) printf '%s\n' 'gateway:gateway-3=103' 'db-service:gateway-3=203' ;;
    control-1) printf '%s\n' 'control:control-1=104' 'db-service:control-1=204' 'usage-worker:1=301' 'usage-worker:2=302' 'log-worker:1=401' 'log-worker:2=402' 'stats-worker:1=501' 'ops-worker:1=601' ;;
    *) return 2 ;;
  esac
}
node() {
  [ "$JUHE_AI_LOG_FILE_ENABLED" = false ]
  printf '%s\n' "$@" > "$HARNESS_ROOT/$VERIFIED_HEALTH_JSON.args"
}
for instance in gateway-1 gateway-2 gateway-3 control-1; do
  VERIFIED_HEALTH_JSON="$instance"
  wait_for_metrics_registry "$instance" 123456789
done
[ "$(rg -c -- '--role-pid' "$HARNESS_ROOT/control-1.args")" -eq 14 ]
for mapping in 'gateway:gateway-1=101' 'db-service:gateway-1=201' 'gateway:gateway-2=102' 'db-service:gateway-2=202' 'gateway:gateway-3=103' 'db-service:gateway-3=203'; do
  rg -qx -- "$mapping" "$HARNESS_ROOT/control-1.args"
done
'@.Replace('__METRICS_GATE_FUNCTION__', $metricsGateFunction).Replace('__INSTANCE_ID_FOR_FUNCTION__', $instanceIdForFunction)
    & $bash.Source -c $metricsGateHarness
    if ($LASTEXITCODE -ne 0) { throw 'Performance topology PID accumulation executable harness failed' }

    $metricsTimeHarness = @'
set -euo pipefail
CURRENT_DIR=/tmp/juhe-ai-performance-harness
INGRESS_PORT=3599
__METRICS_TIME_FUNCTION__
node() {
  [ "$JUHE_AI_LOG_FILE_ENABLED" = false ]
  [ "$1" = "$CURRENT_DIR/backend/dist/scripts/preflight/check-performance-process-metrics-registry.js" ]
  [ "$2" = --print-redis-time-ms ]
  printf '%s\n' 123456789
}
observed_after_ms="$(performance_metrics_registry_time_ms)"
case "$observed_after_ms" in
  ''|*[!0-9]*) exit 1 ;;
esac
'@.Replace('__METRICS_TIME_FUNCTION__', $metricsTimeFunction)
    & $bash.Source -c $metricsTimeHarness
    if ($LASTEXITCODE -ne 0) { throw 'Performance topology metrics timestamp preflight harness failed' }

    $metricsMapperFailureHarness = @'
set -euo pipefail
CURRENT_DIR=/tmp/juhe-ai-performance-harness
GATEWAY_COUNT=3
USAGE_WORKERS=2
LOG_WORKERS=2
VERIFIED_HEALTH_JSON=broken
VERIFIED_GATEWAY_METRICS_ROLE_PIDS=
__METRICS_GATE_FUNCTION__
metrics_registry_role_pids() { return 2; }
node() { return 0; }
wait_for_metrics_registry gateway-1 123456789
'@.Replace('__METRICS_GATE_FUNCTION__', $metricsGateFunction)
    & $bash.Source -c $metricsMapperFailureHarness 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'Performance topology PID mapper failure was swallowed by the registry gate' }

    & $bash.Source ((Join-Path $operationsRoot 'install-launchd-service.sh') -replace '\\', '/') --dry-run --scope user --base-dir '/tmp/juhe-ai|unsafe' --label 'com.example.juhe-ai' 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'launchd installer accepted a sed-unsafe base path' }
    & $bash.Source ((Join-Path $operationsRoot 'install-launchd-service.sh') -replace '\\', '/') --dry-run --scope user --base-dir '/tmp/juhe-ai$(id)' --label 'com.example.juhe-ai' 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'launchd installer accepted command substitution syntax in the generated run script path' }
    & $bash.Source ((Join-Path $operationsRoot 'install-launchd-service.sh') -replace '\\', '/') --dry-run --scope user --base-dir '/tmp/juhe-ai`id`' --label 'com.example.juhe-ai' 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'launchd installer accepted backtick substitution syntax in the generated run script path' }
    & $bash.Source ((Join-Path $operationsRoot 'manage-sing-box.sh') -replace '\\', '/') existing --dry-run --config '/tmp/sing-box-config.json' --log-dir '/tmp/juhe-ai-logs'
    if ($LASTEXITCODE -ne 0) { throw 'sing-box manager dry-run failed' }
    & $bash.Source ((Join-Path $operationsRoot 'manage-sing-box.sh') -replace '\\', '/') launchd --dry-run --config 'relative/config.json' 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'sing-box launchd accepted a relative config path' }
    & $bash.Source ((Join-Path $operationsRoot 'manage-sing-box.sh') -replace '\\', '/') launchd --dry-run --label 'bad/label' 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'sing-box launchd accepted an invalid launchd label' }
    & $bash.Source ((Join-Path $operationsRoot 'manage-sing-box.sh') -replace '\\', '/') launchd --dry-run --config '/tmp/config.json' --log-dir 'relative/logs' 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'sing-box launchd accepted a relative log directory' }
    & $bash.Source ((Join-Path $operationsRoot 'install-launchd-service.sh') -replace '\\', '/') --apply --scope user --base-dir '/tmp/juhe-ai-ops-test' --label 'com.example.juhe-ai' 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'launchd installer apply did not require a local health target' }
    & $bash.Source ((Join-Path $operationsRoot 'temporary-cutover.sh') -replace '\\', '/') --action takeover --dry-run --switch-script '/tmp/switch-upstream.sh' --main-release '/tmp/releases/main/juhe-ai-release' --main-pid 101 --main-port 3000 --temporary-release '/tmp/temporary/releases/candidate/juhe-ai-release' --temporary-pid 202 --temporary-port 3100
    if ($LASTEXITCODE -ne 0) { throw 'temporary cutover dry-run failed' }

    & $bash.Source ((Join-Path $operationsRoot 'temporary-cutover.sh') -replace '\\', '/') --action takeover --dry-run --switch-script '/tmp/switch-upstream.sh' --main-release '/tmp/releases/main/juhe-ai-release' --main-pid 101 --main-port 3000 --temporary-release '/tmp/temporary/releases/candidate/juhe-ai-release' --temporary-pid 101 --temporary-port 3100 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'temporary cutover accepted the same PID for main and temporary services' }

    & $bash.Source ((Join-Path $operationsRoot 'temporary-cutover.sh') -replace '\\', '/') --action takeover --dry-run --switch-script '/tmp/switch-upstream.sh' --main-release '/tmp/releases/main/juhe-ai-release' --main-pid 101 --main-port 3000 --temporary-release '/tmp/temporary/releases/candidate/juhe-ai-release' --temporary-pid 202 --temporary-port 3000 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'temporary cutover accepted the same port for main and temporary services' }

    & $bash.Source ((Join-Path $operationsRoot 'temporary-cutover.sh') -replace '\\', '/') --action takeover --dry-run --switch-script '/tmp/switch-upstream.sh' --main-release '/tmp/releases/main/juhe-ai-release' --main-pid 101 --main-port 3000 --temporary-release '/tmp/temporary/releases/candidate/juhe-ai-release' --temporary-pid 202 --temporary-port 3100 --route-header-name 'X-Test.*' 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'temporary cutover accepted a non-token route header name' }

    $mockHarness = @'
set -euo pipefail
operations_root="$1"
root="$(mktemp -d)"
trap 'rm -rf -- "$root"' EXIT
fake_bin="$root/fake-bin"
mkdir -p "$fake_bin"
cat > "$root/fake-command.c" <<'EOF'
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static const char *base_name(const char *path) {
  const char *slash = strrchr(path, '/');
  const char *backslash = strrchr(path, '\\');
  const char *base = slash && (!backslash || slash > backslash) ? slash + 1 : (backslash ? backslash + 1 : path);
  return base;
}

static int old_plist_present(void) {
  const char *path = getenv("FAKE_PLIST_PATH");
  char line[32];
  if (!path || !*path) return 0;
  FILE *file = fopen(path, "rb");
  if (!file) return 0;
  int old = fgets(line, sizeof line, file) && strncmp(line, "OLD_PLIST", 9) == 0;
  fclose(file);
  return old;
}

static int file_exists(const char *path) {
  FILE *file = path ? fopen(path, "rb") : NULL;
  if (!file) return 0;
  fclose(file);
  return 1;
}

static int file_is_old(const char *path) {
  char line[32];
  FILE *file = path ? fopen(path, "rb") : NULL;
  if (!file) return 0;
  int old = fgets(line, sizeof line, file) && strncmp(line, "OLD_PLIST", 9) == 0;
  fclose(file);
  return old;
}

int main(int argc, char **argv) {
  const char *name = base_name(argv[0]);
  char command[32];
  snprintf(command, sizeof command, "%s", name);
  char *dot = strrchr(command, '.');
  if (dot && strcmp(dot, ".exe") == 0) *dot = '\0';
  if (strcmp(command, "lsof") == 0) {
    const char *mode = getenv("FAKE_LISTENER_MODE");
    const char *phase = getenv("FAKE_PHASE");
    if (old_plist_present() || (mode && strcmp(mode, "none") == 0) || (phase && strcmp(phase, "listener") == 0)) return 1;
    int pid_only = 0;
    for (int i = 1; i < argc; i++) if (strstr(argv[i], "-tiTCP:") == argv[i]) pid_only = 1;
    if (pid_only) {
      if (mode && strcmp(mode, "multiple") == 0) fputs("4242\n4343\n", stdout); else fputs("4242\n", stdout);
    } else {
      fputs("p4242\n", stdout);
      if (mode && strcmp(mode, "public") == 0) fputs("n*:7890\n", stdout); else fputs("n127.0.0.1:7890\n", stdout);
    }
    return 0;
  }
  if (strcmp(command, "ps") == 0) {
    const char *process = getenv("FAKE_PROCESS_NAME");
    puts(process && *process ? process : "/opt/homebrew/bin/sing-box");
    return 0;
  }
  if (strcmp(command, "curl") == 0) {
    const char *log_path = getenv("FAKE_CURL_LOG");
    if (log_path && *log_path) {
      FILE *log = fopen(log_path, "ab");
      if (log) {
        for (int i = 1; i < argc; i++) fprintf(log, "%s%s", i == 1 ? "" : " ", argv[i]);
        fputc('\n', log);
        fclose(log);
      }
    }
    const char *phase = getenv("FAKE_PHASE");
    if (phase && (strcmp(phase, "probe") == 0 || strcmp(phase, "health") == 0)) return 7;
    fputs("204", stdout);
    return 0;
  }
  if (strcmp(command, "plutil") == 0 || strcmp(command, "sleep") == 0 || strcmp(command, "sing-box") == 0) return 0;
  if (strcmp(command, "launchctl") == 0) {
    const char *operation = argc > 1 ? argv[1] : "";
    const char *state = getenv("FAKE_LOADED_STATE");
    const char *plist = getenv("FAKE_PLIST_PATH");
    const char *phase = getenv("FAKE_PHASE");
    if (strcmp(operation, "print") == 0) return file_exists(state) ? 0 : 1;
    if (strcmp(operation, "bootout") == 0) { if (state) remove(state); return 0; }
    if (strcmp(operation, "bootstrap") == 0) {
      const char *next_plist = argc > 3 ? argv[3] : NULL;
      if (!file_is_old(next_plist) && phase && strcmp(phase, "bootstrap") == 0) return 9;
      FILE *file = state ? fopen(state, "wb") : NULL;
      if (!file) return 8;
      fclose(file);
      return 0;
    }
    if (strcmp(operation, "kickstart") == 0) {
      if (!file_is_old(plist) && phase && strcmp(phase, "kickstart") == 0) return 10;
      return 0;
    }
    return 2;
  }
  return 2;
}
EOF
gcc "$root/fake-command.c" -O2 -o "$fake_bin/fake.exe"
cp "$fake_bin/fake.exe" "$fake_bin/lsof.exe"
cp "$fake_bin/fake.exe" "$fake_bin/ps.exe"
cp "$fake_bin/fake.exe" "$fake_bin/curl.exe"
cp "$fake_bin/fake.exe" "$fake_bin/plutil.exe"
cp "$fake_bin/fake.exe" "$fake_bin/sleep.exe"
cp "$fake_bin/fake.exe" "$fake_bin/launchctl.exe"
cp "$fake_bin/fake.exe" "$fake_bin/sing-box.exe"
cp "$fake_bin/fake.exe" "$fake_bin/lsof"
cp "$fake_bin/fake.exe" "$fake_bin/ps"
cp "$fake_bin/fake.exe" "$fake_bin/curl"
cp "$fake_bin/fake.exe" "$fake_bin/plutil"
cp "$fake_bin/fake.exe" "$fake_bin/sleep"
cp "$fake_bin/fake.exe" "$fake_bin/launchctl"
cp "$fake_bin/fake.exe" "$fake_bin/sing-box"
chmod +x "$fake_bin"/*
export PATH="$fake_bin:$PATH"
shell="$(command -v bash)"
export FAKE_CURL_LOG="$root/curl.log"
: > "$FAKE_CURL_LOG"

manager="$operations_root/manage-sing-box.sh"
env -i PATH="$PATH" HOME="$HOME" FAKE_CURL_LOG="$FAKE_CURL_LOG" FAKE_LISTENER_MODE=loopback FAKE_PROCESS_NAME=/opt/homebrew/bin/sing-box "$shell" "$manager" existing --apply --config /tmp/config.json --log-dir /tmp/logs --probe-url https://example.com/ping
if FAKE_LISTENER_MODE=public "$shell" "$manager" existing --apply --config /tmp/config.json --log-dir /tmp/logs --probe-url https://example.com/ping >/dev/null 2>&1; then echo 'public listener was adopted' >&2; exit 20; fi
if FAKE_LISTENER_MODE=loopback FAKE_PROCESS_NAME=/usr/bin/python3 "$shell" "$manager" existing --apply --config /tmp/config.json --log-dir /tmp/logs --probe-url https://example.com/ping >/dev/null 2>&1; then echo 'non-sing-box listener was adopted' >&2; exit 21; fi
if FAKE_LISTENER_MODE=loopback FAKE_PHASE=probe "$shell" "$manager" existing --apply --config /tmp/config.json --log-dir /tmp/logs --probe-url https://example.com/ping >/dev/null 2>&1; then echo 'failed proxy probe was adopted' >&2; exit 22; fi

for phase in bootstrap kickstart listener probe; do
  case_root="$root/sing-box-$phase"
  export HOME="$case_root/home"
  export FAKE_PHASE="$phase"
  export FAKE_LISTENER_MODE=loopback
  export FAKE_PROCESS_NAME=/opt/homebrew/bin/sing-box
  export FAKE_LOADED_STATE="$case_root/loaded"
  export FAKE_PLIST_PATH="$HOME/Library/LaunchAgents/com.example.juhe-ai.sing-box.plist"
  config="$case_root/config.json"
  mkdir -p "$(dirname "$FAKE_PLIST_PATH")" "$case_root/logs"
  printf 'OLD_PLIST\n' > "$FAKE_PLIST_PATH"
  printf '{}\n' > "$config"
  touch "$FAKE_LOADED_STATE"
  if "$shell" "$manager" launchd --apply --binary /bin/true --config "$config" --log-dir "$case_root/logs" --probe-url https://example.com/ping >/dev/null 2>&1; then
    echo "sing-box launchd unexpectedly succeeded during $phase failure" >&2
    exit 30
  fi
  grep -qx OLD_PLIST "$FAKE_PLIST_PATH"
  [ -f "$FAKE_LOADED_STATE" ]
  if find "$(dirname "$FAKE_PLIST_PATH")" -name '*.backup.*' -o -name '*.tmp.*' | grep -q .; then
    echo "sing-box launchd left temporary files after $phase rollback" >&2
    exit 31
  fi
done

installer="$operations_root/install-launchd-service.sh"
case_root="$root/main-health"
export HOME="$case_root/home"
export FAKE_PHASE=health
export FAKE_LOADED_STATE="$case_root/loaded"
export FAKE_PLIST_PATH="$HOME/Library/LaunchAgents/com.example.juhe-ai.plist"
base="$case_root/base"
mkdir -p "$(dirname "$FAKE_PLIST_PATH")" "$base/current" "$base/bin"
printf 'OLD_PLIST\n' > "$FAKE_PLIST_PATH"
printf 'OLD_RUN\n' > "$base/bin/run.sh"
printf '#!/usr/bin/env bash\n' > "$base/current/start.sh"
touch "$FAKE_LOADED_STATE"
if "$shell" "$installer" --apply --scope user --base-dir "$base" --label com.example.juhe-ai --health-port 3000 >/dev/null 2>&1; then
  echo 'main launchd unexpectedly succeeded while both health endpoints failed' >&2
  exit 40
fi
grep -qx OLD_PLIST "$FAKE_PLIST_PATH"
grep -qx OLD_RUN "$base/bin/run.sh"
[ -f "$FAKE_LOADED_STATE" ]

case_root="$root/main-success"
export HOME="$case_root/home"
export FAKE_PHASE=''
export FAKE_LOADED_STATE="$case_root/loaded"
export FAKE_PLIST_PATH="$HOME/Library/LaunchAgents/com.example.juhe-ai.plist"
export FAKE_CURL_LOG="$case_root/curl.log"
base="$case_root/base"
mkdir -p "$(dirname "$FAKE_PLIST_PATH")" "$base/current" "$base/bin"
: > "$FAKE_CURL_LOG"
printf 'OLD_PLIST\n' > "$FAKE_PLIST_PATH"
printf 'OLD_RUN\n' > "$base/bin/run.sh"
printf '#!/usr/bin/env bash\n' > "$base/current/start.sh"
touch "$FAKE_LOADED_STATE"
"$shell" "$installer" --apply --scope user --base-dir "$base" --label com.example.juhe-ai --health-port 3000 >/dev/null
[ "$(grep -c '/__aisys__/health' "$FAKE_CURL_LOG")" -ge 3 ]
[ "$(grep -c '/__aisys__/api/health' "$FAKE_CURL_LOG")" -ge 3 ]
printf 'macOS executable failure simulations passed\n'
'@
    $unixName = (& $bash.Source -c 'uname -s').Trim()
    $gcc = Get-Command gcc -ErrorAction SilentlyContinue
    if ($unixName -in @('Darwin', 'Linux') -and $gcc) {
      & $bash.Source -c $mockHarness bash ($operationsRoot -replace '\\', '/')
      if ($LASTEXITCODE -ne 0) { throw 'macOS operation executable failure simulation failed' }
    } else {
      Write-Warning "Executable launchctl/lsof/curl simulations require Darwin/Linux bash and gcc; current shell reports $unixName. Static, syntax, negative-argument and dry-run gates still ran."
    }
  } finally {
    Pop-Location
  }
} else {
  Write-Warning 'bash is unavailable; shell syntax and dry-run execution were skipped'
}

Write-Host 'macOS operations static and dry-run validation passed'

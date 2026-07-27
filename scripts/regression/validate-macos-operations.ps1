$ErrorActionPreference = 'Stop'

$releaseStartScript = Get-Content -LiteralPath (Join-Path $PSScriptRoot '../../deploy/start.sh') -Raw
if ($releaseStartScript -match '\$\{[^}]+,,\}') {
  throw 'deploy/start.sh must remain compatible with macOS system Bash 3.2'
}
Set-StrictMode -Version Latest

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$operationsRoot = Join-Path $repoRoot 'docs\deploy\macos\operations'
$requiredFiles = @(
  'README.md',
  'install-launchd-service.sh',
  'install-performance-topology.sh',
  'manage-sing-box.sh',
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
$healthCheckIndex = $launchdInstaller.LastIndexOf('wait_for_main_health', [StringComparison]::Ordinal)
$healthStableIndex = $launchdInstaller.LastIndexOf('INSTALL_MUTATED=0', [StringComparison]::Ordinal)
if ($healthCheckIndex -lt 0 -or $healthStableIndex -lt 0 -or $healthCheckIndex -gt $healthStableIndex) {
  throw 'Launchd installer must verify stable local health before marking installation complete'
}

$performanceInstaller = Get-Content -Raw -LiteralPath (Join-Path $operationsRoot 'install-performance-topology.sh')
foreach ($contract in @('--dry-run', '--apply', 'GATEWAY_COUNT=3', 'USAGE_WORKERS=2', 'LOG_WORKERS=2', 'least_conn', 'JUHE_AI_PERFORMANCE_NODE_ROLE', 'JUHE_AI_ACCOUNT_HEALTH_CHECK_DISPATCH_URL', 'location ^~ /__aiinternal__/', 'activation_service_names', 'wait_for_health', 'wait_for_metrics_registry', 'performance_metrics_registry_time_ms', 'metrics_registry_role_pids', 'VERIFIED_HEALTH_JSON', 'VERIFIED_GATEWAY_METRICS_ROLE_PIDS', 'health.processPid', 'health.dbServicePid', 'worker.replicaIndex + 1', '--print-redis-time-ms', '--observed-after-ms', '--role-pid', 'check-performance-process-metrics-registry.js', 'health_identity_matches', '/__aisys__/api/health', 'nginx -t', 'rollback')) {
  if (-not $performanceInstaller.Contains($contract, [StringComparison]::Ordinal)) { throw "Performance topology installer contract missing: $contract" }
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
$activationHealthCheck = $performanceInstaller.IndexOf('  wait_for_health "$name"', $activationLoopStart, [StringComparison]::Ordinal)
$activationRegistryCheck = $performanceInstaller.IndexOf('  wait_for_metrics_registry "$name"', $activationHealthCheck, [StringComparison]::Ordinal)
$activationControlLast = $performanceInstaller.IndexOf("  printf '%s\n' control-1", $activationFunctionStart, [StringComparison]::Ordinal)
$activationLoopEnd = $performanceInstaller.IndexOf("`ndone", $activationRegistryCheck, [StringComparison]::Ordinal)
$activationFence = $performanceInstaller.IndexOf('  metrics_fence_ms="$(performance_metrics_registry_time_ms)"', $activationLoopStart, [StringComparison]::Ordinal)
$activationBootout = $performanceInstaller.IndexOf('  launchctl bootout "$DOMAIN" "$plist"', $activationLoopStart, [StringComparison]::Ordinal)
$activationBootstrap = $performanceInstaller.IndexOf('  launchctl bootstrap "$DOMAIN" "$plist"', $activationLoopStart, [StringComparison]::Ordinal)
$activationKickstart = $performanceInstaller.IndexOf('  launchctl kickstart -k "$DOMAIN/$(service_label "$name")"', $activationBootstrap, [StringComparison]::Ordinal)
if ($activationFunctionStart -lt 0 -or $activationFunctionEnd -lt 0 -or $activationLoopStart -lt 0 -or $activationGatewayLoop -lt 0 -or $activationGatewayLoopEnd -lt $activationGatewayLoop -or $activationControlWithinFunction -lt $activationGatewayLoopEnd -or $activationBootout -lt $activationLoopStart -or $activationFence -lt $activationBootout -or $activationBootstrap -lt $activationFence -or $activationKickstart -lt $activationBootstrap -or $activationHealthCheck -lt $activationKickstart -or $activationRegistryCheck -lt $activationHealthCheck -or $activationLoopEnd -lt $activationRegistryCheck -or $activationControlLast -lt $activationFunctionStart -or $activationControlLast -gt $activationLoopStart) {
  throw 'Performance topology must activate and verify gateway publishers before restarting control/workers'
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
$metricsGateFunctionStart = $performanceInstaller.IndexOf('wait_for_metrics_registry() {', [StringComparison]::Ordinal)
$metricsRolePidFunctionStart = $performanceInstaller.IndexOf('metrics_registry_role_pids() {', $metricsGateFunctionStart, [StringComparison]::Ordinal)
$metricsGateFunction = $performanceInstaller.Substring($metricsGateFunctionStart, $metricsRolePidFunctionStart - $metricsGateFunctionStart)
if ($metricsGateFunction -notmatch '(?m)^  current_role_pid_lines="\$\(metrics_registry_role_pids "\$VERIFIED_HEALTH_JSON"\)"$') {
  throw 'Performance topology must propagate PID mapping helper failures without a fallback suffix'
}
if (-not $metricsGateFunction.Contains('role_pid_lines="$VERIFIED_GATEWAY_METRICS_ROLE_PIDS', [StringComparison]::Ordinal) -or $metricsGateFunction -notmatch 'VERIFIED_GATEWAY_METRICS_ROLE_PIDS="\$VERIFIED_GATEWAY_METRICS_ROLE_PIDS\r?\n\$current_role_pid_lines"') {
  throw 'Performance topology final control gate must reuse the verified PID mappings from every gateway activation'
}
$performanceHealthIndex = $performanceInstaller.LastIndexOf('for name in $(service_names); do wait_for_health', [StringComparison]::Ordinal)
$performanceNginxIndex = $performanceInstaller.LastIndexOf('nginx -s reload', [StringComparison]::Ordinal)
if ($performanceHealthIndex -lt 0 -or $performanceNginxIndex -lt 0 -or $performanceHealthIndex -gt $performanceNginxIndex) {
  throw 'Performance topology must verify every Node service before switching nginx'
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
    & $bash.Source ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/') --dry-run --scope user --base-dir '/tmp/juhe-ai-performance-test' --label-prefix 'com.example.juhe-ai.performance' --nginx-config '/tmp/juhe-ai-performance-test/nginx.conf'
    if ($LASTEXITCODE -ne 0) { throw 'performance topology installer dry-run failed' }
    & $bash.Source ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/') --dry-run --scope user --base-dir '/tmp/juhe-ai-performance-test' --control-port 3102 --gateway-base-port 3101 --gateway-count 3 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'performance topology installer accepted overlapping control and gateway ports' }

    $metricsGateHarness = @'
set -euo pipefail
CURRENT_DIR=/tmp/juhe-ai-performance-harness
GATEWAY_COUNT=3
USAGE_WORKERS=2
LOG_WORKERS=2
VERIFIED_HEALTH_JSON=
VERIFIED_GATEWAY_METRICS_ROLE_PIDS=
HARNESS_ROOT="$(mktemp -d)"
trap 'rm -rf -- "$HARNESS_ROOT"' EXIT
__METRICS_GATE_FUNCTION__
metrics_registry_role_pids() {
  case "$1" in
    gateway-1) printf '%s\n' 'gateway:gateway-1=101' 'db-service:gateway-1=201' ;;
    gateway-2) printf '%s\n' 'gateway:gateway-2=102' 'db-service:gateway-2=202' ;;
    gateway-3) printf '%s\n' 'gateway:gateway-3=103' 'db-service:gateway-3=203' ;;
    control-1) printf '%s\n' 'control:control-1=104' 'db-service:control-1=204' 'usage-worker:1=301' 'usage-worker:2=302' 'log-worker:1=401' 'log-worker:2=402' 'stats-worker:1=501' 'ops-worker:1=601' ;;
    *) return 2 ;;
  esac
}
node() { printf '%s\n' "$@" > "$HARNESS_ROOT/$VERIFIED_HEALTH_JSON.args"; }
for instance in gateway-1 gateway-2 gateway-3 control-1; do
  VERIFIED_HEALTH_JSON="$instance"
  wait_for_metrics_registry "$instance" 123456789
done
[ "$(rg -c -- '--role-pid' "$HARNESS_ROOT/control-1.args")" -eq 14 ]
for mapping in 'gateway:gateway-1=101' 'db-service:gateway-1=201' 'gateway:gateway-2=102' 'db-service:gateway-2=202' 'gateway:gateway-3=103' 'db-service:gateway-3=203'; do
  rg -qx -- "$mapping" "$HARNESS_ROOT/control-1.args"
done
'@.Replace('__METRICS_GATE_FUNCTION__', $metricsGateFunction)
    & $bash.Source -c $metricsGateHarness
    if ($LASTEXITCODE -ne 0) { throw 'Performance topology PID accumulation executable harness failed' }

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

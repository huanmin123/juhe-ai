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
  'wait-performance-slot-drain.sh',
  'retire-performance-slot.sh',
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
foreach ($contract in @('--dry-run', '--apply', '--slot main|temporary', '--release-dir', '--nginx-main-config', '--nginx-config-kind', '--service-user', '--service-group', '--deployment-lock-library', '--drain-script', '--model-readiness-key-file', '--skip-model-readiness-for-non-business-test', 'model-catalog-readiness.js', 'assert_model_readiness_key_file', 'verify_model_catalog', 'verify_ingress_route', 'ROLLBACK_SLOT', 'rollback_proven', 'AUDIT_BLOB_DIR="$BASE_DIR/shared/audit/blobs"', 'JUHE_AI_AUDIT_BLOB_ROOT', 'GATEWAY_COUNT=3', 'USAGE_WORKERS=2', 'LOG_WORKERS=2', 'least_conn', 'JUHE_AI_PERFORMANCE_NODE_ROLE', 'service_instance_id', 'assert_existing_slot_is_inactive_and_drained', 'location ^~ /__aiinternal__', 'X-Juhe-Active-Upstream performance', 'wait_for_health', 'wait_for_ingress', 'health_identity_matches', '/__aisys__/api/health', 'nginx_test', 'nginx_reload', 'acquire_deployment_lock', 'release_deployment_lock', '<key>UserName</key>', '<key>GroupName</key>', 'rollback')) {
  if (-not $performanceInstaller.Contains($contract, [StringComparison]::Ordinal)) { throw "Performance topology installer contract missing: $contract" }
}
$performanceHealthIndex = $performanceInstaller.LastIndexOf('for name in $(service_names); do wait_for_health', [StringComparison]::Ordinal)
$performanceNginxIndex = $performanceInstaller.LastIndexOf("nginx_reload`n", [StringComparison]::Ordinal)
if ($performanceHealthIndex -lt 0 -or $performanceNginxIndex -lt 0 -or $performanceHealthIndex -gt $performanceNginxIndex) {
  throw 'Performance topology must verify every Node service before switching nginx'
}
$performanceDirectReadinessIndex = $performanceInstaller.LastIndexOf('gateway-*) verify_model_catalog', [StringComparison]::Ordinal)
$performanceIngressReadinessIndex = $performanceInstaller.LastIndexOf('verify_model_catalog "http://127.0.0.1:$INGRESS_PORT"', [StringComparison]::Ordinal)
$performanceCommitIndex = $performanceInstaller.LastIndexOf('MUTATED=0', [StringComparison]::Ordinal)
if ($performanceDirectReadinessIndex -lt $performanceHealthIndex -or $performanceDirectReadinessIndex -gt $performanceNginxIndex) {
  throw 'Performance topology must probe every candidate Gateway model catalog before switching nginx'
}
if ($performanceIngressReadinessIndex -lt $performanceNginxIndex -or $performanceIngressReadinessIndex -gt $performanceCommitIndex) {
  throw 'Performance topology must prove ingress model readiness before committing launchd/nginx changes'
}
$rollbackRouteProofIndex = $performanceInstaller.IndexOf('&& verify_ingress_route "$ROLLBACK_SLOT"', [StringComparison]::Ordinal)
$rollbackReadinessProofIndex = $performanceInstaller.IndexOf('&& verify_model_catalog "http://127.0.0.1:$INGRESS_PORT"', [StringComparison]::Ordinal)
$rollbackCandidateBootoutIndex = $performanceInstaller.IndexOf('failed to boot out candidate service after rollback proof', [StringComparison]::Ordinal)
if ($rollbackRouteProofIndex -lt 0 -or $rollbackReadinessProofIndex -lt $rollbackRouteProofIndex -or $rollbackCandidateBootoutIndex -lt $rollbackReadinessProofIndex) {
  throw 'Performance topology rollback must prove prior ingress identity and model readiness before booting out the candidate'
}
$performanceDrain = Get-Content -Raw -LiteralPath (Join-Path $operationsRoot 'wait-performance-slot-drain.sh')
foreach ($contract in @('--slot main|temporary|standalone', 'X-Juhe-Active-Upstream', 'assert_lsof_capability', 'lsof -nP -a -c nginx', 'did not explicitly identify another active performance slot', 'PERFORMANCE_SLOT_DRAINED', 'required_stable_zero=3')) {
  if (-not $performanceDrain.Contains($contract, [StringComparison]::Ordinal)) { throw "Performance slot drain contract missing: $contract" }
}
foreach ($mutation in @('launchctl bootstrap', 'launchctl bootout', 'nginx -s reload', 'rm -rf', 'mv -f')) {
  if ($performanceDrain.Contains($mutation, [StringComparison]::OrdinalIgnoreCase)) { throw "Performance slot drain must remain read-only: $mutation" }
}
$performanceRetire = Get-Content -Raw -LiteralPath (Join-Path $operationsRoot 'retire-performance-slot.sh')
foreach ($contract in @('--dry-run', '--apply', '--slot main|temporary', 'wait-performance-slot-drain.sh', 'launchctl print "$DOMAIN"', 'failed to boot out slot service', 'acquire_deployment_lock', 'PERFORMANCE_SLOT_RETIRED', 'rollback')) {
  if (-not $performanceRetire.Contains($contract, [StringComparison]::Ordinal)) { throw "Performance slot retire contract missing: $contract" }
}

$cutover = Get-Content -Raw -LiteralPath (Join-Path $operationsRoot 'temporary-cutover.sh')
foreach ($contract in @('assert_pid_cwd_port_health', 'API_HEALTH_PATH', 'rollback_target', "trap 'on_exit", '--dry-run', '--apply', 'MAIN_HEADER_VALUE=performance-main', 'TEMP_HEADER_VALUE=performance-temporary', '--model-readiness-key-file', '--model-readiness-runner', '--model-readiness-ingress-base-url', '--main-model-base-url', '--temporary-model-base-url', '--skip-model-readiness-for-non-business-test', 'assert_model_readiness_key_file', 'assert_loopback_model_base_url', 'verify_model_catalog')) {
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
$directMainBusinessProof = $cutover.LastIndexOf('verify_model_catalog "$MAIN_MODEL_BASE_URL"', [StringComparison]::Ordinal)
$directTemporaryBusinessProof = $cutover.LastIndexOf('verify_model_catalog "$TEMP_MODEL_BASE_URL"', [StringComparison]::Ordinal)
$rollbackBusinessProof = $cutover.LastIndexOf('verify_model_catalog "$MODEL_READINESS_INGRESS_BASE_URL"', $attemptMarker, [StringComparison]::Ordinal)
$targetBusinessProof = $cutover.LastIndexOf('verify_model_catalog "$MODEL_READINESS_INGRESS_BASE_URL"', [StringComparison]::Ordinal)
$switchCommitted = $cutover.LastIndexOf('SWITCH_ATTEMPTED=0', [StringComparison]::Ordinal)
if ($directMainBusinessProof -lt 0 -or $directTemporaryBusinessProof -lt $directMainBusinessProof -or $directTemporaryBusinessProof -gt $rollbackProof) {
  throw 'Temporary cutover must use the explicit new runner for both direct slot readiness URLs before checking ingress'
}
if ($rollbackBusinessProof -lt $rollbackProof -or $rollbackBusinessProof -gt $attemptMarker) {
  throw 'Temporary cutover must prove rollback-target model readiness before attempting a switch'
}
if ($targetBusinessProof -lt $adapterInvocation -or $targetBusinessProof -gt $switchCommitted) {
  throw 'Temporary cutover must prove target ingress model readiness before committing the switch'
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
$gnuBash = $null
foreach ($candidate in @(Get-Command bash -All -ErrorAction SilentlyContinue)) {
  $versionOutput = @(& $candidate.Source --version 2>$null)
  $versionExit = $LASTEXITCODE
  if ($versionExit -eq 0 -and ($versionOutput -join "`n") -match 'GNU bash') {
    $gnuBash = $candidate
    break
  }
}
if ($bash) {
  Push-Location $repoRoot
  try {
    foreach ($script in Get-ChildItem -LiteralPath $operationsRoot -Filter '*.sh') {
      & $bash.Source -n ($script.FullName -replace '\\', '/')
      if ($LASTEXITCODE -ne 0) { throw "bash -n failed: $($script.Name)" }
    }
    & $bash.Source ((Join-Path $operationsRoot 'install-launchd-service.sh') -replace '\\', '/') --dry-run --scope user --base-dir '/tmp/juhe-ai-ops-test' --label 'com.example.juhe-ai'
    if ($LASTEXITCODE -ne 0) { throw 'launchd installer dry-run failed' }
    & $bash.Source ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/') --dry-run --scope user --base-dir '/tmp/juhe-ai-performance-test' --label-prefix 'com.example.juhe-ai.performance' --nginx-config '/tmp/juhe-ai-performance-test/nginx.conf' --skip-model-readiness-for-non-business-test
    if ($LASTEXITCODE -ne 0) { throw 'performance topology installer dry-run failed' }
    & $bash.Source ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/') --dry-run --scope user --base-dir '/tmp/juhe-ai-performance-test' 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'performance topology installer did not require a model readiness key file or explicit non-business skip' }
    & $bash.Source ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/') --dry-run --scope user --base-dir '/tmp/juhe-ai-performance-test' --control-port 3102 --gateway-base-port 3101 --gateway-count 3 --skip-model-readiness-for-non-business-test 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'performance topology installer accepted overlapping control and gateway ports' }
    & $bash.Source ((Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/') --dry-run --scope system --base-dir '/tmp/juhe-ai-performance-test' --nginx-config-kind main --nginx-config '/tmp/juhe-ai-performance-test/nginx.conf' --service-user huanmin --service-group staff --skip-model-readiness-for-non-business-test
    if ($LASTEXITCODE -ne 0) { throw 'performance topology installer rejected the system/main-config production contract' }
    & $bash.Source ((Join-Path $operationsRoot 'wait-performance-slot-drain.sh') -replace '\\', '/') --check --slot invalid --control-port 3200 --gateway-base-port 3211 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'performance slot drain accepted an invalid slot' }
    & $bash.Source ((Join-Path $operationsRoot 'retire-performance-slot.sh') -replace '\\', '/') --dry-run --scope system --slot temporary --base-dir '/tmp/juhe-ai-performance-test' --drain-script '/tmp/wait-performance-slot-drain.sh'
    if ($LASTEXITCODE -ne 0) { throw 'performance slot retire dry-run failed' }
    $renderNginx = @'
set -euo pipefail
installer="$1"
slot="$2"
control_port="$3"
gateway_base_port="$4"
source <(sed -n '/^render_nginx_http_body()/,/^wait_for_health()/p' "$installer" | sed '$d')
NGINX_CONFIG_KIND=main
BASE_DIR=/tmp/juhe-ai-performance-render
LOG_DIR=/tmp/juhe-ai-performance-render/logs
GATEWAY_COUNT=3
GATEWAY_BASE_PORT="$gateway_base_port"
CONTROL_PORT="$control_port"
INGRESS_PORT=3099
SLOT="$slot"
render_nginx /dev/fd/1
'@
    $installerForBash = (Join-Path $operationsRoot 'install-performance-topology.sh') -replace '\\', '/'
    $mainNginx = (& $bash.Source -c $renderNginx bash $installerForBash main 3200 3211) -join "`n"
    if ($LASTEXITCODE -ne 0) { throw 'main slot nginx rendering failed' }
    $temporaryNginx = (& $bash.Source -c $renderNginx bash $installerForBash temporary 3300 3311) -join "`n"
    if ($LASTEXITCODE -ne 0) { throw 'temporary slot nginx rendering failed' }
    foreach ($contract in @('worker_processes auto;', 'listen 127.0.0.1:3099;', 'server 127.0.0.1:3200;', 'server 127.0.0.1:3211 ', 'server 127.0.0.1:3212 ', 'server 127.0.0.1:3213 ', 'X-Juhe-Active-Upstream performance-main', 'location ^~ /__aisys__', 'location / {', 'least_conn;')) {
      if (-not $mainNginx.Contains($contract, [StringComparison]::Ordinal)) { throw "rendered main nginx contract missing: $contract" }
    }
    foreach ($contract in @('server 127.0.0.1:3300;', 'server 127.0.0.1:3311 ', 'server 127.0.0.1:3312 ', 'server 127.0.0.1:3313 ', 'X-Juhe-Active-Upstream performance-temporary')) {
      if (-not $temporaryNginx.Contains($contract, [StringComparison]::Ordinal)) { throw "rendered temporary nginx contract missing: $contract" }
    }
    if ($mainNginx.Contains('127.0.0.1:33', [StringComparison]::Ordinal) -or $temporaryNginx.Contains('127.0.0.1:32', [StringComparison]::Ordinal)) {
      throw 'rendered nginx configurations mixed main and temporary slot ports'
    }
    if (($mainNginx.ToCharArray() | Where-Object { $_ -eq '{' }).Count -ne ($mainNginx.ToCharArray() | Where-Object { $_ -eq '}' }).Count) {
      throw 'rendered main nginx braces are unbalanced'
    }
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
    & $bash.Source ((Join-Path $operationsRoot 'temporary-cutover.sh') -replace '\\', '/') --action takeover --dry-run --switch-script '/tmp/switch-upstream.sh' --main-release '/tmp/releases/main/juhe-ai-release' --main-pid 101 --main-port 3000 --temporary-release '/tmp/temporary/releases/candidate/juhe-ai-release' --temporary-pid 202 --temporary-port 3100 --skip-model-readiness-for-non-business-test
    if ($LASTEXITCODE -ne 0) { throw 'temporary cutover dry-run failed' }
    & $bash.Source ((Join-Path $operationsRoot 'temporary-cutover.sh') -replace '\\', '/') --action takeover --dry-run --switch-script '/tmp/switch-upstream.sh' --main-release '/tmp/releases/main/juhe-ai-release' --main-pid 101 --main-port 3000 --temporary-release '/tmp/temporary/releases/candidate/juhe-ai-release' --temporary-pid 202 --temporary-port 3100 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'temporary cutover did not require a model readiness key file or explicit non-business skip' }
    & $bash.Source ((Join-Path $operationsRoot 'temporary-cutover.sh') -replace '\\', '/') --action takeover --dry-run --switch-script '/tmp/switch-upstream.sh' --main-release '/tmp/releases/main/juhe-ai-release' --main-pid 101 --main-port 3000 --temporary-release '/tmp/temporary/releases/candidate/juhe-ai-release' --temporary-pid 202 --temporary-port 3100 --model-readiness-key-file '/tmp/readiness.key' --model-readiness-runner '/tmp/model-catalog-readiness.js' --model-readiness-ingress-base-url 'https://example.com' 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'temporary cutover accepted a non-loopback model readiness URL' }

    & $bash.Source ((Join-Path $operationsRoot 'temporary-cutover.sh') -replace '\\', '/') --action takeover --dry-run --switch-script '/tmp/switch-upstream.sh' --main-release '/tmp/releases/main/juhe-ai-release' --main-pid 101 --main-port 3000 --temporary-release '/tmp/temporary/releases/candidate/juhe-ai-release' --temporary-pid 101 --temporary-port 3100 --skip-model-readiness-for-non-business-test 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'temporary cutover accepted the same PID for main and temporary services' }

    & $bash.Source ((Join-Path $operationsRoot 'temporary-cutover.sh') -replace '\\', '/') --action takeover --dry-run --switch-script '/tmp/switch-upstream.sh' --main-release '/tmp/releases/main/juhe-ai-release' --main-pid 101 --main-port 3000 --temporary-release '/tmp/temporary/releases/candidate/juhe-ai-release' --temporary-pid 202 --temporary-port 3000 --skip-model-readiness-for-non-business-test 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'temporary cutover accepted the same port for main and temporary services' }

    & $bash.Source ((Join-Path $operationsRoot 'temporary-cutover.sh') -replace '\\', '/') --action takeover --dry-run --switch-script '/tmp/switch-upstream.sh' --main-release '/tmp/releases/main/juhe-ai-release' --main-pid 101 --main-port 3000 --temporary-release '/tmp/temporary/releases/candidate/juhe-ai-release' --temporary-pid 202 --temporary-port 3100 --route-header-name 'X-Test.*' --skip-model-readiness-for-non-business-test 2>$null
    if ($LASTEXITCODE -eq 0) { throw 'temporary cutover accepted a non-token route header name' }

    $cutoverReadinessHarness = @'
set -euo pipefail
operations_root="$1"
root="$(mktemp -d /tmp/juhe-ai-cutover-readiness.XXXXXX)"
main_sleep=''
temp_sleep=''
cleanup() {
  [ -z "$main_sleep" ] || kill "$main_sleep" >/dev/null 2>&1 || true
  [ -z "$temp_sleep" ] || kill "$temp_sleep" >/dev/null 2>&1 || true
  rm -rf -- "$root"
}
trap cleanup EXIT

fake_bin="$root/fake-bin"
main_release="$root/main/juhe-ai-release"
temp_release="$root/temporary/juhe-ai-release"
route_state="$root/route.state"
switch_log="$root/switch.log"
failure_marker="$root/readiness-failed-once"
key_file="$root/readiness.key"
readiness_runner="$temp_release/backend/dist/scripts/operations/model-catalog-readiness.js"
mkdir -p "$fake_bin" "$main_release" "$(dirname "$readiness_runner")"
: > "$readiness_runner"
printf 'test-readiness-key\n' > "$key_file"
chmod 600 "$key_file"

sleep 60 & main_sleep="$!"
sleep 60 & temp_sleep="$!"
export FAKE_MAIN_PID="$main_sleep" FAKE_TEMP_PID="$temp_sleep"
export FAKE_MAIN_RELEASE="$(cd "$main_release" && pwd -P)" FAKE_TEMP_RELEASE="$(cd "$temp_release" && pwd -P)"
export FAKE_ROUTE_STATE="$route_state" FAKE_SWITCH_LOG="$switch_log" FAKE_FAILURE_MARKER="$failure_marker"
export FAKE_MAIN_MODEL_URL='http://127.0.0.1:3211' FAKE_TEMP_MODEL_URL='http://127.0.0.1:3311'
export FAKE_INGRESS_MODEL_URL='http://127.0.0.1:3099'

cat > "$fake_bin/lsof" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
args=" $* "
if [[ "$args" == *' -d cwd '* ]]; then
  pid=''
  while [ "$#" -gt 0 ]; do
    if [ "$1" = -p ]; then pid="$2"; break; fi
    shift
  done
  if [ "$pid" = "$FAKE_MAIN_PID" ]; then printf 'n%s\n' "$FAKE_MAIN_RELEASE"; else printf 'n%s\n' "$FAKE_TEMP_RELEASE"; fi
  exit 0
fi
for arg in "$@"; do
  case "$arg" in
    -tiTCP:3000) printf '%s\n' "$FAKE_MAIN_PID"; exit 0 ;;
    -tiTCP:3100) printf '%s\n' "$FAKE_TEMP_PID"; exit 0 ;;
  esac
done
exit 1
EOF
cat > "$fake_bin/ps" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' 'node backend/dist/server.js'
EOF
cat > "$fake_bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
headers=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    -D) headers="$2"; shift 2 ;;
    *) shift ;;
  esac
done
if [ -n "$headers" ]; then
  target="$(cat "$FAKE_ROUTE_STATE")"
  printf 'HTTP/1.1 200 OK\r\nX-Juhe-Active-Upstream: performance-%s\r\n\r\n' "$target" > "$headers"
fi
exit 0
EOF
cat > "$fake_bin/node" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
base_url=''
while [ "$#" -gt 0 ]; do
  if [ "$1" = --base-url ]; then base_url="$2"; shift 2; else shift; fi
done
route="$(cat "$FAKE_ROUTE_STATE")"
if [ "${FAKE_READINESS_MODE:-}" = candidate-fail ] && [ "$base_url" = "$FAKE_TEMP_MODEL_URL" ]; then exit 31; fi
if [ "${FAKE_READINESS_MODE:-}" = ingress-fail ] && [ "$base_url" = "$FAKE_INGRESS_MODEL_URL" ] \
  && [ "$route" = temporary ] && [ ! -f "$FAKE_FAILURE_MARKER" ]; then
  : > "$FAKE_FAILURE_MARKER"
  exit 32
fi
printf '%s\n' 'MODEL_CATALOG_READY models=1 latencyMs=1'
EOF
cat > "$fake_bin/stat" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "${FAKE_KEY_MODE:-600}"
EOF
cat > "$root/switch.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$1" >> "$FAKE_SWITCH_LOG"
if [ "${FAKE_SWITCH_MODE:-}" = rollback-fail ] && [ "$1" = main ]; then exit 41; fi
printf '%s\n' "$1" > "$FAKE_ROUTE_STATE"
EOF
chmod +x "$fake_bin/lsof" "$fake_bin/ps" "$fake_bin/curl" "$fake_bin/node" "$fake_bin/stat" "$root/switch.sh"
export FAKE_BIN="$fake_bin"
export PATH="$fake_bin:$PATH"
lsof() { "$FAKE_BIN/lsof" "$@"; }
ps() { "$FAKE_BIN/ps" "$@"; }
curl() { "$FAKE_BIN/curl" "$@"; }
node() { "$FAKE_BIN/node" "$@"; }
export -f lsof ps curl node

run_cutover() {
  /bin/bash "$operations_root/temporary-cutover.sh" --action takeover --apply \
    --switch-script "$root/switch.sh" \
    --main-release "$main_release" --main-pid "$FAKE_MAIN_PID" --main-port 3000 \
    --temporary-release "$temp_release" --temporary-pid "$FAKE_TEMP_PID" --temporary-port 3100 \
    --ingress-health-url 'http://127.0.0.1:3099/__aisys__/health' \
    --model-readiness-key-file "$key_file" \
    --model-readiness-runner "$readiness_runner" \
    --main-model-base-url "$FAKE_MAIN_MODEL_URL" \
    --temporary-model-base-url "$FAKE_TEMP_MODEL_URL" \
    --model-readiness-ingress-base-url "$FAKE_INGRESS_MODEL_URL"
}

printf '%s\n' main > "$route_state"
: > "$switch_log"
export FAKE_KEY_MODE=644
if run_cutover >/dev/null 2>&1; then echo 'cutover accepted a non-private readiness key file' >&2; exit 50; fi
[ ! -s "$switch_log" ] || { echo 'invalid readiness key mode invoked the switch adapter' >&2; exit 51; }
export FAKE_KEY_MODE=600

printf '%s\n' main > "$route_state"
: > "$switch_log"
export FAKE_READINESS_MODE=candidate-fail
export FAKE_SWITCH_MODE=''
if run_cutover >/dev/null 2>&1; then echo 'candidate readiness failure unexpectedly switched traffic' >&2; exit 52; fi
[ ! -s "$switch_log" ] || { echo 'candidate readiness failure invoked the switch adapter' >&2; exit 53; }
[ "$(cat "$route_state")" = main ] || { echo 'candidate readiness failure changed ingress' >&2; exit 54; }

printf '%s\n' main > "$route_state"
: > "$switch_log"
rm -f -- "$failure_marker"
export FAKE_READINESS_MODE=ingress-fail
export FAKE_SWITCH_MODE=''
if run_cutover >"$root/ingress-fail.out" 2>&1; then echo 'post-switch readiness failure unexpectedly committed' >&2; exit 54; fi
[ "$(cat "$route_state")" = main ] || { echo 'post-switch readiness failure did not restore main ingress' >&2; exit 55; }
[ "$(sed -n '1p' "$switch_log")" = temporary ] || { cat "$root/ingress-fail.out" >&2; echo "post-switch failure did not first target temporary: $(tr '\n' ',' < "$switch_log")" >&2; exit 56; }
[ "$(sed -n '2p' "$switch_log")" = main ] || { echo 'post-switch failure did not roll back to main' >&2; exit 57; }
[ "$(wc -l < "$switch_log" | tr -d ' ')" = 2 ] || { echo 'post-switch failure invoked an unexpected number of switches' >&2; exit 58; }

printf '%s\n' main > "$route_state"
: > "$switch_log"
rm -f -- "$failure_marker"
export FAKE_READINESS_MODE=ingress-fail
export FAKE_SWITCH_MODE=rollback-fail
if run_cutover >"$root/rollback-fail.out" 2>&1; then echo 'rollback adapter failure unexpectedly committed' >&2; exit 59; fi
[ "$(cat "$route_state")" = temporary ] || { echo 'failed rollback simulation did not retain the last proven candidate route' >&2; exit 60; }
[ "$(sed -n '1p' "$switch_log")" = temporary ] || { echo 'failed rollback simulation did not first target temporary' >&2; exit 61; }
[ "$(sed -n '2p' "$switch_log")" = main ] || { echo 'failed rollback simulation did not attempt main rollback' >&2; exit 62; }
[ "$(wc -l < "$switch_log" | tr -d ' ')" = 2 ] || { echo 'failed rollback simulation invoked unexpected switches' >&2; exit 63; }
[ ! -e "$main_release/backend/dist/scripts/operations/model-catalog-readiness.js" ] || { echo 'old main unexpectedly contains a readiness runner' >&2; exit 64; }

printf '%s\n' 'temporary cutover explicit-runner and rollback failure simulations passed'
'@
    if ($gnuBash) {
      & $gnuBash.Source -c $cutoverReadinessHarness bash ($operationsRoot -replace '\\', '/')
      if ($LASTEXITCODE -ne 0) { throw 'Temporary cutover model-readiness executable failure simulation failed' }
    } else {
      Write-Warning 'GNU Bash is unavailable; temporary cutover model-readiness executable failure simulations were skipped'
    }

    $drainSafetyHarness = @'
set -euo pipefail
operations_root="$1"
root="$(mktemp -d /tmp/juhe-ai-drain-safety.XXXXXX)"
trap 'rm -rf -- "$root"' EXIT
fake_bin="$root/fake-bin"
mkdir -p "$fake_bin"
export FAKE_DRAIN_SAMPLE_FILE="$root/drain.samples"
export FAKE_LAUNCHCTL_LOG="$root/launchctl.log"
: > "$FAKE_DRAIN_SAMPLE_FILE"
: > "$FAKE_LAUNCHCTL_LOG"

cat > "$fake_bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "${FAKE_CURL_MODE:-ok}" = fail ]; then echo 'simulated curl failure' >&2; exit 7; fi
printf 'HTTP/1.1 200 OK\r\nX-Juhe-Active-Upstream: performance-temporary\r\n\r\n'
EOF
cat > "$fake_bin/lsof" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "${FAKE_LSOF_MODE:-zero}" = fail ]; then echo 'simulated lsof failure' >&2; exit 9; fi
pid=''
previous=''
for argument in "$@"; do
  if [ "$previous" = -p ]; then pid="$argument"; fi
  previous="$argument"
done
case " $* " in
  *' -d cwd '*) printf 'p%s\nfcwd\nn/tmp\n' "$pid"; exit 0 ;;
  *' -sTCP:LISTEN '*) printf '%s\n' 'p123' 'f10' 'n127.0.0.1:3099'; exit 0 ;;
esac
port=''
for argument in "$@"; do
  case "$argument" in -iTCP:*) port="${argument#-iTCP:}" ;; esac
done
if [ "$port" = 3200 ]; then
  sample=0
  [ ! -s "$FAKE_DRAIN_SAMPLE_FILE" ] || sample="$(cat "$FAKE_DRAIN_SAMPLE_FILE")"
  sample=$((sample + 1))
  printf '%s\n' "$sample" > "$FAKE_DRAIN_SAMPLE_FILE"
  if [ "${FAKE_LSOF_MODE:-zero}" = transient ] && [ "$sample" -eq 2 ]; then
    printf '%s\n' 'COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME' 'nginx 123 test 10u IPv4 0t0 TCP 127.0.0.1:40000->127.0.0.1:3200 (ESTABLISHED)'
    exit 0
  fi
fi
exit 1
EOF
cat > "$fake_bin/sleep" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
cat > "$fake_bin/launchctl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FAKE_LAUNCHCTL_LOG"
exit 0
EOF
chmod +x "$fake_bin"/*
export PATH="$fake_bin:$PATH"
drain="$operations_root/wait-performance-slot-drain.sh"

export FAKE_CURL_MODE=fail FAKE_LSOF_MODE=zero
if /bin/bash "$drain" --check --slot main --control-port 3200 --gateway-base-port 3211 --gateway-count 3 --poll-seconds 1 >/dev/null 2>&1; then
  echo 'drain accepted a failed ingress curl probe' >&2
  exit 71
fi

export FAKE_CURL_MODE=ok FAKE_LSOF_MODE=fail
if /bin/bash "$drain" --check --slot main --control-port 3200 --gateway-base-port 3211 --gateway-count 3 --poll-seconds 1 >/dev/null 2>&1; then
  echo 'drain accepted a failed lsof capability probe' >&2
  exit 72
fi

: > "$FAKE_DRAIN_SAMPLE_FILE"
export FAKE_LSOF_MODE=transient
if /bin/bash "$drain" --check --slot main --control-port 3200 --gateway-base-port 3211 --gateway-count 3 --poll-seconds 1 >/dev/null 2>&1; then
  echo 'drain accepted a transient single zero-connection sample' >&2
  exit 73
fi
[ "$(cat "$FAKE_DRAIN_SAMPLE_FILE")" -ge 2 ] || { echo 'transient drain simulation did not take a second sample' >&2; exit 74; }

: > "$FAKE_DRAIN_SAMPLE_FILE"
export FAKE_LSOF_MODE=zero
/bin/bash "$drain" --check --slot main --control-port 3200 --gateway-base-port 3211 --gateway-count 3 --poll-seconds 1 >/dev/null
[ "$(cat "$FAKE_DRAIN_SAMPLE_FILE")" -ge 3 ] || { echo 'drain check did not require a stable zero window' >&2; exit 75; }

retire_root="$root/retire"
export HOME="$retire_root/home"
mkdir -p "$HOME/Library/LaunchAgents" "$retire_root/base/bin/performance/main"
for name in control-1 gateway-1 gateway-2 gateway-3; do
  : > "$HOME/Library/LaunchAgents/com.example.juhe-ai.performance.main.$name.plist"
  : > "$retire_root/base/bin/performance/main/$name.sh"
done
: > "$FAKE_LAUNCHCTL_LOG"
export FAKE_CURL_MODE=fail
if /bin/bash "$operations_root/retire-performance-slot.sh" --apply --scope user --slot main \
  --base-dir "$retire_root/base" --drain-script "$drain" >/dev/null 2>&1; then
  echo 'retirement accepted an unprovable drain state' >&2
  exit 76
fi
[ ! -s "$FAKE_LAUNCHCTL_LOG" ] || { echo 'retirement invoked launchctl after the drain proof failed' >&2; exit 77; }

printf '%s\n' 'performance drain and retirement fail-closed simulations passed'
'@
    if ($gnuBash) {
      & $gnuBash.Source -c $drainSafetyHarness bash ($operationsRoot -replace '\\', '/')
      if ($LASTEXITCODE -ne 0) { throw 'Performance drain/retirement executable safety simulation failed' }
    } else {
      Write-Warning 'GNU Bash is unavailable; drain/retirement executable safety simulations were skipped'
    }

    $performanceInstallerSafetyHarness = @'
set -euo pipefail
operations_root="$1"
root="$(mktemp -d /tmp/juhe-ai-performance-installer.XXXXXX)"
trap 'rm -rf -- "$root"' EXIT
fake_bin="$root/fake-bin"
mkdir -p "$fake_bin"

cat > "$fake_bin/launchctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  print) exit 0 ;;
  *) printf '%s\n' "$*" >> "$FAKE_LAUNCHCTL_LOG"; exit 0 ;;
esac
EOF
cat > "$fake_bin/plutil" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
cat > "$fake_bin/sleep" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
cat > "$fake_bin/nginx" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case " $* " in
  *' -t '*) exit 0 ;;
  *' -s reload '*)
    count=0
    [ ! -s "$FAKE_NGINX_RELOAD_COUNT" ] || count="$(cat "$FAKE_NGINX_RELOAD_COUNT")"
    count=$((count + 1))
    printf '%s\n' "$count" > "$FAKE_NGINX_RELOAD_COUNT"
    if [ "$FAKE_INSTALLER_MODE" = reload-fail ]; then exit 9; fi
    if grep -q 'performance-temporary' "$FAKE_NGINX_CONFIG"; then printf '%s\n' temporary > "$FAKE_ROUTE_STATE"; else printf '%s\n' main > "$FAKE_ROUTE_STATE"; fi
    exit 0
    ;;
esac
exit 2
EOF
cat > "$fake_bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
headers=''
url=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    -D) headers="$2"; shift 2 ;;
    --max-time|-o) shift 2 ;;
    http://*) url="$1"; shift ;;
    *) shift ;;
  esac
done
route="$(cat "$FAKE_ROUTE_STATE")"
if [ -n "$headers" ]; then
  header_text="HTTP/1.1 200 OK\r\nX-Juhe-Active-Upstream: performance-$route\r\n\r\n"
  if [ "$headers" = - ]; then printf '%b' "$header_text"; else printf '%b' "$header_text" > "$headers"; fi
fi
case "$url" in
  *:3300/__aisys__/health) printf '%s\n' '{"status":"ok","instanceId":"temporary-control-1","nodeRole":"control"}' ;;
  *:3311/__aisys__/health) printf '%s\n' '{"status":"ok","instanceId":"temporary-gateway-1","nodeRole":"gateway"}' ;;
  *:3312/__aisys__/health) printf '%s\n' '{"status":"ok","instanceId":"temporary-gateway-2","nodeRole":"gateway"}' ;;
  *:3313/__aisys__/health) printf '%s\n' '{"status":"ok","instanceId":"temporary-gateway-3","nodeRole":"gateway"}' ;;
  *:3099/__aisys__/health)
    if [ "$route" = temporary ]; then printf '%s\n' '{"status":"ok","instanceId":"temporary-control-1","nodeRole":"control"}'; else printf '%s\n' '{"status":"ok","instanceId":"main-control-1","nodeRole":"control"}'; fi
    ;;
esac
EOF
cat > "$fake_bin/node" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ "${1:-}" != -e ] || exit 0
base_url=''
while [ "$#" -gt 0 ]; do
  if [ "$1" = --base-url ]; then base_url="$2"; shift 2; else shift; fi
done
route="$(cat "$FAKE_ROUTE_STATE")"
if [ "$base_url" = 'http://127.0.0.1:3099' ]; then
  if [ "$FAKE_INSTALLER_MODE" = candidate-ingress-fail ] && [ "$route" = temporary ]; then exit 31; fi
  if [ "$FAKE_INSTALLER_MODE" = all-ingress-fail ]; then exit 32; fi
fi
printf '%s\n' 'MODEL_CATALOG_READY models=1 latencyMs=1'
EOF
cat > "$fake_bin/stat" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' '600'
EOF
chmod +x "$fake_bin"/*
export PATH="$fake_bin:$PATH"

run_case() {
  case_name="$1"
  expected_bootouts="$2"
  expect_candidate="$3"
  case_root="$root/$case_name"
  export HOME="$case_root/home"
  export FAKE_INSTALLER_MODE="$case_name"
  export FAKE_LAUNCHCTL_LOG="$case_root/launchctl.log"
  export FAKE_NGINX_CONFIG="$case_root/nginx.conf"
  export FAKE_NGINX_RELOAD_COUNT="$case_root/nginx.reload-count"
  export FAKE_ROUTE_STATE="$case_root/route.state"
  base="$case_root/base"
  release="$case_root/releases/candidate/juhe-ai-release"
  key_file="$case_root/readiness.key"
  mkdir -p "$HOME/Library/LaunchAgents" "$release/backend/dist/scripts/preflight" "$release/backend/dist/scripts/operations"
  : > "$release/backend/dist/server.js"
  : > "$release/backend/dist/scripts/preflight/check-node-sqlite.js"
  : > "$release/backend/dist/scripts/operations/model-catalog-readiness.js"
  : > "$release/backend/.env"
  printf '%s\n' 'test-readiness-key' > "$key_file"
  chmod 600 "$key_file"
  printf '%s\n' 'X-Juhe-Active-Upstream performance-main' > "$FAKE_NGINX_CONFIG"
  printf '%s\n' main > "$FAKE_ROUTE_STATE"
  : > "$FAKE_LAUNCHCTL_LOG"
  : > "$FAKE_NGINX_RELOAD_COUNT"
  if /bin/bash "$operations_root/install-performance-topology.sh" --apply --scope user \
    --base-dir "$base" --release-dir "$release" --slot temporary \
    --label-prefix com.example.juhe-ai.performance --nginx-config "$FAKE_NGINX_CONFIG" \
    --model-readiness-key-file "$key_file" >/dev/null 2>"$case_root/error.log"; then
    echo "installer safety case unexpectedly succeeded: $case_name" >&2
    exit 81
  fi
  bootouts="$(grep -c '^bootout ' "$FAKE_LAUNCHCTL_LOG" || true)"
  [ "$bootouts" -eq "$expected_bootouts" ] || { cat "$case_root/error.log" >&2; echo "$case_name bootout count=$bootouts expected=$expected_bootouts" >&2; exit 82; }
  control_plist="$HOME/Library/LaunchAgents/com.example.juhe-ai.performance.temporary.control-1.plist"
  control_run="$base/bin/performance/temporary/control-1.sh"
  if [ "$expect_candidate" = yes ]; then
    [ -f "$control_plist" ] && [ -f "$control_run" ] || { echo "$case_name did not retain candidate recovery files" >&2; exit 83; }
    grep -Fq "JUHE_AI_AUDIT_BLOB_ROOT=\"$base/shared/audit/blobs\"" "$control_run" || { echo "$case_name run script did not use the shared audit blob root" >&2; exit 84; }
    [ -d "$base/shared/audit/blobs" ] || { echo "$case_name did not create the shared audit blob directory" >&2; exit 85; }
    find "$case_root" -name 'nginx.conf.performance-candidate-failed.*' -type f -print -quit | grep -q . || { echo "$case_name did not preserve the candidate nginx config" >&2; exit 86; }
  else
    [ ! -e "$control_plist" ] && [ ! -e "$control_run" ] || { echo "$case_name did not clean candidate after fully proven rollback" >&2; exit 87; }
    ! find "$case_root" -name 'nginx.conf.performance-candidate-failed.*' -type f -print -quit | grep -q . || { echo "$case_name left candidate nginx recovery after a proven rollback" >&2; exit 88; }
  fi
}

run_case reload-fail 4 yes
run_case all-ingress-fail 4 yes
run_case candidate-ingress-fail 8 no
printf '%s\n' 'performance installer reload and fail-closed rollback simulations passed'
'@
    if ($gnuBash) {
      & $gnuBash.Source -c $performanceInstallerSafetyHarness bash ($operationsRoot -replace '\\', '/')
      if ($LASTEXITCODE -ne 0) { throw 'Performance installer executable rollback safety simulation failed' }
    } else {
      Write-Warning 'GNU Bash is unavailable; performance installer executable rollback simulations were skipped'
    }

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

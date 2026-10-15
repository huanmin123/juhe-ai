import assert from 'node:assert/strict'
import { chmod, copyFile, mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { execFileSync } from 'node:child_process'
import os from 'node:os'
import path from 'node:path'

const repositoryRoot = path.resolve(import.meta.dirname, '..')
const deployScript = path.join(repositoryRoot, 'docker', 'single-server', 'deploy.sh')
const hostInitScript = path.join(repositoryRoot, 'docker', 'single-server', 'host-init.sh')
const fixtureRoot = await mkdtemp(path.join(os.tmpdir(), 'juhe-maintenance-deploy-test-'))
const localMd5 = 'a'.repeat(32)
const matchingImageMd5 = localMd5
const mismatchedImageMd5 = 'b'.repeat(32)
const driftMd5 = 'c'.repeat(32)

// Windows 下传给 MSYS bash 的路径统一用正斜杠（PATH 之外的 env 值同样适用）。
function toPosix(p) {
  return p.split(path.sep).join('/')
}

function findBash() {
  const candidates = process.platform === 'win32'
    ? [process.env.BASH_EXE, 'E:/Git/bin/bash.exe', 'E:/tools/w64devkit-2.8.0/w64devkit/bin/bash.exe'].filter(Boolean)
    : ['/bin/bash', '/usr/bin/bash']

  for (const candidate of candidates) {
    try {
      const version = execFileSync(candidate, ['--version'], { encoding: 'utf8' })
      assert.match(version, /GNU bash/u, `${candidate} 必须是 GNU Bash，不能误用 ash`)
      return candidate
    } catch {
      // 继续探测已知 Bash 安装位置；最终没有 Bash 时让测试明确失败。
    }
  }
  throw new Error(`找不到可执行 Bash，已检查：${candidates.join(', ')}`)
}

const bash = findBash()

async function makeFixture(name) {
  const root = path.join(fixtureRoot, name)
  const scriptDir = path.join(root, 'docker', 'single-server')
  const mockBin = path.join(root, 'mock-bin')
  await mkdir(scriptDir, { recursive: true })
  for (const project of ['gateway', 'jobs', 'maintenance']) {
    await mkdir(path.join(root, 'backend-go', 'projects', project), { recursive: true })
  }
  await mkdir(mockBin, { recursive: true })
  await copyFile(deployScript, path.join(scriptDir, 'deploy.sh'))
  await chmod(path.join(scriptDir, 'deploy.sh'), 0o755)
  // deploy.sh 以 `bash -s < host-init.sh` 在远端执行主机层自检，fixture 必须携带同目录副本，
  // 否则重定向先于 ssh 失败，脚本永远走不进 mock 的 'bash -s' 分支。
  await copyFile(hostInitScript, path.join(scriptDir, 'host-init.sh'))
  await chmod(path.join(scriptDir, 'host-init.sh'), 0o755)
  // [6/6] 以 stdin 重定向上传 verify-release.sh，gateway/jobs 目标必须有同目录副本。
  await copyFile(path.join(repositoryRoot, 'docker', 'single-server', 'verify-release.sh'), path.join(scriptDir, 'verify-release.sh'))
  // 部署配置同步（compose.yml / Caddyfile / Dockerfile.runtime）会 md5 本地三件，fixture 同步携带。
  for (const configName of ['compose.yml', 'Caddyfile', 'Dockerfile.runtime']) {
    await copyFile(path.join(repositoryRoot, 'docker', 'single-server', configName), path.join(scriptDir, configName))
  }

  // 所有会触及构建、传输、容器或公网的命令都被本地桩替代。
  const mockCommands = {
    go: [
      '#!/usr/bin/env bash', 'set -euo pipefail', 'out=""',
      'while (($#)); do if [[ "$1" == "-o" ]]; then out="$2"; shift 2; else shift; fi; done',
      '[[ -n "$out" ]]', 'mkdir -p "$(dirname "$out")"', 'printf "mock maintenance binary\\n" > "$out"'
    ].join('\n'),
    md5sum: [
      '#!/usr/bin/env bash', 'set -euo pipefail',
      'printf "%s  %s\\n" "$EXPECTED_MD5" "${1:-mock-image}"'
    ].join('\n'),
    tar: [
      '#!/usr/bin/env bash', 'set -euo pipefail',
      'if [[ "${1:-}" == "czf" ]]; then printf "mock archive\\n"; exit 0; fi',
      'if [[ "${1:-}" == "xzf" ]]; then cat >/dev/null; exit 0; fi',
      'echo "unexpected tar invocation" >&2', 'exit 91'
    ].join('\n'),
    ssh: [
      '#!/usr/bin/env bash', 'set -euo pipefail',
      'while (($#)); do case "$1" in -i|-o) shift 2 ;; *) break ;; esac; done',
      'server="$1"', 'shift', 'remote="$1"',
      'printf "%s\\n" "$remote" >> "$SSH_LOG"',
      'case "$remote" in',
      '  "bash -s" ) cat > "$HOST_INIT_RECEIVED" ;;',
      '  "tar xzf - -C /opt/juhe-ai/build/bin && chmod 0755 /opt/juhe-ai/build/bin/juhe-ai-*" ) cat >/dev/null ;;',
      '  "tar xzf - -C /opt/juhe-ai" ) cat >/dev/null ;;',
      '  "md5sum /opt/juhe-ai/build/bin/juhe-ai-"* ) printf "%s  remote\\n" "$EXPECTED_MD5" ;;',
      '  "md5sum /opt/juhe-ai/compose.yml" )',
      '    if [[ "${CONFIG_DRIFT_COMPOSE:-0}" == 1 && ! -f "$CONFIG_QUERY_MARK" ]]; then',
      '      : > "$CONFIG_QUERY_MARK"',
      '      printf "%s  remote\\n" "$CONFIG_DRIFT_MD5"',
      '    else',
      '      printf "%s  remote\\n" "$EXPECTED_MD5"',
      '    fi ;;',
      '  "md5sum /opt/juhe-ai/Caddyfile" | "md5sum /opt/juhe-ai/Dockerfile.runtime" ) printf "%s  remote\\n" "$EXPECTED_MD5" ;;',
      '  "cd /opt/juhe-ai && set -a && . ./.env && set +a && docker compose --profile tool run --rm --no-deps maintenance --verify-account-capability-domain"* )',
      '    grep -q -- "--driver postgres --dsn" <<< "$remote" || { printf "verify command missing --driver/--dsn: %s\\n" "$remote" >&2; exit 92; }',
      '    if [[ "${CAPABILITY_VERIFY_STATUS:-0}" != 0 ]]; then printf "%s\\n" "${CAPABILITY_VERIFY_ERROR:-mock capability domain verify not ready}" >&2; exit "$CAPABILITY_VERIFY_STATUS"; fi',
      '    echo "capability domain verify: m2_finalized ready"; exit 0 ;;',
      '  "cd /opt/juhe-ai && set -a && . ./.env && set +a && docker compose --profile tool run --rm --no-deps maintenance --verify-scheduling-preferences"* )',
      '    grep -q -- "--driver postgres --dsn" <<< "$remote" || { printf "verify command missing --driver/--dsn: %s\\n" "$remote" >&2; exit 92; }',
      '    if [[ "${SCHEDPREF_VERIFY_STATUS:-0}" != 0 ]]; then printf "%s\\n" "${SCHEDPREF_VERIFY_ERROR:-mock scheduling preferences postflight not ready}" >&2; exit "$SCHEDPREF_VERIFY_STATUS"; fi',
      '    echo "scheduling preferences verify: stage=completed"; exit 0 ;;',
      '  "cd /opt/juhe-ai && docker compose --profile tool run --rm --no-deps --entrypoint md5sum maintenance /usr/local/bin/juhe-ai-go-project" )',
      '    if [[ "${IMAGE_CHECK_STATUS:-0}" != 0 ]]; then printf "%s\\n" "${IMAGE_CHECK_ERROR:-mock image check failed}" >&2; exit "$IMAGE_CHECK_STATUS"; fi',
      '    printf "%s  /usr/local/bin/juhe-ai-go-project\\n" "$IMAGE_MD5"; exit "${IMAGE_STATUS:-0}" ;;',
      '  "bash -c "*"docker compose build"* ) exit "${BUILD_STATUS:-0}" ;;',
      '  "docker inspect"*juhe-ai-go-jobs* ) printf "healthy\\n" ;;',
      '  "docker exec juhe-ai-go-jobs"* ) printf "%s  /usr/local/bin/juhe-ai-go-project\\n" "$EXPECTED_MD5" ;;',
      '  "cat > /opt/juhe-ai/verify-release.sh && chmod 0755 /opt/juhe-ai/verify-release.sh" ) cat >/dev/null ;;',
      '  "cd /opt/juhe-ai && bash verify-release.sh" )',
      '    if [[ "${VERIFY_STATUS:-0}" != 0 ]]; then printf "%s\\n" "${VERIFY_ERROR:-mock verify-release failed}" >&2; exit "$VERIFY_STATUS"; fi',
      '    echo "== 结果：PASS =="; exit 0 ;;',
      '  * ) printf "unexpected ssh command: %s\\n" "$remote" >&2; exit 92 ;;', 'esac'
    ].join('\n'),
    curl: [
      '#!/usr/bin/env bash', 'set -euo pipefail',
      'if [[ "${ALLOW_CURL:-0}" != 1 ]]; then printf "curl must not run for maintenance\\n" >&2; exit 93; fi',
      'if [[ "${CURL_STATUS:-0}" != 0 ]]; then exit "$CURL_STATUS"; fi',
      'printf "200"'
    ].join('\n')
  }

  for (const [command, contents] of Object.entries(mockCommands)) {
    const file = path.join(mockBin, command)
    await writeFile(file, contents, { mode: 0o755 })
    await chmod(file, 0o755)
  }

  const bashEnvFile = path.join(root, 'bash-env.sh')
  await writeFile(bashEnvFile, [
    'export PATH="$MOCK_SSH_BIN:/usr/bin:/bin"',
    ...['go', 'md5sum', 'tar', 'ssh', 'curl'].map((command) => `${command}() { "$MOCK_SSH_BIN/${command}" "$@"; }`),
    ''
  ].join('\n'))
  return {
    root, script: path.join(scriptDir, 'deploy.sh'), mockBin,
    sshLog: path.join(root, 'ssh.log'),
    receivedHostInit: path.join(root, 'received-host-init.sh'),
    configQueryMark: path.join(root, 'compose-queried'),
    bashEnvFile
  }
}

function runDeploy(fixture, overrides = {}, target = 'maintenance') {
  const shellMockBin = process.platform === 'win32'
    ? execFileSync('E:/Git/usr/bin/cygpath.exe', ['-u', fixture.mockBin], { encoding: 'utf8' }).trim()
    : fixture.mockBin
  const env = {
    PATH: `${shellMockBin}:/usr/bin:/bin`,
    SystemRoot: process.env.SystemRoot,
    TEMP: process.env.TEMP,
    TMP: process.env.TMP,
    BASH_ENV: process.platform === 'win32'
      ? execFileSync('E:/Git/usr/bin/cygpath.exe', ['-u', fixture.bashEnvFile], { encoding: 'utf8' }).trim()
      : fixture.bashEnvFile,
    MOCK_SSH_BIN: process.platform === 'win32'
      ? execFileSync('E:/Git/usr/bin/cygpath.exe', ['-u', fixture.mockBin], { encoding: 'utf8' }).trim()
      : fixture.mockBin,
    JUHE_AI_DEPLOY_SERVER: 'test-user@mock-host',
    SSH_LOG: toPosix(fixture.sshLog),
    HOST_INIT_RECEIVED: toPosix(fixture.receivedHostInit),
    CONFIG_QUERY_MARK: toPosix(fixture.configQueryMark),
    CONFIG_DRIFT_COMPOSE: '0',
    CONFIG_DRIFT_MD5: driftMd5,
    CAPABILITY_VERIFY_STATUS: '0',
    SCHEDPREF_VERIFY_STATUS: '0',
    VERIFY_STATUS: '0',
    ALLOW_CURL: '0',
    CURL_STATUS: '0',
    EXPECTED_MD5: localMd5,
    IMAGE_MD5: matchingImageMd5,
    BUILD_STATUS: '0',
    IMAGE_CHECK_STATUS: '0',
    IMAGE_STATUS: '0',
    ...overrides
  }
  try {
    const stdout = execFileSync(bash, [fixture.script, target], {
      cwd: fixture.root, env, encoding: 'utf8', timeout: 30_000
    })
    return { status: 0, stdout, stderr: '' }
  } catch (error) {
    return {
      status: error.status ?? -1,
      stdout: error.stdout?.toString() ?? '',
      stderr: error.stderr?.toString() ?? error.message
    }
  }
}

async function readSshCommands(fixture) {
  return (await readFile(fixture.sshLog, 'utf8')).trim().split('\n')
}

async function assertHostInitExecuted(fixture, result) {
  const commands = await readSshCommands(fixture)
  assert(commands.some((line) => line === 'bash -s'), `deploy.sh 应尝试经 ssh 'bash -s' 执行 host-init.sh：\n${commands.join('\n')}`)
  const received = await readFile(fixture.receivedHostInit, 'utf8')
  assert.match(received, /主机层优化自检/u, 'ssh 桩收到的 stdin 必须是 host-init.sh 内容')
  assert.doesNotMatch(`${result.stdout}\n${result.stderr}`, /No such file or directory/u, 'host-init.sh 缺失会让 deploy.sh 假失败（重定向先于 ssh 失败）')
}

async function assertMaintenanceOnlyCommands(fixture, result) {
  const commands = await readSshCommands(fixture)
  assert(commands.some((line) => line.includes('docker compose build maintenance')))
  assert(commands.some((line) => line === 'cd /opt/juhe-ai && docker compose --profile tool run --rm --no-deps --entrypoint md5sum maintenance /usr/local/bin/juhe-ai-go-project'))
  assert(commands.every((line) => !/docker compose up|docker inspect|docker exec|verify-release\.sh|curl|__aisys__\/health|verify-(account-capability-domain|scheduling-preferences)/u.test(line)), commands.join('\n'))
  await assertHostInitExecuted(fixture, result)
}

// ---------------------------------------------------------------------------
// host-init.sh 直接执行用例的桩命令集：fallocate/mkswap/swapon/sysctl/systemctl/df
// 在 Windows Git Bash 上不存在或行为不确定，一律以临时 PATH 桩虚拟 root 行为。
// stub-bin 之外的 grep/sed/awk/chmod 等走真实 /usr/bin。
// ---------------------------------------------------------------------------
async function makeHostInitFixture(name) {
  const root = path.join(fixtureRoot, `host-init-${name}`)
  const stubBin = path.join(root, 'stub-bin')
  const stateDir = path.join(root, 'state')
  await mkdir(stubBin, { recursive: true })
  await mkdir(stateDir, { recursive: true })
  const paths = {
    root, stubBin, hostInit: hostInitScript,
    swapFile: path.join(stateDir, 'swapfile'),
    fstab: path.join(stateDir, 'fstab'),
    sysctlConf: path.join(stateDir, '99-juhe-ai.conf'),
    sysctlState: path.join(stateDir, 'sysctl-state'),
    swaponState: path.join(stateDir, 'swapon-state'),
    journaldConf: path.join(stateDir, 'journald.conf')
  }
  await writeFile(paths.fstab, '', { mode: 0o644 })

  const stubs = {
    swapon: [
      '#!/usr/bin/env bash',
      '# `swapon <file>`（激活）记录到状态文件；`swapon --show` 按状态报告是否已启用',
      'if [ "${1:-}" != "--show" ] && [ -n "${MOCK_SWAPON_STATE:-}" ]; then',
      '  printf "%s\\n" "${1:-}" >> "$MOCK_SWAPON_STATE"',
      'fi',
      'if [ "${1:-}" = "--show" ]; then',
      '  if [ -n "${MOCK_SWAP_ACTIVE:-}" ] || { [ -n "${MOCK_SWAPON_STATE:-}" ] && [ -s "$MOCK_SWAPON_STATE" ]; }; then',
      '    printf "%s file 4G 0 0\\n" "${MOCK_SWAP_FILE:-/swapfile}"',
      '  fi',
      'fi',
      'exit 0'
    ].join('\n'),
    fallocate: [
      '#!/usr/bin/env bash', 'set -euo pipefail',
      '# fallocate -l <size> <file>：创建目标文件即可，测试不校验尺寸',
      'target="${3:?}"', ': > "$target"'
    ].join('\n'),
    mkswap: ['#!/usr/bin/env bash', 'exit 0'].join('\n'),
    sysctl: [
      '#!/usr/bin/env bash', 'set -euo pipefail',
      'STATE_FILE="${MOCK_SYSCTL_STATE:?}"', 'touch "$STATE_FILE"',
      'case "${1:-}" in',
      '  -n)',
      '    key="${2:?}"',
      '    value=$(grep -E "^${key}=" "$STATE_FILE" | tail -n 1 | cut -d= -f2- || true)',
      '    [ -n "$value" ] && printf "%s\\n" "$value" || exit 1',
      '    ;;',
      '  -w)',
      '    kv="${2:?}"', '    key="${kv%%=*}"', '    value="${kv#*=}"',
      '    grep -vE "^${key}=" "$STATE_FILE" > "$STATE_FILE.tmp" || true',
      '    printf "%s=%s\\n" "$key" "$value" >> "$STATE_FILE.tmp"',
      '    mv "$STATE_FILE.tmp" "$STATE_FILE"',
      '    ;;',
      '  -p)',
      '    conf="${2:?}"',
      '    while IFS= read -r line; do',
      '      case "$line" in ""|"#"*) continue ;; esac',
      '      key=$(printf "%s" "$line" | sed -E "s/[[:space:]]*=.*$//" | tr -d "[:space:]")',
      '      value=$(printf "%s" "$line" | sed -E "s/^[^=]*=[[:space:]]*//" | tr -d "[:space:]")',
      '      [ -n "$key" ] || continue',
      '      grep -vE "^${key}=" "$STATE_FILE" > "$STATE_FILE.tmp" || true',
      '      printf "%s=%s\\n" "$key" "$value" >> "$STATE_FILE.tmp"',
      '      mv "$STATE_FILE.tmp" "$STATE_FILE"',
      '    done < "$conf"',
      '    ;;',
      '  *) exit 0 ;;',
      'esac'
    ].join('\n'),
    systemctl: ['#!/usr/bin/env bash', 'exit 0'].join('\n'),
    df: [
      '#!/usr/bin/env bash',
      '# 仿 `df -m /`：第 2 行第 4 列 = 根分区可用 MB',
      'printf "Filesystem 1M-blocks Used Available Capacity Mounted\\nmock 102400 1024 %s 1%% /\\n" "${MOCK_DF_AVAIL_MB:-100000}"'
    ].join('\n')
  }
  for (const [command, contents] of Object.entries(stubs)) {
    const file = path.join(stubBin, command)
    await writeFile(file, contents, { mode: 0o755 })
    await chmod(file, 0o755)
  }
  // MSYS bash 启动时会在传入 PATH 之前插入 /mingw64/bin:/usr/bin 等（df 这类
  // /usr/bin 真身存在的命令会因此绕过桩），与 runDeploy 同法用 BASH_ENV 在
  // 启动后把桩目录重新置顶。
  const bashEnvFile = path.join(root, 'bash-env.sh')
  await writeFile(bashEnvFile, 'export PATH="$MOCK_STUB_BIN:/usr/bin:/bin"\n')
  return { ...paths, bashEnvFile }
}

function runHostInit(fixture, overrides = {}) {
  const shellStubBin = process.platform === 'win32'
    ? execFileSync('E:/Git/usr/bin/cygpath.exe', ['-u', fixture.stubBin], { encoding: 'utf8' }).trim()
    : fixture.stubBin
  const env = {
    PATH: `${shellStubBin}:/usr/bin:/bin`,
    SystemRoot: process.env.SystemRoot,
    TEMP: process.env.TEMP,
    TMP: process.env.TMP,
    BASH_ENV: process.platform === 'win32'
      ? execFileSync('E:/Git/usr/bin/cygpath.exe', ['-u', fixture.bashEnvFile], { encoding: 'utf8' }).trim()
      : fixture.bashEnvFile,
    MOCK_STUB_BIN: shellStubBin,
    JUHE_AI_SWAP_FILE: toPosix(fixture.swapFile),
    JUHE_AI_FSTAB_FILE: toPosix(fixture.fstab),
    JUHE_AI_SYSCTL_CONF: toPosix(fixture.sysctlConf),
    JUHE_AI_JOURNALD_CONF: toPosix(fixture.journaldConf),
    MOCK_SYSCTL_STATE: toPosix(fixture.sysctlState),
    MOCK_SWAPON_STATE: toPosix(fixture.swaponState),
    MOCK_SWAP_FILE: toPosix(fixture.swapFile),
    ...overrides
  }
  try {
    const stdout = execFileSync(bash, [fixture.hostInit], {
      cwd: fixture.root, env, encoding: 'utf8', timeout: 30_000
    })
    return { status: 0, stdout, stderr: '' }
  } catch (error) {
    return {
      status: error.status ?? -1,
      stdout: error.stdout?.toString() ?? '',
      stderr: error.stderr?.toString() ?? error.message
    }
  }
}

async function assertHostInitSuccess(result, expectedStatus = 0) {
  assert.equal(result.status, expectedStatus, `host-init.sh 退出码 ${result.status}（期望 ${expectedStatus}）\nstdout:\n${result.stdout}\nstderr:\n${result.stderr}`)
}

try {
  // 语法检查对测试实际复制的同一份部署脚本执行，不触发发布步骤。
  execFileSync(bash, ['-n', deployScript], { stdio: 'pipe' })
  execFileSync(bash, ['-n', hostInitScript], { stdio: 'pipe' })

  const success = await makeFixture('success')
  const successResult = runDeploy(success)
  assert.equal(successResult.status, 0, successResult.stderr)
  assert.match(successResult.stdout, /maintenance 镜像更新并校验完成/u)
  await assertMaintenanceOnlyCommands(success, successResult)

  const mismatch = await makeFixture('md5-mismatch')
  const mismatchResult = runDeploy(mismatch, { IMAGE_MD5: mismatchedImageMd5 })
  assert.equal(mismatchResult.status, 1, mismatchResult.stderr)
  assert.match(mismatchResult.stderr, /maintenance 镜像内二进制与本地新编译不一致/u)
  await assertMaintenanceOnlyCommands(mismatch, mismatchResult)

  const imageCommandFailure = await makeFixture('image-command-failure')
  const commandError = 'mock image md5 command exit 47'
  const failedResult = runDeploy(imageCommandFailure, { IMAGE_CHECK_STATUS: '47', IMAGE_CHECK_ERROR: commandError })
  assert.equal(failedResult.status, 47, failedResult.stderr)
  assert.match(failedResult.stderr, new RegExp(commandError, 'u'))
  await assertMaintenanceOnlyCommands(imageCommandFailure, failedResult)

  const imagePipelineFailure = await makeFixture('image-pipeline-failure')
  const pipelineResult = runDeploy(imagePipelineFailure, { IMAGE_STATUS: '48' })
  assert.equal(pipelineResult.status, 48, pipelineResult.stderr)
  await assertMaintenanceOnlyCommands(imagePipelineFailure, pipelineResult)

  const buildFailure = await makeFixture('build-failure')
  const buildResult = runDeploy(buildFailure, { BUILD_STATUS: '49' })
  assert.equal(buildResult.status, 1, buildResult.stderr)
  assert.match(buildResult.stderr, /服务器侧 docker compose build\/up 失败/u)
  const buildCommands = await readSshCommands(buildFailure)
  assert(buildCommands.every((line) => !/docker compose up|docker compose --profile tool run|docker inspect|docker exec|verify-release\.sh|curl|__aisys__\/health/u.test(line)), buildCommands.join('\n'))
  await assertHostInitExecuted(buildFailure, buildResult)

  // jobs 主路径：迁移前置核验通过 + compose.yml 漂移自动同步 + 滚动更新 + 健康闭环。
  const jobsSuccess = await makeFixture('jobs-success')
  const jobsResult = runDeploy(jobsSuccess, { ALLOW_CURL: '1', CONFIG_DRIFT_COMPOSE: '1' }, 'jobs')
  assert.equal(jobsResult.status, 0, jobsResult.stderr)
  assert.match(jobsResult.stdout, /发布完成：jobs/u)
  assert.match(jobsResult.stdout, /迁移前置核验（只读；未就绪先完成维护窗口再发布）/u)
  assert.match(jobsResult.stdout, /compose\.yml 漂移/u)
  assert.match(jobsResult.stdout, /配置同步完成并复核一致：compose\.yml/u)
  const jobsCommands = await readSshCommands(jobsSuccess)
  const verifyCommandPattern = /^cd \/opt\/juhe-ai && set -a && \. \.\/\.env && set \+a && docker compose --profile tool run --rm --no-deps maintenance --verify-[a-z-]+ .+ --driver postgres --dsn "\$JUHE_AI_POSTGRES_URL"$/u
  const capabilityVerifyCommand = jobsCommands.find((line) => line.includes('--verify-account-capability-domain'))
  const schedprefVerifyCommand = jobsCommands.find((line) => line.includes('--verify-scheduling-preferences'))
  assert(capabilityVerifyCommand && verifyCommandPattern.test(capabilityVerifyCommand), `前置核验必须 source .env 且显式携带 --driver/--dsn（远端展开）：\n${jobsCommands.join('\n')}`)
  assert(schedprefVerifyCommand && verifyCommandPattern.test(schedprefVerifyCommand), `调度偏好前置核验必须显式携带 --driver/--dsn：\n${jobsCommands.join('\n')}`)
  assert(jobsCommands.some((line) => line === 'tar xzf - -C /opt/juhe-ai'), 'compose.yml 漂移时必须触发配置上传', jobsCommands.join('\n'))
  assert(jobsCommands.some((line) => /docker compose build jobs/u.test(line)), jobsCommands.join('\n'))
  assert(jobsCommands.some((line) => /docker compose up -d jobs/u.test(line)), jobsCommands.join('\n'))
  assert(jobsCommands.some((line) => line.includes('docker inspect') && line.includes('juhe-ai-go-jobs')), jobsCommands.join('\n'))
  await assertHostInitExecuted(jobsSuccess, jobsResult)

  // 前置核验门禁：能力域阶段记录未封存 m2_finalized 时必须在 build/up 之前中止。
  const capabilityGate = await makeFixture('capability-gate-failure')
  const capabilityGateResult = runDeploy(capabilityGate, { CAPABILITY_VERIFY_STATUS: '3' }, 'jobs')
  assert.equal(capabilityGateResult.status, 1, capabilityGateResult.stderr)
  assert.match(capabilityGateResult.stderr, /账户能力域迁移未就绪/u)
  assert.match(capabilityGateResult.stderr, /账户能力域迁移维护窗口/u)
  const capabilityGateCommands = await readSshCommands(capabilityGate)
  assert(capabilityGateCommands.some((line) => line.startsWith('cd /opt/juhe-ai && set -a && . ./.env && set +a && docker compose --profile tool run --rm --no-deps maintenance --verify-account-capability-domain')))
  assert(capabilityGateCommands.every((line) => !/docker compose build|docker compose up/u.test(line)), capabilityGateCommands.join('\n'))

  // 前置核验门禁：调度偏好迁移未完成（postflight exit 3）同样在 build/up 之前中止。
  const schedprefGate = await makeFixture('schedpref-gate-failure')
  const schedprefGateResult = runDeploy(schedprefGate, { SCHEDPREF_VERIFY_STATUS: '3' }, 'jobs')
  assert.equal(schedprefGateResult.status, 1, schedprefGateResult.stderr)
  assert.match(schedprefGateResult.stderr, /调度偏好迁移未完成/u)
  assert.match(schedprefGateResult.stderr, /调度偏好迁移维护窗口/u)
  const schedprefGateCommands = await readSshCommands(schedprefGate)
  assert(schedprefGateCommands.every((line) => !/docker compose build|docker compose up/u.test(line)), schedprefGateCommands.join('\n'))

  // ---- host-init.sh 直接执行（PATH 桩虚拟 root）----
  // 1) 首次创建成功：swap 创建/激活/fstab 持久化、sysctl 写入并应用，全部收敛 exit 0。
  const firstCreate = await makeHostInitFixture('first-create')
  const firstCreateResult = runHostInit(firstCreate)
  await assertHostInitSuccess(firstCreateResult)
  assert.match(firstCreateResult.stdout, /\[FIX\] 创建 4G swapfile/u)
  assert.match(firstCreateResult.stdout, /创建并激活/u)
  assert.match(firstCreateResult.stdout, /\[FIX\] fstab 补写/u)
  assert.match(firstCreateResult.stdout, /\[OK\] 主机层优化全部收敛/u)
  const fstabAfterCreate = await readFile(firstCreate.fstab, 'utf8')
  assert(fstabAfterCreate.includes(`${toPosix(firstCreate.swapFile)} none swap sw 0 0`), fstabAfterCreate)
  const confAfterCreate = await readFile(firstCreate.sysctlConf, 'utf8')
  for (const expected of ['vm.overcommit_memory = 1', 'vm.swappiness = 10', 'vm.min_free_kbytes = 262144', 'vm.watermark_scale_factor = 125']) {
    assert(confAfterCreate.includes(expected), confAfterCreate)
  }
  const stateAfterCreate = await readFile(firstCreate.sysctlState, 'utf8')
  for (const expected of ['vm.overcommit_memory=1', 'vm.swappiness=10', 'vm.min_free_kbytes=262144', 'vm.watermark_scale_factor=125']) {
    assert(stateAfterCreate.includes(expected), stateAfterCreate)
  }
  const secondRunResult = runHostInit(firstCreate)
  await assertHostInitSuccess(secondRunResult)
  assert.match(secondRunResult.stdout, /\[SKIP\] fstab 已有/u)
  assert.match(secondRunResult.stdout, /配置与运行值一致/u)
  assert.doesNotMatch(secondRunResult.stdout, /\[FIX\]/u)

  // 2) swap 已启用但 fstab 缺条目：只补写 fstab，不得误报激活失败，exit 0。
  const swapActiveNoFstab = await makeHostInitFixture('swap-active-no-fstab')
  await writeFile(swapActiveNoFstab.fstab, '/dev/mockdisk none ext4 defaults 0 1\n')
  const swapActiveResult = runHostInit(swapActiveNoFstab, { MOCK_SWAP_ACTIVE: '1' })
  await assertHostInitSuccess(swapActiveResult)
  assert.match(swapActiveResult.stdout, /\[SKIP\] swap 已启用/u)
  assert.match(swapActiveResult.stdout, /\[FIX\] fstab 补写/u)
  assert.doesNotMatch(swapActiveResult.stdout, /swap 激活失败|swap 创建\/启用失败/u)
  const fstabAfterBackfill = await readFile(swapActiveNoFstab.fstab, 'utf8')
  assert(fstabAfterBackfill.includes(`${toPosix(swapActiveNoFstab.swapFile)} none swap sw 0 0`), fstabAfterBackfill)

  // 2b) swap 由其他来源启用（分区/其他文件）：不写 fstab（避免悬空挂载项），exit 0。
  const otherSourceSwap = await makeHostInitFixture('other-source-swap')
  const otherSourceResult = runHostInit(otherSourceSwap, { MOCK_SWAP_ACTIVE: '1', MOCK_SWAP_FILE: '/dev/mockswap' })
  await assertHostInitSuccess(otherSourceResult)
  assert.match(otherSourceResult.stdout, /\[SKIP\] swap 已由其他来源启用/u)
  assert.doesNotMatch(otherSourceResult.stdout, /\[FIX\] fstab 补写/u)
  const fstabOtherSource = await readFile(otherSourceSwap.fstab, 'utf8')
  assert(!fstabOtherSource.includes('swap'), fstabOtherSource)

  // 2c) fstab 只有注释掉的旧 swap 条目：不算已持久化，仍须补写。
  const commentedFstab = await makeHostInitFixture('commented-fstab')
  await writeFile(commentedFstab.fstab, `# ${toPosix(commentedFstab.swapFile)} none swap sw 0 0\n`)
  const commentedResult = runHostInit(commentedFstab, { MOCK_SWAP_ACTIVE: '1' })
  await assertHostInitSuccess(commentedResult)
  assert.match(commentedResult.stdout, /\[FIX\] fstab 补写/u)

  // 2d) 仅有 /swapfile-extra 时，不得把路径前缀当作 /swapfile 条目。
  const prefixCollisionFstab = await makeHostInitFixture('fstab-prefix-collision')
  await writeFile(prefixCollisionFstab.fstab, '/swapfile-extra none swap sw 0 0\n')
  const prefixCollisionResult = runHostInit(prefixCollisionFstab, { MOCK_SWAP_ACTIVE: '1' })
  await assertHostInitSuccess(prefixCollisionResult)
  assert.match(prefixCollisionResult.stdout, /\[FIX\] fstab 补写/u)
  const prefixCollisionAfter = await readFile(prefixCollisionFstab.fstab, 'utf8')
  assert(prefixCollisionAfter.includes('/swapfile none swap sw 0 0'), prefixCollisionAfter)

  // 3) sysctl 值漂移（配置文件与运行值都是 60）：重写配置 + 重新应用后收敛，exit 0。
  const sysctlDrift = await makeHostInitFixture('sysctl-drift')
  await writeFile(sysctlDrift.fstab, `${toPosix(sysctlDrift.swapFile)} none swap sw 0 0\n`)
  await writeFile(sysctlDrift.sysctlConf, 'vm.overcommit_memory = 1\nvm.swappiness = 60\n')
  await writeFile(sysctlDrift.sysctlState, 'vm.overcommit_memory=1\nvm.swappiness=60\n')
  const driftResult = runHostInit(sysctlDrift, { MOCK_SWAP_ACTIVE: '1' })
  await assertHostInitSuccess(driftResult)
  assert.match(driftResult.stdout, /vm\.swappiness 配置已更新为 10/u)
  assert.match(driftResult.stdout, /vm\.swappiness 运行值 60 -> 10 已应用并复核/u)
  const confAfterDrift = await readFile(sysctlDrift.sysctlConf, 'utf8')
  assert(confAfterDrift.includes('vm.swappiness = 10'), confAfterDrift)
  assert(!confAfterDrift.includes('vm.swappiness = 60'), confAfterDrift)
  const stateAfterDrift = await readFile(sysctlDrift.sysctlState, 'utf8')
  assert(stateAfterDrift.includes('vm.swappiness=10'), stateAfterDrift)

  // 4) 磁盘不足：跳过 swap 创建并 FAIL，退出码非 0。
  const diskShortage = await makeHostInitFixture('disk-shortage')
  const diskShortageResult = runHostInit(diskShortage, { MOCK_DF_AVAIL_MB: '900' })
  assert.notEqual(diskShortageResult.status, 0, `磁盘不足必须以非零退出\nstdout:\n${diskShortageResult.stdout}`)
  assert.match(diskShortageResult.stdout, /根分区仅剩 900MB/u)
  assert.match(diskShortageResult.stdout, /存在未收敛项/u)

  process.stdout.write('deploy maintenance mock regression passed (maintenance: success, md5 mismatch, image command failures, build failure; jobs: success + config sync + gate failures; host-init: first-create/idempotent, swap-active fstab backfill, sysctl drift convergence, disk shortage).\n')
} finally {
  const resolvedFixture = path.resolve(fixtureRoot)
  assert(path.isAbsolute(resolvedFixture), '临时 fixture 必须使用绝对路径清理')
  assert(resolvedFixture.startsWith(path.resolve(os.tmpdir()) + path.sep), '拒绝清理临时目录之外的路径')
  await rm(resolvedFixture, { force: true, recursive: true })
}

import assert from 'node:assert/strict'
import { chmod, copyFile, mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { execFileSync } from 'node:child_process'
import os from 'node:os'
import path from 'node:path'

const repositoryRoot = path.resolve(import.meta.dirname, '..')
const deployScript = path.join(repositoryRoot, 'docker', 'single-server', 'deploy.sh')
const fixtureRoot = await mkdtemp(path.join(os.tmpdir(), 'juhe-maintenance-deploy-test-'))
const localMd5 = 'a'.repeat(32)
const matchingImageMd5 = localMd5
const mismatchedImageMd5 = 'b'.repeat(32)

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
  await mkdir(path.join(root, 'backend-go', 'projects', 'maintenance'), { recursive: true })
  await mkdir(mockBin, { recursive: true })
  await copyFile(deployScript, path.join(scriptDir, 'deploy.sh'))
  await chmod(path.join(scriptDir, 'deploy.sh'), 0o755)

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
      '  "tar xzf - -C /opt/juhe-ai/build/bin && chmod 0755 /opt/juhe-ai/build/bin/juhe-ai-*" ) cat >/dev/null ;;',
      '  "md5sum /opt/juhe-ai/build/bin/juhe-ai-maintenance" ) printf "%s  remote\\n" "$EXPECTED_MD5" ;;',
      '  "bash -c "*"docker compose build maintenance"* ) exit "${BUILD_STATUS:-0}" ;;',
      '  "cd /opt/juhe-ai && docker compose --profile tool run --rm --no-deps --entrypoint md5sum maintenance /usr/local/bin/juhe-ai-go-project" )',
      '    if [[ "${IMAGE_CHECK_STATUS:-0}" != 0 ]]; then printf "%s\\n" "${IMAGE_CHECK_ERROR:-mock image check failed}" >&2; exit "$IMAGE_CHECK_STATUS"; fi',
      '    printf "%s  /usr/local/bin/juhe-ai-go-project\\n" "$IMAGE_MD5"; exit "${IMAGE_STATUS:-0}" ;;',
      '  * ) printf "unexpected ssh command: %s\\n" "$remote" >&2; exit 92 ;;', 'esac'
    ].join('\n'),
    curl: ['#!/usr/bin/env bash', 'printf "curl must not run for maintenance\\n" >&2', 'exit 93'].join('\n')
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
  return { root, script: path.join(scriptDir, 'deploy.sh'), mockBin, sshLog: path.join(root, 'ssh.log'), bashEnvFile }
}

function runDeploy(fixture, overrides = {}) {
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
    SSH_LOG: fixture.sshLog,
    EXPECTED_MD5: localMd5,
    IMAGE_MD5: matchingImageMd5,
    BUILD_STATUS: '0',
    IMAGE_CHECK_STATUS: '0',
    IMAGE_STATUS: '0',
    ...overrides
  }
  try {
    const stdout = execFileSync(bash, [fixture.script, 'maintenance'], {
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

async function assertMaintenanceOnlyCommands(fixture) {
  const commands = (await readFile(fixture.sshLog, 'utf8')).trim().split('\n')
  assert(commands.some((line) => line.includes('docker compose build maintenance')))
  assert(commands.some((line) => line === 'cd /opt/juhe-ai && docker compose --profile tool run --rm --no-deps --entrypoint md5sum maintenance /usr/local/bin/juhe-ai-go-project'))
  assert(commands.every((line) => !/docker compose up|docker inspect|docker exec|verify-release\.sh|curl|__aisys__\/health/u.test(line)), commands.join('\n'))
}

try {
  // 语法检查对测试实际复制的同一份部署脚本执行，不触发发布步骤。
  execFileSync(bash, ['-n', deployScript], { stdio: 'pipe' })

  const success = await makeFixture('success')
  const successResult = runDeploy(success)
  assert.equal(successResult.status, 0, successResult.stderr)
  assert.match(successResult.stdout, /maintenance 镜像更新并校验完成/u)
  await assertMaintenanceOnlyCommands(success)

  const mismatch = await makeFixture('md5-mismatch')
  const mismatchResult = runDeploy(mismatch, { IMAGE_MD5: mismatchedImageMd5 })
  assert.equal(mismatchResult.status, 1, mismatchResult.stderr)
  assert.match(mismatchResult.stderr, /maintenance 镜像内二进制与本地新编译不一致/u)
  await assertMaintenanceOnlyCommands(mismatch)

  const imageCommandFailure = await makeFixture('image-command-failure')
  const commandError = 'mock image md5 command exit 47'
  const failedResult = runDeploy(imageCommandFailure, { IMAGE_CHECK_STATUS: '47', IMAGE_CHECK_ERROR: commandError })
  assert.equal(failedResult.status, 47, failedResult.stderr)
  assert.match(failedResult.stderr, new RegExp(commandError, 'u'))
  await assertMaintenanceOnlyCommands(imageCommandFailure)

  const imagePipelineFailure = await makeFixture('image-pipeline-failure')
  const pipelineResult = runDeploy(imagePipelineFailure, { IMAGE_STATUS: '48' })
  assert.equal(pipelineResult.status, 48, pipelineResult.stderr)
  await assertMaintenanceOnlyCommands(imagePipelineFailure)

  const buildFailure = await makeFixture('build-failure')
  const buildResult = runDeploy(buildFailure, { BUILD_STATUS: '49' })
  assert.equal(buildResult.status, 1, buildResult.stderr)
  assert.match(buildResult.stderr, /服务器侧 docker compose build\/up 失败/u)
  const buildCommands = (await readFile(buildFailure.sshLog, 'utf8')).trim().split('\n')
  assert(buildCommands.every((line) => !/docker compose up|docker compose --profile tool run|docker inspect|docker exec|verify-release\.sh|curl|__aisys__\/health/u.test(line)), buildCommands.join('\n'))

  process.stdout.write('deploy maintenance mock regression passed (success, md5 mismatch, image command failures, build failure).\n')
} finally {
  const resolvedFixture = path.resolve(fixtureRoot)
  assert(path.isAbsolute(resolvedFixture), '临时 fixture 必须使用绝对路径清理')
  assert(resolvedFixture.startsWith(path.resolve(os.tmpdir()) + path.sep), '拒绝清理临时目录之外的路径')
  await rm(resolvedFixture, { force: true, recursive: true })
}

import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const repoRoot = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const backendRoot = join(repoRoot, 'backend')
const powershellSource = readFileSync(join(repoRoot, 'deploy', 'start.ps1'), 'utf8')
const shellSource = readFileSync(join(repoRoot, 'deploy', 'start.sh'), 'utf8')

const launcherSources = [
  {
    platform: 'windows',
    runtime: extractPowerShellLauncher('Get-RuntimeLogIndexerNodeLauncher'),
    tableMonitor: extractPowerShellLauncher('Get-TableMonitorNodeLauncher')
  },
  {
    platform: 'unix',
    runtime: extractShellRuntimeLauncher(),
    tableMonitor: extractShellTableMonitorLauncher().replace('process.argv.slice(2)', 'process.argv.slice(1)')
  }
]

for (const launcher of launcherSources) {
  assert.match(launcher.runtime, /appendFileSync/u, `${launcher.platform} F1 launcher must import and use appendFileSync`)
  assert.match(launcher.runtime, /randomUUID/u, `${launcher.platform} F1 launcher must import and use randomUUID`)
  assert.doesNotMatch(launcher.tableMonitor, /appendFileSync|randomUUID/u, `${launcher.platform} F2 launcher must not carry unused F1 instance-ID helpers`)

  assertRuntimeLauncherGeneratesInstanceID(launcher.platform, launcher.runtime)
  assertTableMonitorLauncherRejectsMissingInstanceID(launcher.platform, launcher.tableMonitor)
  assertTableMonitorLauncherStartsWithConfiguredInstanceID(launcher.platform, launcher.tableMonitor)
}

console.log('release Go sidecar launcher regression passed')

function extractPowerShellLauncher(functionName) {
  const match = new RegExp(`function ${functionName} \\{\\s*return @'\\r?\\n([\\s\\S]*?)\\r?\\n'@`, 'u').exec(powershellSource)
  assert.ok(match, `missing ${functionName} in deploy/start.ps1`)
  return match[1]
}

function extractShellRuntimeLauncher() {
  const match = /runtime_log_indexer_pid="\$\(node --input-type=module -e '\r?\n([\s\S]*?)\r?\n' "\$runtime_log_indexer_binary"/u.exec(shellSource)
  assert.ok(match, 'missing F1 inline Node launcher in deploy/start.sh')
  return match[1]
}

function extractShellTableMonitorLauncher() {
  const match = /table_monitor_pid="\$\(\s*\r?\n\s*node --input-type=module - "\$table_monitor_binary" "\$APP_DIR\/backend" "\$table_monitor_log_file" <<'NODE'\r?\n([\s\S]*?)\r?\nNODE/u.exec(shellSource)
  assert.ok(match, 'missing F2 inline Node launcher in deploy/start.sh')
  assert.match(match[1], /process\.argv\.slice\(2\)/u, 'F2 shell launcher must account for Node stdin argv layout')
  return match[1]
}

function assertRuntimeLauncherGeneratesInstanceID(platform, source) {
  const result = runLauncher(source, {})
  try {
    assert.equal(result.status, 0, `${platform} F1 launcher failed: ${result.output}`)
    assert.match(result.output.trim(), /^\d+$/u, `${platform} F1 launcher must report a child PID`)
    assert.match(readFileSync(join(result.backendRoot, '.env'), 'utf8'), /^JUHE_AI_RUNTIME_LOG_INSTANCE_ID=runtime-log-indexer-[0-9a-f-]+$/mu)
  } finally {
    result.cleanup()
  }
}

function assertTableMonitorLauncherRejectsMissingInstanceID(platform, source) {
  const result = runLauncher(source, {})
  try {
    assert.notEqual(result.status, 0, `${platform} F2 launcher must reject a missing owner ID`)
    assert.match(result.output, /JUHE_AI_TABLE_MONITOR_INSTANCE_ID is required/u)
    assert.equal(result.envWritten, false, `${platform} F2 launcher must not generate or persist an owner ID`)
  } finally {
    result.cleanup()
  }
}

function assertTableMonitorLauncherStartsWithConfiguredInstanceID(platform, source) {
  const result = runLauncher(source, { JUHE_AI_TABLE_MONITOR_INSTANCE_ID: 'release-sidecar-regression' })
  try {
    assert.equal(result.status, 0, `${platform} F2 launcher failed with configured owner ID: ${result.output}`)
    assert.match(result.output.trim(), /^\d+$/u, `${platform} F2 launcher must report a child PID`)
    assert.equal(result.envWritten, false, `${platform} F2 launcher must not rewrite backend/.env`)
  } finally {
    result.cleanup()
  }
}

function runLauncher(source, overrides) {
  // Keep the temporary package beneath backend so createRequire() finds the real
  // production dotenv dependency without reading or modifying backend/.env.
  const isolatedBackend = mkdtempSync(join(backendRoot, '.release-launcher-regression-'))
  const logPath = join(isolatedBackend, 'sidecar.log')
  writeFileSync(join(isolatedBackend, 'package.json'), '{"private":true}\n')
  const env = { ...process.env, ...overrides }
  for (const key of [
    'JUHE_AI_ENV_FILE',
    'JUHE_AI_DISABLE_BASE_ENV',
    'JUHE_AI_RUNTIME_LOG_INSTANCE_ID',
    'JUHE_AI_TABLE_MONITOR_INSTANCE_ID',
    'JUHE_AI_DATABASE_DRIVER',
    'JUHE_AI_RUNTIME_MODE',
    'JUHE_AI_RUNTIME_LOG_STORE',
    'JUHE_AI_TABLE_MONITOR_STORE'
  ]) {
    if (!Object.hasOwn(overrides, key)) delete env[key]
  }
  const result = spawnSync(process.execPath, ['--input-type=module', '--eval', source, process.execPath, isolatedBackend, logPath], {
    cwd: repoRoot,
    encoding: 'utf8',
    env,
    timeout: 10000
  })
  return {
    status: result.status,
    output: `${result.stdout ?? ''}${result.stderr ?? ''}`,
    backendRoot: isolatedBackend,
    envWritten: readFileIfPresent(join(isolatedBackend, '.env')) !== null,
    cleanup() {
      rmSync(isolatedBackend, { recursive: true, force: true })
    }
  }
}

function readFileIfPresent(path) {
  try {
    return readFileSync(path, 'utf8')
  } catch (error) {
    if (error && typeof error === 'object' && error.code === 'ENOENT') return null
    throw error
  }
}

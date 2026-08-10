import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const repoRoot = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const backendRoot = join(repoRoot, 'backend')
const powershellSource = readFileSync(join(repoRoot, 'deploy', 'start.ps1'), 'utf8')
const shellSource = readFileSync(join(repoRoot, 'deploy', 'start.sh'), 'utf8')

assert.match(powershellSource, /Start-AuditLogWriter/u, 'Windows F3 launcher must exist')
assert.match(shellSource, /start_audit_log_writer\(\)/u, 'Unix F3 launcher must exist')
for (const source of [powershellSource, shellSource]) {
  assert.match(source, /start_audit_log_writer|Start-AuditLogWriter/u, 'F3 launcher must configure stable instance ID')
  assert.match(source, /audit[-_]log[-_]writer|AUDIT_LOG_INPUT_URL/u, 'F3 launcher must configure Node input URL')
  assert.match(source, /JUHE_AI_AUDIT_LOG_INPUT_SECRET/u, 'F3 launcher must forward F3 input secret')
  assert.match(source, /JUHE_AI_AUDIT_LOG_DATABASE_PATH/u, 'F3 launcher must configure isolated SQLite path')
  assert.match(source, /JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY/u, 'F3 launcher must configure payload blob path')
  assert.match(source, /JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY/u, 'F3 launcher must configure hot-search path')
}

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
  assertRuntimeLauncherForwardsSQLiteIsolationPaths(launcher.platform, launcher.runtime)
  assertTableMonitorLauncherForwardsRuntimeLogPath(launcher.platform, launcher.tableMonitor)
}

assert.match(powershellSource, /function Get-AuditLogWriterNodeLauncher\s*\{/u, 'Windows F3 launcher function must exist')
assert.match(shellSource, /start_audit_log_writer\(\)[\s\S]*JUHE_AI_AUDIT_LOG_INPUT_SECRET/u, 'Unix F3 launcher must forward the input secret')
assert.match(powershellSource, /JUHE_AI_AUDIT_LOG_INPUT_SECRET/u, 'Windows F3 launcher must forward the input secret')

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
  assert.match(result.output, /JUHE_AI_TABLE_MONITOR_INSTANCE_ID is required|SyntaxError/u)
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

function assertRuntimeLauncherForwardsSQLiteIsolationPaths(platform, source) {
  const result = runLauncher(captureChildEnvironment(source), {}, {
    captureChildEnvironment: true,
    baseEnv: [
      'JUHE_AI_DATABASE_DRIVER=sqlite',
      'JUHE_AI_RUNTIME_LOG_DATABASE_PATH=./data/runtime-log.sqlite3',
      'JUHE_AI_TABLE_MONITOR_DATABASE_PATH=./data/table-monitor.sqlite3',
      'JUHE_AI_USAGE_CATALOG_DATABASE_PATH=./data/usage-catalog.sqlite3',
      'JUHE_AI_STATS_DATABASE_PATH=./data/stats.sqlite3',
      'JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT=./data/codex-context/state-shards'
    ].join('\n')
  })
  try {
    assert.equal(result.status, 0, `${platform} F1 launcher failed: ${result.output}`)
    assert.equal(result.childEnvironment.JUHE_AI_TABLE_MONITOR_DATABASE_PATH, join(result.backendRoot, 'data', 'table-monitor.sqlite3'))
    assert.equal(result.childEnvironment.JUHE_AI_USAGE_CATALOG_DATABASE_PATH, join(result.backendRoot, 'data', 'usage-catalog.sqlite3'))
    assert.equal(result.childEnvironment.JUHE_AI_STATS_DATABASE_PATH, join(result.backendRoot, 'data', 'stats.sqlite3'))
    assert.equal(result.childEnvironment.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT, join(result.backendRoot, 'data', 'codex-context', 'state-shards'))
  } finally {
    result.cleanup()
  }
}

function assertTableMonitorLauncherForwardsRuntimeLogPath(platform, source) {
  const result = runLauncher(captureChildEnvironment(source), { JUHE_AI_TABLE_MONITOR_INSTANCE_ID: 'release-sidecar-regression' }, {
    captureChildEnvironment: true,
    baseEnv: [
      'JUHE_AI_DATABASE_DRIVER=sqlite',
      'JUHE_AI_RUNTIME_LOG_DATABASE_PATH=./data/runtime-log.sqlite3',
      'JUHE_AI_TABLE_MONITOR_DATABASE_PATH=./data/table-monitor.sqlite3'
    ].join('\n')
  })
  try {
    assert.equal(result.status, 0, `${platform} F2 launcher failed: ${result.output}`)
    assert.equal(result.childEnvironment.JUHE_AI_RUNTIME_LOG_DATABASE_PATH, join(result.backendRoot, 'data', 'runtime-log.sqlite3'))
  } finally {
    result.cleanup()
  }
}

function captureChildEnvironment(source) {
  assert.match(source, /spawn\(binaryPath, \[\], \{/u, 'launcher must spawn its Go sidecar without positional arguments')
  const synchronousImport = source.replace(
    /import \{ spawn \} from ['"]node:child_process['"]/u,
    "import { spawnSync } from 'node:child_process'\nconst spawn = (binaryPath, args, options) => {\n  const outcome = spawnSync(binaryPath, args, { ...options, detached: false })\n  if (outcome.error) throw outcome.error\n  return { pid: outcome.status === 0 ? 1 : undefined, unref() {} }\n}"
  )
  assert.notEqual(synchronousImport, source, 'launcher must import spawn from node:child_process')
  return synchronousImport.replace('spawn(binaryPath, [], {', 'spawn(binaryPath, [process.argv[4]], {')
}

function runLauncher(source, overrides, options = {}) {
  // Keep the temporary package beneath backend so createRequire() finds the real
  // production dotenv dependency without reading or modifying backend/.env.
  const isolatedBackend = mkdtempSync(join(backendRoot, '.release-launcher-regression-'))
  const logPath = join(isolatedBackend, 'sidecar.log')
  writeFileSync(join(isolatedBackend, 'package.json'), '{"private":true}\n')
  if (options.baseEnv) writeFileSync(join(isolatedBackend, '.env'), `${options.baseEnv}\n`)
  const env = { ...process.env, ...overrides }
  for (const key of [
    'JUHE_AI_ENV_FILE',
    'JUHE_AI_DISABLE_BASE_ENV',
    'JUHE_AI_RUNTIME_LOG_INSTANCE_ID',
    'JUHE_AI_TABLE_MONITOR_INSTANCE_ID',
    'JUHE_AI_DATABASE_DRIVER',
    'JUHE_AI_RUNTIME_MODE',
    'JUHE_AI_RUNTIME_LOG_STORE',
    'JUHE_AI_TABLE_MONITOR_STORE',
    'JUHE_AI_RUNTIME_LOG_DATABASE_PATH',
    'JUHE_AI_TABLE_MONITOR_DATABASE_PATH',
    'JUHE_AI_USAGE_CATALOG_DATABASE_PATH',
    'JUHE_AI_STATS_DATABASE_PATH',
    'JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT'
  ]) {
    if (!Object.hasOwn(overrides, key)) delete env[key]
  }
  const capturePath = join(isolatedBackend, 'child-environment.json')
  const captureScript = join(isolatedBackend, 'capture-child-environment.mjs')
  if (options.captureChildEnvironment) {
    writeFileSync(captureScript, "import { writeFileSync } from 'node:fs'\nwriteFileSync(process.env.JUHE_AI_RELEASE_LAUNCHER_CAPTURE_PATH, JSON.stringify(process.env))\n")
    env.JUHE_AI_RELEASE_LAUNCHER_CAPTURE_PATH = capturePath
  }
  const args = ['--input-type=module', '--eval', source, process.execPath, isolatedBackend, logPath]
  if (options.captureChildEnvironment) args.push(captureScript)
  const result = spawnSync(process.execPath, args, {
    cwd: repoRoot,
    encoding: 'utf8',
    env,
    timeout: 10000
  })
  return {
    status: result.status,
    output: `${result.stdout ?? ''}${result.stderr ?? ''}`,
    backendRoot: isolatedBackend,
    childEnvironment: options.captureChildEnvironment ? JSON.parse(readFileSync(capturePath, 'utf8')) : undefined,
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

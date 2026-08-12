import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const repoRoot = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const backendRoot = join(repoRoot, 'backend')
const startScriptPath = join(repoRoot, 'scripts', 'start-go-sidecar.mjs')
const powershellSource = readFileSync(join(repoRoot, 'deploy', 'start.ps1'), 'utf8')
const shellSource = readFileSync(join(repoRoot, 'deploy', 'start.sh'), 'utf8')
const launcherSource = readFileSync(startScriptPath, 'utf8')

for (const source of [powershellSource, shellSource]) {
  assert.match(source, /juhe-ai-go-sidecar/u, 'release startup must use the single Go sidecar binary')
  assert.match(source, /start-go-sidecar\.mjs/u, 'release startup must call the shared sidecar environment launcher')
  assert.doesNotMatch(source, /juhe-ai-runtime-log-indexer|juhe-ai-table-monitor|juhe-ai-audit-log-writer/u, 'release startup must not retain old Go executable paths')
}

assert.ok(
  shellSource.indexOf('pnpm install --prod --frozen-lockfile --filter juhe-ai-backend...') < shellSource.indexOf('node "$RUNTIME_CHECK_SCRIPT"'),
  'Unix release startup must install production dependencies before the runtime preflight imports them'
)
assert.ok(
  powershellSource.indexOf('pnpm install --prod --frozen-lockfile --filter juhe-ai-backend...') < powershellSource.indexOf('node $runtimeCheckPath'),
  'Windows release startup must install production dependencies before the runtime preflight imports them'
)
assert.match(launcherSource, /release startup does not generate owner identities or transport secrets/u, 'shared launcher must reject missing owner identities and the explicit F3 input secret')
assert.doesNotMatch(launcherSource, /JUHE_AI_AUDIT_LOG_INPUT_SECRET[^\n]*JUHE_AI_SECRET/u, 'shared launcher must not fall back to JUHE_AI_SECRET')
for (const name of ['JUHE_AI_RUNTIME_LOG_INSTANCE_ID', 'JUHE_AI_TABLE_MONITOR_INSTANCE_ID', 'JUHE_AI_AUDIT_LOG_INSTANCE_ID']) {
  assert.match(launcherSource, new RegExp(name, 'u'), `shared launcher must require ${name}`)
}

assertLauncherRejectsMissingOwnerOrSecret()
assertLauncherForwardsAllSQLitePaths()

console.log('release Go sidecar launcher regression passed')

function assertLauncherRejectsMissingOwnerOrSecret() {
  const result = runLauncher({
    'JUHE_AI_RUNTIME_LOG_INSTANCE_ID': 'f1-owner',
    'JUHE_AI_TABLE_MONITOR_INSTANCE_ID': 'f2-owner',
    'JUHE_AI_AUDIT_LOG_INSTANCE_ID': 'f3-owner'
  })
  try {
    assert.notEqual(result.status, 0, 'sidecar launcher must reject a missing F3 input secret')
    assert.match(result.output, /JUHE_AI_AUDIT_LOG_INPUT_SECRET is required/u)
  } finally {
    result.cleanup()
  }
}

function assertLauncherForwardsAllSQLitePaths() {
  const result = runLauncher({
    'JUHE_AI_RUNTIME_LOG_INSTANCE_ID': 'f1-owner',
    'JUHE_AI_TABLE_MONITOR_INSTANCE_ID': 'f2-owner',
    'JUHE_AI_AUDIT_LOG_INSTANCE_ID': 'f3-owner',
    'JUHE_AI_AUDIT_LOG_INPUT_SECRET': 'release-sidecar-input-secret-with-32-bytes'
  }, [
    'JUHE_AI_DATABASE_DRIVER=sqlite',
    'JUHE_AI_RUNTIME_LOG_DATABASE_PATH=./data/runtime-log.sqlite3',
    'JUHE_AI_TABLE_MONITOR_DATABASE_PATH=./data/table-monitor.sqlite3',
    'JUHE_AI_AUDIT_LOG_DATABASE_PATH=./data/audit-log.sqlite3',
    'JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY=./data/audit-payload-blobs',
    'JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY=./data/audit-hot-search',
    'JUHE_AI_USAGE_CATALOG_DATABASE_PATH=./data/usage-catalog.sqlite3',
    'JUHE_AI_STATS_DATABASE_PATH=./data/stats.sqlite3',
    'JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT=./data/codex-context/state-shards'
  ].join('\n'))
  try {
    assert.equal(result.status, 0, `sidecar launcher failed: ${result.output}`)
    assert.equal(result.childEnvironment.JUHE_AI_RUNTIME_LOG_DATABASE_PATH, join(result.backendRoot, 'data', 'runtime-log.sqlite3'))
    assert.equal(result.childEnvironment.JUHE_AI_TABLE_MONITOR_DATABASE_PATH, join(result.backendRoot, 'data', 'table-monitor.sqlite3'))
    assert.equal(result.childEnvironment.JUHE_AI_AUDIT_LOG_DATABASE_PATH, join(result.backendRoot, 'data', 'audit-log.sqlite3'))
    assert.equal(result.childEnvironment.JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY, join(result.backendRoot, 'data', 'audit-payload-blobs'))
    assert.equal(result.childEnvironment.JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY, join(result.backendRoot, 'data', 'audit-hot-search'))
    assert.equal(result.childEnvironment.JUHE_AI_USAGE_CATALOG_DATABASE_PATH, join(result.backendRoot, 'data', 'usage-catalog.sqlite3'))
    assert.equal(result.childEnvironment.JUHE_AI_STATS_DATABASE_PATH, join(result.backendRoot, 'data', 'stats.sqlite3'))
  } finally {
    result.cleanup()
  }
}

function runLauncher(overrides, baseEnv = '') {
  const isolatedBackend = mkdtempSync(join(backendRoot, '.release-go-sidecar-launcher-regression-'))
  const logPath = join(isolatedBackend, 'sidecar.log')
  const capturePath = join(isolatedBackend, 'child-environment.json')
  const testLauncherPath = join(isolatedBackend, 'start-go-sidecar-testable.mjs')
  writeFileSync(join(isolatedBackend, 'package.json'), '{"private":true}\n')
  if (baseEnv) writeFileSync(join(isolatedBackend, '.env'), `${baseEnv}\n`)
  writeFileSync(testLauncherPath, instrumentLauncherForEnvironmentCapture(launcherSource))
  const env = { ...process.env, ...overrides, JUHE_AI_RELEASE_LAUNCHER_CAPTURE_PATH: capturePath }
  for (const key of [
    'JUHE_AI_ENV_FILE', 'JUHE_AI_DISABLE_BASE_ENV', 'JUHE_AI_DATABASE_DRIVER', 'JUHE_AI_RUNTIME_MODE',
    'JUHE_AI_RUNTIME_LOG_INSTANCE_ID', 'JUHE_AI_TABLE_MONITOR_INSTANCE_ID', 'JUHE_AI_AUDIT_LOG_INSTANCE_ID',
    'JUHE_AI_AUDIT_LOG_INPUT_SECRET', 'JUHE_AI_RUNTIME_LOG_STORE', 'JUHE_AI_TABLE_MONITOR_STORE', 'JUHE_AI_AUDIT_LOG_STORE',
    'JUHE_AI_RUNTIME_LOG_DATABASE_PATH', 'JUHE_AI_TABLE_MONITOR_DATABASE_PATH', 'JUHE_AI_AUDIT_LOG_DATABASE_PATH',
    'JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY', 'JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY', 'JUHE_AI_USAGE_CATALOG_DATABASE_PATH',
    'JUHE_AI_STATS_DATABASE_PATH', 'JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT'
  ]) {
    if (!Object.hasOwn(overrides, key)) delete env[key]
  }
  const result = spawnSync(process.execPath, [testLauncherPath, 'sidecar-under-test', isolatedBackend, logPath], {
    cwd: repoRoot,
    encoding: 'utf8',
    env,
    timeout: 10000
  })
  return {
    status: result.status,
    output: `${result.stdout ?? ''}${result.stderr ?? ''}`,
    backendRoot: isolatedBackend,
    childEnvironment: result.status === 0 ? JSON.parse(readFileSync(capturePath, 'utf8')) : undefined,
    cleanup() { rmSync(isolatedBackend, { recursive: true, force: true }) }
  }
}

function instrumentLauncherForEnvironmentCapture(source) {
  const withWriteFile = source.replace(
    "import { closeSync, existsSync, openSync, readFileSync } from 'node:fs'",
    "import { closeSync, existsSync, openSync, readFileSync, writeFileSync } from 'node:fs'"
  )
  assert.notEqual(withWriteFile, source, 'shared launcher must import its filesystem dependencies directly')
  const instrumented = withWriteFile.replace(
    "import { spawn } from 'node:child_process'",
    "const spawn = (_binaryPath, _args, options) => { writeFileSync(process.env.JUHE_AI_RELEASE_LAUNCHER_CAPTURE_PATH, JSON.stringify(options.env)); return { pid: 1, unref() {} } }"
  )
  assert.notEqual(instrumented, withWriteFile, 'shared launcher must import spawn from node:child_process')
  return instrumented
}

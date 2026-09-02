import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const repoRoot = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const backendRoot = join(repoRoot, 'backend')
const launcherPath = join(repoRoot, 'scripts', 'start-go-project.mjs')
const launcherSource = readFileSync(launcherPath, 'utf8')
const powershellSource = readFileSync(join(repoRoot, 'deploy', 'start.ps1'), 'utf8')
const shellSource = readFileSync(join(repoRoot, 'deploy', 'start.sh'), 'utf8')
const j1InputSigningKey = Buffer.alloc(32, 17).toString('base64url')

for (const source of [powershellSource, shellSource]) {
  assert.match(source, /juhe-ai-gateway/u, 'release startup must run Go gateway')
  assert.match(source, /juhe-ai-jobs/u, 'release startup must run Go jobs')
  assert.match(source, /start-go-project\.mjs/u, 'release startup must call the generic Go project launcher')
  assert.doesNotMatch(source, /juhe-ai-go-sidecar/u, 'release startup must not retain the deleted monolithic binary')
}

assert.match(launcherSource, /gateway\|jobs/u, 'launcher must accept only declared Go projects')
assertLauncherRejectsMissingProjectIdentity()
assertLauncherRejectsJ1WithoutGoOwner()
assertLauncherRejectsMissingJ1InputDirectory()
assertLauncherRejectsSqliteJ2Store()
assertLauncherForwardsProjectScopedPaths()
assertLauncherForwardsJ2PathsAndOwner()
assertLauncherForwardsGoRuntimeMetricsConfig()

console.log('release Go project launcher regression passed')

function assertLauncherRejectsMissingProjectIdentity() {
  const result = runLauncher('jobs', {
    JUHE_AI_RUNTIME_LOG_INSTANCE_ID: 'f1-owner'
  })
  try {
    assert.notEqual(result.status, 0, 'jobs launcher must reject a missing F2 owner identity')
    assert.match(result.output, /JUHE_AI_TABLE_MONITOR_INSTANCE_ID is required/u)
  } finally {
    result.cleanup()
  }
}

function assertLauncherRejectsJ1WithoutGoOwner() {
  const result = runLauncher('jobs', {
    JUHE_AI_RUNTIME_LOG_INSTANCE_ID: 'f1-owner',
    JUHE_AI_TABLE_MONITOR_INSTANCE_ID: 'f2-owner',
    JUHE_AI_ACCOUNT_HEALTH_ENABLED: 'true',
    JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER: 'node'
  })
  try {
    assert.notEqual(result.status, 0, 'J1 Go process must reject a Node owner declaration')
    assert.match(result.output, /JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER=go/u)
  } finally {
    result.cleanup()
  }
}

function assertLauncherRejectsMissingJ1InputDirectory() {
  const result = runLauncher('jobs', {
    JUHE_AI_RUNTIME_LOG_INSTANCE_ID: 'f1-owner',
    JUHE_AI_TABLE_MONITOR_INSTANCE_ID: 'f2-owner',
    JUHE_AI_ACCOUNT_HEALTH_ENABLED: 'true',
    JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER: 'go',
    JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID: 'j1-owner',
    JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY: j1InputSigningKey,
    JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET: 'j1-release-credential-secret'
  })
  try {
    assert.notEqual(result.status, 0, 'J1 release startup must reject a missing shared input directory')
    assert.match(result.output, /JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY is required/u)
  } finally {
    result.cleanup()
  }
}

function assertLauncherForwardsProjectScopedPaths() {
  const jobs = runLauncher('jobs', {
    JUHE_AI_RUNTIME_LOG_INSTANCE_ID: 'f1-owner',
    JUHE_AI_TABLE_MONITOR_INSTANCE_ID: 'f2-owner',
    JUHE_AI_ACCOUNT_HEALTH_ENABLED: 'true',
    JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER: 'go',
    JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID: 'j1-owner',
    JUHE_AI_ACCOUNT_HEALTH_STORE: 'sqlite',
    JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH: './data/account-health.sqlite3',
    JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY: './data/account-health-inputs',
    JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY: j1InputSigningKey,
    JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET: 'j1-release-credential-secret'
  }, [
    'JUHE_AI_DATABASE_DRIVER=sqlite',
    'JUHE_AI_RUNTIME_LOG_DATABASE_PATH=./data/runtime-log.sqlite3',
    'JUHE_AI_TABLE_MONITOR_DATABASE_PATH=./data/table-monitor.sqlite3'
  ].join('\n'))
  const gateway = runLauncher('gateway', {
    JUHE_AI_AUDIT_LOG_INSTANCE_ID: 'f3-owner',
    JUHE_AI_AUDIT_LOG_INPUT_SECRET: 'release-audit-input-secret-with-32-bytes',
    JUHE_AI_OPERATION_LOG_INSTANCE_ID: 'f4-owner',
    JUHE_AI_OPERATION_LOG_INPUT_SECRET: 'release-operation-input-secret-32-bytes'
  }, [
    'JUHE_AI_DATABASE_DRIVER=sqlite',
    'JUHE_AI_RUNTIME_LOG_DATABASE_PATH=./data/runtime-log.sqlite3',
    'JUHE_AI_TABLE_MONITOR_DATABASE_PATH=./data/table-monitor.sqlite3',
    'JUHE_AI_AUDIT_LOG_DATABASE_PATH=./data/audit-log.sqlite3',
    'JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY=./data/audit-payload-blobs',
    'JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY=./data/audit-hot-search',
    'JUHE_AI_OPERATION_LOG_DATABASE_PATH=./data/operation-log.sqlite3'
  ].join('\n'))
  try {
    assert.equal(jobs.status, 0, `jobs launcher failed: ${jobs.output}`)
    assert.equal(jobs.childEnvironment.JUHE_AI_RUNTIME_LOG_DATABASE_PATH, join(jobs.backendRoot, 'data', 'runtime-log.sqlite3'))
    assert.equal(jobs.childEnvironment.JUHE_AI_TABLE_MONITOR_DATABASE_PATH, join(jobs.backendRoot, 'data', 'table-monitor.sqlite3'))
    assert.equal(jobs.childEnvironment.JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH, join(jobs.backendRoot, 'data', 'account-health.sqlite3'))
    assert.equal(jobs.childEnvironment.JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY, join(jobs.backendRoot, 'data', 'account-health-inputs'))
    assert.equal(jobs.childEnvironment.JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER, 'go')
    assert.equal(jobs.childEnvironment.JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS, '127.0.0.1:3305')
    assert.equal(gateway.status, 0, `gateway launcher failed: ${gateway.output}`)
    assert.equal(gateway.childEnvironment.JUHE_AI_RUNTIME_LOG_DATABASE_PATH, join(gateway.backendRoot, 'data', 'runtime-log.sqlite3'))
    assert.equal(gateway.childEnvironment.JUHE_AI_TABLE_MONITOR_DATABASE_PATH, join(gateway.backendRoot, 'data', 'table-monitor.sqlite3'))
    assert.equal(gateway.childEnvironment.JUHE_AI_AUDIT_LOG_DATABASE_PATH, join(gateway.backendRoot, 'data', 'audit-log.sqlite3'))
    assert.equal(gateway.childEnvironment.JUHE_AI_OPERATION_LOG_DATABASE_PATH, join(gateway.backendRoot, 'data', 'operation-log.sqlite3'))
    assert.equal(gateway.childEnvironment.JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS, '127.0.0.1:3306')
    assert.equal(gateway.childEnvironment.JUHE_AI_OPERATION_LOG_INPUT_LISTEN_ADDRESS, '127.0.0.1:3304')
  } finally {
    jobs.cleanup()
    gateway.cleanup()
  }
}

function assertLauncherForwardsJ2PathsAndOwner() {
  const jobs = runLauncher('jobs', {
    JUHE_AI_RUNTIME_LOG_INSTANCE_ID: 'f1-owner',
    JUHE_AI_TABLE_MONITOR_INSTANCE_ID: 'f2-owner',
    JUHE_AI_ACCOUNT_BALANCE_ENABLED: 'true',
    JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER: 'go',
    JUHE_AI_ACCOUNT_BALANCE_OWNER_ID: 'j2-owner',
    JUHE_AI_ACCOUNT_BALANCE_STORE: 'postgres',
    JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL: 'postgres://j2-store',
    JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_URL: 'postgres://j2-input',
    JUHE_AI_ACCOUNT_BALANCE_CREDENTIAL_SECRET: 'j2-credential-secret',
    JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET: 'j2-manual-bridge-secret-0123456789',
    JUHE_AI_ACCOUNT_BALANCE_RECOVERY_BATCH_SIZE: '3',
    JUHE_AI_ACCOUNT_BALANCE_CYCLE_BUDGET: '40s'
  }, [
    'JUHE_AI_DATABASE_DRIVER=sqlite',
    'JUHE_AI_RUNTIME_LOG_DATABASE_PATH=./data/runtime-log.sqlite3',
    'JUHE_AI_TABLE_MONITOR_DATABASE_PATH=./data/table-monitor.sqlite3'
  ].join('\n'))
  try {
    assert.equal(jobs.status, 0, `J2 jobs launcher failed: ${jobs.output}`)
    assert.equal(jobs.childEnvironment.JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER, 'go')
    assert.equal(jobs.childEnvironment.JUHE_AI_ACCOUNT_BALANCE_OWNER_ID, 'j2-owner')
    assert.equal(jobs.childEnvironment.JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL, 'postgres://j2-store')
    assert.equal(jobs.childEnvironment.JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_URL, 'postgres://j2-input')
    assert.equal(jobs.childEnvironment.JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET, 'j2-manual-bridge-secret-0123456789')
    assert.equal(jobs.childEnvironment.JUHE_AI_ACCOUNT_BALANCE_RECOVERY_BATCH_SIZE, '3')
    assert.equal(jobs.childEnvironment.JUHE_AI_ACCOUNT_BALANCE_CYCLE_BUDGET, '40s')
  } finally {
    jobs.cleanup()
  }
}

function assertLauncherForwardsGoRuntimeMetricsConfig() {
  const jobs = runLauncher('jobs', {
    JUHE_AI_RUNTIME_LOG_INSTANCE_ID: 'f1-owner',
    JUHE_AI_TABLE_MONITOR_INSTANCE_ID: 'f2-owner',
    JUHE_AI_GO_RUNTIME_METRICS_STORE: 'sqlite',
    JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH: './data/go-runtime-metrics.sqlite3',
    JUHE_AI_GO_RUNTIME_METRICS_INTERVAL: '15s',
    JUHE_AI_GO_RUNTIME_METRICS_RETENTION_DAYS: '30',
    JUHE_AI_GO_RUNTIME_METRICS_SERVICE: 'juhe-ai',
    JUHE_AI_GO_RUNTIME_METRICS_ROLE: 'jobs'
  }, [
    'JUHE_AI_DATABASE_DRIVER=postgres',
    'JUHE_AI_RUNTIME_LOG_STORE=postgres',
    'JUHE_AI_TABLE_MONITOR_STORE=postgres',
    'JUHE_AI_RUNTIME_LOG_POSTGRES_URL=postgres://runtime-log',
    'JUHE_AI_TABLE_MONITOR_POSTGRES_URL=postgres://table-monitor'
  ].join('\n'))
  try {
    assert.equal(jobs.status, 0, `Go runtime metrics launcher failed: ${jobs.output}`)
    assert.equal(jobs.childEnvironment.JUHE_AI_GO_RUNTIME_METRICS_STORE, 'sqlite')
    assert.equal(jobs.childEnvironment.JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH, join(jobs.backendRoot, 'data', 'go-runtime-metrics.sqlite3'))
    assert.equal(jobs.childEnvironment.JUHE_AI_GO_RUNTIME_METRICS_INTERVAL, '15s')
    assert.equal(jobs.childEnvironment.JUHE_AI_GO_RUNTIME_METRICS_RETENTION_DAYS, '30')
    assert.equal(jobs.childEnvironment.JUHE_AI_GO_RUNTIME_METRICS_SERVICE, 'juhe-ai')
    assert.equal(jobs.childEnvironment.JUHE_AI_GO_RUNTIME_METRICS_ROLE, 'jobs')
  } finally {
    jobs.cleanup()
  }
}

function assertLauncherRejectsSqliteJ2Store() {
  const result = runLauncher('jobs', {
    JUHE_AI_RUNTIME_LOG_INSTANCE_ID: 'f1-owner',
    JUHE_AI_TABLE_MONITOR_INSTANCE_ID: 'f2-owner',
    JUHE_AI_ACCOUNT_BALANCE_ENABLED: 'true',
    JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER: 'go',
    JUHE_AI_ACCOUNT_BALANCE_OWNER_ID: 'j2-owner',
    JUHE_AI_ACCOUNT_BALANCE_STORE: 'sqlite',
    JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_URL: 'postgres://j2-input',
    JUHE_AI_ACCOUNT_BALANCE_CREDENTIAL_SECRET: 'j2-credential-secret'
  })
  try {
    assert.notEqual(result.status, 0, 'Go-owner J2 launcher must reject SQLite outcome store')
    assert.match(result.output, /JUHE_AI_ACCOUNT_BALANCE_STORE=postgres/u)
  } finally {
    result.cleanup()
  }
}

function runLauncher(project, overrides, baseEnv = '') {
  const isolatedBackend = mkdtempSync(join(backendRoot, `.release-go-${project}-launcher-regression-`))
  const logPath = join(isolatedBackend, `${project}.log`)
  const capturePath = join(isolatedBackend, 'child-environment.json')
  const testLauncherPath = join(isolatedBackend, 'start-go-project-testable.mjs')
  writeFileSync(join(isolatedBackend, 'package.json'), '{"private":true}\n')
  if (baseEnv) writeFileSync(join(isolatedBackend, '.env'), `${baseEnv}\n`)
  writeFileSync(testLauncherPath, instrumentLauncherForEnvironmentCapture(launcherSource))
  const env = { ...process.env, ...overrides, JUHE_AI_RELEASE_LAUNCHER_CAPTURE_PATH: capturePath }
  for (const key of Object.keys(env)) {
    if (key.startsWith('JUHE_AI_') && !Object.hasOwn(overrides, key) && key !== 'JUHE_AI_RELEASE_LAUNCHER_CAPTURE_PATH') delete env[key]
  }
  const result = spawnSync(process.execPath, [testLauncherPath, project, 'project-under-test', isolatedBackend, logPath], {
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
  const instrumented = withWriteFile.replace(
    "import { spawn } from 'node:child_process'",
    "const spawn = (_binaryPath, _args, options) => { writeFileSync(process.env.JUHE_AI_RELEASE_LAUNCHER_CAPTURE_PATH, JSON.stringify(options.env)); return { pid: 1, unref() {} } }"
  )
  assert.notEqual(instrumented, source, 'launcher must import spawn directly for the capture harness')
  return instrumented
}

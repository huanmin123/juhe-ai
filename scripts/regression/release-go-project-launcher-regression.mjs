import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const repoRoot = resolve(fileURLToPath(new URL('../..', import.meta.url)))
// X02：backend/ 已归档，启动器回归的隔离沙盒改用系统临时目录。
const launcherPath = join(repoRoot, 'scripts', 'start-go-project.mjs')
const launcherSource = readFileSync(launcherPath, 'utf8')
const powershellSource = readFileSync(join(repoRoot, 'deploy', 'start.ps1'), 'utf8')
const shellSource = readFileSync(join(repoRoot, 'deploy', 'start.sh'), 'utf8')
const j1InputSigningKey = Buffer.alloc(32, 17).toString('base64url')

for (const source of [powershellSource, shellSource]) {
  assert.match(source, /juhe-ai-gateway/u, 'release startup must run Go gateway')
  assert.match(source, /juhe-ai-jobs/u, 'release startup must run Go jobs')
  assert.doesNotMatch(source, /start-go-project\.mjs/u, 'release startup must not depend on the retired Node launcher')
  assert.doesNotMatch(source, /juhe-ai-go-sidecar/u, 'release startup must not retain the deleted monolithic binary')
}
assert.match(powershellSource, /Start-Process -FilePath \$binaryPath/u, 'Windows release startup must launch Go directly')
assert.match(shellSource, /nohup "\$binary"/u, 'Unix release startup must launch Go directly')
for (const source of [powershellSource, shellSource]) {
  assert.match(source, /JUHE_AI_ENV_FILE/u, 'release startup must honor the documented env overlay')
  assert.match(source, /\.env\.capacity/u, 'release startup must honor the documented capacity env file')
}

assert.match(launcherSource, /gateway\|jobs/u, 'launcher must accept only declared Go projects')
assertLauncherStartsWithoutProjectIdentity()
assertLauncherRejectsJ1WithoutGoOwner()
assertLauncherLeavesJ1IdentityToGoDefaults()
assertLauncherForwardsJ2StoreVerbatimToGo()
assertLauncherForwardsProjectScopedPaths()
assertLauncherForwardsGatewayOwnershipGates()
assertLauncherForwardsGatewayJobsOrigins()
assertLauncherForwardsGatewayRuntimeConfig()
assertLauncherForwardsJ2PathsAndOwner()
assertLauncherForwardsGoRuntimeMetricsConfig()
assertReleaseScriptsCreateGoOnlyBackendRoot()

console.log('release Go project launcher regression passed')

// 2026-09-19 零配置收口：F1/F2 INSTANCE_ID 缺省主机名、store/路径由 Go 侧自
// 派生，launcher 不得再硬校验 owner 身份；零注入启动必须成功，且 JUHE_AI_DATA_DIR
// 透传、已删除的家族开关残留被 drop。
function assertLauncherStartsWithoutProjectIdentity() {
  const result = runLauncher('jobs', {
    JUHE_AI_DATA_DIR: './zero-config-data',
    JUHE_AI_JOBS_STATS_ENABLED: 'false',
    JUHE_AI_JOBS_PROBE_ENABLED: 'false',
    JUHE_AI_JOBS_RETENTION_ENABLED: 'false',
    JUHE_AI_JOBS_RETENTION_CHAT_ENABLED: 'false',
    JUHE_AI_JOBS_RETENTION_DATA_ENABLED: 'false',
    JUHE_AI_JOBS_RETENTION_RECORD_MAINTENANCE_ENABLED: 'false',
    JUHE_AI_JOBS_RETENTION_EXPIRED_ACCOUNT_ENABLED: 'false',
    JUHE_AI_JOBS_RETENTION_API_KEY_RETRY_ENABLED: 'false',
    JUHE_AI_JOBS_RETENTION_ACCOUNT_RETRY_ENABLED: 'false'
  })
  try {
    assert.equal(result.status, 0, `jobs launcher must start without owner identity env: ${result.output}`)
    assert.equal(result.childEnvironment.JUHE_AI_DATA_DIR, './zero-config-data',
      'JUHE_AI_DATA_DIR must be forwarded to the jobs child')
    assert.equal(result.childEnvironment.JUHE_AI_RUNTIME_LOG_INSTANCE_ID, undefined,
      'the launcher must not inject F1 instance identity (Go defaults to the hostname)')
    assert.equal(result.childEnvironment.JUHE_AI_TABLE_MONITOR_INSTANCE_ID, undefined,
      'the launcher must not inject F2 instance identity (Go defaults to the hostname)')
    for (const name of [
      'JUHE_AI_JOBS_STATS_ENABLED', 'JUHE_AI_JOBS_OAUTH_ENABLED', 'JUHE_AI_JOBS_TASK_RUNS_ENABLED',
      'JUHE_AI_JOBS_USAGE_WRITER_ENABLED', 'JUHE_AI_JOBS_BALANCE_DETECT_ENABLED',
      'JUHE_AI_JOBS_RETENTION_ENABLED', 'JUHE_AI_JOBS_PROBE_ENABLED',
      'JUHE_AI_JOBS_RETENTION_CHAT_ENABLED', 'JUHE_AI_JOBS_RETENTION_DATA_ENABLED',
      'JUHE_AI_JOBS_RETENTION_RECORD_MAINTENANCE_ENABLED', 'JUHE_AI_JOBS_RETENTION_EXPIRED_ACCOUNT_ENABLED',
      'JUHE_AI_JOBS_RETENTION_API_KEY_RETRY_ENABLED', 'JUHE_AI_JOBS_RETENTION_ACCOUNT_RETRY_ENABLED'
    ]) {
      assert.equal(result.childEnvironment[name], undefined,
        `the removed job family switch ${name} must not be forwarded to the jobs child`)
    }
  } finally {
    result.cleanup()
  }
}

function assertLauncherRejectsJ1WithoutGoOwner() {
  const result = runLauncher('jobs', {
    // 2026-09-19 起 J1 无 ENABLED 开关、强制常开：INSTANCE_ID 等身份项缺省由
    // Go 派生，但显式非 go 的 owner 声明仍须拒绝。
    JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER: 'node'
  })
  try {
    assert.notEqual(result.status, 0, 'J1 Go process must reject a Node owner declaration')
    assert.match(result.output, /JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER=go/u)
  } finally {
    result.cleanup()
  }
}

// 2026-09-19 零配置收口：J1 的 INSTANCE_ID 缺省主机名、INPUT_DIRECTORY 自动
// 创建、SIGNING_KEY 自动生成并持久化、CREDENTIAL_SECRET 缺省取 JUHE_AI_SECRET、
// STORE 跟随 driver——launcher 不注入、不硬校验，缺省启动必须成功。
function assertLauncherLeavesJ1IdentityToGoDefaults() {
  const result = runLauncher('jobs', {})
  try {
    assert.equal(result.status, 0, `J1 must start without launcher-provided identity env: ${result.output}`)
    for (const name of [
      'JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID',
      'JUHE_AI_ACCOUNT_HEALTH_STORE',
      'JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH',
      'JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY',
      'JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY',
      'JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET'
    ]) {
      assert.equal(result.childEnvironment[name], undefined,
        `the launcher must not inject ${name} (Go derives it when unset)`)
    }
  } finally {
    result.cleanup()
  }
}

function assertLauncherForwardsProjectScopedPaths() {
  const jobs = runLauncher('jobs', {
    // 2026-09-19 零配置收口：J1 必填项校验已删除，以下显式值原样透传。
    JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY: './data/account-health-inputs',
    JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY: j1InputSigningKey,
    // J1/worker 强制常开后无 ENABLED 开关：以下历史开关残留必须被 launcher
    // 显式 drop，不得进入 jobs 子进程（开关移除回归注入）。
    JUHE_AI_ACCOUNT_HEALTH_ENABLED: 'true',
    JUHE_AI_JOBS_WORKER_ENABLED: 'false',
    JUHE_AI_JOBS_STATS_ENABLED: 'false',
    JUHE_AI_JOBS_OAUTH_ENABLED: 'false',
    JUHE_AI_JOBS_TASK_RUNS_ENABLED: 'false',
    JUHE_AI_JOBS_USAGE_WRITER_ENABLED: 'false',
    JUHE_AI_JOBS_BALANCE_DETECT_ENABLED: 'false',
    JUHE_AI_JOBS_RETENTION_ENABLED: 'false',
    JUHE_AI_JOBS_RETENTION_CHAT_ENABLED: 'false',
    JUHE_AI_JOBS_RETENTION_DATA_ENABLED: 'false',
    JUHE_AI_JOBS_RETENTION_RECORD_MAINTENANCE_ENABLED: 'false',
    JUHE_AI_JOBS_RETENTION_EXPIRED_ACCOUNT_ENABLED: 'false',
    JUHE_AI_JOBS_RETENTION_API_KEY_RETRY_ENABLED: 'false',
    JUHE_AI_JOBS_RETENTION_ACCOUNT_RETRY_ENABLED: 'false',
    JUHE_AI_JOBS_PROBE_ENABLED: 'false',
    JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER: 'go',
    JUHE_AI_ACCOUNT_HEALTH_STORE: 'sqlite',
    JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH: './data/account-health.sqlite3'
  }, [
    'JUHE_AI_DATABASE_DRIVER=sqlite',
    'JUHE_AI_DATA_DIR=./data',
    'JUHE_AI_RUNTIME_LOG_DATABASE_PATH=./data/runtime-log.sqlite3',
    'JUHE_AI_TABLE_MONITOR_DATABASE_PATH=./data/table-monitor.sqlite3'
  ].join('\n'))
  const gateway = runLauncher('gateway', {
    JUHE_AI_AUDIT_LOG_INSTANCE_ID: 'f3-owner',
    JUHE_AI_OPERATION_LOG_INSTANCE_ID: 'f4-owner'
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
    // 2026-09-19 零配置收口：SQLite 路径注入表删除，显式值原样透传，
    // 相对路径由 Go 子进程按 cwd/语义自行解析。
    assert.equal(jobs.childEnvironment.JUHE_AI_RUNTIME_LOG_DATABASE_PATH, './data/runtime-log.sqlite3')
    assert.equal(jobs.childEnvironment.JUHE_AI_TABLE_MONITOR_DATABASE_PATH, './data/table-monitor.sqlite3')
    assert.equal(jobs.childEnvironment.JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH, './data/account-health.sqlite3')
    assert.equal(jobs.childEnvironment.JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY, './data/account-health-inputs')
    assert.equal(jobs.childEnvironment.JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY, j1InputSigningKey)
    assert.equal(jobs.childEnvironment.JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER, 'go')
    assert.equal(jobs.childEnvironment.JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS, '127.0.0.1:3305')
    assert.equal(jobs.childEnvironment.JUHE_AI_DATA_DIR, './data', 'JUHE_AI_DATA_DIR must be forwarded to the jobs child')
    // 开关移除回归：历史 ENABLED/worker 总开关与 7 个任务族开关、6 个 retention
    // 子开关残留不得进入 jobs 子进程（任务族 2026-09-19 起强制常开）。
    assert.equal(jobs.childEnvironment.JUHE_AI_ACCOUNT_HEALTH_ENABLED, undefined,
      'the removed J1 ENABLED switch must not be forwarded to the jobs child')
    assert.equal(jobs.childEnvironment.JUHE_AI_JOBS_WORKER_ENABLED, undefined,
      'the removed worker master switch must not be forwarded to the jobs child')
    for (const name of [
      'JUHE_AI_JOBS_STATS_ENABLED', 'JUHE_AI_JOBS_OAUTH_ENABLED', 'JUHE_AI_JOBS_TASK_RUNS_ENABLED',
      'JUHE_AI_JOBS_USAGE_WRITER_ENABLED', 'JUHE_AI_JOBS_BALANCE_DETECT_ENABLED',
      'JUHE_AI_JOBS_RETENTION_ENABLED', 'JUHE_AI_JOBS_PROBE_ENABLED',
      'JUHE_AI_JOBS_RETENTION_CHAT_ENABLED', 'JUHE_AI_JOBS_RETENTION_DATA_ENABLED',
      'JUHE_AI_JOBS_RETENTION_RECORD_MAINTENANCE_ENABLED', 'JUHE_AI_JOBS_RETENTION_EXPIRED_ACCOUNT_ENABLED',
      'JUHE_AI_JOBS_RETENTION_API_KEY_RETRY_ENABLED', 'JUHE_AI_JOBS_RETENTION_ACCOUNT_RETRY_ENABLED'
    ]) {
      assert.equal(jobs.childEnvironment[name], undefined,
        `the removed job family switch ${name} must not be forwarded to the jobs child`)
    }
    assert.equal(gateway.status, 0, `gateway launcher failed: ${gateway.output}`)
    assert.equal(gateway.childEnvironment.JUHE_AI_RUNTIME_LOG_DATABASE_PATH, './data/runtime-log.sqlite3')
    assert.equal(gateway.childEnvironment.JUHE_AI_TABLE_MONITOR_DATABASE_PATH, './data/table-monitor.sqlite3')
    assert.equal(gateway.childEnvironment.JUHE_AI_AUDIT_LOG_DATABASE_PATH, './data/audit-log.sqlite3')
    assert.equal(gateway.childEnvironment.JUHE_AI_OPERATION_LOG_DATABASE_PATH, './data/operation-log.sqlite3')
    assert.equal(gateway.childEnvironment.JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS, '127.0.0.1:3306')
    // 去跨进程战役第四刀：loopback input listen 地址随 F3/F4 监听器删除，
    // launcher 不得再生成或转发该 env。
    assert.equal(gateway.childEnvironment.JUHE_AI_OPERATION_LOG_INPUT_LISTEN_ADDRESS, undefined,
      'the removed F4 input listen address must not be forwarded to the gateway child')
    assert.equal(gateway.childEnvironment.JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS, undefined,
      'the removed F3 input listen address must not be forwarded to the gateway child')
  } finally {
    jobs.cleanup()
    gateway.cleanup()
  }
}

function assertLauncherForwardsGatewayOwnershipGates() {
  const gateway = runLauncher('gateway', {
    JUHE_AI_AUDIT_LOG_INSTANCE_ID: 'f3-owner',
    JUHE_AI_OPERATION_LOG_INSTANCE_ID: 'f4-owner'
  }, [
    'JUHE_AI_DATABASE_DRIVER=sqlite',
    'JUHE_AI_BUSINESS_OWNER=gateway',
    'JUHE_AI_BUSINESS_HANDOFF_CONFIRMED=true',
    'JUHE_AI_BUSINESS_NODE_WRITER_STOPPED=true',
    'JUHE_AI_BUSINESS_SCHEMA_READY=true',
    'JUHE_AI_BUSINESS_OWNER_EPOCH=epoch-launcher-regression',
    'JUHE_AI_BUSINESS_CUTOVER_EVIDENCE_PATH=./data/cutover-evidence.json',
    'JUHE_AI_BUSINESS_DATABASE_PATH=./data/business.sqlite3',
    'JUHE_AI_BUSINESS_POSTGRES_URL=postgres://business-owner',
    // 2026-09-21 起 SYSTEM_API/CHAIN 功能开关移除：显式残留值必须被剥离，
    // 不进入子进程（开关清零后子进程恒开，不存在可开关假象）。
    'JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=true',
    'JUHE_AI_GATEWAY_CHAIN_ENABLED=true'
  ].join('\n'))
  try {
    assert.equal(gateway.status, 0, `gateway ownership-gate launcher failed: ${gateway.output}`)
    assert.equal(gateway.childEnvironment.JUHE_AI_BUSINESS_OWNER, 'gateway')
    assert.equal(gateway.childEnvironment.JUHE_AI_BUSINESS_HANDOFF_CONFIRMED, 'true')
    assert.equal(gateway.childEnvironment.JUHE_AI_BUSINESS_NODE_WRITER_STOPPED, 'true')
    assert.equal(gateway.childEnvironment.JUHE_AI_BUSINESS_SCHEMA_READY, 'true')
    assert.equal(gateway.childEnvironment.JUHE_AI_BUSINESS_OWNER_EPOCH, 'epoch-launcher-regression')
    assert.equal(gateway.childEnvironment.JUHE_AI_BUSINESS_CUTOVER_EVIDENCE_PATH, './data/cutover-evidence.json')
    assert.equal(gateway.childEnvironment.JUHE_AI_BUSINESS_DATABASE_PATH, './data/business.sqlite3')
    assert.equal(gateway.childEnvironment.JUHE_AI_BUSINESS_POSTGRES_URL, 'postgres://business-owner')
    assert.equal(gateway.childEnvironment.JUHE_AI_GATEWAY_SYSTEM_API_ENABLED, undefined,
      '已移除的 SYSTEM_API 开关残留值必须被 launcher 剥离')
    assert.equal(gateway.childEnvironment.JUHE_AI_GATEWAY_CHAIN_ENABLED, undefined,
      '已移除的 CHAIN 开关残留值必须被 launcher 剥离')
  } finally {
    gateway.cleanup()
  }
}

function assertLauncherForwardsGatewayJobsOrigins() {
  const gateway = runLauncher('gateway', {
    JUHE_AI_AUDIT_LOG_INSTANCE_ID: 'f3-owner',
    JUHE_AI_OPERATION_LOG_INSTANCE_ID: 'f4-owner'
  }, [
    'JUHE_AI_DATABASE_DRIVER=sqlite',
    'JUHE_AI_GO_RUNTIME_METRICS_STORE=sqlite',
    'JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH=./data/go-runtime-metrics.sqlite3',
    // 去跨进程战役第二刀：健康检查派发改走 DB outbox，launcher 不得再转发
    // 已删除的 JUHE_AI_JOBS_INTERNAL_URL（该 env 进入 gateway 子进程即为缺陷）。
    'JUHE_AI_JOBS_INTERNAL_URL=http://127.0.0.1:4306',
    // 去跨进程战役第三刀：gateway 进程内自采样并直查共享 trend 库，跨进程
    // metrics URL 已删除，launcher 不得再转发（该 env 进入 gateway 子进程
    // 即为缺陷）。
    'JUHE_AI_GO_RUNTIME_METRICS_URL=http://127.0.0.1:4305',
    // 去跨进程战役第四刀：F3/F4 loopback ingest 监听器（3303/3304）与 J2
    // 手动桥已删除，历史 input env 进入子进程即为缺陷。
    'JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS=127.0.0.1:3303',
    'JUHE_AI_AUDIT_LOG_INPUT_SECRET=release-audit-input-secret-with-32-bytes',
    'JUHE_AI_AUDIT_LOG_INPUT_URL=http://127.0.0.1:3303',
    'JUHE_AI_OPERATION_LOG_INPUT_LISTEN_ADDRESS=127.0.0.1:3304',
    'JUHE_AI_OPERATION_LOG_INPUT_SECRET=release-operation-input-secret-32-bytes',
    'JUHE_AI_OPERATION_LOG_INPUT_URL=http://127.0.0.1:3304'
  ].join('\n'))
  try {
    assert.equal(gateway.status, 0, `gateway jobs-origin launcher failed: ${gateway.output}`)
    assert.equal(gateway.childEnvironment.JUHE_AI_GO_RUNTIME_METRICS_STORE, 'sqlite')
    assert.equal(gateway.childEnvironment.JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH, './data/go-runtime-metrics.sqlite3')
    assert.equal(gateway.childEnvironment.JUHE_AI_GO_RUNTIME_METRICS_URL, undefined,
      'the removed JUHE_AI_GO_RUNTIME_METRICS_URL must not be forwarded to the gateway child')
    assert.equal(gateway.childEnvironment.JUHE_AI_JOBS_INTERNAL_URL, undefined,
      'the removed JUHE_AI_JOBS_INTERNAL_URL must not be forwarded to the gateway child')
    for (const name of [
      'JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS', 'JUHE_AI_AUDIT_LOG_INPUT_SECRET', 'JUHE_AI_AUDIT_LOG_INPUT_URL',
      'JUHE_AI_OPERATION_LOG_INPUT_LISTEN_ADDRESS', 'JUHE_AI_OPERATION_LOG_INPUT_SECRET', 'JUHE_AI_OPERATION_LOG_INPUT_URL'
    ]) {
      assert.equal(gateway.childEnvironment[name], undefined,
        `the removed F3/F4 loopback input env ${name} must not be forwarded to the gateway child`)
    }
  } finally {
    gateway.cleanup()
  }
}

function assertLauncherForwardsGatewayRuntimeConfig() {
  const gateway = runLauncher('gateway', {
    JUHE_AI_AUDIT_LOG_INSTANCE_ID: 'f3-owner',
    JUHE_AI_OPERATION_LOG_INSTANCE_ID: 'f4-owner'
  }, [
    'JUHE_AI_DATABASE_DRIVER=sqlite',
    'JUHE_AI_HOST=127.0.0.1',
    'JUHE_AI_PORT=4300',
    'JUHE_AI_COOKIE_SECURE=true',
    'JUHE_AI_COOKIE_SAME_SITE=strict',
    'JUHE_AI_RUNTIME_STATE_DRIVER=redis',
    'JUHE_AI_REDIS_STATE_URL=redis://state.example.test:6379/9',
    'JUHE_AI_UNLISTED_FUTURE_GATEWAY_SETTING=kept'
  ].join('\n'))
  try {
    assert.equal(gateway.status, 0, `gateway runtime-config launcher failed: ${gateway.output}`)
    assert.equal(gateway.childEnvironment.JUHE_AI_HOST, '127.0.0.1')
    assert.equal(gateway.childEnvironment.JUHE_AI_PORT, '4300')
    assert.equal(gateway.childEnvironment.JUHE_AI_COOKIE_SECURE, 'true')
    assert.equal(gateway.childEnvironment.JUHE_AI_COOKIE_SAME_SITE, 'strict')
    assert.equal(gateway.childEnvironment.JUHE_AI_RUNTIME_STATE_DRIVER, 'redis')
    assert.equal(gateway.childEnvironment.JUHE_AI_REDIS_STATE_URL, 'redis://state.example.test:6379/9')
    assert.equal(gateway.childEnvironment.JUHE_AI_UNLISTED_FUTURE_GATEWAY_SETTING, 'kept')
    // 2026-09-19 零配置语义：SYSTEM_API/CHAIN 开关缺省 true，由 Go 侧解析；
    // launcher 未配置时不得注入。
    assert.equal(gateway.childEnvironment.JUHE_AI_GATEWAY_SYSTEM_API_ENABLED, undefined,
      'the launcher must not inject the gateway system API switch (Go defaults it to true)')
    assert.equal(gateway.childEnvironment.JUHE_AI_GATEWAY_CHAIN_ENABLED, undefined,
      'the launcher must not inject the gateway chain switch (Go defaults it to true)')
  } finally {
    gateway.cleanup()
  }
}

function assertReleaseScriptsCreateGoOnlyBackendRoot() {
  assert.match(
    shellSource,
    /mkdir -p backend\r?\nif \[ ! -f backend\/\.env \]; then/u,
    'Unix go-only startup must create the omitted backend root before writing backend/.env'
  )
  assert.match(
    powershellSource,
    /New-Item -ItemType Directory -Force 'backend' \| Out-Null\r?\nif \(-not \(Test-Path -LiteralPath 'backend\/\.env'\)\)/u,
    'Windows go-only startup must create the omitted backend root before writing backend/.env'
  )
}

function assertLauncherForwardsJ2PathsAndOwner() {
  const jobs = runLauncher('jobs', {
    // 2026-09-19 零配置收口：J1 必填项校验已删除，J2 显式配置原样透传
    //（2026-09-21 起 ENABLED 开关移除，家族由 PG 连接串激活）。
    JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER: 'go',
    JUHE_AI_ACCOUNT_BALANCE_OWNER_ID: 'j2-owner',
    JUHE_AI_ACCOUNT_BALANCE_STORE: 'postgres',
    JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL: 'postgres://j2-store',
    JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_URL: 'postgres://j2-input',
    JUHE_AI_ACCOUNT_BALANCE_CREDENTIAL_SECRET: 'j2-credential-secret',
    // 去跨进程战役第四刀回归注入：历史 env 残留的 J2 手动桥 secret 必须被
    // launcher 显式 drop，不得进入 jobs 子进程。
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
    assert.equal(jobs.childEnvironment.JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET, undefined,
      'the removed J2 manual-bridge secret must not be forwarded to the jobs child')
    assert.equal(jobs.childEnvironment.JUHE_AI_ACCOUNT_BALANCE_RECOVERY_BATCH_SIZE, '3')
    assert.equal(jobs.childEnvironment.JUHE_AI_ACCOUNT_BALANCE_CYCLE_BUDGET, '40s')
  } finally {
    jobs.cleanup()
  }
}

function assertLauncherForwardsGoRuntimeMetricsConfig() {
  const jobs = runLauncher('jobs', {
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
    // 2026-09-19 零配置收口：metrics 库路径不再被 launcher 绝对化，原样透传。
    assert.equal(jobs.childEnvironment.JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH, './data/go-runtime-metrics.sqlite3')
    assert.equal(jobs.childEnvironment.JUHE_AI_GO_RUNTIME_METRICS_INTERVAL, '15s')
    assert.equal(jobs.childEnvironment.JUHE_AI_GO_RUNTIME_METRICS_RETENTION_DAYS, '30')
    assert.equal(jobs.childEnvironment.JUHE_AI_GO_RUNTIME_METRICS_SERVICE, 'juhe-ai')
    assert.equal(jobs.childEnvironment.JUHE_AI_GO_RUNTIME_METRICS_ROLE, 'jobs')
  } finally {
    jobs.cleanup()
  }
}

function assertLauncherForwardsJ2StoreVerbatimToGo() {
  // 2026-09-21 起 launcher 层 J2 校验（含 STORE=postgres 强制）已移除：
  // store 合法性由 Go 加载期 fail-closed 判定，launcher 对显式配置只做
  // 原样透传，不再改写、不再拦截。
  const result = runLauncher('jobs', {
    JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER: 'go',
    JUHE_AI_ACCOUNT_BALANCE_OWNER_ID: 'j2-owner',
    JUHE_AI_ACCOUNT_BALANCE_STORE: 'sqlite',
    JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL: 'postgres://j2-store',
    JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_URL: 'postgres://j2-input',
    JUHE_AI_ACCOUNT_BALANCE_CREDENTIAL_SECRET: 'j2-credential-secret'
  })
  try {
    assert.equal(result.status, 0, `launcher 必须放行 J2 配置由 Go 侧校验: ${result.output}`)
    assert.equal(result.childEnvironment.JUHE_AI_ACCOUNT_BALANCE_STORE, 'sqlite')
    assert.equal(result.childEnvironment.JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL, 'postgres://j2-store')
  } finally {
    result.cleanup()
  }
}

function runLauncher(project, overrides, baseEnv = '') {
  const isolatedBackend = mkdtempSync(join(tmpdir(), `juhe-ai-release-go-${project}-launcher-regression-`))
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

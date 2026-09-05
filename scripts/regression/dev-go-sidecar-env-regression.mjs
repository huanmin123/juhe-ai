import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const repoRoot = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const scriptsRoot = join(repoRoot, 'scripts')
const sourcePath = join(scriptsRoot, 'dev.mjs')
const fixtureRoot = mkdtempSync(join(repoRoot, '.dev-go-project-env-regression-'))
const modulePath = join(scriptsRoot, `.dev-go-project-env-regression-${process.pid}-${Date.now()}.mjs`)
const environmentKeys = [
  'JUHE_AI_DATABASE_DRIVER',
  'JUHE_AI_POSTGRES_URL',
  'JUHE_AI_GO_RUNTIME_METRICS_ENABLED',
  'JUHE_AI_GO_RUNTIME_METRICS_STORE',
  'JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH',
  'JUHE_AI_GO_RUNTIME_METRICS_POSTGRES_URL',
  'JUHE_AI_GO_RUNTIME_METRICS_INTERVAL',
  'JUHE_AI_GO_RUNTIME_METRICS_RETENTION_DAYS',
  'JUHE_AI_GO_RUNTIME_METRICS_SERVICE',
  'JUHE_AI_GO_RUNTIME_METRICS_ROLE',
  'JUHE_AI_RUNTIME_LOG_STORE',
  'JUHE_AI_TABLE_MONITOR_STORE',
  'JUHE_AI_AUDIT_LOG_STORE',
  'JUHE_AI_OPERATION_LOG_STORE',
  'JUHE_AI_RUNTIME_LOG_INSTANCE_ID',
  'JUHE_AI_TABLE_MONITOR_INSTANCE_ID',
  'JUHE_AI_AUDIT_LOG_INSTANCE_ID',
  'JUHE_AI_AUDIT_LOG_INPUT_SECRET',
  'JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS',
  'JUHE_AI_AUDIT_LOG_INPUT_URL',
  'JUHE_AI_OPERATION_LOG_INSTANCE_ID',
  'JUHE_AI_OPERATION_LOG_INPUT_SECRET',
  'JUHE_AI_OPERATION_LOG_INPUT_LISTEN_ADDRESS',
  'JUHE_AI_OPERATION_LOG_INPUT_URL',
  'JUHE_AI_OPERATION_LOG_DATABASE_PATH',
  'JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH',
  'JUHE_AI_RUNTIME_LOG_DATABASE_PATH',
  'JUHE_AI_TABLE_MONITOR_DATABASE_PATH',
  'JUHE_AI_DATABASE_PATH',
  'JUHE_AI_DATASET_DATABASE_PATH',
  'JUHE_AI_USAGE_CATALOG_DATABASE_PATH',
  'JUHE_AI_STATS_DATABASE_PATH',
  'JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT'
]
const previousEnvironment = new Map(environmentKeys.map((key) => [key, process.env[key]]))

try {
  for (const key of environmentKeys) delete process.env[key]
  // Node 原行为差异：Node 时代的 dev.mjs 从 backend/.env 读取后端环境变量，
  // 相对路径以 backend root（backend/data/...）为基。backend/ 已随 X02 迁移
  // 物理删除，go-only 终态的 dev.mjs 改从仓库根 .env 读取（JUHE_AI_ENV_FILE
  // overlay 相对仓库根解析），dev 数据/日志相对路径统一落在 gitignored 的
  // .local/dev/{data,logs} 下。fixture 按终态语义构造。
  const fixtureDevDataRoot = join(fixtureRoot, '.local', 'dev', 'data')
  writeFileSync(join(fixtureRoot, '.env'), [
    'JUHE_AI_DATABASE_DRIVER=sqlite',
    'JUHE_AI_GO_RUNTIME_METRICS_STORE=sqlite',
    'JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH=./data/go-runtime-metrics.sqlite3',
    'JUHE_AI_GO_RUNTIME_METRICS_INTERVAL=15s',
    'JUHE_AI_GO_RUNTIME_METRICS_RETENTION_DAYS=30',
    'JUHE_AI_GO_RUNTIME_METRICS_SERVICE=juhe-ai',
    'JUHE_AI_GO_RUNTIME_METRICS_ROLE=jobs',
    'JUHE_AI_RUNTIME_LOG_DATABASE_PATH=./data/runtime-log.sqlite3',
    'JUHE_AI_TABLE_MONITOR_DATABASE_PATH=./data/table-monitor.sqlite3',
    'JUHE_AI_DATABASE_PATH=./data/juhe-ai.sqlite3',
    'JUHE_AI_DATASET_DATABASE_PATH=./data/juhe-ai-dataset.sqlite3',
    'JUHE_AI_USAGE_CATALOG_DATABASE_PATH=./data/juhe-ai-usage-catalog.sqlite3',
    'JUHE_AI_STATS_DATABASE_PATH=./data/juhe-ai-stats.sqlite3',
    'JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT=./data/codex-context/state-shards',
    'JUHE_AI_AUDIT_LOG_INSTANCE_ID=dev-audit-log',
    'JUHE_AI_AUDIT_LOG_INPUT_SECRET=dev-audit-log-input-secret-with-32-bytes',
    'JUHE_AI_OPERATION_LOG_INSTANCE_ID=dev-operation-log',
    'JUHE_AI_OPERATION_LOG_INPUT_SECRET=dev-operation-log-input-secret-with-32-bytes'
  ].join('\n'))

  writeFileSync(modulePath, buildTestableModule(readFileSync(sourcePath, 'utf8'), fixtureRoot))
  const module = await import(`${pathToFileURL(modulePath).href}?v=${Date.now()}`)
  const sidecarEnv = module.resolveGoProjectEnv()

  assert.equal(sidecarEnv.JUHE_AI_RUNTIME_LOG_STORE, 'sqlite')
  assert.equal(sidecarEnv.JUHE_AI_GO_RUNTIME_METRICS_STORE, 'sqlite')
  assert.equal(sidecarEnv.JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH, join(fixtureDevDataRoot, 'data', 'go-runtime-metrics.sqlite3'))
  assert.equal(sidecarEnv.JUHE_AI_GO_RUNTIME_METRICS_INTERVAL, '15s')
  assert.equal(sidecarEnv.JUHE_AI_GO_RUNTIME_METRICS_RETENTION_DAYS, '30')
  assert.equal(sidecarEnv.JUHE_AI_GO_RUNTIME_METRICS_SERVICE, 'juhe-ai')
  assert.equal(sidecarEnv.JUHE_AI_GO_RUNTIME_METRICS_ROLE, 'jobs')
  assert.equal(sidecarEnv.JUHE_AI_TABLE_MONITOR_STORE, 'sqlite')
  assert.equal(sidecarEnv.JUHE_AI_AUDIT_LOG_STORE, 'sqlite')
  assert.equal(sidecarEnv.JUHE_AI_OPERATION_LOG_STORE, 'sqlite')
  assert.equal(sidecarEnv.JUHE_AI_RUNTIME_LOG_INSTANCE_ID, 'dev-go-jobs-runtime-log')
  assert.equal(sidecarEnv.JUHE_AI_TABLE_MONITOR_INSTANCE_ID, 'dev-go-jobs-table-monitor')
  assert.equal(sidecarEnv.JUHE_AI_OPERATION_LOG_INPUT_LISTEN_ADDRESS, '127.0.0.1:3304')
  assert.equal(sidecarEnv.JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS, '127.0.0.1:3305')
  assert.equal(sidecarEnv.JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS, '127.0.0.1:3306')
  assert.equal(sidecarEnv.JUHE_AI_AUDIT_LOG_INSTANCE_ID, 'dev-audit-log')
  assert.equal(sidecarEnv.JUHE_AI_OPERATION_LOG_INSTANCE_ID, 'dev-operation-log')
  assert.equal(sidecarEnv.JUHE_AI_OPERATION_LOG_INPUT_LISTEN_ADDRESS, '127.0.0.1:3304')
  assert.equal(sidecarEnv.JUHE_AI_OPERATION_LOG_INPUT_URL, 'http://127.0.0.1:3304')
  assert.equal(sidecarEnv.JUHE_AI_TABLE_MONITOR_DATABASE_PATH, join(fixtureDevDataRoot, 'data', 'table-monitor.sqlite3'))
  assert.equal(sidecarEnv.JUHE_AI_USAGE_CATALOG_DATABASE_PATH, join(fixtureDevDataRoot, 'data', 'juhe-ai-usage-catalog.sqlite3'))
  assert.equal(sidecarEnv.JUHE_AI_STATS_DATABASE_PATH, join(fixtureDevDataRoot, 'data', 'juhe-ai-stats.sqlite3'))
  assert.equal(sidecarEnv.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT, join(fixtureDevDataRoot, 'data', 'codex-context', 'state-shards'))
  assert.equal(sidecarEnv.JUHE_AI_RUNTIME_LOG_DATABASE_PATH, join(fixtureDevDataRoot, 'data', 'runtime-log.sqlite3'))
  assert.equal(sidecarEnv.JUHE_AI_OPERATION_LOG_DATABASE_PATH, join(fixtureDevDataRoot, 'juhe-ai-operation-log.sqlite3'))
  assert.equal(sidecarEnv.JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH, join(fixtureDevDataRoot, 'data', 'juhe-ai.sqlite3'))

  const postgresFixture = readFileSync(join(fixtureRoot, '.env'), 'utf8')
    .replace(/^JUHE_AI_GO_RUNTIME_METRICS_STORE=.*(?:\r?\n|$)/mu, 'JUHE_AI_GO_RUNTIME_METRICS_STORE=postgres\n')
    .replace(/^JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH=.*(?:\r?\n|$)/mu, '')
    + '\nJUHE_AI_POSTGRES_URL=postgres://dev.example/juhe_ai\n'
  writeFileSync(join(fixtureRoot, '.env'), postgresFixture)
  const postgresEnv = module.resolveGoProjectEnv()
  assert.equal(postgresEnv.JUHE_AI_GO_RUNTIME_METRICS_STORE, 'postgres')
  assert.equal(postgresEnv.JUHE_AI_GO_RUNTIME_METRICS_POSTGRES_URL, 'postgres://dev.example/juhe_ai')

  delete process.env.JUHE_AI_AUDIT_LOG_INSTANCE_ID
  delete process.env.JUHE_AI_AUDIT_LOG_INPUT_SECRET
  delete process.env.JUHE_AI_OPERATION_LOG_INSTANCE_ID
  delete process.env.JUHE_AI_OPERATION_LOG_INPUT_SECRET
  writeFileSync(join(fixtureRoot, '.env'), readFileSync(join(fixtureRoot, '.env'), 'utf8')
    .replace(/^JUHE_AI_AUDIT_LOG_INSTANCE_ID=.*(?:\r?\n|$)/mu, '')
    .replace(/^JUHE_AI_AUDIT_LOG_INPUT_SECRET=.*(?:\r?\n|$)/mu, '')
    .replace(/^JUHE_AI_OPERATION_LOG_INSTANCE_ID=.*(?:\r?\n|$)/mu, '')
    .replace(/^JUHE_AI_OPERATION_LOG_INPUT_SECRET=.*(?:\r?\n|$)/mu, ''))
  const generatedEnv = module.resolveGoProjectEnv()
  assert.equal(generatedEnv.JUHE_AI_AUDIT_LOG_INSTANCE_ID, `dev-go-gateway-audit-log-pid-${process.pid}`)
  assert.equal(generatedEnv.JUHE_AI_OPERATION_LOG_INSTANCE_ID, `dev-go-gateway-operation-log-pid-${process.pid}`)
  assert.match(generatedEnv.JUHE_AI_AUDIT_LOG_INPUT_SECRET, /^[a-f0-9]{64}$/u)
  assert.match(generatedEnv.JUHE_AI_OPERATION_LOG_INPUT_SECRET, /^[a-f0-9]{64}$/u)
  assert.notEqual(generatedEnv.JUHE_AI_AUDIT_LOG_INPUT_SECRET, generatedEnv.JUHE_AI_OPERATION_LOG_INPUT_SECRET)
} finally {
  for (const [key, value] of previousEnvironment) {
    if (value === undefined) delete process.env[key]
    else process.env[key] = value
  }
  rmSync(modulePath, { force: true })
  rmSync(fixtureRoot, { recursive: true, force: true })
}

console.log('dev Go project environment regression passed')

function buildTestableModule(source, root) {
  const startup = /try \{[\s\S]*?\r?\nawait new Promise\(\(\) => undefined\)\r?\n\r?\n/u
  const withoutStartup = source.replace(startup, '')
  assert.notEqual(withoutStartup, source, 'could not remove dev process startup block for resolver regression')
  const rootDeclaration = "const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')"
  assert.match(withoutStartup, new RegExp(rootDeclaration.replace(/[.*+?^${}()|[\]\\]/gu, '\\$&'), 'u'))
  const fixtureSource = withoutStartup.replace(rootDeclaration, `const root = ${JSON.stringify(root)}`)
  return `${fixtureSource}\nexport { resolveGoProjectEnv }\n`
}

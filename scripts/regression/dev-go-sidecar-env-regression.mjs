import assert from 'node:assert/strict'
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const repoRoot = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const scriptsRoot = join(repoRoot, 'scripts')
const sourcePath = join(scriptsRoot, 'dev.mjs')
const fixtureRoot = mkdtempSync(join(repoRoot, '.dev-go-project-env-regression-'))
const modulePath = join(scriptsRoot, `.dev-go-project-env-regression-${process.pid}-${Date.now()}.mjs`)
// 2026-09-19 零配置终态：resolveGoProjectEnv 只钉住数据/日志根、注入 owner
// 租约启动参数并 drop 已删除的 F3/F4 loopback input env；其余后端 env 从仓
// 库根 .env / 父进程环境原样转发，不再改写逐库路径或生成 instance id。
const environmentKeys = [
  'JUHE_AI_DATA_DIR',
  'JUHE_AI_LOG_DIR',
  'JUHE_AI_DATABASE_DRIVER',
  'JUHE_AI_POSTGRES_URL',
  'JUHE_AI_GO_RUNTIME_METRICS_INTERVAL',
  'JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT',
  'JUHE_AI_AUDIT_LOG_OWNER_LEASE',
  'JUHE_AI_OPERATION_LOG_OWNER_LEASE',
  'JUHE_AI_RUNTIME_LOG_OWNER_LEASE',
  'JUHE_AI_TABLE_MONITOR_OWNER_LEASE',
  'JUHE_AI_AUDIT_LOG_INPUT_SECRET',
  'JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS',
  'JUHE_AI_AUDIT_LOG_INPUT_URL',
  'JUHE_AI_OPERATION_LOG_INPUT_SECRET',
  'JUHE_AI_OPERATION_LOG_INPUT_LISTEN_ADDRESS',
  'JUHE_AI_OPERATION_LOG_INPUT_URL'
]
const previousEnvironment = new Map(environmentKeys.map((key) => [key, process.env[key]]))

try {
  for (const key of environmentKeys) delete process.env[key]
  // go-only 终态：dev.mjs 从仓库根 .env 读取后端环境变量（JUHE_AI_ENV_FILE
  // overlay 相对仓库根解析），fixture 按零配置语义构造：逐库路径不进入
  // fixture（旧式注入会与 Go 固定名表脱节，破坏 gateway 零配置自举建库）。
  writeFileSync(join(fixtureRoot, '.env'), [
    'JUHE_AI_DATABASE_DRIVER=sqlite',
    'JUHE_AI_GO_RUNTIME_METRICS_INTERVAL=15s',
    // 去跨进程战役第四刀回归注入：历史 .env 残留的 F3/F4 loopback input env
    // 必须被 dev 启动器显式 drop，不得进入 Go 子进程。
    'JUHE_AI_AUDIT_LOG_INPUT_SECRET=dev-audit-log-input-secret-with-32-bytes',
    'JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS=127.0.0.1:3303',
    'JUHE_AI_AUDIT_LOG_INPUT_URL=http://127.0.0.1:3303',
    'JUHE_AI_OPERATION_LOG_INPUT_SECRET=dev-operation-log-input-secret-with-32-bytes',
    'JUHE_AI_OPERATION_LOG_INPUT_LISTEN_ADDRESS=127.0.0.1:3304',
    'JUHE_AI_OPERATION_LOG_INPUT_URL=http://127.0.0.1:3304'
  ].join('\n'))

  writeFileSync(modulePath, buildTestableModule(readFileSync(sourcePath, 'utf8'), fixtureRoot))
  const module = await import(`${pathToFileURL(modulePath).href}?v=${Date.now()}`)
  const sidecarEnv = module.resolveGoProjectEnv()

  // .env 值原样转发（零配置下不再改写逐库路径）。
  assert.equal(sidecarEnv.JUHE_AI_DATABASE_DRIVER, 'sqlite')
  assert.equal(sidecarEnv.JUHE_AI_GO_RUNTIME_METRICS_INTERVAL, '15s')
  // 数据/日志根钉住 dev 专属目录并确保存在（SQLite 无法在不存在的目录建库，
  // 删库冷启动必须自愈）。
  assert.equal(sidecarEnv.JUHE_AI_DATA_DIR, join(fixtureRoot, '.local', 'dev', 'data'))
  assert.equal(sidecarEnv.JUHE_AI_LOG_DIR, join(fixtureRoot, '.local', 'dev', 'logs'))
  assert.ok(existsSync(sidecarEnv.JUHE_AI_DATA_DIR), 'dev data root must exist before gateway cold start')
  assert.ok(existsSync(sidecarEnv.JUHE_AI_LOG_DIR), 'dev log root must exist before gateway cold start')
  // owner 租约启动参数默认注入：旧进程被强杀（taskkill /f、关终端窗口、
  // Windows 8s 宽限兜底）后租约未释放；gateway F3/F4 注入有界等待让立即重
  // 启自动接管而不是 fail-fast，jobs F1/F2 靠 supervisor 重试。dev TTL 统一
  // 收紧到 10s（F3/F4 与 F1 生产默认 30s、F2 生产默认 5 分钟）让接管等待以
  // 秒计。
  assert.equal(sidecarEnv.JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT, '45s')
  assert.equal(sidecarEnv.JUHE_AI_AUDIT_LOG_OWNER_LEASE, '10s')
  assert.equal(sidecarEnv.JUHE_AI_OPERATION_LOG_OWNER_LEASE, '10s')
  assert.equal(sidecarEnv.JUHE_AI_RUNTIME_LOG_OWNER_LEASE, '10s')
  assert.equal(sidecarEnv.JUHE_AI_TABLE_MONITOR_OWNER_LEASE, '10s')
  // 去跨进程战役第四刀：F3/F4 loopback input env 必须显式 drop（不得转发）。
  assert.equal(sidecarEnv.JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS, undefined, 'the removed audit input listen address must not be forwarded')
  assert.equal(sidecarEnv.JUHE_AI_AUDIT_LOG_INPUT_SECRET, undefined, 'the removed audit input secret must not be forwarded')
  assert.equal(sidecarEnv.JUHE_AI_AUDIT_LOG_INPUT_URL, undefined, 'the removed audit input URL must not be forwarded')
  assert.equal(sidecarEnv.JUHE_AI_OPERATION_LOG_INPUT_LISTEN_ADDRESS, undefined, 'the removed operation input listen address must not be forwarded')
  assert.equal(sidecarEnv.JUHE_AI_OPERATION_LOG_INPUT_SECRET, undefined, 'the removed operation input secret must not be forwarded')
  assert.equal(sidecarEnv.JUHE_AI_OPERATION_LOG_INPUT_URL, undefined, 'the removed operation input URL must not be forwarded')

  // .env overlay 仍生效：新增条目原样转发，不破坏既有驱动转发契约。
  const postgresFixture = readFileSync(join(fixtureRoot, '.env'), 'utf8')
    + '\nJUHE_AI_POSTGRES_URL=postgres://dev.example/juhe_ai\n'
  writeFileSync(join(fixtureRoot, '.env'), postgresFixture)
  const postgresEnv = module.resolveGoProjectEnv()
  assert.equal(postgresEnv.JUHE_AI_DATABASE_DRIVER, 'sqlite')
  assert.equal(postgresEnv.JUHE_AI_POSTGRES_URL, 'postgres://dev.example/juhe_ai')

  // 用户显式配置优先于 dev 默认：进程环境与 .env 两个来源都要生效。
  process.env.JUHE_AI_AUDIT_LOG_OWNER_LEASE = '7s'
  const explicitProcessEnv = module.resolveGoProjectEnv()
  assert.equal(explicitProcessEnv.JUHE_AI_AUDIT_LOG_OWNER_LEASE, '7s')
  process.env.JUHE_AI_TABLE_MONITOR_OWNER_LEASE = '15s'
  const explicitJobsLeaseEnv = module.resolveGoProjectEnv()
  assert.equal(explicitJobsLeaseEnv.JUHE_AI_TABLE_MONITOR_OWNER_LEASE, '15s')
  delete process.env.JUHE_AI_AUDIT_LOG_OWNER_LEASE
  delete process.env.JUHE_AI_TABLE_MONITOR_OWNER_LEASE
  writeFileSync(join(fixtureRoot, '.env'), readFileSync(join(fixtureRoot, '.env'), 'utf8')
    + 'JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT=20s\n')
  const explicitDotEnv = module.resolveGoProjectEnv()
  assert.equal(explicitDotEnv.JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT, '20s')

  // 租约等待的人话提示：Info 等待行触发并携带租约标签；普通日志行与
  // fail-fast error 行（不含 "waiting for predecessor lease expiry"）不触发。
  const waitLine = '{"level":"INFO","msg":"owner lease held by another owner process, waiting for predecessor lease expiry","lease":"F3 audit","waitBudget":"45s"}'
  const notice = module.leaseWaitNotice(waitLine)
  assert.ok(notice?.note.includes('F3 audit'), 'the waiting log line must produce a user note naming the lease')
  assert.equal(module.leaseWaitNotice('{"level":"INFO","msg":"juhe-ai-gateway started"}'), undefined)
  assert.equal(module.leaseWaitNotice('F3 audit owner lease held by another owner process'), undefined, 'the fail-fast error line must not produce the waiting note')

  // jobs 租约重试的人话提示：supervisor 的 ERROR 重试行触发并携带组件名；
  // 普通日志行与"获取…失败"的真实 DB 错误行（不含"已由另一个 Go 实例持有"）
  // 不触发。
  const retryLine = '{"time":"2026-09-23T21:21:41.7498865+08:00","level":"ERROR","msg":"sidecar component failed; retrying","component":"F2 table-monitor","cause":"表监控 owner lease 已由另一个 Go 实例持有","consecutiveFailures":1,"retryDelay":"1s"}'
  const jobsNotice = module.jobsLeaseRetryNotice(retryLine)
  assert.ok(jobsNotice?.note.includes('F2 table-monitor'), 'the retry log line must produce a user note naming the component')
  const f1RetryLine = '{"level":"ERROR","msg":"sidecar component failed; retrying","component":"F1 runtime-log-indexer","cause":"运行日志 owner lease 已由另一个 Go 实例持有","consecutiveFailures":2,"retryDelay":"2s"}'
  assert.ok(module.jobsLeaseRetryNotice(f1RetryLine)?.note.includes('F1 runtime-log-indexer'))
  assert.equal(module.jobsLeaseRetryNotice('{"level":"INFO","msg":"F2 table-monitor sample complete"}'), undefined)
  assert.equal(module.jobsLeaseRetryNotice('{"level":"ERROR","msg":"sidecar component failed; retrying","component":"F2 table-monitor","cause":"获取表监控 owner lease 失败: open db"}'), undefined, 'the db failure line must not produce the lease retry note')
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
  return `${fixtureSource}\nexport { leaseWaitNotice, jobsLeaseRetryNotice, resolveGoProjectEnv }\n`
}

import { closeSync, existsSync, openSync, readFileSync } from 'node:fs'
import { isAbsolute, resolve } from 'node:path'
import { spawn } from 'node:child_process'

const [project, binaryPath, backendRoot, logPath] = process.argv.slice(2)
if (!['gateway', 'jobs'].includes(project) || !binaryPath || !backendRoot || !logPath) {
  throw new Error('Usage: node scripts/start-go-project.mjs <gateway|jobs> <binary-path> <backend-root> <log-path>')
}

// X02/X03 go-only：Node backend 已归档，go-only 发布包不再携带 backend 的
// node_modules，dotenv 无法再从 backend/package.json 解析。这里内联一个
// 覆盖 dotenv 基础语义（KEY=VALUE、export 前缀、单双引号、行内/整行注释、
// 空值 trim）的最小解析器，启动器自此零第三方依赖。
function parseEnvFile(content) {
  const parsed = {}
  for (const rawLine of content.split(/\r?\n/)) {
    let line = rawLine.trim()
    if (!line || line.startsWith('#')) continue
    if (line.startsWith('export ')) line = line.slice('export '.length).trim()
    const equalsIndex = line.indexOf('=')
    if (equalsIndex <= 0) continue
    const key = line.slice(0, equalsIndex).trim()
    if (!/^[A-Za-z_][A-Za-z0-9_.]*$/.test(key)) continue
    let value = line.slice(equalsIndex + 1).trim()
    if (value.length >= 2 && ((value.startsWith('"') && value.endsWith('"')) || (value.startsWith("'") && value.endsWith("'")))) {
      value = value.slice(1, -1)
    } else {
      const hashIndex = value.indexOf(' #')
      if (hashIndex !== -1) value = value.slice(0, hashIndex).trim()
    }
    parsed[key] = value
  }
  return parsed
}

const readEnv = (path) => existsSync(path) ? parseEnvFile(readFileSync(path, 'utf8')) : {}
const baseEnvPath = resolve(backendRoot, '.env')
const capacityEnvPath = resolve(backendRoot, '.env.capacity')
const disableBaseEnv = String(process.env.JUHE_AI_DISABLE_BASE_ENV ?? '').trim().toLowerCase() === 'true'
const baseEnv = disableBaseEnv ? {} : readEnv(baseEnvPath)
const overlayName = (process.env.JUHE_AI_ENV_FILE ?? baseEnv.JUHE_AI_ENV_FILE ?? '').trim()
const overlayEnv = overlayName ? readEnv(isAbsolute(overlayName) ? overlayName : resolve(backendRoot, overlayName)) : {}
const capacityEnv = Object.fromEntries(Object.entries(readEnv(capacityEnvPath)).filter(([name]) => isCapacityEnvironmentVariable(name)))

function isCapacityEnvironmentVariable(name) {
  return name.startsWith('JUHE_AI_CONCURRENCY_')
    || name.startsWith('JUHE_AI_ACCOUNT_')
    || name.startsWith('JUHE_AI_BACKGROUND_')
    || name.startsWith('JUHE_AI_GATEWAY_')
    || name.startsWith('JUHE_AI_DB_')
    || name.startsWith('JUHE_AI_CHAT_DB_SERVICE_')
    || name.startsWith('JUHE_AI_REDIS_STREAM_')
    || name.startsWith('JUHE_AI_USAGE_SPOOL_')
    || name === 'JUHE_AI_SYSTEM_API_DB_SERVICE_MAX_IN_FLIGHT'
    || /^JUHE_AI_(GATEWAY|USAGE|LOG|STATS|OPS)_WORKER_REPLICAS$/u.test(name)
}

function configured(name) {
  if (Object.hasOwn(process.env, name)) return { defined: true, value: process.env[name] ?? '' }
  if (Object.hasOwn(overlayEnv, name)) return { defined: true, value: overlayEnv[name] ?? '' }
  if (Object.hasOwn(capacityEnv, name)) return { defined: true, value: capacityEnv[name] ?? '' }
  if (Object.hasOwn(baseEnv, name)) return { defined: true, value: baseEnv[name] ?? '' }
  return { defined: false, value: '' }
}

function absoluteBackendPath(value, fallback) {
  const selected = String(value || fallback).trim()
  return isAbsolute(selected) ? selected : resolve(backendRoot, selected)
}

const commonNames = [
  // 2026-10-24/25 存储形态收口：PostgreSQL + Redis 唯一形态，JUHE_AI_RUNTIME_MODE /
  // JUHE_AI_DATABASE_DRIVER 等模式开关与 SQLite 路径族已退役并进入下方拒转清单。
  'JUHE_AI_POSTGRES_URL', 'JUHE_AI_LOG_DIR',
  // 2026-09-21 起 JUHE_AI_LOG_FILE_ENABLED 开关移除：日志文件恒开。
  'JUHE_AI_LOG_RETENTION_DAYS', 'JUHE_AI_LOG_MAX_FILES',
  'JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER'
]
const projectNames = project === 'jobs'
  ? [
      'JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS',
      'JUHE_AI_RUNTIME_LOG_POSTGRES_URL',
      'JUHE_AI_RUNTIME_LOG_INSTANCE_ID', 'JUHE_AI_RUNTIME_LOG_OWNER_LEASE', 'JUHE_AI_RUNTIME_LOG_ONCE',
      'JUHE_AI_RUNTIME_LOG_POLL_INTERVAL', 'JUHE_AI_RUNTIME_LOG_RETENTION_INTERVAL',
      'JUHE_AI_RUNTIME_LOG_RETENTION_DAYS', 'JUHE_AI_RUNTIME_LOG_BATCH_SIZE',
      // 2026-10-14 监控改造：JUHE_AI_TABLE_MONITOR_* 与 JUHE_AI_GO_RUNTIME_METRICS_*
      // 两家族已随 F2 表监控组件与 gometrics 采样链删除，不再转发；残留值按
      // 前缀拒转清单剥离。
      'JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID',
      'JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL',
      'JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY', 'JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY',
      'JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY_ID',
      'JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL', 'JUHE_AI_ACCOUNT_HEALTH_DIRECT_INPUT_LIMIT',
      'JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET', 'JUHE_AI_ACCOUNT_HEALTH_SCAN_INTERVAL',
      'JUHE_AI_ACCOUNT_HEALTH_OWNER_LEASE', 'JUHE_AI_ACCOUNT_HEALTH_PROBE_TIMEOUT',
      'JUHE_AI_ACCOUNT_HEALTH_MAX_RESPONSE_BYTES', 'JUHE_AI_ACCOUNT_HEALTH_MAX_CONCURRENCY',
      // 2026-09-21 起 JUHE_AI_ACCOUNT_BALANCE_ENABLED 开关移除：J2 余额检测
      // 依赖 PG 连接串即激活，恒开。
      'JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER', 'JUHE_AI_ACCOUNT_BALANCE_OWNER_ID',
      'JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL',
      'JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_URL', 'JUHE_AI_ACCOUNT_BALANCE_CREDENTIAL_SECRET',
      'JUHE_AI_ACCOUNT_BALANCE_SCAN_INTERVAL', 'JUHE_AI_ACCOUNT_BALANCE_OWNER_LEASE', 'JUHE_AI_ACCOUNT_BALANCE_ACCOUNT_LEASE',
      'JUHE_AI_ACCOUNT_BALANCE_INPUT_TTL', 'JUHE_AI_ACCOUNT_BALANCE_PROBE_TIMEOUT',
      'JUHE_AI_ACCOUNT_BALANCE_MAX_RESPONSE_BYTES', 'JUHE_AI_ACCOUNT_BALANCE_MAX_CONCURRENCY', 'JUHE_AI_ACCOUNT_BALANCE_BATCH_SIZE',
      'JUHE_AI_ACCOUNT_BALANCE_RECOVERY_BATCH_SIZE', 'JUHE_AI_ACCOUNT_BALANCE_CYCLE_BUDGET'
    ]
  : [
      'JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS',
      // 2026-09-21 起 JUHE_AI_GATEWAY_SYSTEM_API_ENABLED / JUHE_AI_GATEWAY_CHAIN_ENABLED
      // 开关移除：系统 API 与组合链恒开。
      // 2026-09-30 Node→Go 切流门禁 env 家族（BUSINESS_OWNER / HANDOFF_CONFIRMED /
      // CUTOVER_EVIDENCE_PATH 等）随迁移完成整体退役；BUSINESS_POSTGRES_URL 仍在役
      // （缺省回落 JUHE_AI_POSTGRES_URL）。
      'JUHE_AI_BUSINESS_POSTGRES_URL',
      // 2026-10-14 监控改造：gateway 进程内 gometrics 自采样与 go-runtime-trend
      // 读接口已删除，JUHE_AI_GO_RUNTIME_METRICS_* / JUHE_AI_TABLE_MONITOR_*
      // 两家族不再转发；残留值按前缀拒转清单剥离。
      'JUHE_AI_AUDIT_LOG_POSTGRES_URL',
      'JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY', 'JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY',
      'JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH', 'JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL',
      'JUHE_AI_AUDIT_LOG_POSTGRES_SCHEMA', 'JUHE_AI_AUDIT_LOG_INSTANCE_ID', 'JUHE_AI_AUDIT_LOG_OWNER_LEASE',
      'JUHE_AI_AUDIT_LOG_RETENTION_INTERVAL', 'JUHE_AI_AUDIT_LOG_RETENTION_BATCH_SIZE',
      'JUHE_AI_OPERATION_LOG_POSTGRES_URL',
      'JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH', 'JUHE_AI_OPERATION_LOG_INSTANCE_ID',
      'JUHE_AI_OPERATION_LOG_OWNER_LEASE', 'JUHE_AI_OPERATION_LOG_RETENTION_INTERVAL',
      'JUHE_AI_OPERATION_LOG_RETENTION_BATCH_SIZE'
    ]

const env = { ...process.env }
// Go-only release packages configure both projects through backend/.env. The
// original sidecar launcher forwarded a fixed subset, which silently dropped
// newly migrated Go settings (for example host/port and Redis drivers). Keep
// the documented precedence in configured(): a caller-provided environment
// variable still wins over the overlay, capacity file, and base file.
const configuredJuheNames = new Set([
  ...Object.keys(baseEnv),
  ...Object.keys(capacityEnv),
  ...Object.keys(overlayEnv)
].filter((name) => name.startsWith('JUHE_AI_')))
for (const name of configuredJuheNames) {
  const value = configured(name)
  if (value.defined) env[name] = value.value
}
// The explicit lists below retain the startup-only responsibilities that need
// validation or path normalization; they no longer limit .env forwarding.
for (const name of [...commonNames, ...projectNames]) {
  const value = configured(name)
  if (value.defined) env[name] = value.value
}
// 已删除的跨进程互调 env（去跨进程战役第二/三/四刀）：即使残留在历史 .env
// 或父进程环境中也不得进入 Go 子进程——gateway/jobs 之间已无任何 HTTP 互调，
// F3/F4 loopback ingest 监听器（3303/3304）与 J2 手动桥已删除，这些变量进
// 入子进程即为缺陷。
const removedCrossProcessEnvNames = [
  'JUHE_AI_JOBS_INTERNAL_URL',
  'JUHE_AI_GO_RUNTIME_METRICS_URL',
  'JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS', 'JUHE_AI_AUDIT_LOG_INPUT_SECRET', 'JUHE_AI_AUDIT_LOG_INPUT_URL',
  'JUHE_AI_AUDIT_LOG_INPUT_MAX_BYTES', 'JUHE_AI_AUDIT_LOG_INPUT_TIMEOUT',
  'JUHE_AI_OPERATION_LOG_INPUT_LISTEN_ADDRESS', 'JUHE_AI_OPERATION_LOG_INPUT_SECRET', 'JUHE_AI_OPERATION_LOG_INPUT_URL',
  'JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET'
]
for (const name of removedCrossProcessEnvNames) delete env[name]

// 2026-10-14 监控改造：gometrics 采样链与 F2 表监控组件整体删除，两家族 env
// 全部退役——按前缀拒转，即使残留在历史 .env 或父进程环境中也不得进入 Go
// 子进程（含既有拒转条目 JUHE_AI_GO_RUNTIME_METRICS_URL / _ENABLED 覆盖面）。
const removedMonitoringEnvPrefixes = ['JUHE_AI_GO_RUNTIME_METRICS_', 'JUHE_AI_TABLE_MONITOR_']
for (const prefix of removedMonitoringEnvPrefixes) {
  for (const name of Object.keys(env)) {
    if (name.startsWith(prefix)) delete env[name]
  }
}

// 2026-09-19 J1/worker 启用开关移除：J1 账户健康检查与 worker 任务族改为
// 强制常开（Go 侧缺配置 fail-fast），历史开关值即使残留在历史 .env 或父进程
// 环境中也不得进入 Go 子进程，避免制造仍可开关的假象。
// 同日零配置收口：7 个 worker 任务族开关与 6 个 retention 子开关一并删除、
// 任务恒开，残留值同款处理，不进入 Go 子进程。
const removedLegacySwitchEnvNames = [
  'JUHE_AI_ACCOUNT_HEALTH_ENABLED',
  'JUHE_AI_JOBS_WORKER_ENABLED',
  'JUHE_AI_JOBS_STATS_ENABLED',
  'JUHE_AI_JOBS_OAUTH_ENABLED',
  'JUHE_AI_JOBS_TASK_RUNS_ENABLED',
  'JUHE_AI_JOBS_USAGE_WRITER_ENABLED',
  'JUHE_AI_JOBS_BALANCE_DETECT_ENABLED',
  'JUHE_AI_JOBS_RETENTION_ENABLED',
  'JUHE_AI_JOBS_RETENTION_CHAT_ENABLED',
  'JUHE_AI_JOBS_RETENTION_DATA_ENABLED',
  'JUHE_AI_JOBS_RETENTION_RECORD_MAINTENANCE_ENABLED',
  'JUHE_AI_JOBS_RETENTION_EXPIRED_ACCOUNT_ENABLED',
  'JUHE_AI_JOBS_RETENTION_API_KEY_RETRY_ENABLED',
  'JUHE_AI_JOBS_RETENTION_ACCOUNT_RETRY_ENABLED',
  'JUHE_AI_JOBS_PROBE_ENABLED',
  // 2026-09-21 功能开关清零：以下开关已在 Go 侧移除，残留值不进入子进程，
  // 避免制造仍可开关的假象。
  'JUHE_AI_GATEWAY_SYSTEM_API_ENABLED',
  'JUHE_AI_GATEWAY_CHAIN_ENABLED',
  'JUHE_AI_CHAT_DIAGNOSTIC_TOOL_ENABLED',
  'JUHE_AI_AUDIT_LOG_ENABLED',
  'JUHE_AI_LOG_FILE_ENABLED',
  'JUHE_AI_J3B_ENABLED',
  'JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_ENABLED',
  'JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_READ_ENABLED',
  'JUHE_AI_PROXY_LATENCY_ENABLED',
  // 注意：JUHE_AI_PROXY_LATENCY_MANAGEMENT_ENABLED 不在剥离列表——它是仍在
  // 服役的部署面监听参数（J3a 管理 API 由 CI 显式启停），并非功能开关。
  'JUHE_AI_ACCOUNT_BALANCE_ENABLED',
  'JUHE_AI_GO_RUNTIME_METRICS_ENABLED',
  'JUHE_AI_ACCOUNT_HEALTH_JOBS_PROJECTION_DISABLED'
]
for (const name of removedLegacySwitchEnvNames) delete env[name]

// 2026-10-24/25 存储形态收口（PLAN-20261024T051925945Z）：SQLite + memory 本地
// 模式物理退役，PostgreSQL + Redis 是唯一存储形态——运行模式四开关、SQLite 路径
// 族、分片族与 store 选择族 env 全部退役，Go 侧已不读取。即使残留在历史 .env 或
// 父进程环境中也不得进入 Go 子进程，避免制造"仍在配置"的假象。
const removedSqliteModeEnvNames = [
  'JUHE_AI_RUNTIME_MODE', 'JUHE_AI_DATABASE_DRIVER', 'JUHE_AI_CACHE_DRIVER', 'JUHE_AI_RUNTIME_STATE_DRIVER',
  'JUHE_AI_DATABASE_PATH', 'JUHE_AI_BUSINESS_DATABASE_PATH', 'JUHE_AI_CHAT_DATABASE_PATH',
  'JUHE_AI_DATASET_DATABASE_PATH', 'JUHE_AI_RUNTIME_LOG_DATABASE_PATH', 'JUHE_AI_USAGE_CATALOG_DATABASE_PATH',
  'JUHE_AI_STATS_DATABASE_PATH', 'JUHE_AI_ACCOUNT_BALANCE_DATABASE_PATH', 'JUHE_AI_AUDIT_LOG_DATABASE_PATH',
  'JUHE_AI_OPERATION_LOG_DATABASE_PATH', 'JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH',
  'JUHE_AI_ACCOUNT_HEALTH_JOBS_OUTCOME_SQLITE_PATH', 'JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH',
  'JUHE_AI_AUDIT_LOG_STORE', 'JUHE_AI_OPERATION_LOG_STORE', 'JUHE_AI_RUNTIME_LOG_STORE',
  'JUHE_AI_J3B_STORE', 'JUHE_AI_ACCOUNT_HEALTH_STORE', 'JUHE_AI_ACCOUNT_BALANCE_STORE',
  'JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE'
]
const removedSqliteModeEnvPrefixes = ['JUHE_AI_USAGE_SHARD_', 'JUHE_AI_CODEX_CONTEXT_STATE_SHARD_']
for (const name of removedSqliteModeEnvNames) delete env[name]
for (const prefix of removedSqliteModeEnvPrefixes) {
  for (const name of Object.keys(env)) {
    if (name.startsWith(prefix)) delete env[name]
  }
}

// 2026-09-30 Node→Go 切流完成时整体退役的切流门禁 env 家族（基线 §6）：残留值
// 即使在历史 .env 或父进程环境中也不得进入 Go 子进程。
const removedCutoverGateEnvNames = [
  'JUHE_AI_BUSINESS_OWNER', 'JUHE_AI_BUSINESS_HANDOFF_CONFIRMED',
  'JUHE_AI_BUSINESS_NODE_WRITER_STOPPED', 'JUHE_AI_BUSINESS_SCHEMA_READY',
  'JUHE_AI_BUSINESS_OWNER_EPOCH', 'JUHE_AI_BUSINESS_CUTOVER_EVIDENCE_PATH'
]
for (const name of removedCutoverGateEnvNames) delete env[name]

// 2026-09-19 零配置收口（2026-10-25 存储收口后收窄为 PG 恒定语义）：Go 侧在未
// 配置时自动派生普通文件目录（JUHE_AI_DATA_DIR 缺省 ./data 下派生
// usage-record-spool / codex-context / logs / chat-assets / audit-blob）与
// listen address / log dir 两个启动期默认值；PG/Redis 连接串缺失由 Go 侧
// fail-fast，launcher 不注入任何存储语义。
if (project === 'jobs') {
  // J1 账户健康检查强制常开：OWNER 缺省 go，显式非 go 拒绝；INSTANCE_ID 缺省
  // 主机名、INPUT_DIRECTORY 自动创建、SIGNING_KEY 自动生成并持久化、
  // CREDENTIAL_SECRET 缺省取 JUHE_AI_SECRET、输入源恒 PG 直读（2026-10-25 起
  // sqlite/files 输入臂与 *_STORE 已退役），均由 Go 侧解析，launcher 不再硬校验或注入。
  const accountHealthOwner = String(env.JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER ?? '').trim().toLowerCase()
  if (accountHealthOwner && accountHealthOwner !== 'go') {
    throw new Error('JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER=go is the only accepted owner; release startup refuses a non-Go J1 owner.')
  }
  // 2026-09-21 JUHE_AI_ACCOUNT_BALANCE_ENABLED 开关移除：J2 余额检测强制
  // 常开，owner 缺省 go、连接串/凭据缺省回落主配置，均由 Go 侧解析，
  // launcher 不再硬校验或注入。
  env.JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS = String(env.JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS ?? '').trim() || '127.0.0.1:3305'
  env.JUHE_AI_LOG_DIR = absoluteBackendPath(env.JUHE_AI_LOG_DIR, './logs')
} else {
  // gateway：F3/F4 INSTANCE_ID 缺省主机名，blob/热搜目录等路径类 env 未配置时
  // 由 Go 侧按 JUHE_AI_DATA_DIR 派生，launcher 不再硬校验或注入。
  env.JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS = String(env.JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS ?? '').trim() || '127.0.0.1:3306'
  env.JUHE_AI_LOG_DIR = absoluteBackendPath(env.JUHE_AI_LOG_DIR, './logs')
}

const logFd = openSync(logPath, 'a')
try {
  const child = spawn(binaryPath, [], {
    cwd: backendRoot,
    detached: true,
    windowsHide: true,
    env,
    stdio: ['ignore', logFd, logFd]
  })
  if (!child.pid) throw new Error(`Unable to start Go ${project} project.`)
  child.unref()
  process.stdout.write(String(child.pid))
} finally {
  closeSync(logFd)
}

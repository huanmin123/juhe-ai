import { closeSync, existsSync, openSync, readFileSync } from 'node:fs'
import { isAbsolute, resolve } from 'node:path'
import { createRequire } from 'node:module'
import { spawn } from 'node:child_process'

const [project, binaryPath, backendRoot, logPath] = process.argv.slice(2)
if (!['gateway', 'jobs'].includes(project) || !binaryPath || !backendRoot || !logPath) {
  throw new Error('Usage: node scripts/start-go-project.mjs <gateway|jobs> <binary-path> <backend-root> <log-path>')
}

const require = createRequire(resolve(backendRoot, 'package.json'))
const { parse } = require('dotenv')
const readEnv = (path) => existsSync(path) ? parse(readFileSync(path)) : {}
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

function resolveStore(componentName, env, runtimeMode, hasPerformanceHints) {
  const explicit = configured(componentName).value.trim().toLowerCase()
  const driver = configured('JUHE_AI_DATABASE_DRIVER').value.trim().toLowerCase()
  const inferred = runtimeMode === 'performance' || (!runtimeMode && hasPerformanceHints) ? 'postgres' : 'sqlite'
  const store = explicit || driver || inferred
  if (store !== 'sqlite' && store !== 'postgres') {
    throw new Error(`${componentName} must be sqlite or postgres.`)
  }
  env[componentName] = store
  return store
}

function absoluteBackendPath(value, fallback) {
  const selected = String(value || fallback).trim()
  return isAbsolute(selected) ? selected : resolve(backendRoot, selected)
}

const commonNames = [
  'JUHE_AI_DATABASE_DRIVER', 'JUHE_AI_RUNTIME_MODE', 'JUHE_AI_POSTGRES_URL', 'JUHE_AI_LOG_DIR',
  'JUHE_AI_LOG_FILE_ENABLED', 'JUHE_AI_LOG_RETENTION_DAYS', 'JUHE_AI_LOG_MAX_FILES', 'JUHE_AI_RG_PATH',
  'JUHE_AI_DATABASE_PATH', 'JUHE_AI_DATASET_DATABASE_PATH', 'JUHE_AI_USAGE_CATALOG_DATABASE_PATH',
  'JUHE_AI_STATS_DATABASE_PATH', 'JUHE_AI_USAGE_SHARD_ROOT', 'JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT'
]
const projectNames = project === 'jobs'
  ? [
      'JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS',
      'JUHE_AI_RUNTIME_LOG_STORE', 'JUHE_AI_RUNTIME_LOG_POSTGRES_URL', 'JUHE_AI_RUNTIME_LOG_DATABASE_PATH',
      'JUHE_AI_RUNTIME_LOG_INSTANCE_ID', 'JUHE_AI_RUNTIME_LOG_OWNER_LEASE', 'JUHE_AI_RUNTIME_LOG_ONCE',
      'JUHE_AI_RUNTIME_LOG_POLL_INTERVAL', 'JUHE_AI_RUNTIME_LOG_RETENTION_INTERVAL',
      'JUHE_AI_RUNTIME_LOG_RETENTION_DAYS', 'JUHE_AI_RUNTIME_LOG_BATCH_SIZE',
      'JUHE_AI_TABLE_MONITOR_STORE', 'JUHE_AI_TABLE_MONITOR_POSTGRES_URL', 'JUHE_AI_TABLE_MONITOR_DATABASE_PATH',
      'JUHE_AI_TABLE_MONITOR_INSTANCE_ID', 'JUHE_AI_TABLE_MONITOR_INTERVAL', 'JUHE_AI_TABLE_MONITOR_RUN_TIMEOUT',
      'JUHE_AI_TABLE_MONITOR_OWNER_LEASE', 'JUHE_AI_TABLE_MONITOR_RETENTION_DAYS',
      'JUHE_AI_TABLE_MONITOR_MAX_TABLES', 'JUHE_AI_TABLE_MONITOR_MAX_CONCURRENT_SOURCES',
      'JUHE_AI_TABLE_MONITOR_RETENTION_BATCH_SIZE', 'JUHE_AI_TABLE_MONITOR_RETENTION_MAX_BATCHES'
    ]
  : [
      'JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS',
      'JUHE_AI_RUNTIME_LOG_DATABASE_PATH', 'JUHE_AI_TABLE_MONITOR_DATABASE_PATH',
      'JUHE_AI_AUDIT_LOG_STORE', 'JUHE_AI_AUDIT_LOG_POSTGRES_URL', 'JUHE_AI_AUDIT_LOG_DATABASE_PATH',
      'JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY', 'JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY',
      'JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH', 'JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL',
      'JUHE_AI_AUDIT_LOG_POSTGRES_SCHEMA', 'JUHE_AI_AUDIT_LOG_INSTANCE_ID', 'JUHE_AI_AUDIT_LOG_OWNER_LEASE',
      'JUHE_AI_AUDIT_LOG_RETENTION_INTERVAL', 'JUHE_AI_AUDIT_LOG_RETENTION_BATCH_SIZE',
      'JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS', 'JUHE_AI_AUDIT_LOG_INPUT_SECRET', 'JUHE_AI_AUDIT_LOG_INPUT_URL',
      'JUHE_AI_AUDIT_LOG_INPUT_TIMEOUT_MS',
      'JUHE_AI_OPERATION_LOG_STORE', 'JUHE_AI_OPERATION_LOG_POSTGRES_URL', 'JUHE_AI_OPERATION_LOG_DATABASE_PATH',
      'JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH', 'JUHE_AI_OPERATION_LOG_INSTANCE_ID',
      'JUHE_AI_OPERATION_LOG_OWNER_LEASE', 'JUHE_AI_OPERATION_LOG_RETENTION_INTERVAL',
      'JUHE_AI_OPERATION_LOG_RETENTION_BATCH_SIZE', 'JUHE_AI_OPERATION_LOG_INPUT_LISTEN_ADDRESS',
      'JUHE_AI_OPERATION_LOG_INPUT_SECRET', 'JUHE_AI_OPERATION_LOG_INPUT_URL', 'JUHE_AI_OPERATION_LOG_INPUT_TIMEOUT_MS'
    ]

const env = { ...process.env }
for (const name of [...commonNames, ...projectNames]) {
  const value = configured(name)
  if (value.defined) env[name] = value.value
}

const runtimeMode = String(env.JUHE_AI_RUNTIME_MODE ?? '').trim().toLowerCase()
const hasPerformanceHints = ['JUHE_AI_POSTGRES_URL', 'JUHE_AI_REDIS_CACHE_URL', 'JUHE_AI_REDIS_STATE_URL', 'JUHE_AI_REDIS_QUEUE_URL']
  .some((name) => Boolean(configured(name).value.trim()))

if (project === 'jobs') {
  for (const name of ['JUHE_AI_RUNTIME_LOG_INSTANCE_ID', 'JUHE_AI_TABLE_MONITOR_INSTANCE_ID']) {
    if (!String(env[name] ?? '').trim()) throw new Error(`${name} is required; release startup does not generate owner identities.`)
  }
  const runtimeLogStore = resolveStore('JUHE_AI_RUNTIME_LOG_STORE', env, runtimeMode, hasPerformanceHints)
  const tableMonitorStore = resolveStore('JUHE_AI_TABLE_MONITOR_STORE', env, runtimeMode, hasPerformanceHints)
  env.JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS = String(env.JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS ?? '').trim() || '127.0.0.1:3305'
  env.JUHE_AI_LOG_DIR = absoluteBackendPath(env.JUHE_AI_LOG_DIR, './logs')
  if (runtimeLogStore === 'sqlite' || tableMonitorStore === 'sqlite') {
    env.JUHE_AI_DATABASE_PATH = absoluteBackendPath(env.JUHE_AI_DATABASE_PATH, './data/juhe-ai.sqlite3')
    env.JUHE_AI_DATASET_DATABASE_PATH = absoluteBackendPath(env.JUHE_AI_DATASET_DATABASE_PATH, './data/juhe-ai-dataset.sqlite3')
    env.JUHE_AI_USAGE_CATALOG_DATABASE_PATH = absoluteBackendPath(env.JUHE_AI_USAGE_CATALOG_DATABASE_PATH, './data/juhe-ai-usage-catalog.sqlite3')
    env.JUHE_AI_STATS_DATABASE_PATH = absoluteBackendPath(env.JUHE_AI_STATS_DATABASE_PATH, './data/juhe-ai-stats.sqlite3')
    env.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = absoluteBackendPath(env.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT, './data/codex-context/state-shards')
    env.JUHE_AI_RUNTIME_LOG_DATABASE_PATH = absoluteBackendPath(env.JUHE_AI_RUNTIME_LOG_DATABASE_PATH, './data/juhe-ai-runtime-log.sqlite3')
    env.JUHE_AI_TABLE_MONITOR_DATABASE_PATH = absoluteBackendPath(env.JUHE_AI_TABLE_MONITOR_DATABASE_PATH, './data/juhe-ai-table-monitor.sqlite3')
  }
} else {
  for (const name of ['JUHE_AI_AUDIT_LOG_INSTANCE_ID', 'JUHE_AI_AUDIT_LOG_INPUT_SECRET', 'JUHE_AI_OPERATION_LOG_INSTANCE_ID', 'JUHE_AI_OPERATION_LOG_INPUT_SECRET']) {
    if (!String(env[name] ?? '').trim()) throw new Error(`${name} is required; release startup does not generate owner identities or transport secrets.`)
  }
  const auditLogStore = resolveStore('JUHE_AI_AUDIT_LOG_STORE', env, runtimeMode, hasPerformanceHints)
  const operationLogStore = resolveStore('JUHE_AI_OPERATION_LOG_STORE', env, runtimeMode, hasPerformanceHints)
  env.JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS = String(env.JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS ?? '').trim() || '127.0.0.1:3306'
  env.JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS = String(env.JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS ?? '').trim() || '127.0.0.1:3303'
  env.JUHE_AI_AUDIT_LOG_INPUT_URL = String(env.JUHE_AI_AUDIT_LOG_INPUT_URL ?? '').trim() || 'http://127.0.0.1:3303'
  env.JUHE_AI_OPERATION_LOG_INPUT_LISTEN_ADDRESS = String(env.JUHE_AI_OPERATION_LOG_INPUT_LISTEN_ADDRESS ?? '').trim() || '127.0.0.1:3304'
  env.JUHE_AI_OPERATION_LOG_INPUT_URL = String(env.JUHE_AI_OPERATION_LOG_INPUT_URL ?? '').trim() || 'http://127.0.0.1:3304'
  env.JUHE_AI_LOG_DIR = absoluteBackendPath(env.JUHE_AI_LOG_DIR, './logs')
  if (auditLogStore === 'sqlite' || operationLogStore === 'sqlite') {
    env.JUHE_AI_DATABASE_PATH = absoluteBackendPath(env.JUHE_AI_DATABASE_PATH, './data/juhe-ai.sqlite3')
    env.JUHE_AI_DATASET_DATABASE_PATH = absoluteBackendPath(env.JUHE_AI_DATASET_DATABASE_PATH, './data/juhe-ai-dataset.sqlite3')
    env.JUHE_AI_RUNTIME_LOG_DATABASE_PATH = absoluteBackendPath(env.JUHE_AI_RUNTIME_LOG_DATABASE_PATH, './data/juhe-ai-runtime-log.sqlite3')
    env.JUHE_AI_TABLE_MONITOR_DATABASE_PATH = absoluteBackendPath(env.JUHE_AI_TABLE_MONITOR_DATABASE_PATH, './data/juhe-ai-table-monitor.sqlite3')
    env.JUHE_AI_AUDIT_LOG_DATABASE_PATH = absoluteBackendPath(env.JUHE_AI_AUDIT_LOG_DATABASE_PATH, './data/juhe-ai-audit-log.sqlite3')
    env.JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY = absoluteBackendPath(env.JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY, './data/audit-payload-blobs')
    env.JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY = absoluteBackendPath(env.JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY, './data/audit-hot-search')
    env.JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH = absoluteBackendPath(env.JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH, env.JUHE_AI_DATABASE_PATH)
    env.JUHE_AI_OPERATION_LOG_DATABASE_PATH = absoluteBackendPath(env.JUHE_AI_OPERATION_LOG_DATABASE_PATH, './data/juhe-ai-operation-log.sqlite3')
    env.JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH = absoluteBackendPath(env.JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH, env.JUHE_AI_DATABASE_PATH)
    env.JUHE_AI_USAGE_SHARD_ROOT = absoluteBackendPath(env.JUHE_AI_USAGE_SHARD_ROOT, './data/usage-shards')
    env.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = absoluteBackendPath(env.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT, './data/codex-context/state-shards')
  }
  if (auditLogStore === 'postgres' && !String(env.JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL ?? '').trim()) {
    env.JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL = String(env.JUHE_AI_AUDIT_LOG_POSTGRES_URL || env.JUHE_AI_POSTGRES_URL || '').trim()
  }
  if (operationLogStore === 'postgres' && !String(env.JUHE_AI_OPERATION_LOG_POSTGRES_URL ?? '').trim()) {
    env.JUHE_AI_OPERATION_LOG_POSTGRES_URL = String(env.JUHE_AI_OPERATION_LOG_POSTGRES_URL || env.JUHE_AI_POSTGRES_URL || '').trim()
  }
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

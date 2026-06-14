import { existsSync, readFileSync } from 'node:fs'
import { dirname, isAbsolute, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { parse } from 'dotenv'

export interface RuntimeConfig {
  processRole: ProcessRole
  workerRole: WorkerRuntimeRole
  host: string
  port: number
  httpSecurity: {
    cors: {
      allowedOrigins: string[]
      allowAnyOrigin: boolean
    }
    cookie: {
      secure: boolean
      sameSite: CookieSameSiteRuntimeConfig
    }
    trustProxy: boolean | number
  }
  upstreamUrlSecurity: {
    allowPrivateBaseUrls: boolean
    privateBaseUrlAllowlist: string[]
  }
  dbServiceHttpHost: string
  dbServiceHttpPort: number
  databasePath: string
  datasetDatabasePath: string
  statsDatabasePath: string
  usageShardRoot: string
  usageShardCount: number
  secret: string
  oauthProxyUrl?: string
  gateway: {
    bodyInFlightMaxBytes: number
  }
  log: {
    level: LogLevel
    directory: string
    fileEnabled: boolean
    consoleEnabled: boolean
    maxFileBytes: number
    retentionDays: number
    maxFiles: number
    cleanupIntervalMinutes: number
  }
  smokeTest: {
    backendUrl: string
    adminUsername: string
    adminPassword: string
    accountName: string
    model: string
    prompt: string
  }
}

export type LogLevel = 'trace' | 'debug' | 'info' | 'warn' | 'error' | 'fatal' | 'silent'
export type ProcessRole = 'server' | 'worker' | 'db-service'
export type WorkerRuntimeRole = 'worker' | 'metrics-worker' | 'ingest-worker'
export type CookieSameSiteRuntimeConfig = 'lax' | 'strict' | 'none'
export const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
export const localEnvPath = resolve(backendRoot, '.env')
export const defaultDatabasePath = resolve(backendRoot, 'data', 'juhe-ai.sqlite3')
export const defaultDatasetDatabasePath = resolve(backendRoot, 'data', 'juhe-ai-dataset.sqlite3')
export const defaultStatsDatabasePath = resolve(backendRoot, 'data', 'juhe-ai-stats.sqlite3')
export const defaultUsageShardRoot = resolve(backendRoot, 'data', 'usage-shards')
export const defaultRuntimeSecret = 'juhe-ai-dev-secret-change-me'
const minimumProductionSecretLength = 32

const localEnv = loadLocalEnv(localEnvPath)

export const runtimeConfig: RuntimeConfig = {
  processRole: processRoleConfig('JUHE_AI_PROCESS_ROLE', 'server'),
  workerRole: workerRoleConfig('JUHE_AI_WORKER_ROLE', 'worker'),
  host: stringConfig('JUHE_AI_HOST', '127.0.0.1'),
  port: numberConfig('JUHE_AI_PORT', 3000, 1, 65535),
  dbServiceHttpHost: stringConfig('JUHE_AI_DB_SERVICE_HTTP_HOST', '127.0.0.1'),
  dbServiceHttpPort: numberConfig('JUHE_AI_DB_SERVICE_HTTP_PORT', 0, 0, 65535),
  databasePath: pathConfig('JUHE_AI_DATABASE_PATH', defaultDatabasePath),
  datasetDatabasePath: pathConfig('JUHE_AI_DATASET_DATABASE_PATH', defaultDatasetDatabasePath),
  statsDatabasePath: pathConfig('JUHE_AI_STATS_DATABASE_PATH', defaultStatsDatabasePath),
  usageShardRoot: pathConfig('JUHE_AI_USAGE_SHARD_ROOT', defaultUsageShardRoot),
  usageShardCount: numberConfig('JUHE_AI_USAGE_SHARD_COUNT', 16, 1, 256),
  secret: secretConfig('JUHE_AI_SECRET', defaultRuntimeSecret),
  httpSecurity: httpSecurityConfig(),
  upstreamUrlSecurity: upstreamUrlSecurityConfig(),
  oauthProxyUrl: optionalStringConfig('JUHE_AI_OAUTH_PROXY_URL'),
  gateway: {
    bodyInFlightMaxBytes: numberConfig('JUHE_AI_GATEWAY_BODY_IN_FLIGHT_MAX_MB', 256, 16, 4096) * 1024 * 1024
  },
  log: {
    level: logLevelConfig('JUHE_AI_LOG_LEVEL', 'info'),
    directory: pathConfig('JUHE_AI_LOG_DIR', resolve(backendRoot, 'logs')),
    fileEnabled: booleanConfig('JUHE_AI_LOG_FILE_ENABLED', true),
    consoleEnabled: booleanConfig('JUHE_AI_LOG_CONSOLE_ENABLED', true),
    maxFileBytes: numberConfig('JUHE_AI_LOG_MAX_FILE_MB', 100, 1, 1024) * 1024 * 1024,
    retentionDays: numberConfig('JUHE_AI_LOG_RETENTION_DAYS', 30, 1, 30),
    maxFiles: numberConfig('JUHE_AI_LOG_MAX_FILES', 500, 1, 500),
    cleanupIntervalMinutes: numberConfig('JUHE_AI_LOG_CLEANUP_INTERVAL_MINUTES', 60, 1, 1440)
  },
  smokeTest: {
    backendUrl: stringConfig('JUHE_AI_BACKEND_URL', 'http://127.0.0.1:3000'),
    adminUsername: stringConfig('JUHE_AI_SMOKE_ADMIN_USERNAME', 'admin'),
    adminPassword: stringConfig('JUHE_AI_SMOKE_ADMIN_PASSWORD', 'admin'),
    accountName: stringConfig('JUHE_AI_SMOKE_ACCOUNT_NAME', ''),
    model: stringConfig('JUHE_AI_SMOKE_MODEL', 'gpt-5.4-mini'),
    prompt: stringConfig('JUHE_AI_SMOKE_PROMPT', '只输出 OK')
  }
}

function loadLocalEnv(path: string): Record<string, string> {
  if (!existsSync(path)) {
    return {}
  }
  return parse(readFileSync(path))
}

function stringConfig(name: string, fallback: string): string {
  const value = rawStringConfig(name)
  return value ? value : fallback
}

function rawStringConfig(name: string): string | undefined {
  return process.env[name]?.trim() ?? localEnv[name]?.trim()
}

function secretConfig(name: string, fallback: string): string {
  const value = stringConfig(name, fallback)
  assertProductionSecret(name, value)
  return value
}

function assertProductionSecret(name: string, value: string): void {
  if (!isProductionRuntime()) return
  if (value === defaultRuntimeSecret || value.length < minimumProductionSecretLength) {
    throw new Error(`${name} 在生产环境必须配置为至少 ${minimumProductionSecretLength} 位的稳定随机密钥，不能使用默认开发密钥或过短密钥`)
  }
}

export function isProductionRuntime(): boolean {
  return (process.env.NODE_ENV?.trim() ?? localEnv.NODE_ENV?.trim() ?? '').toLowerCase() === 'production'
}

function optionalStringConfig(name: string): string | undefined {
  const value = rawStringConfig(name)
  return value ? value : undefined
}

function booleanConfig(name: string, fallback: boolean): boolean {
  const value = stringConfig(name, '').toLowerCase()
  if (!value) return fallback
  if (['1', 'true', 'yes', 'on'].includes(value)) return true
  if (['0', 'false', 'no', 'off'].includes(value)) return false
  return fallback
}

function logLevelConfig(name: string, fallback: LogLevel): LogLevel {
  const value = stringConfig(name, '').toLowerCase()
  return isLogLevel(value) ? value : fallback
}

function processRoleConfig(name: string, fallback: ProcessRole): ProcessRole {
  const value = stringConfig(name, '').toLowerCase()
  if (value === 'worker') return 'worker'
  if (value === 'db-service') return 'db-service'
  return fallback
}

function workerRoleConfig(name: string, fallback: WorkerRuntimeRole): WorkerRuntimeRole {
  const value = stringConfig(name, '').toLowerCase()
  if (value === 'metrics-worker') return 'metrics-worker'
  if (value === 'ingest-worker') return 'ingest-worker'
  return fallback
}

function numberConfig(name: string, fallback: number, min: number, max: number): number {
  const value = Number(stringConfig(name, String(fallback)))
  if (!Number.isFinite(value)) {
    return fallback
  }
  return Math.min(Math.max(Math.trunc(value), min), max)
}

function pathConfig(name: string, fallback: string): string {
  const value = stringConfig(name, fallback)
  return isAbsolute(value) ? value : resolve(backendRoot, value)
}

function httpSecurityConfig(): RuntimeConfig['httpSecurity'] {
  const production = isProductionRuntime()
  const allowedOrigins = allowedOriginsConfig('JUHE_AI_ALLOWED_ORIGINS', production)
  const cookieSecure = strictBooleanConfig('JUHE_AI_COOKIE_SECURE', production)
  const cookieSameSite = cookieSameSiteConfig('JUHE_AI_COOKIE_SAME_SITE', 'lax')
  if (cookieSameSite === 'none' && !cookieSecure) {
    throw new Error('JUHE_AI_COOKIE_SAME_SITE=none 时必须启用 JUHE_AI_COOKIE_SECURE=true')
  }
  return {
    cors: {
      allowedOrigins,
      allowAnyOrigin: !production && allowedOrigins.length === 0
    },
    cookie: {
      secure: cookieSecure,
      sameSite: cookieSameSite
    },
    trustProxy: trustProxyConfig('JUHE_AI_TRUST_PROXY')
  }
}

function allowedOriginsConfig(name: string, requireExplicit: boolean): string[] {
  const rawValue = rawStringConfig(name) ?? ''
  const parts = rawValue.split(',').map((part) => part.trim()).filter(Boolean)
  if (parts.includes('*')) {
    throw new Error(`${name} 不允许配置 *；需要逐项填写完整后台前端 Origin`)
  }
  const origins = Array.from(new Set(parts.map((part) => normalizeAllowedOrigin(name, part))))
  if (requireExplicit && origins.length === 0) {
    throw new Error(`${name} 在生产环境必须显式配置后台前端 Origin，不能继续反射任意跨域来源`)
  }
  return origins
}

function normalizeAllowedOrigin(name: string, value: string): string {
  let url: URL
  try {
    url = new URL(value)
  } catch {
    throw new Error(`${name} 包含无效 Origin：${value}`)
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') {
    throw new Error(`${name} 只允许 http 或 https Origin：${value}`)
  }
  if (url.username || url.password || url.search || url.hash || (url.pathname && url.pathname !== '/')) {
    throw new Error(`${name} 只能填写 Origin，不要包含路径、查询、片段或用户名密码：${value}`)
  }
  return url.origin
}

function strictBooleanConfig(name: string, fallback: boolean): boolean {
  const value = rawStringConfig(name)?.toLowerCase()
  if (!value) return fallback
  if (['1', 'true', 'yes', 'on'].includes(value)) return true
  if (['0', 'false', 'no', 'off'].includes(value)) return false
  throw new Error(`${name} 只能配置为 true/false/1/0/yes/no/on/off`)
}

function cookieSameSiteConfig(name: string, fallback: CookieSameSiteRuntimeConfig): CookieSameSiteRuntimeConfig {
  const value = rawStringConfig(name)?.toLowerCase()
  if (!value) return fallback
  if (value === 'lax' || value === 'strict' || value === 'none') return value
  throw new Error(`${name} 只能配置为 lax、strict 或 none`)
}

function trustProxyConfig(name: string): boolean | number {
  const value = rawStringConfig(name)?.toLowerCase()
  if (!value) return false
  if (['true', 'yes', 'on'].includes(value)) return true
  if (['false', 'no', 'off'].includes(value)) return false
  const numericValue = Number(value)
  if (Number.isInteger(numericValue) && numericValue >= 0 && numericValue <= 16) {
    return numericValue
  }
  throw new Error(`${name} 只能配置为 true/false 或 0-16 的反向代理跳数`)
}

function upstreamUrlSecurityConfig(): RuntimeConfig['upstreamUrlSecurity'] {
  const allowPrivateBaseUrls = strictBooleanConfig('JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS', false)
  const privateBaseUrlAllowlist = listConfig('JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST')
  if (isProductionRuntime() && allowPrivateBaseUrls) {
    throw new Error('JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS 只能用于本地开发或回归测试，生产环境不能启用')
  }
  if (isProductionRuntime() && privateBaseUrlAllowlist.length > 0) {
    throw new Error('JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST 只能用于本地开发或回归测试，生产环境不能配置')
  }
  return {
    allowPrivateBaseUrls,
    privateBaseUrlAllowlist
  }
}

function listConfig(name: string): string[] {
  const value = rawStringConfig(name) ?? ''
  return Array.from(new Set(value.split(',').map((part) => part.trim()).filter(Boolean)))
}

function isLogLevel(value: string | undefined): value is LogLevel {
  return value === 'trace'
    || value === 'debug'
    || value === 'info'
    || value === 'warn'
    || value === 'error'
    || value === 'fatal'
    || value === 'silent'
}

import { existsSync, readFileSync } from 'node:fs'
import { dirname, isAbsolute, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { parse } from 'dotenv'

export interface RuntimeConfig {
  processRole: ProcessRole
  host: string
  port: number
  dbServiceHttpHost: string
  dbServiceHttpPort: number
  databasePath: string
  datasetDatabasePath: string
  statsDatabasePath: string
  usageShardRoot: string
  usageShardCount: number
  secret: string
  oauthProxyUrl?: string
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

export const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
export const localEnvPath = resolve(backendRoot, '.env')
export const defaultDatabasePath = resolve(backendRoot, 'data', 'juhe-ai.sqlite3')
export const defaultDatasetDatabasePath = resolve(backendRoot, 'data', 'juhe-ai-dataset.sqlite3')
export const defaultStatsDatabasePath = resolve(backendRoot, 'data', 'juhe-ai-stats.sqlite3')
export const defaultUsageShardRoot = resolve(backendRoot, 'data', 'usage-shards')

const localEnv = loadLocalEnv(localEnvPath)

export const runtimeConfig: RuntimeConfig = {
  processRole: processRoleConfig('JUHE_AI_PROCESS_ROLE', 'server'),
  host: stringConfig('JUHE_AI_HOST', '127.0.0.1'),
  port: numberConfig('JUHE_AI_PORT', 3000, 1, 65535),
  dbServiceHttpHost: stringConfig('JUHE_AI_DB_SERVICE_HTTP_HOST', '127.0.0.1'),
  dbServiceHttpPort: numberConfig('JUHE_AI_DB_SERVICE_HTTP_PORT', 0, 0, 65535),
  databasePath: pathConfig('JUHE_AI_DATABASE_PATH', defaultDatabasePath),
  datasetDatabasePath: pathConfig('JUHE_AI_DATASET_DATABASE_PATH', defaultDatasetDatabasePath),
  statsDatabasePath: pathConfig('JUHE_AI_STATS_DATABASE_PATH', defaultStatsDatabasePath),
  usageShardRoot: pathConfig('JUHE_AI_USAGE_SHARD_ROOT', defaultUsageShardRoot),
  usageShardCount: numberConfig('JUHE_AI_USAGE_SHARD_COUNT', 16, 1, 256),
  secret: stringConfig('JUHE_AI_SECRET', 'juhe-ai-dev-secret-change-me'),
  oauthProxyUrl: optionalStringConfig('JUHE_AI_OAUTH_PROXY_URL'),
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
  const value = process.env[name]?.trim() ?? localEnv[name]?.trim()
  return value ? value : fallback
}

function optionalStringConfig(name: string): string | undefined {
  const value = process.env[name]?.trim() ?? localEnv[name]?.trim()
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

function isLogLevel(value: string | undefined): value is LogLevel {
  return value === 'trace'
    || value === 'debug'
    || value === 'info'
    || value === 'warn'
    || value === 'error'
    || value === 'fatal'
    || value === 'silent'
}

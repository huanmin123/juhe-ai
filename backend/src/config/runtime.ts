import { existsSync, readFileSync } from 'node:fs'
import { dirname, isAbsolute, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { parse } from 'dotenv'

export interface RuntimeConfig {
  processRole: ProcessRole
  host: string
  port: number
  databasePath: string
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
    accountName: string
    model: string
    prompt: string
  }
}

export type LogLevel = 'trace' | 'debug' | 'info' | 'warn' | 'error' | 'fatal' | 'silent'
export type ProcessRole = 'server' | 'worker'

export const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
export const localEnvPath = resolve(backendRoot, '.env')

const localEnv = loadLocalEnv(localEnvPath)

export const runtimeConfig: RuntimeConfig = {
  processRole: processRoleConfig('JUHE_AI_PROCESS_ROLE', 'server'),
  host: stringConfig('JUHE_AI_HOST', '127.0.0.1'),
  port: numberConfig('JUHE_AI_PORT', 3000, 1, 65535),
  databasePath: pathConfig('JUHE_AI_DATABASE_PATH', resolve(backendRoot, 'data', 'juhe-ai.sqlite3')),
  secret: stringConfig('JUHE_AI_SECRET', 'juhe-ai-dev-secret-change-me'),
  oauthProxyUrl: optionalStringConfig('JUHE_AI_OAUTH_PROXY_URL'),
  log: {
    level: logLevelConfig('JUHE_AI_LOG_LEVEL', 'info'),
    directory: pathConfig('JUHE_AI_LOG_DIR', resolve(backendRoot, 'logs')),
    fileEnabled: booleanConfig('JUHE_AI_LOG_FILE_ENABLED', true),
    consoleEnabled: booleanConfig('JUHE_AI_LOG_CONSOLE_ENABLED', true),
    maxFileBytes: numberConfig('JUHE_AI_LOG_MAX_FILE_MB', 100, 1, 1024) * 1024 * 1024,
    retentionDays: numberConfig('JUHE_AI_LOG_RETENTION_DAYS', 14, 1, 3650),
    maxFiles: numberConfig('JUHE_AI_LOG_MAX_FILES', 30, 1, 10000),
    cleanupIntervalMinutes: numberConfig('JUHE_AI_LOG_CLEANUP_INTERVAL_MINUTES', 60, 1, 1440)
  },
  smokeTest: {
    backendUrl: stringConfig('JUHE_AI_BACKEND_URL', 'http://127.0.0.1:3000'),
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
  const value = localEnv[name]?.trim()
  return value ? value : fallback
}

function optionalStringConfig(name: string): string | undefined {
  const value = localEnv[name]?.trim()
  return value ? value : undefined
}

function booleanConfig(name: string, fallback: boolean): boolean {
  const value = localEnv[name]?.trim().toLowerCase()
  if (!value) return fallback
  if (['1', 'true', 'yes', 'on'].includes(value)) return true
  if (['0', 'false', 'no', 'off'].includes(value)) return false
  return fallback
}

function logLevelConfig(name: string, fallback: LogLevel): LogLevel {
  const value = localEnv[name]?.trim().toLowerCase()
  return isLogLevel(value) ? value : fallback
}

function processRoleConfig(name: string, fallback: ProcessRole): ProcessRole {
  const value = process.env[name]?.trim().toLowerCase() ?? localEnv[name]?.trim().toLowerCase()
  return value === 'worker' ? 'worker' : fallback
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

import { existsSync, readFileSync } from 'node:fs'
import { dirname, isAbsolute, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { parse } from 'dotenv'

export interface RuntimeConfig {
  host: string
  port: number
  databasePath: string
  secret: string
  oauthProxyUrl?: string
  smokeTest: {
    backendUrl: string
    accountName: string
    model: string
    prompt: string
  }
}

export const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
export const localEnvPath = resolve(backendRoot, '.env')

const localEnv = loadLocalEnv(localEnvPath)

export const runtimeConfig: RuntimeConfig = {
  host: stringConfig('JUHE_AI_HOST', '127.0.0.1'),
  port: numberConfig('JUHE_AI_PORT', 3000, 1, 65535),
  databasePath: pathConfig('JUHE_AI_DATABASE_PATH', resolve(backendRoot, 'data', 'juhe-ai.sqlite3')),
  secret: stringConfig('JUHE_AI_SECRET', 'juhe-ai-dev-secret-change-me'),
  oauthProxyUrl: optionalStringConfig('JUHE_AI_OAUTH_PROXY_URL'),
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

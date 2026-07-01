import { spawnSync } from 'node:child_process'
import { existsSync, readFileSync } from 'node:fs'
import { dirname, isAbsolute, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { parse } from 'dotenv'

const recommendedNodeVersion = '官方 Node.js LTS：22.x LTS（>=22.13.0）或 24.x LTS（>=24.11.0）'
const verifyCommand = 'pnpm --filter juhe-ai-backend check:runtime'
const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')
const localEnvPath = resolve(backendRoot, '.env')
let cachedLocalEnv: Record<string, string> | undefined
let cachedLocalEnvOverlay: Record<string, string> | undefined
const sqliteCapabilityCheckScript = `
const { DatabaseSync } = await import('node:sqlite')
const database = new DatabaseSync(':memory:')

try {
  database.exec("CREATE TABLE __juhe_ai_sqlite_check(id TEXT PRIMARY KEY, content TEXT NOT NULL)")
  database.exec("INSERT INTO __juhe_ai_sqlite_check(id, content) VALUES ('runtime', 'node sqlite')")
  const row = database.prepare("SELECT content FROM __juhe_ai_sqlite_check WHERE id = ?").get('runtime')
  if (!row || row.content !== 'node sqlite') {
    throw new Error('node:sqlite query check failed')
  }
} finally {
  database.close()
}
`

function formatCheckError(checkResult: ReturnType<typeof spawnSync>): string {
  if (checkResult.error) {
    const code = 'code' in checkResult.error ? String((checkResult.error as NodeJS.ErrnoException).code) : undefined
    return code
      ? `${checkResult.error.name} [${code}]: ${checkResult.error.message}`
      : `${checkResult.error.name}: ${checkResult.error.message}`
  }

  const stderr = String(checkResult.stderr ?? '').trim()
  const stdout = String(checkResult.stdout ?? '').trim()
  return stderr || stdout || `node 进程退出码 ${String(checkResult.status ?? 'unknown')}`
}

function exitWithRuntimeError(title: string, extraLines: string[] = []): never {
  console.error([
    title,
    `当前版本：${process.version}`,
    `LTS 标识：${process.release.lts ? String(process.release.lts) : '非 LTS'}`,
    `Node 路径：${process.execPath}`,
    `建议版本：${recommendedNodeVersion}`,
    '处理方式：切换到官方 Node.js LTS 后重新安装依赖并启动项目。',
    `验证命令：${verifyCommand}`,
    ...extraLines
  ].join('\n'))
  process.exit(1)
}

if (!process.release.lts) {
  exitWithRuntimeError('[juhe-ai] 当前 Node.js 不是官方 LTS 发行版，后端已停止启动。')
}

const runtimeMode = rawConfig('JUHE_AI_RUNTIME_MODE').toLowerCase() || (hasPerformanceDriverHints() ? 'performance' : 'standalone')
const databaseDriver = rawConfig('JUHE_AI_DATABASE_DRIVER').toLowerCase()
  || (runtimeMode === 'performance' ? 'postgres' : 'sqlite')

if (runtimeMode === 'performance' || databaseDriver === 'postgres') {
  process.exit(0)
}

const checkResult = spawnSync(process.execPath, [
  '--no-warnings',
  '--input-type=module',
  '-e',
  sqliteCapabilityCheckScript
], {
  encoding: 'utf8'
})

if (checkResult.status !== 0 || checkResult.error) {
  exitWithRuntimeError(
    '[juhe-ai] 当前 Node.js 的内置 SQLite 不完整，后端需要可用的 node:sqlite。',
    [`原始错误：${formatCheckError(checkResult)}`]
  )
}

function rawConfig(name: string): string {
  return (process.env[name]?.trim() ?? loadLocalEnvOverlay()[name]?.trim() ?? loadLocalEnv()[name]?.trim() ?? '')
}

function hasPerformanceDriverHints(): boolean {
  return [
    'JUHE_AI_POSTGRES_URL',
    'JUHE_AI_REDIS_CACHE_URL',
    'JUHE_AI_REDIS_STATE_URL',
    'JUHE_AI_REDIS_QUEUE_URL'
  ].some((name) => Boolean(rawConfig(name)))
}

function loadLocalEnv(): Record<string, string> {
  if (cachedLocalEnv) return cachedLocalEnv
  if (!existsSync(localEnvPath)) {
    cachedLocalEnv = {}
    return cachedLocalEnv
  }
  cachedLocalEnv = parse(readFileSync(localEnvPath))
  return cachedLocalEnv
}

function loadLocalEnvOverlay(): Record<string, string> {
  if (cachedLocalEnvOverlay) return cachedLocalEnvOverlay
  const overlayPath = envFilePathConfig(process.env.JUHE_AI_ENV_FILE ?? loadLocalEnv().JUHE_AI_ENV_FILE)
  if (!overlayPath || !existsSync(overlayPath)) {
    cachedLocalEnvOverlay = {}
    return cachedLocalEnvOverlay
  }
  cachedLocalEnvOverlay = parse(readFileSync(overlayPath))
  return cachedLocalEnvOverlay
}

function envFilePathConfig(value: string | undefined): string | undefined {
  const trimmedValue = value?.trim()
  if (!trimmedValue) return undefined
  return isAbsolute(trimmedValue) ? trimmedValue : resolve(backendRoot, trimmedValue)
}

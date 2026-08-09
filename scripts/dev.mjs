import { spawn, spawnSync } from 'node:child_process'
import { existsSync, readFileSync } from 'node:fs'
import { dirname, isAbsolute, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  resolveDevelopmentAutoLoginUsername,
  resolveDevelopmentBackendTarget
} from './dev-config.mjs'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const frontendRoot = resolve(root, 'frontend')
const backendRoot = resolve(root, 'backend')
const backendGoRoot = resolve(root, 'backend-go')
const backendReadyTimeoutMs = positiveInteger(process.env.JUHE_AI_DEV_BACKEND_READY_TIMEOUT_MS, 60_000)
const backendTarget = resolveBackendTarget()
const backendHealthUrl = resolveBackendHealthUrl(backendTarget)
const pnpmRunner = resolvePnpmRunner()

let backend
let frontend
let runtimeLogIndexer
let tableMonitor
let shuttingDown = false

process.on('SIGINT', () => shutdown(130))
process.on('SIGTERM', () => shutdown(143))
process.on('SIGHUP', () => shutdown(129))

try {
  console.log('[dev] starting backend...')
  const backendReadiness = createBackendReadinessTracker(backendHealthUrl, backendReadyTimeoutMs)
  backend = startPnpm(['--filter', 'juhe-ai-backend', 'dev'], 'backend', backendReadiness.acceptChunk)
  await backendReadiness.ready
  console.log(`[dev] backend system API is ready: ${backendHealthUrl}`)
  runtimeLogIndexer = startRuntimeLogIndexer()
  tableMonitor = startTableMonitor()
  console.log('[dev] starting frontend...')
  frontend = startPnpm(['--filter', 'juhe-ai-frontend', 'dev'], 'frontend')
} catch (error) {
  console.error(`[dev] ${error instanceof Error ? error.message : String(error)}`)
  shutdown(1)
}

await new Promise(() => undefined)

function startPnpm(args, label, onOutput) {
  const developmentAutoLoginUsername = resolveDevelopmentAutoLoginUsername(process.env.JUHE_AI_DEV_AUTO_LOGIN_USERNAME)
  const childEnv = label === 'backend'
    ? {
        ...process.env,
        ...(developmentAutoLoginUsername === undefined
          ? {}
          : { JUHE_AI_DEV_AUTO_LOGIN_USERNAME: developmentAutoLoginUsername })
      }
    : {
        ...process.env,
        VITE_JUHE_AI_BACKEND_TARGET: backendTarget
      }
  const child = spawn(pnpmRunner.command, [...pnpmRunner.args, ...args], {
    cwd: root,
    env: childEnv,
    shell: pnpmRunner.shell,
    stdio: ['inherit', 'pipe', 'pipe']
  })

  pipeChildOutput(child.stdout, process.stdout, onOutput)
  pipeChildOutput(child.stderr, process.stderr, onOutput)

  monitorChild(child, label)

  return child
}

function startRuntimeLogIndexer() {
  const childEnv = resolveRuntimeLogIndexerEnv()
  console.log(`[dev] starting Go runtime log indexer (${childEnv.JUHE_AI_RUNTIME_LOG_INSTANCE_ID})...`)
  const child = spawn('go', ['run', './cmd/juhe-ai-runtime-log-indexer'], {
    cwd: backendGoRoot,
    env: childEnv,
    detached: process.platform !== 'win32',
    shell: false,
    stdio: ['inherit', 'pipe', 'pipe']
  })

  pipeChildOutput(child.stdout, process.stdout)
  pipeChildOutput(child.stderr, process.stderr)
  monitorChild(child, 'Go runtime log indexer')

  return child
}

function startTableMonitor() {
  const childEnv = resolveTableMonitorEnv()
  console.log(`[dev] starting Go table monitor (${childEnv.JUHE_AI_TABLE_MONITOR_INSTANCE_ID})...`)
  const child = spawn('go', ['run', './cmd/juhe-ai-table-monitor'], {
    cwd: backendGoRoot,
    env: childEnv,
    detached: process.platform !== 'win32',
    shell: false,
    stdio: ['inherit', 'pipe', 'pipe']
  })

  pipeChildOutput(child.stdout, process.stdout)
  pipeChildOutput(child.stderr, process.stderr)
  monitorChild(child, 'Go table monitor')

  return child
}

function monitorChild(child, label) {
  child.on('error', (error) => {
    if (shuttingDown) return
    console.error(`[dev] failed to start ${label}: ${error.message}`)
    shutdown(1)
  })

  child.on('exit', (code, signal) => {
    if (shuttingDown) return
    const exitCode = typeof code === 'number' ? code : 1
    const reason = signal ? `signal ${signal}` : `exit code ${exitCode}`
    console.error(`[dev] ${label} stopped with ${reason}`)
    shutdown(exitCode)
  })
}

function pipeChildOutput(source, target, onOutput) {
  source?.on('data', (chunk) => {
    target.write(chunk)
    onOutput?.(chunk)
  })
}

function resolvePnpmRunner() {
  const corepackPnpmPath = resolve(dirname(process.execPath), 'node_modules', 'corepack', 'dist', 'pnpm.js')
  if (existsSync(corepackPnpmPath)) {
    return { command: process.execPath, args: [corepackPnpmPath], shell: false }
  }
  return { command: 'pnpm', args: [], shell: process.platform === 'win32' }
}

function createBackendReadinessTracker(fallbackUrl, timeoutMs) {
  let serverStarted = false
  let dbServiceStarted = false
  let outputBuffer = ''
  let settled = false
  let resolveReady
  let rejectReady
  const startedAt = Date.now()
  const ready = new Promise((resolvePromise, rejectPromise) => {
    resolveReady = resolvePromise
    rejectReady = rejectPromise
  })
  const timeout = setTimeout(() => {
    rejectIfPending(new Error(`timed out waiting for backend startup logs: ${fallbackUrl}`))
  }, timeoutMs)
  const fallbackDelayMs = Math.min(10_000, Math.max(1000, Math.floor(timeoutMs / 4)))
  const fallback = setTimeout(() => {
    const remainingMs = Math.max(1000, timeoutMs - (Date.now() - startedAt))
    waitForBackendReady(fallbackUrl, remainingMs).then(resolveIfPending, rejectIfPending)
  }, fallbackDelayMs)

  return {
    ready,
    acceptChunk(chunk) {
      outputBuffer += chunk.toString('utf8')
      const lines = outputBuffer.split(/\r?\n/)
      outputBuffer = lines.pop() ?? ''
      for (const line of lines) {
        acceptBackendLogLine(line)
      }
    }
  }

  function acceptBackendLogLine(line) {
    if (line.includes('"event":"server_started"')) {
      serverStarted = true
    }
    if (line.includes('"event":"db_service_started"')) {
      dbServiceStarted = true
    }
    if (serverStarted && dbServiceStarted) {
      resolveIfPending()
    }
  }

  function resolveIfPending() {
    if (settled) return
    settled = true
    clearTimeout(timeout)
    clearTimeout(fallback)
    resolveReady()
  }

  function rejectIfPending(error) {
    if (settled) return
    settled = true
    clearTimeout(timeout)
    clearTimeout(fallback)
    rejectReady(error)
  }
}

async function waitForBackendReady(url, timeoutMs) {
  const deadline = Date.now() + timeoutMs
  let lastError = ''

  while (Date.now() < deadline) {
    if (backend?.exitCode !== null && backend?.exitCode !== undefined) {
      throw new Error(`backend exited before system API became ready, exit code ${backend.exitCode}`)
    }

    try {
      const response = await fetchWithTimeout(url, 1000)
      if (response.ok) return
      lastError = `HTTP ${response.status}`
    } catch (error) {
      lastError = error instanceof Error ? error.message : String(error)
    }

    await sleep(250)
  }

  const detail = lastError ? ` Last error: ${lastError}` : ''
  throw new Error(`timed out waiting for backend system API: ${url}.${detail}`)
}

async function fetchWithTimeout(url, timeoutMs) {
  const controller = new AbortController()
  const timeout = setTimeout(() => controller.abort(), timeoutMs)
  try {
    return await fetch(url, { signal: controller.signal })
  } finally {
    clearTimeout(timeout)
  }
}

function resolveBackendTarget() {
  const frontendEnv = loadFrontendEnv()
  return resolveDevelopmentBackendTarget(process.env, frontendEnv, loadBackendEnv())
}

function resolveRuntimeLogIndexerEnv() {
  const childEnv = { ...loadBackendEnv(), ...process.env }
  const configuredStore = firstConfiguredValue(
    childEnv.JUHE_AI_RUNTIME_LOG_STORE,
    childEnv.JUHE_AI_DATABASE_DRIVER
  )
  const inferredStore = childEnv.JUHE_AI_RUNTIME_MODE?.trim().toLowerCase() === 'performance' || childEnv.JUHE_AI_POSTGRES_URL?.trim()
    ? 'postgres'
    : 'sqlite'

  childEnv.JUHE_AI_RUNTIME_LOG_STORE = configuredStore ?? inferredStore
  childEnv.JUHE_AI_DATABASE_PATH = resolveBackendPath(
    childEnv.JUHE_AI_DATABASE_PATH,
    resolve(backendRoot, 'data', 'juhe-ai.sqlite3')
  )
  childEnv.JUHE_AI_DATASET_DATABASE_PATH = resolveBackendPath(
    childEnv.JUHE_AI_DATASET_DATABASE_PATH,
    resolve(backendRoot, 'data', 'juhe-ai-dataset.sqlite3')
  )
  childEnv.JUHE_AI_RUNTIME_LOG_DATABASE_PATH = resolveBackendPath(
    childEnv.JUHE_AI_RUNTIME_LOG_DATABASE_PATH,
    resolve(backendRoot, 'data', 'juhe-ai-runtime-log.sqlite3')
  )
  childEnv.JUHE_AI_TABLE_MONITOR_DATABASE_PATH = resolveBackendPath(
    childEnv.JUHE_AI_TABLE_MONITOR_DATABASE_PATH,
    resolve(backendRoot, 'data', 'juhe-ai-table-monitor.sqlite3')
  )
  childEnv.JUHE_AI_LOG_DIR = resolveBackendPath(childEnv.JUHE_AI_LOG_DIR, resolve(backendRoot, 'logs'))
  childEnv.JUHE_AI_RUNTIME_LOG_INSTANCE_ID = firstConfiguredValue(
    childEnv.JUHE_AI_RUNTIME_LOG_INSTANCE_ID,
    'dev-runtime-log-indexer'
  )
  delete childEnv.JUHE_AI_RUNTIME_LOG_ONCE

  return childEnv
}

function resolveTableMonitorEnv() {
  const childEnv = { ...loadBackendEnv(), ...process.env }
  const configuredStore = firstConfiguredValue(
    childEnv.JUHE_AI_TABLE_MONITOR_STORE,
    childEnv.JUHE_AI_DATABASE_DRIVER
  )
  const inferredStore = childEnv.JUHE_AI_RUNTIME_MODE?.trim().toLowerCase() === 'performance' || childEnv.JUHE_AI_POSTGRES_URL?.trim()
    ? 'postgres'
    : 'sqlite'

  childEnv.JUHE_AI_TABLE_MONITOR_STORE = configuredStore ?? inferredStore
  childEnv.JUHE_AI_DATABASE_PATH = resolveBackendPath(
    childEnv.JUHE_AI_DATABASE_PATH,
    resolve(backendRoot, 'data', 'juhe-ai.sqlite3')
  )
  childEnv.JUHE_AI_DATASET_DATABASE_PATH = resolveBackendPath(
    childEnv.JUHE_AI_DATASET_DATABASE_PATH,
    resolve(backendRoot, 'data', 'juhe-ai-dataset.sqlite3')
  )
  childEnv.JUHE_AI_USAGE_CATALOG_DATABASE_PATH = resolveBackendPath(
    childEnv.JUHE_AI_USAGE_CATALOG_DATABASE_PATH,
    resolve(backendRoot, 'data', 'juhe-ai-usage-catalog.sqlite3')
  )
  childEnv.JUHE_AI_STATS_DATABASE_PATH = resolveBackendPath(
    childEnv.JUHE_AI_STATS_DATABASE_PATH,
    resolve(backendRoot, 'data', 'juhe-ai-stats.sqlite3')
  )
  childEnv.JUHE_AI_TABLE_MONITOR_DATABASE_PATH = resolveBackendPath(
    childEnv.JUHE_AI_TABLE_MONITOR_DATABASE_PATH,
    resolve(backendRoot, 'data', 'juhe-ai-table-monitor.sqlite3')
  )
  childEnv.JUHE_AI_RUNTIME_LOG_DATABASE_PATH = resolveBackendPath(
    childEnv.JUHE_AI_RUNTIME_LOG_DATABASE_PATH,
    resolve(backendRoot, 'data', 'juhe-ai-runtime-log.sqlite3')
  )
  childEnv.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = resolveBackendPath(
    childEnv.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT,
    resolve(backendRoot, 'data', 'codex-context', 'state-shards')
  )
  childEnv.JUHE_AI_TABLE_MONITOR_INSTANCE_ID = firstConfiguredValue(
    childEnv.JUHE_AI_TABLE_MONITOR_INSTANCE_ID,
    'dev-table-monitor'
  )

  return childEnv
}

function resolveBackendPath(value, fallback) {
  const configuredValue = value?.trim()
  return configuredValue ? (isAbsolute(configuredValue) ? configuredValue : resolve(backendRoot, configuredValue)) : fallback
}

function firstConfiguredValue(...values) {
  for (const value of values) {
    const trimmed = value?.trim()
    if (trimmed) return trimmed
  }
  return undefined
}

function resolveBackendHealthUrl(target) {
  try {
    const base = target.endsWith('/') ? target : `${target}/`
    return new URL('__aisys__/api/health', base).toString()
  } catch {
    throw new Error(`invalid VITE_JUHE_AI_BACKEND_TARGET: ${target}`)
  }
}

function loadFrontendEnv() {
  const mode = process.env.MODE || 'development'
  const envFiles = [
    '.env',
    '.env.local',
    `.env.${mode}`,
    `.env.${mode}.local`
  ]
  const env = {}

  for (const fileName of envFiles) {
    const filePath = resolve(frontendRoot, fileName)
    if (!existsSync(filePath)) continue
    Object.assign(env, parseEnvFile(readFileSync(filePath, 'utf8')))
  }

  return env
}

function loadBackendEnv() {
  const baseEnv = loadEnvFile(resolve(backendRoot, '.env'))
  const overlayValue = process.env.JUHE_AI_ENV_FILE?.trim() || baseEnv.JUHE_AI_ENV_FILE?.trim()
  if (!overlayValue) return baseEnv

  const overlayPath = isAbsolute(overlayValue) ? overlayValue : resolve(backendRoot, overlayValue)
  return { ...baseEnv, ...loadEnvFile(overlayPath) }
}

function loadEnvFile(filePath) {
  return existsSync(filePath) ? parseEnvFile(readFileSync(filePath, 'utf8')) : {}
}

function parseEnvFile(content) {
  const env = {}
  for (const rawLine of content.split(/\r?\n/)) {
    const line = rawLine.trim()
    if (!line || line.startsWith('#')) continue
    const match = /^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$/.exec(line)
    if (!match) continue
    env[match[1]] = parseEnvValue(match[2])
  }
  return env
}

function parseEnvValue(value) {
  const trimmed = value.trim()
  if (trimmed.length >= 2) {
    const quote = trimmed[0]
    if ((quote === '"' || quote === "'") && trimmed[trimmed.length - 1] === quote) {
      return trimmed.slice(1, -1)
    }
  }
  const commentIndex = trimmed.indexOf(' #')
  return commentIndex >= 0 ? trimmed.slice(0, commentIndex).trim() : trimmed
}

function positiveInteger(value, fallback) {
  const parsed = Number(value)
  return Number.isInteger(parsed) && parsed > 0 ? parsed : fallback
}

function sleep(ms) {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
}

function shutdown(exitCode) {
  if (shuttingDown) return
  shuttingDown = true
  stopChild(frontend)
  stopChild(tableMonitor, { processGroup: process.platform !== 'win32' })
  stopChild(runtimeLogIndexer, { processGroup: process.platform !== 'win32' })
  stopChild(backend)
  process.exit(exitCode)
}

function stopChild(child, options = {}) {
  if (!child || child.exitCode !== null || child.killed || !child.pid) return

  if (process.platform === 'win32') {
    spawnSync('taskkill', ['/pid', String(child.pid), '/t', '/f'], { stdio: 'ignore' })
    return
  }

  if (options.processGroup) {
    try {
      process.kill(-child.pid, 'SIGTERM')
      return
    } catch (error) {
      if (!(error instanceof Error) || !('code' in error) || error.code !== 'ESRCH') {
        console.error(`[dev] failed to stop ${child.pid} process group: ${error instanceof Error ? error.message : String(error)}`)
      }
    }
  }

  child.kill('SIGTERM')
}

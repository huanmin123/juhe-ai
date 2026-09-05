import { spawn, spawnSync } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { existsSync, readFileSync } from 'node:fs'
import { dirname, isAbsolute, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  resolveDevelopmentBackendTarget
} from './dev-config.mjs'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const frontendRoot = resolve(root, 'frontend')
const backendGoRoot = resolve(root, 'backend-go')
// Node backend（juhe-ai-backend）已物理归档到 migration-backup/node/final-archive/
// （X02，2026-09-04）。dev 启动器为 go-only：Go gateway 直接提供管理面、公开面与
// /v1 网关路由；dev 专属数据/日志 fallback 统一落在 gitignored 的 .local/dev/ 下，
// 不再写回已删除的 backend/ 目录。
const devDataRoot = resolve(root, '.local', 'dev', 'data')
const devLogRoot = resolve(root, '.local', 'dev', 'logs')
const backendTarget = resolveBackendTarget()
const pnpmRunner = resolvePnpmRunner()

let frontend
let goGateway
let goJobs
let shuttingDown = false

let goProjectEnv

process.on('SIGINT', () => shutdown(130))
process.on('SIGTERM', () => shutdown(143))
process.on('SIGHUP', () => shutdown(129))

try {
  goProjectEnv = resolveGoProjectEnv()
  goGateway = startGoProject('gateway')
  goJobs = startGoProject('jobs')
  console.log('[dev] starting frontend...')
  frontend = startPnpm(['--filter', 'juhe-ai-frontend', 'dev'], 'frontend')
} catch (error) {
  console.error(`[dev] ${error instanceof Error ? error.message : String(error)}`)
  shutdown(1)
}

await new Promise(() => undefined)

function startPnpm(args, label) {
  const childEnv = {
    ...process.env,
    VITE_JUHE_AI_BACKEND_TARGET: backendTarget
  }
  const child = spawn(pnpmRunner.command, [...pnpmRunner.args, ...args], {
    cwd: root,
    env: childEnv,
    shell: pnpmRunner.shell,
    stdio: ['inherit', 'pipe', 'pipe']
  })

  pipeChildOutput(child.stdout, process.stdout)
  pipeChildOutput(child.stderr, process.stderr)

  monitorChild(child, label)

  return child
}

function startGoProject(project) {
  const command = project === 'gateway' ? 'juhe-ai-gateway' : 'juhe-ai-jobs'
  console.log(`[dev] starting Go ${project} project...`)
  const child = spawn('go', ['run', `./cmd/${command}`], {
    cwd: resolve(backendGoRoot, 'projects', project),
    env: goProjectEnv,
    detached: process.platform !== 'win32',
    shell: false,
    stdio: ['inherit', 'pipe', 'pipe']
  })
  pipeChildOutput(child.stdout, process.stdout)
  pipeChildOutput(child.stderr, process.stderr)
  monitorChild(child, `Go ${project}`)
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

function resolveBackendTarget() {
  const frontendEnv = loadFrontendEnv()
  return resolveDevelopmentBackendTarget(process.env, frontendEnv, loadBackendEnv())
}

function resolveGoProjectEnv() {
  const childEnv = { ...loadBackendEnv(), ...process.env }
  const inferredStore = childEnv.JUHE_AI_RUNTIME_MODE?.trim().toLowerCase() === 'performance' || childEnv.JUHE_AI_POSTGRES_URL?.trim()
    ? 'postgres'
    : 'sqlite'
  childEnv.JUHE_AI_RUNTIME_LOG_STORE = firstConfiguredValue(
    childEnv.JUHE_AI_RUNTIME_LOG_STORE,
    childEnv.JUHE_AI_DATABASE_DRIVER,
    inferredStore
  )
  childEnv.JUHE_AI_TABLE_MONITOR_STORE = firstConfiguredValue(
    childEnv.JUHE_AI_TABLE_MONITOR_STORE,
    childEnv.JUHE_AI_DATABASE_DRIVER,
    inferredStore
  )
  childEnv.JUHE_AI_AUDIT_LOG_STORE = firstConfiguredValue(
    childEnv.JUHE_AI_AUDIT_LOG_STORE,
    childEnv.JUHE_AI_DATABASE_DRIVER,
    childEnv.JUHE_AI_AUDIT_LOG_POSTGRES_URL ? 'postgres' : undefined,
    inferredStore
  )
  childEnv.JUHE_AI_OPERATION_LOG_STORE = firstConfiguredValue(
    childEnv.JUHE_AI_OPERATION_LOG_STORE,
    childEnv.JUHE_AI_DATABASE_DRIVER,
    childEnv.JUHE_AI_OPERATION_LOG_POSTGRES_URL ? 'postgres' : undefined,
    inferredStore
  )
  childEnv.JUHE_AI_DATABASE_PATH = resolveBackendPath(
    childEnv.JUHE_AI_DATABASE_PATH,
    resolve(devDataRoot, 'juhe-ai.sqlite3')
  )
  childEnv.JUHE_AI_DATASET_DATABASE_PATH = resolveBackendPath(
    childEnv.JUHE_AI_DATASET_DATABASE_PATH,
    resolve(devDataRoot, 'juhe-ai-dataset.sqlite3')
  )
  childEnv.JUHE_AI_RUNTIME_LOG_DATABASE_PATH = resolveBackendPath(
    childEnv.JUHE_AI_RUNTIME_LOG_DATABASE_PATH,
    resolve(devDataRoot, 'juhe-ai-runtime-log.sqlite3')
  )
  childEnv.JUHE_AI_TABLE_MONITOR_DATABASE_PATH = resolveBackendPath(
    childEnv.JUHE_AI_TABLE_MONITOR_DATABASE_PATH,
    resolve(devDataRoot, 'juhe-ai-table-monitor.sqlite3')
  )
  // In the dev profile, an explicitly enabled Go metrics PostgreSQL store may
  // reuse the already selected dev application connection. The child process
  // still receives a concrete dedicated variable; release/Compose paths keep
  // requiring an explicit metrics URL to avoid accidental cross-environment
  // writes.
  if (childEnv.JUHE_AI_GO_RUNTIME_METRICS_STORE?.trim().toLowerCase() === 'postgres'
    && !childEnv.JUHE_AI_GO_RUNTIME_METRICS_POSTGRES_URL?.trim()
    && childEnv.JUHE_AI_POSTGRES_URL?.trim()) {
    childEnv.JUHE_AI_GO_RUNTIME_METRICS_POSTGRES_URL = childEnv.JUHE_AI_POSTGRES_URL
  }
  if (childEnv.JUHE_AI_GO_RUNTIME_METRICS_STORE?.trim().toLowerCase() === 'sqlite'
    && childEnv.JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH?.trim()) {
    childEnv.JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH = resolveBackendPath(
      childEnv.JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH,
      childEnv.JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH
    )
  }
  childEnv.JUHE_AI_USAGE_CATALOG_DATABASE_PATH = resolveBackendPath(
    childEnv.JUHE_AI_USAGE_CATALOG_DATABASE_PATH,
    resolve(devDataRoot, 'juhe-ai-usage-catalog.sqlite3')
  )
  childEnv.JUHE_AI_STATS_DATABASE_PATH = resolveBackendPath(
    childEnv.JUHE_AI_STATS_DATABASE_PATH,
    resolve(devDataRoot, 'juhe-ai-stats.sqlite3')
  )
  childEnv.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = resolveBackendPath(
    childEnv.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT,
    resolve(devDataRoot, 'codex-context', 'state-shards')
  )
  childEnv.JUHE_AI_LOG_DIR = resolveBackendPath(childEnv.JUHE_AI_LOG_DIR, devLogRoot)
  childEnv.JUHE_AI_RUNTIME_LOG_INSTANCE_ID = firstConfiguredValue(
    childEnv.JUHE_AI_RUNTIME_LOG_INSTANCE_ID,
    'dev-go-jobs-runtime-log'
  )
  childEnv.JUHE_AI_TABLE_MONITOR_INSTANCE_ID = firstConfiguredValue(
    childEnv.JUHE_AI_TABLE_MONITOR_INSTANCE_ID,
    'dev-go-jobs-table-monitor'
  )
  const instanceID = firstConfiguredValue(
    childEnv.JUHE_AI_AUDIT_LOG_INSTANCE_ID,
    `dev-go-gateway-audit-log-pid-${process.pid}`
  )
  const secret = firstConfiguredValue(
    childEnv.JUHE_AI_AUDIT_LOG_INPUT_SECRET,
    randomBytes(32).toString('hex')
  )
  const listenAddress = firstConfiguredValue(childEnv.JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS, '127.0.0.1:3303')
  const inputPort = listenAddress.slice(listenAddress.lastIndexOf(':') + 1)
  childEnv.JUHE_AI_AUDIT_LOG_INSTANCE_ID = instanceID
  childEnv.JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS = listenAddress
  childEnv.JUHE_AI_AUDIT_LOG_INPUT_SECRET = secret
  childEnv.JUHE_AI_AUDIT_LOG_INPUT_URL = firstConfiguredValue(childEnv.JUHE_AI_AUDIT_LOG_INPUT_URL, `http://127.0.0.1:${inputPort}`)
  childEnv.JUHE_AI_AUDIT_LOG_DATABASE_PATH = resolveBackendPath(childEnv.JUHE_AI_AUDIT_LOG_DATABASE_PATH, resolve(devDataRoot, 'juhe-ai-audit-log.sqlite3'))
  childEnv.JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY = resolveBackendPath(childEnv.JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY, resolve(devDataRoot, 'audit-payload-blobs'))
  childEnv.JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY = resolveBackendPath(childEnv.JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY, resolve(devDataRoot, 'audit-hot-search'))
  childEnv.JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH = resolveBackendPath(childEnv.JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH, childEnv.JUHE_AI_DATABASE_PATH || resolve(devDataRoot, 'juhe-ai.sqlite3'))
  const operationInstanceID = firstConfiguredValue(
    childEnv.JUHE_AI_OPERATION_LOG_INSTANCE_ID,
    `dev-go-gateway-operation-log-pid-${process.pid}`
  )
  const operationSecret = firstConfiguredValue(
    childEnv.JUHE_AI_OPERATION_LOG_INPUT_SECRET,
    randomBytes(32).toString('hex')
  )
  const operationListenAddress = firstConfiguredValue(childEnv.JUHE_AI_OPERATION_LOG_INPUT_LISTEN_ADDRESS, '127.0.0.1:3304')
  const operationInputPort = operationListenAddress.slice(operationListenAddress.lastIndexOf(':') + 1)
  childEnv.JUHE_AI_OPERATION_LOG_INSTANCE_ID = operationInstanceID
  childEnv.JUHE_AI_OPERATION_LOG_INPUT_LISTEN_ADDRESS = operationListenAddress
  childEnv.JUHE_AI_OPERATION_LOG_INPUT_SECRET = operationSecret
  childEnv.JUHE_AI_OPERATION_LOG_INPUT_URL = firstConfiguredValue(childEnv.JUHE_AI_OPERATION_LOG_INPUT_URL, `http://127.0.0.1:${operationInputPort}`)
  childEnv.JUHE_AI_OPERATION_LOG_DATABASE_PATH = resolveBackendPath(childEnv.JUHE_AI_OPERATION_LOG_DATABASE_PATH, resolve(devDataRoot, 'juhe-ai-operation-log.sqlite3'))
  childEnv.JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH = resolveBackendPath(childEnv.JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH, childEnv.JUHE_AI_DATABASE_PATH || resolve(devDataRoot, 'juhe-ai.sqlite3'))
  childEnv.JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS = firstConfiguredValue(childEnv.JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS, '127.0.0.1:3305')
  childEnv.JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS = firstConfiguredValue(childEnv.JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS, '127.0.0.1:3306')
  childEnv.JUHE_AI_DATABASE_PATH = resolveBackendPath(childEnv.JUHE_AI_DATABASE_PATH, resolve(devDataRoot, 'juhe-ai.sqlite3'))
  childEnv.JUHE_AI_DATASET_DATABASE_PATH = resolveBackendPath(childEnv.JUHE_AI_DATASET_DATABASE_PATH, resolve(devDataRoot, 'juhe-ai-dataset.sqlite3'))
  childEnv.JUHE_AI_USAGE_CATALOG_DATABASE_PATH = resolveBackendPath(childEnv.JUHE_AI_USAGE_CATALOG_DATABASE_PATH, resolve(devDataRoot, 'juhe-ai-usage-catalog.sqlite3'))
  childEnv.JUHE_AI_STATS_DATABASE_PATH = resolveBackendPath(childEnv.JUHE_AI_STATS_DATABASE_PATH, resolve(devDataRoot, 'juhe-ai-stats.sqlite3'))
  childEnv.JUHE_AI_RUNTIME_LOG_DATABASE_PATH = resolveBackendPath(childEnv.JUHE_AI_RUNTIME_LOG_DATABASE_PATH, resolve(devDataRoot, 'juhe-ai-runtime-log.sqlite3'))
  childEnv.JUHE_AI_TABLE_MONITOR_DATABASE_PATH = resolveBackendPath(childEnv.JUHE_AI_TABLE_MONITOR_DATABASE_PATH, resolve(devDataRoot, 'juhe-ai-table-monitor.sqlite3'))
  childEnv.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = resolveBackendPath(childEnv.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT, resolve(devDataRoot, 'codex-context', 'state-shards'))
  childEnv.JUHE_AI_USAGE_SHARD_ROOT = resolveBackendPath(childEnv.JUHE_AI_USAGE_SHARD_ROOT, resolve(devDataRoot, 'usage-shards'))
  childEnv.JUHE_AI_AUDIT_LOG_OWNER_LEASE = firstConfiguredValue(childEnv.JUHE_AI_AUDIT_LOG_OWNER_LEASE, '30s')
  childEnv.JUHE_AI_AUDIT_LOG_RETENTION_INTERVAL = firstConfiguredValue(childEnv.JUHE_AI_AUDIT_LOG_RETENTION_INTERVAL, '1m')
  if (childEnv.JUHE_AI_AUDIT_LOG_STORE === 'postgres' && !childEnv.JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL) {
    childEnv.JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL = childEnv.JUHE_AI_AUDIT_LOG_POSTGRES_URL ?? childEnv.JUHE_AI_POSTGRES_URL ?? ''
  }
  if (childEnv.JUHE_AI_OPERATION_LOG_STORE === 'postgres' && !childEnv.JUHE_AI_OPERATION_LOG_POSTGRES_URL) {
    childEnv.JUHE_AI_OPERATION_LOG_POSTGRES_URL = childEnv.JUHE_AI_POSTGRES_URL ?? ''
  }
  return childEnv
}

function resolveBackendPath(value, fallback) {
  const configuredValue = value?.trim()
  return configuredValue ? (isAbsolute(configuredValue) ? configuredValue : resolve(devDataRoot, configuredValue)) : fallback
}

function firstConfiguredValue(...values) {
  for (const value of values) {
    const trimmed = value?.trim()
    if (trimmed) return trimmed
  }
  return undefined
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
  // backend/.env 随 Node backend 归档移除；dev 环境变量现在从仓库根 .env
  // 读取（JUHE_AI_ENV_FILE overlay 相对仓库根解析）。
  const baseEnv = loadEnvFile(resolve(root, '.env'))
  const overlayValue = process.env.JUHE_AI_ENV_FILE?.trim() || baseEnv.JUHE_AI_ENV_FILE?.trim()
  if (!overlayValue) return baseEnv

  const overlayPath = isAbsolute(overlayValue) ? overlayValue : resolve(root, overlayValue)
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

function shutdown(exitCode) {
  if (shuttingDown) return
  shuttingDown = true
  stopChild(frontend)
  stopChild(goJobs, { processGroup: process.platform !== 'win32' })
  stopChild(goGateway, { processGroup: process.platform !== 'win32' })
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

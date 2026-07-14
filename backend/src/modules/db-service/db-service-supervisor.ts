import { fork, type ChildProcess } from 'node:child_process'
import { existsSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { attachDbServiceProcess } from './db-service-ipc.js'

let dbServiceProcess: ChildProcess | undefined
let restartTimer: NodeJS.Timeout | undefined
let restartAttempts = 0
let stopping = false
let shutdownHooksInstalled = false
let dbServiceReady = false
const dbServiceReadyCallbacks = new Set<() => void>()

const currentModulePath = fileURLToPath(import.meta.url)
const sourceRoot = resolve(dirname(currentModulePath), '../..')
const backendRoot = resolve(sourceRoot, '..')
const dbServiceSourcePath = resolve(sourceRoot, 'db-service.ts')
const dbServiceDistPath = resolve(sourceRoot, 'db-service.js')
const dbServiceRestartBaseDelayMs = 1000
const dbServiceRestartMaxDelayMs = 30_000

interface DbServiceSupervisorOptions {
  onReady?: () => void
}

export function startDbServiceSupervisor(options: DbServiceSupervisorOptions = {}): void {
  if (runtimeConfig.processRole !== 'server') {
    return
  }

  if (options.onReady) {
    registerDbServiceReadyCallback(options.onReady)
  }

  if (dbServiceProcess) {
    return
  }

  stopping = false
  startDbServiceProcess()
  installSupervisorShutdownHooks()
}

function startDbServiceProcess(): void {
  const entry = resolveDbServiceEntry()
  dbServiceReady = false
  const child = fork(entry.modulePath, [], {
    cwd: backendRoot,
    env: {
      ...process.env,
      JUHE_AI_PROCESS_ROLE: 'db-service'
    },
    execArgv: entry.execArgv,
    serialization: 'advanced',
    stdio: ['ignore', 'pipe', 'pipe', 'ipc']
  })

  dbServiceProcess = child
  attachDbServiceProcess(child, {
    onReady: () => {
      restartAttempts = 0
      dbServiceReady = true
      notifyDbServiceReadyCallbacks()
    }
  })
  pipeDbServiceOutput(child)

  logger.info({
    event: 'db_service_spawned',
    pid: child.pid,
    modulePath: entry.modulePath,
    execArgv: entry.execArgv
  }, '数据库服务已创建')

  child.once('exit', (code, signal) => {
    logger.warn({
      event: 'db_service_exited',
      pid: child.pid,
      code,
      signal,
      stopping
    }, '数据库服务已退出')
    if (dbServiceProcess !== child) {
      return
    }
    dbServiceProcess = undefined
    dbServiceReady = false
    if (!stopping) {
      scheduleDbServiceRestart()
    }
  })

  child.once('error', (error) => {
    logger.error(errorLogFields(error, {
      event: 'db_service_spawn_failed'
    }), '数据库服务启动失败')
    if (dbServiceProcess !== child) {
      return
    }
    dbServiceProcess = undefined
    dbServiceReady = false
    if (!stopping) {
      scheduleDbServiceRestart()
    }
  })
}

function registerDbServiceReadyCallback(callback: () => void): void {
  dbServiceReadyCallbacks.add(callback)
  if (dbServiceReady) {
    const timer = setTimeout(() => runDbServiceReadyCallback(callback), 0)
    timer.unref()
  }
}

function notifyDbServiceReadyCallbacks(): void {
  for (const callback of dbServiceReadyCallbacks) {
    runDbServiceReadyCallback(callback)
  }
}

function runDbServiceReadyCallback(callback: () => void): void {
  try {
    callback()
  } catch (error) {
    logger.error(errorLogFields(error, {
      event: 'db_service_ready_callback_failed'
    }), 'DB service ready 回调执行失败')
  }
}

function resolveDbServiceEntry(): { modulePath: string; execArgv: string[] } {
  if (existsSync(dbServiceDistPath)) {
    return {
      modulePath: dbServiceDistPath,
      execArgv: []
    }
  }

  return {
    modulePath: dbServiceSourcePath,
    execArgv: process.execArgv.filter((arg) => !arg.startsWith('--inspect'))
  }
}

function pipeDbServiceOutput(child: ChildProcess): void {
  child.stdout?.on('data', (chunk: Buffer) => {
    process.stdout.write(chunk)
  })
  child.stderr?.on('data', (chunk: Buffer) => {
    process.stderr.write(chunk)
  })
}

function scheduleDbServiceRestart(): void {
  if (restartTimer) {
    return
  }

  restartAttempts += 1
  const delayMs = Math.min(dbServiceRestartMaxDelayMs, dbServiceRestartBaseDelayMs * 2 ** Math.min(restartAttempts - 1, 5))
  restartTimer = setTimeout(() => {
    restartTimer = undefined
    startDbServiceProcess()
  }, delayMs)
  restartTimer.unref()
}

function installSupervisorShutdownHooks(): void {
  if (shutdownHooksInstalled) {
    return
  }
  shutdownHooksInstalled = true

  process.once('exit', () => stopDbServiceSupervisor())
}

export function stopDbServiceSupervisor(): void {
  stopping = true
  if (restartTimer) {
    clearTimeout(restartTimer)
    restartTimer = undefined
  }
  if (dbServiceProcess && !dbServiceProcess.killed) {
    dbServiceProcess.kill('SIGTERM')
  }
}

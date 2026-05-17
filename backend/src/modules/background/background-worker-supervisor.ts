import { fork, type ChildProcess } from 'node:child_process'
import { existsSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { attachBackgroundWorkerProcess } from './background-ipc.js'

let workerProcess: ChildProcess | undefined
let restartTimer: NodeJS.Timeout | undefined
let restartAttempts = 0
let stopping = false
let shutdownHooksInstalled = false

const currentModulePath = fileURLToPath(import.meta.url)
const sourceRoot = resolve(dirname(currentModulePath), '../..')
const backendRoot = resolve(sourceRoot, '..')
const workerSourcePath = resolve(sourceRoot, 'worker.ts')
const workerDistPath = resolve(sourceRoot, 'worker.js')
const workerRestartBaseDelayMs = 1000
const workerRestartMaxDelayMs = 30_000

export function startBackgroundWorkerSupervisor(): void {
  if (runtimeConfig.processRole !== 'server' || workerProcess) {
    return
  }

  stopping = false
  startWorkerProcess()
  installSupervisorShutdownHooks()
}

function startWorkerProcess(): void {
  const entry = resolveWorkerEntry()
  const child = fork(entry.modulePath, [], {
    cwd: backendRoot,
    env: {
      ...process.env,
      JUHE_AI_PROCESS_ROLE: 'worker'
    },
    execArgv: entry.execArgv,
    serialization: 'advanced',
    stdio: ['ignore', 'pipe', 'pipe', 'ipc']
  })

  workerProcess = child
  attachBackgroundWorkerProcess(child)
  pipeWorkerOutput(child)

  logger.info({
    event: 'background_worker_spawned',
    pid: child.pid,
    modulePath: entry.modulePath,
    execArgv: entry.execArgv
  }, '后台 worker 已创建')

  child.once('exit', (code, signal) => {
    workerProcess = undefined
    logger.warn({
      event: 'background_worker_exited',
      pid: child.pid,
      code,
      signal,
      stopping
    }, '后台 worker 已退出')
    if (!stopping) {
      scheduleWorkerRestart()
    }
  })

  child.once('error', (error) => {
    logger.error(errorLogFields(error, {
      event: 'background_worker_spawn_failed'
    }), '后台 worker 启动失败')
  })
}

function resolveWorkerEntry(): { modulePath: string; execArgv: string[] } {
  if (existsSync(workerDistPath)) {
    return {
      modulePath: workerDistPath,
      execArgv: []
    }
  }

  return {
    modulePath: workerSourcePath,
    execArgv: process.execArgv.filter((arg) => !arg.startsWith('--inspect'))
  }
}

function pipeWorkerOutput(child: ChildProcess): void {
  child.stdout?.on('data', (chunk: Buffer) => {
    process.stdout.write(chunk)
  })
  child.stderr?.on('data', (chunk: Buffer) => {
    process.stderr.write(chunk)
  })
}

function scheduleWorkerRestart(): void {
  if (restartTimer) {
    return
  }

  restartAttempts += 1
  const delayMs = Math.min(workerRestartMaxDelayMs, workerRestartBaseDelayMs * 2 ** Math.min(restartAttempts - 1, 5))
  restartTimer = setTimeout(() => {
    restartTimer = undefined
    startWorkerProcess()
  }, delayMs)
  restartTimer.unref()
}

function installSupervisorShutdownHooks(): void {
  if (shutdownHooksInstalled) {
    return
  }
  shutdownHooksInstalled = true

  process.once('exit', () => stopWorkerProcess())
  process.once('SIGINT', () => exitAfterWorkerStop(0))
  process.once('SIGTERM', () => exitAfterWorkerStop(0))
}

function stopWorkerProcess(): void {
  stopping = true
  if (restartTimer) {
    clearTimeout(restartTimer)
    restartTimer = undefined
  }
  if (workerProcess && !workerProcess.killed) {
    workerProcess.kill('SIGTERM')
  }
}

function exitAfterWorkerStop(exitCode: number): never {
  stopWorkerProcess()
  process.exit(exitCode)
}

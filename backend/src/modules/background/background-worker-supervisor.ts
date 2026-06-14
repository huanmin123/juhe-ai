import { fork, type ChildProcess } from 'node:child_process'
import { existsSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { attachBackgroundWorkerProcess, type BackgroundWorkerProcessRole } from './background-ipc.js'

interface SupervisedWorkerState {
  process?: ChildProcess
  restartTimer?: NodeJS.Timeout
  restartAttempts: number
}

const supervisedWorkerRoles: BackgroundWorkerProcessRole[] = [
  'worker',
  'metrics-worker',
  'ingest-worker',
  'stats-worker',
  'snapshot-worker',
  'probe-worker',
  'maintenance-worker'
]
const supervisedWorkers = new Map<BackgroundWorkerProcessRole, SupervisedWorkerState>(
  supervisedWorkerRoles.map((role) => [role, { restartAttempts: 0 }])
)
let stopping = false
let shutdownHooksInstalled = false
let startupSequenceRunning = false

const currentModulePath = fileURLToPath(import.meta.url)
const sourceRoot = resolve(dirname(currentModulePath), '../..')
const backendRoot = resolve(sourceRoot, '..')
const workerSourcePath = resolve(sourceRoot, 'worker.ts')
const workerDistPath = resolve(sourceRoot, 'worker.js')
const workerRestartBaseDelayMs = 1000
const workerRestartMaxDelayMs = 30_000
const workerStartupReadyTimeoutMs = 1_500
const workerStartupStaggerMs = 250

export function startBackgroundWorkerSupervisor(): void {
  if (runtimeConfig.processRole !== 'server') {
    return
  }

  stopping = false
  startWorkerProcessesInSequence()
  installSupervisorShutdownHooks()
}

function startWorkerProcessesInSequence(): void {
  if (startupSequenceRunning) {
    return
  }
  startupSequenceRunning = true
  let index = 0
  const startNext = () => {
    if (stopping) {
      startupSequenceRunning = false
      return
    }
    const role = supervisedWorkerRoles[index]
    index += 1
    if (!role) {
      startupSequenceRunning = false
      return
    }
    startWorkerProcess(role, {
      onStartupSettled: () => {
        const timer = setTimeout(startNext, workerStartupStaggerMs)
        timer.unref()
      }
    })
  }
  startNext()
}

function startWorkerProcess(
  role: BackgroundWorkerProcessRole,
  options: { onStartupSettled?: () => void } = {}
): void {
  const state = supervisedWorkerState(role)
  if (state.process) {
    options.onStartupSettled?.()
    return
  }

  const entry = resolveWorkerEntry()
  let startupSettled = false
  const startupTimeout = options.onStartupSettled
    ? setTimeout(() => settleStartup(), workerStartupReadyTimeoutMs)
    : undefined
  startupTimeout?.unref()
  const settleStartup = () => {
    if (startupSettled) {
      return
    }
    startupSettled = true
    if (startupTimeout) {
      clearTimeout(startupTimeout)
    }
    options.onStartupSettled?.()
  }
  const child = fork(entry.modulePath, [], {
    cwd: backendRoot,
    env: {
      ...process.env,
      JUHE_AI_PROCESS_ROLE: 'worker',
      JUHE_AI_WORKER_ROLE: role
    },
    execArgv: entry.execArgv,
    serialization: 'advanced',
    stdio: ['ignore', 'pipe', 'pipe', 'ipc']
  })

  state.process = child
  attachBackgroundWorkerProcess(child, {
    role,
    onReady: () => {
      state.restartAttempts = 0
      settleStartup()
    }
  })
  pipeWorkerOutput(child)

  logger.info({
    event: 'background_worker_spawned',
    workerRole: role,
    pid: child.pid,
    modulePath: entry.modulePath,
    execArgv: entry.execArgv
  }, backgroundWorkerRoleMessage(role, '已创建'))

  child.once('exit', (code, signal) => {
    settleStartup()
    logger.warn({
      event: 'background_worker_exited',
      workerRole: role,
      pid: child.pid,
      code,
      signal,
      stopping
    }, backgroundWorkerRoleMessage(role, '已退出'))
    if (state.process !== child) {
      return
    }
    state.process = undefined
    if (!stopping) {
      scheduleWorkerRestart(role)
    }
  })

  child.once('error', (error) => {
    settleStartup()
    logger.error(errorLogFields(error, {
      event: 'background_worker_spawn_failed',
      workerRole: role
    }), backgroundWorkerRoleMessage(role, '启动失败'))
    if (state.process !== child) {
      return
    }
    state.process = undefined
    if (!stopping) {
      scheduleWorkerRestart(role)
    }
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

function scheduleWorkerRestart(role: BackgroundWorkerProcessRole): void {
  const state = supervisedWorkerState(role)
  if (state.restartTimer) {
    return
  }

  state.restartAttempts += 1
  const delayMs = Math.min(workerRestartMaxDelayMs, workerRestartBaseDelayMs * 2 ** Math.min(state.restartAttempts - 1, 5))
  state.restartTimer = setTimeout(() => {
    state.restartTimer = undefined
    startWorkerProcess(role)
  }, delayMs)
  state.restartTimer.unref()
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
  for (const state of supervisedWorkers.values()) {
    if (state.restartTimer) {
      clearTimeout(state.restartTimer)
      state.restartTimer = undefined
    }
    if (state.process && !state.process.killed) {
      state.process.kill('SIGTERM')
    }
  }
}

function exitAfterWorkerStop(exitCode: number): never {
  stopWorkerProcess()
  process.exit(exitCode)
}

function supervisedWorkerState(role: BackgroundWorkerProcessRole): SupervisedWorkerState {
  const state = supervisedWorkers.get(role)
  if (!state) {
    throw new Error(`未知后台 worker 角色：${role}`)
  }
  return state
}

function backgroundWorkerRoleMessage(role: BackgroundWorkerProcessRole, action: string): string {
  if (role === 'metrics-worker') return `后台 metrics-worker ${action}`
  if (role === 'ingest-worker') return `后台 ingest-worker ${action}`
  if (role === 'stats-worker') return `后台 stats-worker ${action}`
  if (role === 'snapshot-worker') return `后台 snapshot-worker ${action}`
  if (role === 'probe-worker') return `后台 probe-worker ${action}`
  if (role === 'maintenance-worker') return `后台 maintenance-worker ${action}`
  return `后台 worker ${action}`
}

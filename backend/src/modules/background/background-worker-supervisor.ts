import { fork, type ChildProcess } from 'node:child_process'
import { existsSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { forwardSupervisorOutput } from '../../shared/supervisor-output.js'
import {
  createSupervisorRestartState,
  recordSupervisorChildReady,
  recordSupervisorChildStopped,
  supervisorRestartDelayMs,
  type SupervisorRestartState
} from '../../shared/supervisor-restart-policy.js'
import {
  attachBackgroundAuxiliaryWorkerProcess,
  attachBackgroundWorkerProcess,
  type BackgroundWorkerProcessRole
} from './background-ipc.js'

interface SupervisedWorkerState {
  process?: ChildProcess
  restartTimer?: NodeJS.Timeout
  restartState: SupervisorRestartState
  ready: boolean
}

export interface BackgroundWorkerSupervisorProcessRuntime {
  key: string
  role: BackgroundWorkerProcessRole
  replicaIndex: number
  pid?: number
  ready: boolean
}

interface SupervisedWorkerSpec {
  key: string
  role: BackgroundWorkerProcessRole
  replicaIndex: number
  ipcRole?: BackgroundWorkerProcessRole
}

const supervisedWorkerSpecs = buildSupervisedWorkerSpecs()
const supervisedWorkers = new Map<string, SupervisedWorkerState>(
  supervisedWorkerSpecs.map((spec) => [spec.key, { restartState: createSupervisorRestartState(), ready: false }])
)
let stopping = false
let shutdownHooksInstalled = false
let startupSequenceRunning = false

const currentModulePath = fileURLToPath(import.meta.url)
const sourceRoot = resolve(dirname(currentModulePath), '../..')
const backendRoot = resolve(sourceRoot, '..')
const workerSourcePath = resolve(sourceRoot, 'worker.ts')
const workerDistPath = resolve(sourceRoot, 'worker.js')
const workerStartupReadyTimeoutMs = 1_500
const workerStartupStaggerMs = 250

export function startBackgroundWorkerSupervisor(): void {
  if (runtimeConfig.processRole !== 'server'
    || runtimeConfig.blueGreenOwnerMode === 'drain'
    || !runtimeConfig.topology.backgroundWorkerSupervisorEnabled) {
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
    const spec = supervisedWorkerSpecs[index]
    index += 1
    if (!spec) {
      startupSequenceRunning = false
      return
    }
    startWorkerProcess(spec, {
      onStartupSettled: () => {
        const timer = setTimeout(startNext, workerStartupStaggerMs)
        timer.unref()
      }
    })
  }
  startNext()
}

function startWorkerProcess(
  spec: SupervisedWorkerSpec,
  options: { onStartupSettled?: () => void } = {}
): void {
  const state = supervisedWorkerState(spec.key)
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
      JUHE_AI_WORKER_ROLE: spec.role,
      JUHE_AI_WORKER_REPLICA_INDEX: String(spec.replicaIndex),
      JUHE_AI_INSTANCE_ID: `${runtimeConfig.instanceId}-${spec.role}-${spec.replicaIndex + 1}`
    },
    execArgv: entry.execArgv,
    serialization: 'advanced',
    stdio: ['ignore', 'pipe', 'pipe', 'ipc']
  })

  state.process = child
  state.ready = false
  if (spec.ipcRole) {
    attachBackgroundWorkerProcess(child, {
      role: spec.ipcRole,
      onReady: () => {
        state.ready = true
        state.restartState = recordSupervisorChildReady(state.restartState, Date.now())
        settleStartup()
      }
    })
  } else if (spec.role === 'usage-worker' || spec.role === 'log-worker') {
    attachBackgroundAuxiliaryWorkerProcess(child, {
      role: spec.role,
      onReady: () => {
        state.ready = true
        state.restartState = recordSupervisorChildReady(state.restartState, Date.now())
        settleStartup()
      }
    })
  }
  pipeWorkerOutput(child)

  logger.info({
    event: 'background_worker_spawned',
    workerRole: spec.role,
    workerReplicaIndex: spec.replicaIndex,
    workerInstanceKey: spec.key,
    pid: child.pid,
    modulePath: entry.modulePath,
    execArgv: entry.execArgv
  }, backgroundWorkerRoleMessage(spec, '已创建'))

  child.once('exit', (code, signal) => {
    settleStartup()
    logger.warn({
      event: 'background_worker_exited',
      workerRole: spec.role,
      workerReplicaIndex: spec.replicaIndex,
      workerInstanceKey: spec.key,
      pid: child.pid,
      code,
      signal,
      stopping
    }, backgroundWorkerRoleMessage(spec, '已退出'))
    if (state.process !== child) {
      return
    }
    state.process = undefined
    state.ready = false
    if (!stopping) {
      state.restartState = recordSupervisorChildStopped(state.restartState, Date.now())
      scheduleWorkerRestart(spec)
    }
  })

  child.once('error', (error) => {
    settleStartup()
    logger.error(errorLogFields(error, {
      event: 'background_worker_spawn_failed',
      workerRole: spec.role,
      workerReplicaIndex: spec.replicaIndex,
      workerInstanceKey: spec.key
    }), backgroundWorkerRoleMessage(spec, '启动失败'))
    if (state.process !== child) {
      return
    }
    state.process = undefined
    state.ready = false
    if (!stopping) {
      state.restartState = recordSupervisorChildStopped(state.restartState, Date.now())
      scheduleWorkerRestart(spec)
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
    forwardSupervisorOutput(process.stdout, chunk)
  })
  child.stderr?.on('data', (chunk: Buffer) => {
    forwardSupervisorOutput(process.stderr, chunk)
  })
}

function scheduleWorkerRestart(spec: SupervisedWorkerSpec): void {
  const state = supervisedWorkerState(spec.key)
  if (state.restartTimer) {
    return
  }

  const delayMs = supervisorRestartDelayMs(state.restartState.restartAttempts)
  state.restartTimer = setTimeout(() => {
    state.restartTimer = undefined
    startWorkerProcess(spec)
  }, delayMs)
  state.restartTimer.unref()
}

function installSupervisorShutdownHooks(): void {
  if (shutdownHooksInstalled) {
    return
  }
  shutdownHooksInstalled = true

  process.once('exit', () => stopBackgroundWorkerSupervisor())
}

export function stopBackgroundWorkerSupervisor(): void {
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

function supervisedWorkerState(key: string): SupervisedWorkerState {
  const state = supervisedWorkers.get(key)
  if (!state) {
    throw new Error(`未知后台 worker 实例：${key}`)
  }
  return state
}

export function getBackgroundWorkerSupervisorRuntime(): BackgroundWorkerSupervisorProcessRuntime[] {
  if (runtimeConfig.blueGreenOwnerMode === 'drain' || !runtimeConfig.topology.backgroundWorkerSupervisorEnabled) return []
  return supervisedWorkerSpecs.map((spec) => {
    const state = supervisedWorkerState(spec.key)
    return {
      key: spec.key,
      role: spec.role,
      replicaIndex: spec.replicaIndex,
      pid: state.process?.pid,
      ready: state.ready
    }
  })
}

function backgroundWorkerRoleMessage(spec: SupervisedWorkerSpec, action: string): string {
  return `后台 ${spec.role}#${spec.replicaIndex + 1} ${action}`
}

function buildSupervisedWorkerSpecs(): SupervisedWorkerSpec[] {
  if (runtimeConfig.runtimeMode !== 'performance') {
    return [
      workerSpec('ingest-worker', 0, 'ingest-worker'),
      workerSpec('stats-worker', 0, 'stats-worker'),
      workerSpec('ops-worker', 0, 'ops-worker')
    ]
  }

  return [
    ...replicatedWorkerSpecs('usage-worker', runtimeConfig.topology.usageWorkerReplicas, 'ingest-worker'),
    ...replicatedWorkerSpecs('log-worker', runtimeConfig.topology.logWorkerReplicas),
    ...replicatedWorkerSpecs('stats-worker', runtimeConfig.topology.statsWorkerReplicas, 'stats-worker'),
    ...replicatedWorkerSpecs('ops-worker', runtimeConfig.topology.opsWorkerReplicas, 'ops-worker')
  ]
}

function replicatedWorkerSpecs(
  role: BackgroundWorkerProcessRole,
  count: number,
  primaryIpcRole?: BackgroundWorkerProcessRole
): SupervisedWorkerSpec[] {
  return Array.from({ length: count }, (_value, replicaIndex) =>
    workerSpec(role, replicaIndex, replicaIndex === 0 ? primaryIpcRole : undefined))
}

function workerSpec(
  role: BackgroundWorkerProcessRole,
  replicaIndex: number,
  ipcRole?: BackgroundWorkerProcessRole
): SupervisedWorkerSpec {
  return {
    key: `${role}:${replicaIndex}`,
    role,
    replicaIndex,
    ipcRole
  }
}

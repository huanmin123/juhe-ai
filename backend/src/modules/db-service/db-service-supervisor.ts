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
  supervisorRestartDelayMs
} from '../../shared/supervisor-restart-policy.js'
import { attachDbServiceProcess, getDbServiceState } from './db-service-ipc.js'
import {
  createDbServiceHealthRecoveryState,
  dbServiceHealthRecoveryDefaults,
  recordDbServiceHealthProbe,
  resetDbServiceHealthRecoveryState
} from './db-service-health-recovery.js'

let dbServiceProcess: ChildProcess | undefined
let restartTimer: NodeJS.Timeout | undefined
let dbServiceHealthTimer: NodeJS.Timeout | undefined
let dbServiceForceKillTimer: NodeJS.Timeout | undefined
let dbServiceHealthProbeInFlight = false
let dbServiceHealthRecoveryState = createDbServiceHealthRecoveryState(0)
let restartState = createSupervisorRestartState()
let stopping = false
let shutdownHooksInstalled = false
let dbServiceReady = false
const dbServiceReadyCallbacks = new Set<() => void>()

const currentModulePath = fileURLToPath(import.meta.url)
const sourceRoot = resolve(dirname(currentModulePath), '../..')
const backendRoot = resolve(sourceRoot, '..')
const dbServiceSourcePath = resolve(sourceRoot, 'db-service.ts')
const dbServiceDistPath = resolve(sourceRoot, 'db-service.js')
const dbServiceHealthProbeIntervalMs = 15_000
const dbServiceHealthProbeTimeoutMs = 5_000
const dbServiceForceKillDelayMs = 10_000

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
  dbServiceHealthRecoveryState = resetDbServiceHealthRecoveryState(dbServiceHealthRecoveryState, Date.now())
  startDbServiceHealthMonitor(child)
  attachDbServiceProcess(child, {
    onReady: () => {
      restartState = recordSupervisorChildReady(restartState, Date.now())
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
    clearDbServiceHealthMonitor()
    dbServiceProcess = undefined
    dbServiceReady = false
    if (!stopping) {
      restartState = recordSupervisorChildStopped(restartState, Date.now())
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
    clearDbServiceHealthMonitor()
    dbServiceProcess = undefined
    dbServiceReady = false
    if (!stopping) {
      restartState = recordSupervisorChildStopped(restartState, Date.now())
      scheduleDbServiceRestart()
    }
  })
}

function startDbServiceHealthMonitor(child: ChildProcess): void {
  clearDbServiceHealthProbeTimer()
  dbServiceHealthTimer = setInterval(() => {
    void probeDbServiceHealth(child)
  }, dbServiceHealthProbeIntervalMs)
  dbServiceHealthTimer.unref()
}

async function probeDbServiceHealth(child: ChildProcess): Promise<void> {
  if (stopping || dbServiceProcess !== child || dbServiceHealthProbeInFlight) return
  const nowMs = Date.now()
  if (nowMs - dbServiceHealthRecoveryState.childStartedAtMs < dbServiceHealthRecoveryDefaults.startupGraceMs) return

  dbServiceHealthProbeInFlight = true
  let healthy = false
  let probeError: unknown
  try {
    const state = getDbServiceState()
    if (state.pid === child.pid && state.ready && state.httpHost && state.httpPort) {
      const response = await fetch(`http://${state.httpHost}:${state.httpPort}/__aisys__/api/health`, {
        signal: AbortSignal.timeout(dbServiceHealthProbeTimeoutMs)
      })
      await response.arrayBuffer()
      healthy = response.ok
    }
  } catch (error) {
    probeError = error
  } finally {
    dbServiceHealthProbeInFlight = false
  }
  if (stopping || dbServiceProcess !== child) return

  const result = recordDbServiceHealthProbe(dbServiceHealthRecoveryState, { nowMs: Date.now(), healthy })
  dbServiceHealthRecoveryState = result.state
  if (healthy) return

  const fields = {
    event: 'db_service_health_probe_failed',
    pid: child.pid,
    action: result.action,
    consecutiveFailures: result.state.consecutiveFailures,
    recoveryAttemptsInWindow: result.state.recoveryAttemptsMs.length,
    errorMessage: probeError instanceof Error ? probeError.message : probeError ? String(probeError) : undefined
  }
  if (result.action === 'recover') {
    logger.error(fields, 'DB service health 连续失败，定向终止当前子进程')
    terminateUnhealthyDbServiceChild(child)
    return
  }
  if (result.action === 'suppressed_budget' || result.action === 'suppressed_cooldown') {
    logger.error(fields, 'DB service health 恢复受冷却或预算保护阻断，仅记录告警')
    return
  }
  logger.warn(fields, 'DB service health 探测失败')
}

function terminateUnhealthyDbServiceChild(child: ChildProcess): void {
  if (stopping || dbServiceProcess !== child || child.pid === undefined) return
  clearDbServiceHealthProbeTimer()
  const childPid = child.pid
  child.kill('SIGTERM')
  if (dbServiceForceKillTimer) clearTimeout(dbServiceForceKillTimer)
  dbServiceForceKillTimer = setTimeout(() => {
    dbServiceForceKillTimer = undefined
    if (stopping || !isSameRunningDbServiceChild(child, childPid)) return
    logger.error({ event: 'db_service_health_force_kill', pid: childPid }, 'DB service TERM 后未退出，强制终止同一子进程')
    child.kill('SIGKILL')
  }, dbServiceForceKillDelayMs)
  dbServiceForceKillTimer.unref()
}

function isSameRunningDbServiceChild(child: ChildProcess, childPid: number): boolean {
  return dbServiceProcess === child
    && child.pid === childPid
    && child.exitCode === null
    && child.signalCode === null
}

function clearDbServiceHealthProbeTimer(): void {
  if (dbServiceHealthTimer) {
    clearInterval(dbServiceHealthTimer)
    dbServiceHealthTimer = undefined
  }
}

function clearDbServiceHealthMonitor(): void {
  clearDbServiceHealthProbeTimer()
  dbServiceHealthProbeInFlight = false
  if (dbServiceForceKillTimer) {
    clearTimeout(dbServiceForceKillTimer)
    dbServiceForceKillTimer = undefined
  }
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
    forwardSupervisorOutput(process.stdout, chunk)
  })
  child.stderr?.on('data', (chunk: Buffer) => {
    forwardSupervisorOutput(process.stderr, chunk)
  })
}

function scheduleDbServiceRestart(): void {
  if (restartTimer) {
    return
  }

  const delayMs = supervisorRestartDelayMs(restartState.restartAttempts)
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
  clearDbServiceHealthMonitor()
  if (restartTimer) {
    clearTimeout(restartTimer)
    restartTimer = undefined
  }
  if (dbServiceProcess && !dbServiceProcess.killed) {
    dbServiceProcess.kill('SIGTERM')
  }
}

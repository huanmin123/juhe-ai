import type { ChildProcess } from 'node:child_process'

import type { BackgroundWorkerProcessRole } from './background-ipc.types.js'

export interface BackgroundWorkerChildProcessSet {
  metricsWorkerProcess?: ChildProcess
  ingestWorkerProcess?: ChildProcess
  statsWorkerProcess?: ChildProcess
  snapshotWorkerProcess?: ChildProcess
  probeWorkerProcess?: ChildProcess
  maintenanceWorkerProcess?: ChildProcess
}

export function workerPidFromReadyRecord(record: Record<string, unknown>, currentPid: number | undefined): number | undefined {
  return typeof record.pid === 'number' ? record.pid : currentPid
}

export function workerPidFromBrokenChild(child: ChildProcess | undefined, currentPid: number | undefined): number | undefined {
  return child?.pid ?? currentPid
}

export function roleForBackgroundWorkerChild(
  child: ChildProcess | undefined,
  processes: BackgroundWorkerChildProcessSet
): BackgroundWorkerProcessRole {
  if (child === processes.metricsWorkerProcess) return 'metrics-worker'
  if (child === processes.ingestWorkerProcess) return 'ingest-worker'
  if (child === processes.statsWorkerProcess) return 'stats-worker'
  if (child === processes.snapshotWorkerProcess) return 'snapshot-worker'
  if (child === processes.probeWorkerProcess) return 'probe-worker'
  if (child === processes.maintenanceWorkerProcess) return 'maintenance-worker'
  return 'worker'
}

export function terminateBrokenWorkerIpc(
  role: BackgroundWorkerProcessRole,
  error: unknown,
  child: ChildProcess | undefined
): void {
  if (child && !child.killed) {
    try {
      child.kill('SIGTERM')
    } catch (killError) {
      process.stderr.write(`[background-worker] 终止 IPC 异常 ${role} 失败：${errorMessage(killError)}\n`)
    }
  }
  if (error) {
    process.stderr.write(`[background-worker] ${role} IPC 已断开：${errorMessage(error)}\n`)
  }
}

export function writeParentIpcBrokenLog(error: unknown): void {
  process.stderr.write(`[background-worker] 父进程 IPC 已断开：${errorMessage(error)}\n`)
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

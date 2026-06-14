import type {
  BackgroundWorkerIpcQueuesRuntime,
  BackgroundWorkerRuntimeSnapshot,
  BackgroundWorkerState
} from './background-ipc.types.js'
import { buildBackgroundWorkerRoleState, type BackgroundWorkerRoleStateInput } from './background-ipc-worker-state.js'

export interface BackgroundWorkerStateSnapshotTotals {
  regularWorker: number
  ingestUsageRecord: number
  ingestRegularWorker: number
  probeWorker: number
  maintenanceWorker: number
}

export interface BackgroundWorkerStateSnapshotRoles {
  metricsWorker: BackgroundWorkerRoleStateInput
  ingestWorker: BackgroundWorkerRoleStateInput
  statsWorker: BackgroundWorkerRoleStateInput
  snapshotWorker: BackgroundWorkerRoleStateInput
  probeWorker: BackgroundWorkerRoleStateInput
  maintenanceWorker: BackgroundWorkerRoleStateInput
}

export interface BackgroundWorkerStateSnapshotInput {
  pid?: number
  ready: boolean
  lastSnapshot?: BackgroundWorkerRuntimeSnapshot
  pendingMessageCounts: BackgroundWorkerStateSnapshotTotals
  pendingMessageBytes: BackgroundWorkerStateSnapshotTotals
  pendingQueues: BackgroundWorkerIpcQueuesRuntime
  pendingSnapshotRequestCount: number
  timedOutSnapshotRequestCount: number
  rejectedSnapshotRequestCount: number
  pendingProcessEventLoopRequestCount: number
  timedOutProcessEventLoopRequestCount: number
  failedProcessEventLoopRequestCount: number
  roles: BackgroundWorkerStateSnapshotRoles
}

export function buildBackgroundWorkerStateSnapshot(input: BackgroundWorkerStateSnapshotInput): BackgroundWorkerState {
  return {
    pid: input.pid,
    ready: input.ready,
    metricsWorker: buildBackgroundWorkerRoleState(input.roles.metricsWorker),
    ingestWorker: buildBackgroundWorkerRoleState(input.roles.ingestWorker),
    statsWorker: buildBackgroundWorkerRoleState(input.roles.statsWorker),
    snapshotWorker: buildBackgroundWorkerRoleState(input.roles.snapshotWorker),
    probeWorker: buildBackgroundWorkerRoleState(input.roles.probeWorker),
    maintenanceWorker: buildBackgroundWorkerRoleState(input.roles.maintenanceWorker),
    lastSnapshot: input.lastSnapshot,
    pendingMessageCount: totalBackgroundWorkerStateSnapshotValues(input.pendingMessageCounts),
    pendingMessageBytes: totalBackgroundWorkerStateSnapshotValues(input.pendingMessageBytes),
    pendingQueues: input.pendingQueues,
    pendingSnapshotRequestCount: input.pendingSnapshotRequestCount,
    timedOutSnapshotRequestCount: input.timedOutSnapshotRequestCount,
    rejectedSnapshotRequestCount: input.rejectedSnapshotRequestCount,
    pendingProcessEventLoopRequestCount: input.pendingProcessEventLoopRequestCount,
    timedOutProcessEventLoopRequestCount: input.timedOutProcessEventLoopRequestCount,
    failedProcessEventLoopRequestCount: input.failedProcessEventLoopRequestCount
  }
}

function totalBackgroundWorkerStateSnapshotValues(values: BackgroundWorkerStateSnapshotTotals): number {
  return values.regularWorker
    + values.ingestUsageRecord
    + values.ingestRegularWorker
    + values.probeWorker
    + values.maintenanceWorker
}

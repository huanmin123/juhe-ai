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
  opsWorker: number
}

export interface BackgroundWorkerStateSnapshotRoles {
  ingestWorker: BackgroundWorkerRoleStateInput
  statsWorker: BackgroundWorkerRoleStateInput
  opsWorker: BackgroundWorkerRoleStateInput
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
  pendingDbServiceRequestCount?: number
  oldestPendingDbServiceRequestMs?: number
  rejectedDbServiceRequestCount?: number
  timedOutDbServiceRequestCount?: number
  pendingProcessEventLoopRequestCount: number
  timedOutProcessEventLoopRequestCount: number
  failedProcessEventLoopRequestCount: number
  roles: BackgroundWorkerStateSnapshotRoles
}

export function buildBackgroundWorkerStateSnapshot(input: BackgroundWorkerStateSnapshotInput): BackgroundWorkerState {
  return {
    pid: input.pid,
    ready: input.ready,
    ingestWorker: buildBackgroundWorkerRoleState(input.roles.ingestWorker),
    statsWorker: buildBackgroundWorkerRoleState(input.roles.statsWorker),
    opsWorker: buildBackgroundWorkerRoleState(input.roles.opsWorker),
    lastSnapshot: input.lastSnapshot,
    pendingMessageCount: totalBackgroundWorkerStateSnapshotValues(input.pendingMessageCounts),
    pendingMessageBytes: totalBackgroundWorkerStateSnapshotValues(input.pendingMessageBytes),
    pendingQueues: input.pendingQueues,
    pendingSnapshotRequestCount: input.pendingSnapshotRequestCount,
    timedOutSnapshotRequestCount: input.timedOutSnapshotRequestCount,
    rejectedSnapshotRequestCount: input.rejectedSnapshotRequestCount,
    pendingDbServiceRequestCount: input.pendingDbServiceRequestCount,
    oldestPendingDbServiceRequestMs: input.oldestPendingDbServiceRequestMs,
    rejectedDbServiceRequestCount: input.rejectedDbServiceRequestCount,
    timedOutDbServiceRequestCount: input.timedOutDbServiceRequestCount,
    pendingProcessEventLoopRequestCount: input.pendingProcessEventLoopRequestCount,
    timedOutProcessEventLoopRequestCount: input.timedOutProcessEventLoopRequestCount,
    failedProcessEventLoopRequestCount: input.failedProcessEventLoopRequestCount
  }
}

function totalBackgroundWorkerStateSnapshotValues(values: BackgroundWorkerStateSnapshotTotals): number {
  return values.regularWorker
    + values.ingestUsageRecord
    + values.ingestRegularWorker
    + values.opsWorker
}

import type {
  BackgroundWorkerIpcQueuesRuntime,
  BackgroundWorkerRoleState,
  BackgroundWorkerRuntimeSnapshot
} from './background-ipc.types.js'

export interface BackgroundWorkerRoleStateInput {
  pid?: number
  ready: boolean
  lastSnapshot?: BackgroundWorkerRuntimeSnapshot
  pendingMessageCount?: number
  pendingMessageBytes?: number
  pendingQueues?: BackgroundWorkerIpcQueuesRuntime
  pendingWriteRequestCount?: number
  oldestPendingWriteMs?: number
  rejectedWriteRequestCount?: number
  timedOutWriteRequestCount?: number
  pendingSnapshotRequestCount: number
  timedOutSnapshotRequestCount: number
  rejectedSnapshotRequestCount: number
}

export function buildBackgroundWorkerRoleState(input: BackgroundWorkerRoleStateInput): BackgroundWorkerRoleState {
  const state: BackgroundWorkerRoleState = {
    pid: input.pid,
    ready: input.ready,
    lastSnapshot: input.lastSnapshot,
    pendingSnapshotRequestCount: input.pendingSnapshotRequestCount,
    timedOutSnapshotRequestCount: input.timedOutSnapshotRequestCount,
    rejectedSnapshotRequestCount: input.rejectedSnapshotRequestCount
  }
  if (input.pendingMessageCount !== undefined) {
    state.pendingMessageCount = input.pendingMessageCount
  }
  if (input.pendingMessageBytes !== undefined) {
    state.pendingMessageBytes = input.pendingMessageBytes
  }
  if (input.pendingQueues) {
    state.pendingQueues = input.pendingQueues
  }
  if (input.pendingWriteRequestCount !== undefined) {
    state.pendingWriteRequestCount = input.pendingWriteRequestCount
  }
  if (input.oldestPendingWriteMs !== undefined) {
    state.oldestPendingWriteMs = input.oldestPendingWriteMs
  }
  if (input.rejectedWriteRequestCount !== undefined) {
    state.rejectedWriteRequestCount = input.rejectedWriteRequestCount
  }
  if (input.timedOutWriteRequestCount !== undefined) {
    state.timedOutWriteRequestCount = input.timedOutWriteRequestCount
  }
  return state
}

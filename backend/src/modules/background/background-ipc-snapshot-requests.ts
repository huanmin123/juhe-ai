import { randomUUID } from 'node:crypto'
import type { ChildProcess } from 'node:child_process'

import { runtimeConfig } from '../../config/runtime.js'
import type {
  BackgroundWorkerMessage,
  BackgroundWorkerProcessRole,
  BackgroundWorkerRuntimeSnapshot,
  PendingRequest
} from './background-ipc.types.js'
import { failIpcPendingRequests, finishIpcPendingRequest, timeoutIpcPendingRequest } from './background-ipc-pending-requests.js'

interface SnapshotRequestState {
  pendingRequests: Map<string, PendingRequest>
  lastSnapshot?: BackgroundWorkerRuntimeSnapshot
  timedOutSnapshotRequestCount: number
  rejectedSnapshotRequestCount: number
}

export interface SnapshotRequestStats {
  lastSnapshot?: BackgroundWorkerRuntimeSnapshot
  pendingSnapshotRequestCount: number
  timedOutSnapshotRequestCount: number
  rejectedSnapshotRequestCount: number
}

const workerSnapshotRequests: SnapshotRequestState = createSnapshotRequestState()
const ingestWorkerSnapshotRequests: SnapshotRequestState = createSnapshotRequestState()
const statsWorkerSnapshotRequests: SnapshotRequestState = createSnapshotRequestState()
const opsWorkerSnapshotRequests: SnapshotRequestState = createSnapshotRequestState()

export async function requestQueuedWorkerSnapshot(input: {
  queueWorkerMessage: (message: BackgroundWorkerMessage) => boolean
  timeoutMs: number
  workerProcess?: ChildProcess
}): Promise<BackgroundWorkerRuntimeSnapshot | undefined> {
  const state = snapshotRequestStateForRole('worker')
  if (runtimeConfig.processRole === 'worker') {
    return state.lastSnapshot
  }
  if (!input.workerProcess) {
    return undefined
  }

  const requestId = randomUUID()
  return await new Promise<BackgroundWorkerRuntimeSnapshot | undefined>((resolve) => {
    registerSnapshotRequest(state, requestId, resolve, input.timeoutMs)
    const queued = input.queueWorkerMessage({
      type: 'background_worker_status_request',
      requestId
    })
    if (!queued) {
      state.rejectedSnapshotRequestCount += 1
      finishWorkerSnapshotResponse('worker', requestId, undefined)
    }
  })
}

export async function requestDirectWorkerSnapshot(
  role: Extract<BackgroundWorkerProcessRole, 'ingest-worker'>,
  input: {
    child?: ChildProcess
    markIpcBroken: (error: unknown, child: ChildProcess) => void
    ready: boolean
    timeoutMs: number
  }
): Promise<BackgroundWorkerRuntimeSnapshot | undefined> {
  const state = snapshotRequestStateForRole(role)
  if (runtimeConfig.processRole === 'worker') {
    return runtimeConfig.workerRole === role ? state.lastSnapshot : undefined
  }
  if (!input.child || !input.child.connected || !input.ready) {
    return undefined
  }

  const child = input.child
  const requestId = randomUUID()
  return await new Promise<BackgroundWorkerRuntimeSnapshot | undefined>((resolve) => {
    registerSnapshotRequest(state, requestId, resolve, input.timeoutMs)
    sendSnapshotRequestToChild({
      child,
      onSendError: (error) => {
        state.rejectedSnapshotRequestCount += 1
        finishWorkerSnapshotResponse(role, requestId, undefined)
        input.markIpcBroken(error, child)
      },
      requestId
    })
  })
}

export async function requestRoleWorkerSnapshot(
  role: Extract<BackgroundWorkerProcessRole, 'stats-worker' | 'ops-worker'>,
  input: {
    child?: ChildProcess
    markIpcBroken: (error: unknown, child: ChildProcess) => void
    ready: boolean
    timeoutMs: number
  }
): Promise<BackgroundWorkerRuntimeSnapshot | undefined> {
  const state = snapshotRequestStateForRole(role)
  if (runtimeConfig.processRole === 'worker') {
    return runtimeConfig.workerRole === role ? state.lastSnapshot : undefined
  }
  if (!input.child || !input.child.connected || !input.ready) {
    return undefined
  }

  const child = input.child
  const requestId = randomUUID()
  return await new Promise<BackgroundWorkerRuntimeSnapshot | undefined>((resolve) => {
    registerSnapshotRequest(state, requestId, resolve, input.timeoutMs)
    sendSnapshotRequestToChild({
      child,
      onSendError: (error) => {
        state.rejectedSnapshotRequestCount += 1
        finishWorkerSnapshotResponse(role, requestId, undefined)
        input.markIpcBroken(error, child)
      },
      requestId
    })
  })
}

export function finishWorkerSnapshotResponse(
  role: BackgroundWorkerProcessRole,
  requestId: string,
  snapshot: BackgroundWorkerRuntimeSnapshot | undefined
): void {
  const state = snapshotRequestStateForRole(role)
  finishIpcPendingRequest(state.pendingRequests, requestId, snapshot)
  if (snapshot && typeof snapshot === 'object') {
    state.lastSnapshot = snapshot
  }
}

export function failWorkerSnapshotPendingRequests(role: BackgroundWorkerProcessRole): void {
  failIpcPendingRequests(snapshotRequestStateForRole(role).pendingRequests)
}

export function snapshotRequestStats(role: BackgroundWorkerProcessRole): SnapshotRequestStats {
  const state = snapshotRequestStateForRole(role)
  return {
    lastSnapshot: state.lastSnapshot,
    pendingSnapshotRequestCount: state.pendingRequests.size,
    timedOutSnapshotRequestCount: state.timedOutSnapshotRequestCount,
    rejectedSnapshotRequestCount: state.rejectedSnapshotRequestCount
  }
}

function createSnapshotRequestState(): SnapshotRequestState {
  return {
    pendingRequests: new Map<string, PendingRequest>(),
    timedOutSnapshotRequestCount: 0,
    rejectedSnapshotRequestCount: 0
  }
}

function registerSnapshotRequest(
  state: SnapshotRequestState,
  requestId: string,
  resolve: (snapshot: BackgroundWorkerRuntimeSnapshot | undefined) => void,
  timeoutMs: number
): void {
  const timeout = setTimeout(() => {
    if (timeoutIpcPendingRequest(state.pendingRequests, requestId)) {
      state.timedOutSnapshotRequestCount += 1
    }
  }, timeoutMs)
  state.pendingRequests.set(requestId, { resolve, reject: () => undefined, timeout })
}

function sendSnapshotRequestToChild(input: {
  child: ChildProcess
  onSendError: (error: unknown) => void
  requestId: string
}): void {
  try {
    input.child.send({
      type: 'background_worker_status_request',
      requestId: input.requestId
    } satisfies BackgroundWorkerMessage, (error) => {
      if (error) {
        input.onSendError(error)
      }
    })
  } catch (error) {
    input.onSendError(error)
  }
}

function snapshotRequestStateForRole(role: BackgroundWorkerProcessRole): SnapshotRequestState {
  switch (role) {
    case 'ingest-worker':
      return ingestWorkerSnapshotRequests
    case 'stats-worker':
      return statsWorkerSnapshotRequests
    case 'ops-worker':
      return opsWorkerSnapshotRequests
    default:
      return workerSnapshotRequests
  }
}

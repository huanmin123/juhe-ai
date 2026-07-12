export type SpeedFirstBodyAdmissionRejectReason =
  | 'queue_disabled'
  | 'queue_full'
  | 'api_key_queue_full'
  | 'timeout'
  | 'aborted'

export type SpeedFirstBodyAdmissionDecision =
  | { acquired: true; waitedMs: number; release: () => void }
  | { acquired: false; reason: SpeedFirstBodyAdmissionRejectReason; waitedMs: number }

export interface SpeedFirstBodyAdmissionInput {
  systemAccountId: string
  routeStrategyId: string
  groupId: string
  apiKeyId: string
  capacity: number
  maxQueueWaitMs: number
  maxQueueSize: number
  perApiKeyQueueLimit: number
  signal?: AbortSignal
}

interface AdmissionState {
  key: string
  capacity: number
  active: number
  queue: AdmissionQueueItem[]
  perApiKeyQueued: Map<string, number>
}

interface AdmissionQueueItem {
  apiKeyId: string
  enqueuedAtMs: number
  timer: NodeJS.Timeout
  signal?: AbortSignal
  abortListener?: () => void
  resolve: (decision: SpeedFirstBodyAdmissionDecision) => void
}

const states = new Map<string, AdmissionState>()

export function acquireSpeedFirstBodyAdmission(input: SpeedFirstBodyAdmissionInput): Promise<SpeedFirstBodyAdmissionDecision> {
  const key = admissionKey(input)
  const state = states.get(key) ?? createState(key, input.capacity)
  state.capacity = positiveInteger(input.capacity, 1)
  states.set(key, state)
  const startedAtMs = Date.now()
  if (input.signal?.aborted) {
    return Promise.resolve({ acquired: false, reason: 'aborted', waitedMs: 0 })
  }
  if (state.queue.length === 0 && state.active < state.capacity) {
    state.active += 1
    return Promise.resolve(acquiredDecision(state, 0))
  }
  const maxQueueWaitMs = nonNegativeInteger(input.maxQueueWaitMs, 0)
  if (maxQueueWaitMs === 0) {
    return Promise.resolve({ acquired: false, reason: 'queue_disabled', waitedMs: 0 })
  }
  if (state.queue.length >= positiveInteger(input.maxQueueSize, 1)) {
    return Promise.resolve({ acquired: false, reason: 'queue_full', waitedMs: 0 })
  }
  const apiKeyQueued = state.perApiKeyQueued.get(input.apiKeyId) ?? 0
  if (apiKeyQueued >= positiveInteger(input.perApiKeyQueueLimit, 1)) {
    return Promise.resolve({ acquired: false, reason: 'api_key_queue_full', waitedMs: 0 })
  }

  return new Promise<SpeedFirstBodyAdmissionDecision>((resolve) => {
    const item: AdmissionQueueItem = {
      apiKeyId: input.apiKeyId,
      enqueuedAtMs: startedAtMs,
      timer: setTimeout(() => completeQueuedItem(state, item, {
        acquired: false,
        reason: 'timeout',
        waitedMs: Date.now() - startedAtMs
      }), maxQueueWaitMs),
      signal: input.signal,
      resolve
    }
    if (input.signal) {
      item.abortListener = () => completeQueuedItem(state, item, {
        acquired: false,
        reason: 'aborted',
        waitedMs: Date.now() - startedAtMs
      })
      input.signal.addEventListener('abort', item.abortListener, { once: true })
    }
    state.queue.push(item)
    state.perApiKeyQueued.set(input.apiKeyId, apiKeyQueued + 1)
  })
}

export function speedFirstBodyAdmissionSnapshot(): Array<{ key: string; capacity: number; active: number; queued: number }> {
  return [...states.values()].map((state) => ({
    key: state.key,
    capacity: state.capacity,
    active: state.active,
    queued: state.queue.length
  }))
}

export function clearSpeedFirstBodyAdmissionsForTest(): void {
  for (const state of states.values()) {
    for (const item of [...state.queue]) {
      completeQueuedItem(state, item, { acquired: false, reason: 'aborted', waitedMs: Date.now() - item.enqueuedAtMs })
    }
  }
  states.clear()
}

function acquiredDecision(state: AdmissionState, waitedMs: number): SpeedFirstBodyAdmissionDecision {
  let released = false
  return {
    acquired: true,
    waitedMs,
    release: () => {
      if (released) return
      released = true
      state.active = Math.max(0, state.active - 1)
      wakeQueuedItems(state)
      cleanupState(state)
    }
  }
}

function wakeQueuedItems(state: AdmissionState): void {
  while (state.active < state.capacity && state.queue.length > 0) {
    const item = state.queue[0]
    if (!item) return
    removeQueuedItem(state, item)
    state.active += 1
    item.resolve(acquiredDecision(state, Date.now() - item.enqueuedAtMs))
  }
}

function completeQueuedItem(state: AdmissionState, item: AdmissionQueueItem, decision: SpeedFirstBodyAdmissionDecision): void {
  if (!state.queue.includes(item)) return
  removeQueuedItem(state, item)
  item.resolve(decision)
  cleanupState(state)
}

function removeQueuedItem(state: AdmissionState, item: AdmissionQueueItem): void {
  const index = state.queue.indexOf(item)
  if (index >= 0) state.queue.splice(index, 1)
  clearTimeout(item.timer)
  if (item.signal && item.abortListener) item.signal.removeEventListener('abort', item.abortListener)
  const count = state.perApiKeyQueued.get(item.apiKeyId) ?? 0
  if (count <= 1) state.perApiKeyQueued.delete(item.apiKeyId)
  else state.perApiKeyQueued.set(item.apiKeyId, count - 1)
}

function cleanupState(state: AdmissionState): void {
  if (state.active === 0 && state.queue.length === 0) states.delete(state.key)
}

function createState(key: string, capacity: number): AdmissionState {
  return { key, capacity: positiveInteger(capacity, 1), active: 0, queue: [], perApiKeyQueued: new Map() }
}

function admissionKey(input: Pick<SpeedFirstBodyAdmissionInput, 'systemAccountId' | 'routeStrategyId' | 'groupId'>): string {
  return `${input.systemAccountId}:${input.routeStrategyId}:${input.groupId}`
}

function positiveInteger(value: number, fallback: number): number {
  return Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : fallback
}

function nonNegativeInteger(value: number, fallback: number): number {
  return Number.isFinite(value) ? Math.max(0, Math.trunc(value)) : fallback
}

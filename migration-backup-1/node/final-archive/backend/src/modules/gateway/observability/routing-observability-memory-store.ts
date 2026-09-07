import {
  gatewayRoutingObservationMetricKey,
  gatewayRoutingObservabilityMetricCapacity,
  type GatewayRoutingObservation,
  type GatewayRoutingObservationBatchEntry,
  type GatewayRoutingObservabilitySnapshot,
  type GatewayRoutingObservabilityStore
} from './routing-observability-store.js'

export class MemoryGatewayRoutingObservabilityStore implements GatewayRoutingObservabilityStore {
  private readonly counters = new Map<string, number>()
  private recordedEvents = 0
  private updatedAtMs = 0

  async record(observation: GatewayRoutingObservation, nowMs = Date.now()): Promise<void> {
    await this.recordBatch([{ observation, count: 1 }], nowMs)
  }

  async recordBatch(entries: readonly GatewayRoutingObservationBatchEntry[], nowMs = Date.now()): Promise<void> {
    if (entries.length === 0) return
    const now = normalizedNow(nowMs)
    const increments = new Map<string, number>()
    for (const entry of entries) {
      const count = positiveCount(entry.count)
      const key = gatewayRoutingObservationMetricKey(entry.observation)
      increments.set(key, saturatedAdd(increments.get(key) ?? 0, count))
    }
    const newMetricCount = [...increments.keys()].filter((key) => !this.counters.has(key)).length
    if (this.counters.size + newMetricCount > gatewayRoutingObservabilityMetricCapacity) {
      throw new Error('routing observability metric capacity exhausted')
    }
    let recordedIncrement = 0
    for (const [key, count] of increments) {
      this.counters.set(key, saturatedAdd(this.counters.get(key) ?? 0, count))
      recordedIncrement = saturatedAdd(recordedIncrement, count)
    }
    this.recordedEvents = saturatedAdd(this.recordedEvents, recordedIncrement)
    this.updatedAtMs = Math.max(this.updatedAtMs, now)
  }

  async snapshot(): Promise<GatewayRoutingObservabilitySnapshot> {
    return {
      version: 1,
      recordedEvents: this.recordedEvents,
      updatedAtMs: this.updatedAtMs,
      counters: Object.fromEntries([...this.counters].sort(([left], [right]) => left.localeCompare(right)))
    }
  }
}

function positiveCount(value: number): number {
  if (!Number.isSafeInteger(value) || value <= 0) throw new RangeError('routing observability count 必须是正安全整数')
  return value
}

function normalizedNow(value: number): number {
  if (!Number.isSafeInteger(value) || value < 0) throw new RangeError('routing observability nowMs 必须是非负安全整数')
  return value
}

function saturatedAdd(left: number, right: number): number {
  return Math.min(Number.MAX_SAFE_INTEGER, left + right)
}

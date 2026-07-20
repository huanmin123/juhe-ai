import { redisNamespacedGroup, redisNamespacedKey } from './redis-namespace.js'

export interface RedisStreamDrainCommandClient {
  sendCommand(command: string[]): Promise<unknown>
}

export interface RedisStreamDrainContract {
  name: string
  streamKey: string
  groupName: string
}

export interface RedisStreamDrainGroupSnapshot {
  name: string
  pending: number | null
  lag: number | null
  lastDeliveredId?: string
  oldestPendingIdleMs?: number
}

export interface RedisStreamDrainStreamSnapshot {
  name: string
  streamKey: string
  length: number | null
  groups: RedisStreamDrainGroupSnapshot[]
  drained: boolean
}

export interface RedisStreamDrainSnapshot {
  checkedAt: string
  xaddCalls?: number
  streams: RedisStreamDrainStreamSnapshot[]
  drained: boolean
}

export class RedisStreamDrainStabilityTracker {
  private consecutiveStableWindows = 0
  private previousXaddCalls?: number

  constructor(private readonly requiredStableWindows: number) {
    if (!Number.isInteger(requiredStableWindows) || requiredStableWindows < 2) {
      throw new Error('Redis Stream 排空至少需要两个连续稳定窗口')
    }
  }

  observe(snapshot: RedisStreamDrainSnapshot): boolean {
    if (!snapshot.drained || snapshot.xaddCalls === undefined) {
      this.reset(snapshot.xaddCalls)
      return false
    }
    if (this.previousXaddCalls === snapshot.xaddCalls) {
      this.consecutiveStableWindows += 1
    } else {
      this.consecutiveStableWindows = 1
    }
    this.previousXaddCalls = snapshot.xaddCalls
    return this.consecutiveStableWindows >= this.requiredStableWindows
  }

  private reset(xaddCalls: number | undefined): void {
    this.consecutiveStableWindows = 0
    this.previousXaddCalls = xaddCalls
  }
}

export const redisStreamQueueContracts = {
  usageRecords: { name: 'usage-records', streamKey: 'juhe-ai:queue:usage-records', groupName: 'juhe-ai:usage-record-writers' },
  auditLogs: { name: 'audit-logs', streamKey: 'juhe-ai:queue:audit-logs', groupName: 'juhe-ai:audit-log-writers' },
  operationLogs: { name: 'operation-logs', streamKey: 'juhe-ai:queue:operation-logs', groupName: 'juhe-ai:operation-log-writers' },
  publicApiLogs: { name: 'public-api-logs', streamKey: 'juhe-ai:queue:public-api-logs', groupName: 'juhe-ai:public-api-log-writers' },
  recordMaintenance: { name: 'record-maintenance', streamKey: 'juhe-ai:queue:record-maintenance', groupName: 'juhe-ai:record-maintenance-writers' }
} as const

export const redisStreamDrainContracts: RedisStreamDrainContract[] = Object.values(redisStreamQueueContracts).map(
  (contract) => ({
    name: contract.name,
    streamKey: redisNamespacedKey(contract.streamKey),
    groupName: redisNamespacedGroup(contract.groupName)
  })
)

export async function inspectRedisStreamDrain(
  client: RedisStreamDrainCommandClient,
  contracts: RedisStreamDrainContract[] = redisStreamDrainContracts
): Promise<RedisStreamDrainSnapshot> {
  const streams = await Promise.all(contracts.map((contract) => inspectStream(client, contract)))
  const xaddCalls = parseXaddCalls(await client.sendCommand(['INFO', 'commandstats']))
  return {
    checkedAt: new Date().toISOString(),
    ...(xaddCalls === undefined ? {} : { xaddCalls }),
    streams,
    drained: streams.every((stream) => stream.drained)
  }
}

async function inspectStream(
  client: RedisStreamDrainCommandClient,
  contract: RedisStreamDrainContract
): Promise<RedisStreamDrainStreamSnapshot> {
  const length = optionalNonNegativeInteger(await client.sendCommand(['XLEN', contract.streamKey])) ?? null
  const rawGroups = await client.sendCommand(['XINFO', 'GROUPS', contract.streamKey]).catch((error) => {
    if (isMissingStreamError(error)) return []
    throw error
  })
  const groupRows = Array.isArray(rawGroups) ? rawGroups : []
  const groups = await Promise.all(groupRows.map(async (row) => {
    const fields = redisFieldMap(row)
    const name = String(fields.get('name') ?? '')
    const pendingSummary = name
      ? await client.sendCommand(['XPENDING', contract.streamKey, name])
      : undefined
    const pending = pendingCount(pendingSummary, fields.get('pending'))
    const oldestPending = name && pending !== null && pending > 0
      ? await client.sendCommand(['XPENDING', contract.streamKey, name, '-', '+', '1'])
      : undefined
    const lastDeliveredId = optionalString(fields.get('last-delivered-id'))
    const oldestPendingIdleMs = oldestPendingIdle(oldestPending)
    return {
      name,
      pending,
      lag: optionalNonNegativeInteger(fields.get('lag')) ?? null,
      ...(lastDeliveredId === undefined ? {} : { lastDeliveredId }),
      ...(oldestPendingIdleMs === undefined ? {} : { oldestPendingIdleMs })
    }
  }))
  const expectedGroup = groups.find((group) => group.name === contract.groupName)
  const drained = length === 0
    && Boolean(expectedGroup)
    && groups.every((group) => group.pending === 0 && group.lag === 0)
  return {
    name: contract.name,
    streamKey: contract.streamKey,
    length,
    groups,
    drained
  }
}

function redisFieldMap(row: unknown): Map<string, unknown> {
  const fields = new Map<string, unknown>()
  if (typeof row === 'object' && row !== null && !Array.isArray(row)) {
    for (const [key, value] of Object.entries(row)) {
      fields.set(key, value)
    }
    return fields
  }
  if (!Array.isArray(row)) return fields
  for (let index = 0; index + 1 < row.length; index += 2) {
    fields.set(String(row[index]), row[index + 1])
  }
  return fields
}

function pendingCount(summary: unknown, fallback: unknown): number | null {
  if (Array.isArray(summary) && summary.length > 0) {
    return optionalNonNegativeInteger(summary[0]) ?? null
  }
  return optionalNonNegativeInteger(fallback) ?? null
}

function oldestPendingIdle(summary: unknown): number | undefined {
  if (!Array.isArray(summary) || !Array.isArray(summary[0]) || summary[0].length < 3) return undefined
  return optionalNonNegativeInteger(summary[0][2])
}

function optionalString(value: unknown): string | undefined {
  return typeof value === 'string' && value ? value : undefined
}

function parseXaddCalls(info: unknown): number | undefined {
  if (typeof info !== 'string') return undefined
  const match = /^cmdstat_xadd:calls=(\d+)/m.exec(info)
  return match?.[1] === undefined ? 0 : optionalNonNegativeInteger(match[1])
}

function optionalNonNegativeInteger(value: unknown): number | undefined {
  if (value === null || value === undefined || value === '') return undefined
  const parsed = typeof value === 'number' ? value : Number(value)
  return Number.isSafeInteger(parsed) && parsed >= 0 ? parsed : undefined
}

function isMissingStreamError(error: unknown): boolean {
  return error instanceof Error && /no such key/i.test(error.message)
}

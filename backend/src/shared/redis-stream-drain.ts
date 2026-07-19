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
  pending: number
  lag: number
}

export interface RedisStreamDrainStreamSnapshot {
  name: string
  streamKey: string
  length: number
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
  runtimeLogIndex: { name: 'runtime-log-index', streamKey: 'juhe-ai:queue:runtime-log-index', groupName: 'juhe-ai:runtime-log-index-writers' },
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
  const length = nonNegativeInteger(await client.sendCommand(['XLEN', contract.streamKey]))
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
    return {
      name,
      pending: pendingCount(pendingSummary, fields.get('pending')),
      lag: nonNegativeInteger(fields.get('lag'))
    }
  }))
  const expectedGroup = groups.find((group) => group.name === contract.groupName)
  const drained = length === 0
    && (groups.length === 0 || Boolean(expectedGroup))
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
  if (!Array.isArray(row)) return new Map()
  const fields = new Map<string, unknown>()
  for (let index = 0; index + 1 < row.length; index += 2) {
    fields.set(String(row[index]), row[index + 1])
  }
  return fields
}

function pendingCount(summary: unknown, fallback: unknown): number {
  if (Array.isArray(summary) && summary.length > 0) {
    return nonNegativeInteger(summary[0])
  }
  return nonNegativeInteger(fallback)
}

function parseXaddCalls(info: unknown): number | undefined {
  if (typeof info !== 'string') return undefined
  const match = /^cmdstat_xadd:calls=(\d+)/m.exec(info)
  return match?.[1] === undefined ? 0 : nonNegativeInteger(match[1])
}

function nonNegativeInteger(value: unknown): number {
  const parsed = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(parsed) ? Math.max(0, Math.trunc(parsed)) : 0
}

function isMissingStreamError(error: unknown): boolean {
  return error instanceof Error && /no such key/i.test(error.message)
}

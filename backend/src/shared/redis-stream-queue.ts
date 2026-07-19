import { hostname } from 'node:os'

import { runtimeConfig } from '../config/runtime.js'
import { errorLogFields, logger } from './logger.js'
import { createDedicatedRedisClient, getRedisClient, type RedisCommandClient } from './redis-client.js'
import { redisNamespacedGroup, redisNamespacedKey } from './redis-namespace.js'
import { redisQueueFenceKey } from './redis-queue-fence.js'

export interface RedisStreamQueueOptions<T> {
  streamKey: string
  groupName: string
  consumerName?: string
  readCount?: number
  blockMs?: number
  claimIdleMs?: number
  redisUrl?: string
  encode?: (payload: T) => string
  decode?: (payload: string) => T
  producerClient?: () => Promise<RedisCommandClient>
}

export interface RedisStreamMessage<T> {
  id: string
  payload: T
}

export interface RedisStreamQueueRuntime {
  streamLength: number
  pendingCount: number
  lag?: number
  consumers?: number
  lastDeliveredId?: string
  entriesRead?: number
  oldestPendingId?: string
  newestPendingId?: string
}

export interface RedisStreamBacklogInspection<T> {
  runtime: RedisStreamQueueRuntime
  messages: Array<RedisStreamMessage<T>>
  pendingTruncated: boolean
  undeliveredTruncated: boolean
}

export class RedisStreamQueue<T> {
  private readonly streamKey: string
  private readonly groupName: string
  private readonly consumerName: string
  private readonly readCount: number
  private readonly blockMs: number
  private readonly claimIdleMs: number
  private readonly redisUrl: string
  private readonly fenceKey: string
  private readonly encode: (payload: T) => string
  private readonly decode: (payload: string) => T
  private readonly producerClient: () => Promise<RedisCommandClient>
  private consumerClientPromise: Promise<RedisCommandClient> | undefined
  private groupReadyPromise: Promise<void> | undefined

  constructor(options: RedisStreamQueueOptions<T>) {
    this.streamKey = redisNamespacedKey(options.streamKey)
    this.groupName = redisNamespacedGroup(options.groupName)
    this.consumerName = options.consumerName
      ? redisNamespacedGroup(options.consumerName)
      : defaultRedisStreamConsumerName(this.groupName)
    this.readCount = options.readCount ?? runtimeConfig.queue.redisStreamReadCount
    this.blockMs = options.blockMs ?? runtimeConfig.queue.redisStreamBlockMs
    this.claimIdleMs = options.claimIdleMs ?? runtimeConfig.queue.redisStreamClaimIdleMs
    this.redisUrl = options.redisUrl ?? requiredRedisQueueUrl()
    this.fenceKey = redisQueueFenceKey()
    this.encode = options.encode ?? ((payload) => JSON.stringify(payload))
    this.decode = options.decode ?? ((payload) => JSON.parse(payload) as T)
    this.producerClient = options.producerClient ?? (() => getRedisClient(this.redisUrl))
  }

  async enqueue(payload: T): Promise<string> {
    return await this.enqueueEncoded(this.encode(payload))
  }

  async enqueueEncoded(encodedPayload: string): Promise<string> {
    const client = await this.producerClient()
    const id = await client.eval(redisEnqueueWithFenceScript, {
      keys: [this.fenceKey, this.streamKey],
      arguments: ['payload', encodedPayload]
    })
    return String(id ?? '')
  }

  async readNew(): Promise<Array<RedisStreamMessage<T>>> {
    await this.ensureGroup()
    const client = await this.consumerClient()
    try {
      const result = await this.readNewUnsafe(client)
      return this.parseStreamReadResult(result)
    } catch (error) {
      if (!isRedisNoGroupError(error)) {
        throw error
      }
      await this.recreateGroupAfterNoGroup()
      const result = await this.readNewUnsafe(client)
      return this.parseStreamReadResult(result)
    }
  }

  async claimPending(): Promise<Array<RedisStreamMessage<T>>> {
    await this.ensureGroup()
    const client = await this.consumerClient()
    try {
      const result = await this.claimPendingUnsafe(client)
      return this.parseAutoClaimResult(result)
    } catch (error) {
      if (!isRedisNoGroupError(error)) {
        throw error
      }
      await this.recreateGroupAfterNoGroup()
      const result = await this.claimPendingUnsafe(client)
      return this.parseAutoClaimResult(result)
    }
  }

  async ack(ids: string[]): Promise<number> {
    const normalizedIds = ids.map((id) => id.trim()).filter(Boolean)
    if (!normalizedIds.length) return 0
    const client = await getRedisClient(this.redisUrl)
    const result = await client.eval(redisAckAndDeleteMessagesScript, {
      keys: [this.streamKey],
      arguments: [this.groupName, ...normalizedIds]
    })
    return Number(result ?? 0)
  }

  async inspectRuntime(): Promise<RedisStreamQueueRuntime> {
    await this.ensureGroup()
    const client = await getRedisClient(this.redisUrl)
    const [groupsResult, pendingResult, streamLengthResult] = await Promise.all([
      client.sendCommand(['XINFO', 'GROUPS', this.streamKey]),
      client.sendCommand(['XPENDING', this.streamKey, this.groupName]),
      client.sendCommand(['XLEN', this.streamKey])
    ])
    const groupRuntime = parseGroupRuntime(groupsResult, this.groupName)
    const pendingRuntime = parsePendingRuntime(pendingResult)
    return {
      ...groupRuntime,
      ...pendingRuntime,
      streamLength: numberField(streamLengthResult) ?? 0,
      pendingCount: pendingRuntime.pendingCount ?? groupRuntime.pendingCount ?? 0
    }
  }

  async inspectBacklogMessages(limit = 2): Promise<Array<RedisStreamMessage<T>>> {
    return (await this.inspectBacklog(limit)).messages
  }

  async inspectBacklog(limit = 256): Promise<RedisStreamBacklogInspection<T>> {
    await this.ensureGroup()
    const runtime = await this.inspectRuntime()
    const client = await getRedisClient(this.redisUrl)
    const normalizedLimit = Math.max(1, Math.trunc(limit))
    const output: Array<RedisStreamMessage<T>> = []
    const seen = new Set<string>()
    const addEntries = (entries: Array<RedisStreamMessage<T>>) => {
      for (const entry of entries) {
        if (seen.has(entry.id)) continue
        seen.add(entry.id)
        output.push(entry)
      }
    }

    const pending = await this.inspectPendingMessages(client, normalizedLimit)
    const pendingIds = pending.ids
    addEntries(pending.entries)

    const remainingLimit = Math.max(0, normalizedLimit - seen.size)
    let undeliveredScanned = 0
    if (remainingLimit > 0 && runtime.lastDeliveredId) {
      const start = runtime.lastDeliveredId ? `(${runtime.lastDeliveredId}` : '-'
      const entries = this.parseEntries(await client.sendCommand([
        'XRANGE',
        this.streamKey,
        start,
        '+',
        'COUNT',
        String(remainingLimit)
      ]))
      undeliveredScanned = entries.length
      addEntries(entries)
    }

    return {
      runtime,
      messages: output,
      pendingTruncated: runtime.pendingCount > pendingIds.length,
      undeliveredTruncated: runtime.lag !== undefined
        ? runtime.lag > undeliveredScanned
        : runtime.lastDeliveredId !== undefined && (remainingLimit === 0 || undeliveredScanned >= remainingLimit)
    }
  }

  async closeConsumer(): Promise<void> {
    const promise = this.consumerClientPromise
    this.consumerClientPromise = undefined
    if (!promise) return
    const client = await promise
    if (typeof client.destroy === 'function') {
      client.destroy()
      return
    }
    if (typeof client.quit === 'function') {
      await client.quit()
    }
  }

  private ensureGroup(): Promise<void> {
    if (!this.groupReadyPromise) {
      this.groupReadyPromise = this.ensureGroupUnsafe().catch((error) => {
        this.groupReadyPromise = undefined
        throw error
      })
    }
    return this.groupReadyPromise
  }

  private async ensureGroupUnsafe(): Promise<void> {
    const client = await getRedisClient(this.redisUrl)
    try {
      await client.sendCommand(['XGROUP', 'CREATE', this.streamKey, this.groupName, '0', 'MKSTREAM'])
    } catch (error) {
      if (!isRedisBusyGroupError(error)) {
        throw error
      }
    }
  }

  private async recreateGroupAfterNoGroup(): Promise<void> {
    this.groupReadyPromise = undefined
    await this.ensureGroup()
  }

  private async readNewUnsafe(client: RedisCommandClient): Promise<unknown> {
    return await client.sendCommand([
      'XREADGROUP',
      'GROUP',
      this.groupName,
      this.consumerName,
      'COUNT',
      String(this.readCount),
      'BLOCK',
      String(this.blockMs),
      'STREAMS',
      this.streamKey,
      '>'
    ])
  }

  private async claimPendingUnsafe(client: RedisCommandClient): Promise<unknown> {
    return await client.sendCommand([
      'XAUTOCLAIM',
      this.streamKey,
      this.groupName,
      this.consumerName,
      String(this.claimIdleMs),
      '0-0',
      'COUNT',
      String(this.readCount)
    ])
  }

  private consumerClient(): Promise<RedisCommandClient> {
    if (!this.consumerClientPromise) {
      this.consumerClientPromise = createDedicatedRedisClient(this.redisUrl).catch((error) => {
        this.consumerClientPromise = undefined
        throw error
      })
    }
    return this.consumerClientPromise
  }

  private parseStreamReadResult(result: unknown): Array<RedisStreamMessage<T>> {
    const output: Array<RedisStreamMessage<T>> = []
    if (!Array.isArray(result) && result && typeof result === 'object') {
      for (const entries of Object.values(result as Record<string, unknown>)) {
        output.push(...this.parseEntries(entries))
      }
      return output
    }
    const streams = Array.isArray(result) ? result : []
    for (const stream of streams) {
      if (!Array.isArray(stream) || stream.length < 2) continue
      output.push(...this.parseEntries(stream[1]))
    }
    return output
  }

  private parseAutoClaimResult(result: unknown): Array<RedisStreamMessage<T>> {
    if (!Array.isArray(result) && result && typeof result === 'object') {
      const messages = (result as Record<string, unknown>).messages
      return this.parseEntries(messages)
    }
    if (!Array.isArray(result) || result.length < 2) return []
    return this.parseEntries(result[1])
  }

  private async inspectPendingMessages(client: RedisCommandClient, limit: number): Promise<{ ids: string[]; entries: Array<RedisStreamMessage<T>> }> {
    const result = await client.eval(redisInspectPendingMessagesScript, {
      keys: [this.streamKey],
      arguments: [
        this.groupName,
        String(Math.max(1, Math.trunc(limit)))
      ]
    })
    return this.parsePendingMessageInspection(result)
  }

  private parseEntries(entries: unknown): Array<RedisStreamMessage<T>> {
    if (!Array.isArray(entries)) return []
    const output: Array<RedisStreamMessage<T>> = []
    for (const entry of entries) {
      if (!Array.isArray(entry) || entry.length < 2) continue
      const id = String(entry[0] ?? '')
      const payload = fieldValue(entry[1], 'payload')
      if (!id || payload === undefined) continue
      try {
        output.push({
          id,
          payload: this.decode(payload)
        })
      } catch (error) {
        this.ackPoisonMessage(id, error)
      }
    }
    return output
  }

  private parsePendingMessageInspection(result: unknown): { ids: string[]; entries: Array<RedisStreamMessage<T>> } {
    if (!Array.isArray(result)) return { ids: [], entries: [] }
    const ids: string[] = []
    const entries: Array<RedisStreamMessage<T>> = []
    for (let index = 0; index < result.length; index += 2) {
      const id = stringField(result[index])
      if (id) ids.push(id)
      entries.push(...this.parseEntries(result[index + 1]))
    }
    return { ids, entries }
  }

  private ackPoisonMessage(id: string, error: unknown): void {
    logger.error(errorLogFields(error, {
      event: 'redis_stream_message_decode_failed',
      streamKey: this.streamKey,
      groupName: this.groupName,
      messageId: id
    }), 'Redis Stream 消息解码失败，已跳过坏消息并尝试 ack')
    void this.ack([id]).catch((ackError) => {
      logger.error(errorLogFields(ackError, {
        event: 'redis_stream_poison_message_ack_failed',
        streamKey: this.streamKey,
        groupName: this.groupName,
        messageId: id
      }), 'Redis Stream 坏消息 ack 失败，后续消费将再次尝试')
    })
  }
}

function parseGroupRuntime(result: unknown, groupName: string): Partial<RedisStreamQueueRuntime> {
  const groups = Array.isArray(result) ? result : []
  for (const group of groups) {
    const fields = fieldMap(group)
    if (String(fields.get('name') ?? '') !== groupName) continue
    return {
      pendingCount: numberField(fieldAlias(fields, 'pending', 'pendingCount')) ?? 0,
      lag: numberField(fieldAlias(fields, 'lag')),
      consumers: numberField(fieldAlias(fields, 'consumers')),
      lastDeliveredId: stringField(fieldAlias(fields, 'last-delivered-id', 'lastDeliveredId', 'last_delivered_id')),
      entriesRead: numberField(fieldAlias(fields, 'entries-read', 'entriesRead', 'entries_read'))
    }
  }
  return { pendingCount: 0, lag: 0 }
}

function parsePendingRuntime(result: unknown): Partial<RedisStreamQueueRuntime> {
  if (Array.isArray(result)) {
    return {
      pendingCount: numberField(result[0]) ?? 0,
      oldestPendingId: stringField(result[1]),
      newestPendingId: stringField(result[2])
    }
  }
  if (result && typeof result === 'object') {
    const fields = new Map(Object.entries(result as Record<string, unknown>))
    return {
      pendingCount: numberField(fieldAlias(fields, 'pending', 'count', 'pendingCount', 'pending-count')) ?? 0,
      oldestPendingId: stringField(fieldAlias(fields, 'firstId', 'first-id', 'smallestId', 'smallest-id', 'start', 'startId', 'start-id', 'oldestPendingId', 'oldest-pending-id')),
      newestPendingId: stringField(fieldAlias(fields, 'lastId', 'last-id', 'greatestId', 'greatest-id', 'end', 'endId', 'end-id', 'newestPendingId', 'newest-pending-id'))
    }
  }
  return { pendingCount: 0 }
}

const redisInspectPendingMessagesScript = `
local pending = redis.call('XPENDING', KEYS[1], ARGV[1], '-', '+', ARGV[2])
local output = {}
for _, item in ipairs(pending) do
  local id = item[1]
  output[#output + 1] = id
  output[#output + 1] = redis.call('XRANGE', KEYS[1], id, id, 'COUNT', 1)
end
return output
`

const redisEnqueueWithFenceScript = `
if redis.call('GET', KEYS[1]) then
  return redis.error_reply('QUEUE_QUIESCED')
end
return redis.call('XADD', KEYS[2], '*', ARGV[1], ARGV[2])
`

const redisAckAndDeleteMessagesScript = `
local group_name = ARGV[1]
local acked = 0
for index = 2, #ARGV do
  local id = ARGV[index]
  local result = redis.call('XACK', KEYS[1], group_name, id)
  if result > 0 then
    acked = acked + result
    redis.call('XDEL', KEYS[1], id)
  end
end
return acked
`

function fieldAlias(fields: Map<string, unknown>, ...names: string[]): unknown {
  for (const name of names) {
    if (fields.has(name)) return fields.get(name)
  }
  return undefined
}

function fieldMap(fields: unknown): Map<string, unknown> {
  if (Array.isArray(fields)) {
    const output = new Map<string, unknown>()
    for (let index = 0; index < fields.length; index += 2) {
      output.set(String(fields[index] ?? ''), fields[index + 1])
    }
    return output
  }
  if (fields && typeof fields === 'object') {
    return new Map(Object.entries(fields as Record<string, unknown>))
  }
  return new Map()
}

function numberField(value: unknown): number | undefined {
  if (typeof value === 'number' && Number.isFinite(value)) return Math.max(0, Math.trunc(value))
  if (typeof value === 'bigint') return Number(value)
  if (typeof value === 'string' && value.trim()) {
    const numeric = Number(value)
    return Number.isFinite(numeric) ? Math.max(0, Math.trunc(numeric)) : undefined
  }
  return undefined
}

function stringField(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const trimmed = value.trim()
  return trimmed && trimmed !== 'null' ? trimmed : undefined
}

function requiredRedisQueueUrl(): string {
  const url = runtimeConfig.redis.queueUrl
  if (!url) {
    throw new Error('JUHE_AI_REDIS_QUEUE_URL 在 Redis Stream queue driver 下必须配置')
  }
  return url
}

function defaultRedisStreamConsumerName(groupName: string): string {
  return `${sanitizeRedisStreamPart(groupName)}:${sanitizeRedisStreamPart(hostname())}:${process.pid}`
}

function sanitizeRedisStreamPart(value: string): string {
  return value.trim().replace(/[^a-zA-Z0-9_.:-]+/g, '_') || 'consumer'
}

function isRedisBusyGroupError(error: unknown): boolean {
  return String(error instanceof Error ? error.message : error).includes('BUSYGROUP')
}

function isRedisNoGroupError(error: unknown): boolean {
  return String(error instanceof Error ? error.message : error).includes('NOGROUP')
}

function fieldValue(fields: unknown, name: string): string | undefined {
  if (Array.isArray(fields)) {
    for (let index = 0; index < fields.length; index += 2) {
      if (String(fields[index] ?? '') === name) {
        const value = fields[index + 1]
        return typeof value === 'string' ? value : value === undefined ? undefined : String(value)
      }
    }
    return undefined
  }
  if (fields && typeof fields === 'object') {
    const value = (fields as Record<string, unknown>)[name]
    return typeof value === 'string' ? value : value === undefined ? undefined : String(value)
  }
  return undefined
}

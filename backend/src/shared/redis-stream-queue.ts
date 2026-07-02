import { hostname } from 'node:os'

import { runtimeConfig } from '../config/runtime.js'
import { errorLogFields, logger } from './logger.js'
import { createDedicatedRedisClient, getRedisClient, type RedisCommandClient } from './redis-client.js'

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
}

export interface RedisStreamMessage<T> {
  id: string
  payload: T
}

export interface RedisStreamQueueRuntime {
  pendingCount: number
  lag?: number
  consumers?: number
  lastDeliveredId?: string
  entriesRead?: number
  oldestPendingId?: string
  newestPendingId?: string
}

export class RedisStreamQueue<T> {
  private readonly streamKey: string
  private readonly groupName: string
  private readonly consumerName: string
  private readonly readCount: number
  private readonly blockMs: number
  private readonly claimIdleMs: number
  private readonly redisUrl: string
  private readonly encode: (payload: T) => string
  private readonly decode: (payload: string) => T
  private consumerClientPromise: Promise<RedisCommandClient> | undefined
  private groupReadyPromise: Promise<void> | undefined

  constructor(options: RedisStreamQueueOptions<T>) {
    this.streamKey = options.streamKey
    this.groupName = options.groupName
    this.consumerName = options.consumerName ?? defaultRedisStreamConsumerName(options.groupName)
    this.readCount = options.readCount ?? runtimeConfig.queue.redisStreamReadCount
    this.blockMs = options.blockMs ?? runtimeConfig.queue.redisStreamBlockMs
    this.claimIdleMs = options.claimIdleMs ?? runtimeConfig.queue.redisStreamClaimIdleMs
    this.redisUrl = options.redisUrl ?? requiredRedisQueueUrl()
    this.encode = options.encode ?? ((payload) => JSON.stringify(payload))
    this.decode = options.decode ?? ((payload) => JSON.parse(payload) as T)
  }

  async enqueue(payload: T): Promise<string> {
    const client = await getRedisClient(this.redisUrl)
    const command = ['XADD', this.streamKey, '*', 'payload', this.encode(payload)]
    const id = await client.sendCommand(command)
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
    const result = await client.sendCommand(['XACK', this.streamKey, this.groupName, ...normalizedIds])
    return Number(result ?? 0)
  }

  async inspectRuntime(): Promise<RedisStreamQueueRuntime> {
    await this.ensureGroup()
    const client = await getRedisClient(this.redisUrl)
    const [groupsResult, pendingResult] = await Promise.all([
      client.sendCommand(['XINFO', 'GROUPS', this.streamKey]),
      client.sendCommand(['XPENDING', this.streamKey, this.groupName])
    ])
    const groupRuntime = parseGroupRuntime(groupsResult, this.groupName)
    const pendingRuntime = parsePendingRuntime(pendingResult)
    return {
      ...groupRuntime,
      ...pendingRuntime,
      pendingCount: pendingRuntime.pendingCount ?? groupRuntime.pendingCount ?? 0
    }
  }

  async inspectBacklogMessages(limit = 2): Promise<Array<RedisStreamMessage<T>>> {
    await this.ensureGroup()
    const runtime = await this.inspectRuntime()
    const client = await getRedisClient(this.redisUrl)
    const output: Array<RedisStreamMessage<T>> = []
    const seen = new Set<string>()
    const addEntries = (entries: Array<RedisStreamMessage<T>>) => {
      for (const entry of entries) {
        if (seen.has(entry.id)) continue
        seen.add(entry.id)
        output.push(entry)
      }
    }

    if (runtime.oldestPendingId) {
      addEntries(this.parseEntries(await client.sendCommand([
        'XRANGE',
        this.streamKey,
        runtime.oldestPendingId,
        runtime.oldestPendingId,
        'COUNT',
        '1'
      ])))
    }

    if ((runtime.lag ?? 0) > 0) {
      const start = runtime.lastDeliveredId ? `(${runtime.lastDeliveredId}` : '-'
      addEntries(this.parseEntries(await client.sendCommand([
        'XRANGE',
        this.streamKey,
        start,
        '+',
        'COUNT',
        String(Math.max(1, Math.trunc(limit)))
      ])))
    }

    return output
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
      pendingCount: numberField(fields.get('pending')) ?? 0,
      lag: numberField(fields.get('lag')),
      consumers: numberField(fields.get('consumers')),
      lastDeliveredId: stringField(fields.get('last-delivered-id')),
      entriesRead: numberField(fields.get('entries-read'))
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
      pendingCount: numberField(fields.get('pending')) ?? numberField(fields.get('count')) ?? 0,
      oldestPendingId: stringField(fields.get('firstId')) ?? stringField(fields.get('smallestId')) ?? stringField(fields.get('start')),
      newestPendingId: stringField(fields.get('lastId')) ?? stringField(fields.get('greatestId')) ?? stringField(fields.get('end'))
    }
  }
  return { pendingCount: 0 }
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

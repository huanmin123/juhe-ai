import { hostname } from 'node:os'

import { runtimeConfig } from '../config/runtime.js'
import { createDedicatedRedisClient, getRedisClient, type RedisCommandClient } from './redis-client.js'

export interface RedisStreamQueueOptions<T> {
  streamKey: string
  groupName: string
  consumerName?: string
  maxLen?: number
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

export class RedisStreamQueue<T> {
  private readonly streamKey: string
  private readonly groupName: string
  private readonly consumerName: string
  private readonly maxLen: number
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
    this.maxLen = options.maxLen ?? runtimeConfig.queue.redisStreamMaxLen
    this.readCount = options.readCount ?? runtimeConfig.queue.redisStreamReadCount
    this.blockMs = options.blockMs ?? runtimeConfig.queue.redisStreamBlockMs
    this.claimIdleMs = options.claimIdleMs ?? runtimeConfig.queue.redisStreamClaimIdleMs
    this.redisUrl = options.redisUrl ?? requiredRedisQueueUrl()
    this.encode = options.encode ?? ((payload) => JSON.stringify(payload))
    this.decode = options.decode ?? ((payload) => JSON.parse(payload) as T)
  }

  async enqueue(payload: T): Promise<string> {
    const client = await getRedisClient(this.redisUrl)
    const id = await client.sendCommand([
      'XADD',
      this.streamKey,
      'MAXLEN',
      '~',
      String(this.maxLen),
      '*',
      'payload',
      this.encode(payload)
    ])
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
      await client.sendCommand(['XGROUP', 'CREATE', this.streamKey, this.groupName, '$', 'MKSTREAM'])
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
      output.push({
        id,
        payload: this.decode(payload)
      })
    }
    return output
  }
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

import { hostname } from 'node:os'

import { runtimeConfig } from '../config/runtime.js'
import { errorLogFields, logger } from './logger.js'
import {
  createDedicatedRedisClient,
  getRedisClient,
  invalidateRedisClient,
  isRecoverableRedisClientError,
  RedisOperationDeadlineError,
  runRedisOperationWithDeadline,
  type RedisCommandClient
} from './redis-client.js'
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
  producerTimeoutMs?: number
  backlogCreatedAt?: (payload: T) => string | undefined
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

export interface RedisStreamBacklogWatermark {
  ready: boolean
  oldestCreatedAt?: string
  backfilledCount: number
  failureReason?: 'backfill_incomplete' | 'unreadable_message' | 'invalid_created_at'
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
  private readonly producerClient: (() => Promise<RedisCommandClient>) | undefined
  private readonly producerTimeoutMs: number
  private readonly backlogCreatedAt: ((payload: T) => string | undefined) | undefined
  private readonly backlogCreatedAtIndexKey: string
  private readonly backlogCreatedAtCursorKey: string
  private readonly backlogCreatedAtReadyKey: string
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
    this.producerClient = options.producerClient
    this.producerTimeoutMs = Math.max(1, Math.trunc(options.producerTimeoutMs ?? 3_000))
    this.backlogCreatedAt = options.backlogCreatedAt
    this.backlogCreatedAtIndexKey = `${this.streamKey}:backlog-created-at`
    this.backlogCreatedAtCursorKey = `${this.backlogCreatedAtIndexKey}:backfill-cursor`
    this.backlogCreatedAtReadyKey = `${this.backlogCreatedAtIndexKey}:ready`
  }

  async enqueue(payload: T): Promise<string> {
    const createdAt = this.backlogCreatedAt?.(payload)
    const score = createdAt === undefined ? undefined : backlogCreatedAtScore(createdAt)
    if (this.backlogCreatedAt && score === undefined) {
      throw new Error('Redis Stream backlog createdAt 无效')
    }
    return await this.enqueueEncodedInternal(this.encode(payload), score)
  }

  async enqueueEncoded(encodedPayload: string): Promise<string> {
    if (this.backlogCreatedAt) {
      throw new Error('配置 backlog createdAt 索引的 Redis Stream 禁止绕过结构化入队')
    }
    return await this.enqueueEncodedInternal(encodedPayload)
  }

  private async enqueueEncodedInternal(encodedPayload: string, backlogCreatedAtScore?: number): Promise<string> {
    const enqueue = (client: RedisCommandClient) => backlogCreatedAtScore === undefined
      ? client.eval(redisEnqueueWithFenceScript, {
          keys: [this.fenceKey, this.streamKey],
          arguments: ['payload', encodedPayload]
        })
      : client.eval(redisEnqueueWithFenceAndBacklogIndexScript, {
          keys: [this.fenceKey, this.streamKey, this.backlogCreatedAtIndexKey],
          arguments: ['payload', encodedPayload, String(backlogCreatedAtScore)]
        })
    if (!this.producerClient) {
      const id = await runRedisOperationWithDeadline(this.redisUrl, {
        operationName: 'Redis Stream 入队',
        timeoutMs: this.producerTimeoutMs
      }, enqueue)
      return String(id ?? '')
    }

    const deadlineAtMs = Date.now() + this.producerTimeoutMs
    const clientPromise = this.producerClient()
    let client: RedisCommandClient | undefined
    try {
      client = await awaitRedisStreamProducerStep(clientPromise, deadlineAtMs, 'Redis Stream producer 连接')
      const id = await awaitRedisStreamProducerStep(enqueue(client), deadlineAtMs, 'Redis Stream 入队')
      return String(id ?? '')
    } catch (error) {
      if (error instanceof RedisOperationDeadlineError) {
        if (client) {
          client.destroy?.()
        } else {
          void clientPromise.then((lateClient) => lateClient.destroy?.(), () => undefined)
        }
      } else if (client && isRecoverableRedisClientError(error)) {
        await invalidateRedisClient(this.redisUrl, client)
      }
      throw error
    }
  }

  async readNew(): Promise<Array<RedisStreamMessage<T>>> {
    await this.ensureGroup()
    const client = await this.consumerClient()
    try {
      const result = await this.readNewUnsafe(client)
      return this.parseStreamReadResult(result)
    } catch (error) {
      if (isRecoverableRedisClientError(error)) {
        this.resetConsumerClient(client)
      }
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
      if (isRecoverableRedisClientError(error)) {
        this.resetConsumerClient(client)
      }
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
    const client = this.producerClient ? await this.producerClient() : await getRedisClient(this.redisUrl)
    const result = this.backlogCreatedAt
      ? await client.eval(redisAckDeleteAndRemoveBacklogIndexScript, {
          keys: [this.streamKey, this.backlogCreatedAtIndexKey],
          arguments: [this.groupName, ...normalizedIds]
        })
      : await client.eval(redisAckAndDeleteMessagesScript, {
          keys: [this.streamKey],
          arguments: [this.groupName, ...normalizedIds]
        })
    return Number(result ?? 0)
  }

  async inspectOldestBacklogCreatedAt(backfillLimit = 512): Promise<RedisStreamBacklogWatermark> {
    if (!this.backlogCreatedAt) {
      throw new Error('Redis Stream 未配置 backlog createdAt 索引')
    }
    const normalizedLimit = Math.max(1, Math.trunc(backfillLimit))
    const inspect = async (client: RedisCommandClient): Promise<RedisStreamBacklogWatermark> => {
      const current = await this.readBacklogCreatedAtIndex(client)
      if (current.ready) return current

      const cursor = stringField(await client.sendCommand(['GET', this.backlogCreatedAtCursorKey]))
      const rawEntries = await client.sendCommand([
        'XRANGE',
        this.streamKey,
        cursor ? `(${cursor}` : '-',
        '+',
        'COUNT',
        String(normalizedLimit)
      ])
      const parsed = this.backlogCreatedAtEntries(rawEntries)
      if (parsed.failureReason) {
        return {
          ready: false,
          backfilledCount: 0,
          failureReason: parsed.failureReason
        }
      }

      const finished = parsed.entries.length < normalizedLimit
      const lastId = parsed.entries.at(-1)?.id ?? cursor ?? '0-0'
      await client.eval(redisBackfillBacklogCreatedAtIndexScript, {
        keys: [
          this.streamKey,
          this.backlogCreatedAtIndexKey,
          this.backlogCreatedAtCursorKey,
          this.backlogCreatedAtReadyKey
        ],
        arguments: [
          lastId,
          finished ? '1' : '0',
          ...parsed.entries.flatMap((entry) => [entry.id, String(entry.score)])
        ]
      })
      if (finished) {
        return {
          ...await this.readBacklogCreatedAtIndex(client),
          backfilledCount: parsed.entries.length
        }
      }
      return {
        ready: false,
        backfilledCount: parsed.entries.length,
        failureReason: 'backfill_incomplete'
      }
    }
    if (this.producerClient) {
      return await inspect(await this.producerClient())
    }
    return await runRedisOperationWithDeadline(this.redisUrl, {
      operationName: 'Redis Stream backlog createdAt 水位检查',
      timeoutMs: this.producerTimeoutMs
    }, inspect)
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

  private resetConsumerClient(expectedClient: RedisCommandClient): void {
    const clientPromise = this.consumerClientPromise
    if (!clientPromise) return
    void clientPromise.then((client) => {
      if (client !== expectedClient || this.consumerClientPromise !== clientPromise) return
      this.consumerClientPromise = undefined
      client.destroy?.()
    }, () => {
      if (this.consumerClientPromise === clientPromise) {
        this.consumerClientPromise = undefined
      }
    })
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
        this.recordPoisonMessage(id, error)
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

  private async readBacklogCreatedAtIndex(client: RedisCommandClient): Promise<RedisStreamBacklogWatermark> {
    const result = await client.eval(redisReadBacklogCreatedAtIndexScript, {
      keys: [
        this.streamKey,
        this.backlogCreatedAtIndexKey,
        this.backlogCreatedAtCursorKey,
        this.backlogCreatedAtReadyKey
      ],
      arguments: []
    })
    if (!Array.isArray(result) || Number(result[0] ?? 0) !== 1) {
      return { ready: false, backfilledCount: 0, failureReason: 'backfill_incomplete' }
    }
    const rawScore = stringField(result[1])
    if (!rawScore) return { ready: true, backfilledCount: 0 }
    const score = Number(rawScore)
    return Number.isFinite(score)
      ? { ready: true, oldestCreatedAt: new Date(score).toISOString(), backfilledCount: 0 }
      : { ready: false, backfilledCount: 0, failureReason: 'invalid_created_at' }
  }

  private backlogCreatedAtEntries(rawEntries: unknown): {
    entries: Array<{ id: string; score: number }>
    failureReason?: 'unreadable_message' | 'invalid_created_at'
  } {
    if (!Array.isArray(rawEntries)) return { entries: [], failureReason: 'unreadable_message' }
    const entries: Array<{ id: string; score: number }> = []
    for (const rawEntry of rawEntries) {
      if (!Array.isArray(rawEntry) || rawEntry.length < 2) {
        return { entries: [], failureReason: 'unreadable_message' }
      }
      const id = String(rawEntry[0] ?? '')
      const encodedPayload = fieldValue(rawEntry[1], 'payload')
      if (!id || encodedPayload === undefined) {
        return { entries: [], failureReason: 'unreadable_message' }
      }
      let payload: T
      try {
        payload = this.decode(encodedPayload)
      } catch (error) {
        this.recordPoisonMessage(id, error)
        return { entries: [], failureReason: 'unreadable_message' }
      }
      const createdAt = this.backlogCreatedAt?.(payload)
      const score = createdAt === undefined ? undefined : backlogCreatedAtScore(createdAt)
      if (score === undefined) {
        return { entries: [], failureReason: 'invalid_created_at' }
      }
      entries.push({ id, score })
    }
    return { entries }
  }

  private recordPoisonMessage(id: string, error: unknown): void {
    logger.error(errorLogFields(error, {
      event: 'redis_stream_message_decode_failed',
      streamKey: this.streamKey,
      groupName: this.groupName,
      messageId: id
    }), 'Redis Stream 消息解码失败，消息保留 pending 并阻断排空')
  }
}

async function awaitRedisStreamProducerStep<T>(promise: Promise<T>, deadlineAtMs: number, operationName: string): Promise<T> {
  const remainingMs = Math.max(0, deadlineAtMs - Date.now())
  if (remainingMs === 0) throw new RedisOperationDeadlineError(operationName)
  let timer: ReturnType<typeof setTimeout> | undefined
  try {
    return await Promise.race([
      promise,
      new Promise<T>((_resolve, reject) => {
        timer = setTimeout(() => reject(new RedisOperationDeadlineError(operationName)), remainingMs)
        timer.unref?.()
      })
    ])
  } finally {
    if (timer) clearTimeout(timer)
    promise.catch(() => undefined)
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

function backlogCreatedAtScore(value: string): number | undefined {
  const time = Date.parse(value)
  return Number.isFinite(time) ? time : undefined
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

const redisEnqueueWithFenceAndBacklogIndexScript = `
if redis.call('GET', KEYS[1]) then
  return redis.error_reply('QUEUE_QUIESCED')
end
local id = redis.call('XADD', KEYS[2], '*', ARGV[1], ARGV[2])
redis.call('ZADD', KEYS[3], ARGV[3], id)
return id
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

const redisAckDeleteAndRemoveBacklogIndexScript = `
local group_name = ARGV[1]
local acked = 0
for index = 2, #ARGV do
  local id = ARGV[index]
  local result = redis.call('XACK', KEYS[1], group_name, id)
  if result > 0 then
    acked = acked + result
    redis.call('XDEL', KEYS[1], id)
    redis.call('ZREM', KEYS[2], id)
  end
end
return acked
`

const redisBackfillBacklogCreatedAtIndexScript = `
for index = 3, #ARGV, 2 do
  local id = ARGV[index]
  local score = ARGV[index + 1]
  if #redis.call('XRANGE', KEYS[1], id, id, 'COUNT', 1) > 0 then
    redis.call('ZADD', KEYS[2], score, id)
  end
end
redis.call('SET', KEYS[3], ARGV[1])
if ARGV[2] == '1' then
  redis.call('SET', KEYS[4], '1')
end
return 1
`

const redisReadBacklogCreatedAtIndexScript = `
if redis.call('GET', KEYS[4]) ~= '1' then
  return {0}
end
if redis.call('XLEN', KEYS[1]) ~= redis.call('ZCARD', KEYS[2]) then
  redis.call('DEL', KEYS[2], KEYS[3], KEYS[4])
  return {0}
end
local oldest = redis.call('ZRANGE', KEYS[2], 0, 0, 'WITHSCORES')
if #oldest == 0 then
  return {1, ''}
end
return {1, oldest[2]}
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

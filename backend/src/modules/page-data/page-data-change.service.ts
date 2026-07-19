import { createHash, randomUUID } from 'node:crypto'

export const PAGE_DATA_PROTOCOL_VERSION = 2
export const PAGE_DATA_MAX_CONFIRM_DOMAINS = 32
export const PAGE_DATA_MAX_EVENT_OWNERS = 256
const PAGE_DATA_MAX_FIELD_MASK = 32

export const pageDataDomains = [
  'accounts.static',
  'accounts.runtime',
  'accounts.options',
  'usage.records',
  'announcements.public',
  'providers.catalog',
  'groups.static',
  'systemAccounts.options',
  'teams.options',
  'routeStrategies.options',
  'stats.overview',
  'stats.accountUsage',
  'stats.aiPerformance'
] as const

const globalPageDataDomains = new Set<PageDataDomain>(['announcements.public'])

export type PageDataDomain = typeof pageDataDomains[number]
export type PageDataViewScope = 'self' | 'admin'
export type PageDataChangeOperation = 'upsert' | 'delete' | 'append' | 'range_reset' | 'window_replace'
export type PageDataConfirmAction = 'unchanged' | 'delta' | 'reload' | 'reset'

export interface PageDataRevisionToken {
  protocolVersion: number
  epoch: string
  scope: string
  domain: PageDataDomain
  sequence: number
  resetSequence: number
}

export interface PageDataScope {
  readonly streamKey: string
  readonly fingerprint: string
}

export interface PageDataChangeEvent {
  eventId: string
  domain: PageDataDomain
  entityId?: string
  operation: PageDataChangeOperation
  fieldMask: string[]
  ownerSystemAccountIds: string[]
  membershipChanged: boolean
  orderChanged: boolean
  filterChanged: boolean
  pageChanged: boolean
  occurredAt: string
  allScopes?: boolean
}

export interface PageDataChangeProjection {
  entityId?: string
  operation: PageDataChangeOperation
  fieldMask: string[]
  membershipChanged: boolean
  orderChanged: boolean
  filterChanged: boolean
  pageChanged: boolean
}

export interface PageDataConfirmDomainResult {
  action: PageDataConfirmAction
  token: PageDataRevisionToken
  changes?: PageDataChangeProjection[]
}

export interface PageDataConfirmResult {
  serverTime: string
  domains: Partial<Record<PageDataDomain, PageDataConfirmDomainResult>>
}

export interface PageDataChangeStore {
  confirm(
    scope: PageDataScope,
    domains: Record<string, PageDataRevisionToken | undefined>
  ): Promise<PageDataConfirmResult>
  publish(event: PageDataChangeEvent): Promise<void>
}

export interface PageDataRedisClient {
  get(key: string): Promise<string | null>
  set(key: string, value: string, options?: Record<string, unknown>): Promise<string | null>
  eval(script: string, options: { keys: string[]; arguments: string[] }): Promise<unknown>
  sendCommand(command: string[]): Promise<unknown>
}

interface SequencedEvent {
  sequence: number
  event: PageDataChangeEvent
}

interface MemoryStream {
  sequence: number
  events: SequencedEvent[]
  eventIds: Set<string>
}

interface PageDataStreamSnapshot {
  sequence: number
  events: SequencedEvent[]
}

const redisPublishScript = `
-- page_data_publish_v1
local sequenceKey = KEYS[1]
local logKey = KEYS[2]
local dedupeKey = KEYS[3]
local eventId = ARGV[1]
local rawEvent = ARGV[2]
local maxEvents = tonumber(ARGV[3])
local ttlSeconds = tonumber(ARGV[4])
if redis.call('SISMEMBER', dedupeKey, eventId) == 1 then
  return tonumber(redis.call('GET', sequenceKey) or '0')
end
redis.call('SADD', dedupeKey, eventId)
local sequence = redis.call('INCR', sequenceKey)
redis.call('RPUSH', logKey, tostring(sequence) .. '\\t' .. rawEvent)
redis.call('LTRIM', logKey, -maxEvents, -1)
redis.call('EXPIRE', logKey, ttlSeconds)
redis.call('EXPIRE', dedupeKey, ttlSeconds)
return sequence
`

const redisConfirmScript = `
-- page_data_confirm_v1
local epoch = redis.call('GET', KEYS[1])
if not epoch then
  redis.call('SET', KEYS[1], ARGV[1], 'NX')
  epoch = redis.call('GET', KEYS[1]) or ARGV[1]
end
local domainCount = tonumber(ARGV[2])
local result = { epoch }
local keyIndex = 2
local argumentIndex = 3
for _ = 1, domainCount do
  local sequence = tonumber(redis.call('GET', KEYS[keyIndex]) or '0')
  local resetSequence = tonumber(redis.call('GET', KEYS[keyIndex + 2]) or '0')
  local knownEpoch = ARGV[argumentIndex]
  local knownSequence = tonumber(ARGV[argumentIndex + 1])
  local knownResetSequence = tonumber(ARGV[argumentIndex + 2])
  local rawLog = {}
  if knownEpoch == epoch
    and knownSequence >= 0
    and knownSequence < sequence
    and knownResetSequence == resetSequence then
    rawLog = redis.call('LRANGE', KEYS[keyIndex + 1], 0, -1)
  end
  table.insert(result, { sequence, resetSequence, rawLog })
  keyIndex = keyIndex + 3
  argumentIndex = argumentIndex + 3
end
return result
`

export function pageDataScope(input: {
  viewerSystemAccountId: string
  viewScope: PageDataViewScope
  targetSystemAccountId?: string
}): PageDataScope {
  const viewerId = requiredId(input.viewerSystemAccountId, '当前系统账户')
  const streamKey = input.viewScope === 'self'
    ? `owner:${viewerId}`
    : input.targetSystemAccountId
      ? `owner:${requiredId(input.targetSystemAccountId, '目标系统账户')}`
      : 'global'
  const fingerprintSource = [
    PAGE_DATA_PROTOCOL_VERSION,
    viewerId,
    input.viewScope,
    input.targetSystemAccountId?.trim() || '*',
    streamKey
  ].join('|')
  return {
    streamKey,
    fingerprint: createHash('sha256').update(fingerprintSource).digest('base64url').slice(0, 24)
  }
}

export function createMemoryPageDataChangeStore(options: {
  epoch?: string
  maxEventsPerStream?: number
  now?: () => Date
} = {}): PageDataChangeStore {
  const epoch = options.epoch?.trim() || randomUUID()
  const maxEventsPerStream = positiveInteger(options.maxEventsPerStream, 256)
  const now = options.now ?? (() => new Date())
  const streams = new Map<string, MemoryStream>()

  return {
    async confirm(scope, requestedDomains) {
      const domains = validatedConfirmDomains(requestedDomains)
      const result: PageDataConfirmResult = {
        serverTime: now().toISOString(),
        domains: {}
      }
      for (const [domain, knownToken] of domains) {
        const stream = streams.get(streamId(confirmStreamKey(scope, domain), domain))
        const resetStream = streams.get(streamId('all', domain))
        const currentSequence = stream?.sequence ?? 0
        const token = revisionToken(epoch, scope, domain, currentSequence, resetStream?.sequence ?? 0)
        result.domains[domain] = confirmDomainResult({
          epoch,
          scope,
          domain,
          token,
          knownToken,
          stream,
          resetStream
        })
      }
      return result
    },
    async publish(event) {
      validateChangeEvent(event)
      const streamKeys = publishStreamKeys(event)
      for (const scopeStreamKey of streamKeys) {
        const id = streamId(scopeStreamKey, event.domain)
        const stream = streams.get(id) ?? { sequence: 0, events: [], eventIds: new Set<string>() }
        if (stream.eventIds.has(event.eventId)) continue
        stream.sequence += 1
        stream.events.push({ sequence: stream.sequence, event: cloneEvent(event) })
        stream.eventIds.add(event.eventId)
        while (stream.events.length > maxEventsPerStream) {
          const removed = stream.events.shift()
          if (removed) stream.eventIds.delete(removed.event.eventId)
        }
        streams.set(id, stream)
      }
    }
  }
}

export function createRedisPageDataChangeStore(options: {
  client: PageDataRedisClient
  keyPrefix: string
  epoch?: string
  maxEventsPerStream?: number
  ttlSeconds?: number
  now?: () => Date
}): PageDataChangeStore {
  const keyPrefix = options.keyPrefix.trim().replace(/[^a-zA-Z0-9:_-]/g, '_')
  if (!keyPrefix) throw new Error('页面数据 Redis keyPrefix 不能为空')
  const proposedEpoch = options.epoch?.trim() || randomUUID()
  const epochKey = `${keyPrefix}:epoch:v${PAGE_DATA_PROTOCOL_VERSION}`
  const maxEventsPerStream = positiveInteger(options.maxEventsPerStream, 256)
  const ttlSeconds = positiveInteger(options.ttlSeconds, 8 * 24 * 60 * 60)
  const now = options.now ?? (() => new Date())
  let epochPromise: Promise<string> | undefined

  const sharedEpoch = (): Promise<string> => {
    epochPromise ??= ensureRedisEpoch(options.client, epochKey, proposedEpoch, ttlSeconds).catch((error) => {
      epochPromise = undefined
      throw error
    })
    return epochPromise
  }

  return {
    async confirm(scope, requestedDomains) {
      const domains = validatedConfirmDomains(requestedDomains)
      const redisKeys = [epochKey]
      const redisArguments = [proposedEpoch, String(domains.length)]
      for (const [domain, knownToken] of domains) {
        const streamKeys = redisStreamKeys(keyPrefix, confirmStreamKey(scope, domain), domain)
        const resetKeys = redisStreamKeys(keyPrefix, 'all', domain)
        redisKeys.push(streamKeys.sequenceKey, streamKeys.logKey, resetKeys.sequenceKey)
        const structurallyValidToken = knownToken && validKnownTokenExceptEpoch(knownToken, { scope, domain })
          ? knownToken
          : undefined
        redisArguments.push(
          structurallyValidToken?.epoch ?? '',
          String(structurallyValidToken?.sequence ?? -1),
          String(structurallyValidToken?.resetSequence ?? -1)
        )
      }
      const snapshots = parseRedisConfirmSnapshots(
        await options.client.eval(redisConfirmScript, { keys: redisKeys, arguments: redisArguments }),
        domains.length
      )
      const epoch = snapshots.epoch
      const result: PageDataConfirmResult = {
        serverTime: now().toISOString(),
        domains: {}
      }
      domains.forEach(([domain, knownToken], index) => {
        const snapshot = snapshots.domains[index]
        if (!snapshot) throw new Error('页面数据 Redis confirm 快照缺少数据域')
        const token = revisionToken(epoch, scope, domain, snapshot.sequence, snapshot.resetSequence)
        result.domains[domain] = confirmDomainResult({
          epoch,
          scope,
          domain,
          token,
          knownToken,
          stream: snapshot,
          resetStream: { sequence: snapshot.resetSequence, events: [] }
        })
      })
      return result
    },
    async publish(event) {
      validateChangeEvent(event)
      await sharedEpoch()
      const streamKeys = publishStreamKeys(event)
      await Promise.all(Array.from(streamKeys, async (scopeStreamKey) => {
        const keys = redisStreamKeys(keyPrefix, scopeStreamKey, event.domain)
        await options.client.eval(redisPublishScript, {
          keys: [keys.sequenceKey, keys.logKey, keys.dedupeKey],
          arguments: [event.eventId, JSON.stringify(cloneEvent(event)), String(maxEventsPerStream), String(ttlSeconds)]
        })
      }))
    }
  }
}

function confirmDomainResult(input: {
  epoch: string
  scope: PageDataScope
  domain: PageDataDomain
  token: PageDataRevisionToken
  knownToken?: PageDataRevisionToken
  stream?: PageDataStreamSnapshot
  resetStream?: PageDataStreamSnapshot
}): PageDataConfirmDomainResult {
  const { knownToken, token, stream } = input
  if (!knownToken || !validKnownToken(knownToken, input)) {
    return { action: 'reload', token }
  }
  if (knownToken.resetSequence !== token.resetSequence) {
    return { action: 'reset', token }
  }
  if (knownToken.sequence === token.sequence) {
    return { action: 'unchanged', token }
  }
  if (knownToken.sequence > token.sequence) {
    return { action: 'reset', token }
  }
  const events = stream?.events.filter((item) => item.sequence > knownToken.sequence) ?? []
  if (!events.length
    || events.some((item, index) => item.sequence !== knownToken.sequence + index + 1)
    || events[events.length - 1]?.sequence !== token.sequence) {
    return { action: 'reset', token }
  }
  if (events.some((item) => item.event.operation === 'range_reset')) {
    return { action: 'reset', token }
  }
  if (events.some(({ event }) => event.membershipChanged || event.orderChanged || event.filterChanged || event.pageChanged)) {
    return { action: 'reload', token }
  }
  return {
    action: 'delta',
    token,
    changes: events.map(({ event }) => projectEvent(event))
  }
}

async function ensureRedisEpoch(
  client: PageDataRedisClient,
  key: string,
  proposedEpoch: string,
  _ttlSeconds: number
): Promise<string> {
  const existing = await client.get(key)
  if (existing) return existing
  const inserted = await client.set(key, proposedEpoch, { NX: true })
  if (inserted === 'OK') return proposedEpoch
  return (await client.get(key)) ?? proposedEpoch
}

function parseRedisConfirmSnapshots(value: unknown, expectedDomains: number): {
  epoch: string
  domains: Array<PageDataStreamSnapshot & { resetSequence: number }>
} {
  if (!Array.isArray(value) || value.length !== expectedDomains + 1) {
    throw new Error('页面数据 Redis confirm 快照格式无效')
  }
  const epoch = redisString(value[0])
  if (!epoch) throw new Error('页面数据 Redis epoch 无效')
  const domains = value.slice(1).map((rawSnapshot) => {
    if (!Array.isArray(rawSnapshot) || rawSnapshot.length !== 3 || !Array.isArray(rawSnapshot[2])) {
      throw new Error('页面数据 Redis domain 快照格式无效')
    }
    return {
      sequence: nonNegativeInteger(rawSnapshot[0]),
      resetSequence: nonNegativeInteger(rawSnapshot[1]),
      events: rawSnapshot[2].flatMap((rawEvent) => parseRedisSequencedEvent(rawEvent))
    }
  })
  return { epoch, domains }
}

function redisString(value: unknown): string {
  return typeof value === 'string' ? value : value instanceof Buffer ? value.toString('utf8') : ''
}

function parseRedisSequencedEvent(value: unknown): SequencedEvent[] {
  const text = typeof value === 'string' ? value : value instanceof Buffer ? value.toString('utf8') : ''
  const separator = text.indexOf('\t')
  if (separator <= 0) return []
  const sequence = nonNegativeInteger(text.slice(0, separator))
  if (sequence <= 0) return []
  try {
    const event = JSON.parse(text.slice(separator + 1)) as PageDataChangeEvent
    validateChangeEvent(event)
    return [{ sequence, event }]
  } catch {
    return []
  }
}

function redisStreamKeys(keyPrefix: string, scopeStreamKey: string, domain: PageDataDomain): {
  sequenceKey: string
  logKey: string
  dedupeKey: string
} {
  const suffix = `${safeRedisPart(scopeStreamKey)}:${safeRedisPart(domain)}`
  return {
    sequenceKey: `${keyPrefix}:sequence:${suffix}`,
    logKey: `${keyPrefix}:log:${suffix}`,
    dedupeKey: `${keyPrefix}:dedupe:${suffix}`
  }
}

function safeRedisPart(value: string): string {
  return value.replace(/[^a-zA-Z0-9:._-]/g, '_')
}

function validKnownToken(token: PageDataRevisionToken, input: {
  epoch: string
  scope: PageDataScope
  domain: PageDataDomain
}): boolean {
  return validKnownTokenExceptEpoch(token, input)
    && token.epoch === input.epoch
}

function validKnownTokenExceptEpoch(token: PageDataRevisionToken, input: {
  scope: PageDataScope
  domain: PageDataDomain
}): boolean {
  return token.protocolVersion === PAGE_DATA_PROTOCOL_VERSION
    && token.scope === input.scope.fingerprint
    && token.domain === input.domain
    && Number.isSafeInteger(token.sequence)
    && token.sequence >= 0
    && Number.isSafeInteger(token.resetSequence)
    && token.resetSequence >= 0
}

function revisionToken(
  epoch: string,
  scope: PageDataScope,
  domain: PageDataDomain,
  sequence: number,
  resetSequence: number
): PageDataRevisionToken {
  return {
    protocolVersion: PAGE_DATA_PROTOCOL_VERSION,
    epoch,
    scope: scope.fingerprint,
    domain,
    sequence,
    resetSequence
  }
}

function validatedConfirmDomains(
  input: Record<string, PageDataRevisionToken | undefined>
): Array<[PageDataDomain, PageDataRevisionToken | undefined]> {
  const entries = Object.entries(input ?? {})
  if (entries.length > PAGE_DATA_MAX_CONFIRM_DOMAINS) {
    throw new Error(`单次最多确认 ${PAGE_DATA_MAX_CONFIRM_DOMAINS} 个数据域`)
  }
  const knownDomains = new Set<string>(pageDataDomains)
  return entries.map(([domain, token]) => {
    if (!knownDomains.has(domain)) throw new Error(`不支持的数据域：${domain}`)
    return [domain as PageDataDomain, token]
  })
}

function validateChangeEvent(event: PageDataChangeEvent): void {
  if (!pageDataDomains.includes(event.domain)) throw new Error(`不支持的数据域：${event.domain}`)
  if (!event.eventId?.trim()) throw new Error('页面数据变更 eventId 不能为空')
  if (!['upsert', 'delete', 'append', 'range_reset', 'window_replace'].includes(event.operation)) {
    throw new Error('页面数据变更 operation 无效')
  }
  if (!Array.isArray(event.fieldMask) || !event.fieldMask.every((field) => typeof field === 'string' && field.trim())) {
    throw new Error('页面数据变更 fieldMask 无效')
  }
  if (event.fieldMask.length > PAGE_DATA_MAX_FIELD_MASK) throw new Error('页面数据变更字段过多')
  if (!Array.isArray(event.ownerSystemAccountIds)) throw new Error('页面数据变更 owner 作用域无效')
  if (event.ownerSystemAccountIds.length > PAGE_DATA_MAX_EVENT_OWNERS) throw new Error('页面数据变更 owner 作用域过多')
  event.ownerSystemAccountIds.forEach((id) => requiredId(id, '页面数据变更 owner'))
  for (const flag of ['membershipChanged', 'orderChanged', 'filterChanged', 'pageChanged'] as const) {
    if (typeof event[flag] !== 'boolean') throw new Error(`页面数据变更 ${flag} 无效`)
  }
  if (!Number.isFinite(Date.parse(event.occurredAt))) throw new Error('页面数据变更 occurredAt 无效')
  if (event.allScopes !== undefined && typeof event.allScopes !== 'boolean') throw new Error('页面数据变更 allScopes 无效')
}

export function isPageDataChangeEvent(value: unknown): value is PageDataChangeEvent {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  try {
    validateChangeEvent(value as PageDataChangeEvent)
    return true
  } catch {
    return false
  }
}

function projectEvent(event: PageDataChangeEvent): PageDataChangeProjection {
  return {
    ...(event.entityId ? { entityId: event.entityId } : {}),
    operation: event.operation,
    fieldMask: [...event.fieldMask],
    membershipChanged: event.membershipChanged,
    orderChanged: event.orderChanged,
    filterChanged: event.filterChanged,
    pageChanged: event.pageChanged
  }
}

function cloneEvent(event: PageDataChangeEvent): PageDataChangeEvent {
  return {
    ...event,
    fieldMask: [...event.fieldMask],
    ownerSystemAccountIds: [...event.ownerSystemAccountIds]
  }
}

function streamId(scopeStreamKey: string, domain: PageDataDomain): string {
  return `${scopeStreamKey}|${domain}`
}

function confirmStreamKey(scope: PageDataScope, domain: PageDataDomain): string {
  return globalPageDataDomains.has(domain) ? 'global' : scope.streamKey
}

function publishStreamKeys(event: PageDataChangeEvent): Set<string> {
  if (event.allScopes) return new Set(['global', 'all'])
  if (globalPageDataDomains.has(event.domain)) return new Set(['global'])
  return new Set(['global', ...event.ownerSystemAccountIds.map((id) => `owner:${id.trim()}`)])
}

function requiredId(value: string, label: string): string {
  const normalized = value?.trim()
  if (!normalized) throw new Error(`${label}不能为空`)
  return normalized
}

function positiveInteger(value: number | undefined, fallback: number): number {
  return Number.isFinite(value) ? Math.max(1, Math.trunc(value as number)) : fallback
}

function nonNegativeInteger(value: unknown): number {
  const numeric = Number(value ?? 0)
  return Number.isSafeInteger(numeric) && numeric >= 0 ? numeric : 0
}

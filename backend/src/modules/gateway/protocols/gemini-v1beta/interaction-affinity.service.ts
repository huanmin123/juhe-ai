import { createHash } from 'node:crypto'

import type { Request } from 'express'

import { runtimeConfig } from '../../../../config/runtime.js'
import { GEMINI_PROVIDER_CODE } from '../../../../domain/provider-protocol.js'
import { createProcessLocalResourceCache } from '../../../../shared/cache.js'
import { createRuntimeStateStore, type RuntimeStateStore } from '../../../../shared/runtime-state-store.js'
import { GatewayRequestValidationError } from '../../request/validation-error.js'
import type { UpstreamAccount } from '../openai-v1/route-helpers.js'

export interface GeminiInteractionAffinityScope {
  systemAccountId: string
  apiKeyId?: string
  groupId: string
}

export interface GeminiInteractionAffinityBinding {
  interactionId: string
  accountId: string
  groupId: string
  providerCode: string
  providerProtocolProfileId?: string
  createdAtMs: number
}

export interface GeminiInteractionAffinityMutationResult {
  action: 'remembered' | 'deleted' | 'refreshed' | 'none'
  interactionId?: string
}

export class GeminiInteractionAffinityUnavailableError extends GatewayRequestValidationError {
  readonly originalError: unknown

  constructor(operation: 'remember' | 'delete', originalError: unknown) {
    super(
      operation === 'remember'
        ? 'Gemini Interaction 账号亲和记录暂时不可用，请重试'
        : 'Gemini Interaction 账号亲和删除暂时不可用，请重试',
      'interaction_affinity_unavailable',
      { statusCode: 503, type: 'service_unavailable' }
    )
    this.name = 'GeminiInteractionAffinityUnavailableError'
    this.originalError = originalError
  }
}

const interactionAffinityTtlMs = 7 * 24 * 60 * 60 * 1000
const interactionAffinityMaxEntries = 20_000
const interactionAffinityMemoryCache = createProcessLocalResourceCache<string, GeminiInteractionAffinityBinding>({
  name: 'gateway:gemini-interaction-affinity',
  max: interactionAffinityMaxEntries,
  ttlMs: interactionAffinityTtlMs,
  updateAgeOnGet: true
})
const interactionAffinityStateStore = createRuntimeStateStore('gateway-gemini-interaction-affinity')
type GeminiInteractionAffinityStateStore = Pick<RuntimeStateStore, 'getJson' | 'setJson' | 'delete'>
let interactionAffinityStateStoreForTest: GeminiInteractionAffinityStateStore | undefined

export function geminiInteractionResourceIdFromRequest(req: Request): string | undefined {
  const match = geminiInteractionResourcePathMatch(req)
  if (!match) return undefined
  return normalizeInteractionId(decodePathSegment(match[1]))
}

export function isGeminiInteractionResourceRequest(req: Request): boolean {
  return geminiInteractionResourcePathMatch(req) !== undefined
}

export function isGeminiInteractionCreateRequest(req: Request): boolean {
  return req.method.toUpperCase() === 'POST' && normalizedRequestPath(req).toLowerCase() === '/interactions'
}

export async function resolveGeminiInteractionAffinityAsync(input: {
  req: Request
  scope: GeminiInteractionAffinityScope
}): Promise<GeminiInteractionAffinityBinding | undefined> {
  const interactionId = geminiInteractionResourceIdFromRequest(input.req)
  if (!interactionId) return undefined
  const key = affinityKey(input.scope, interactionId)
  const binding = interactionAffinityStateStoreForTest
    ? await interactionAffinityStateStoreForTest.getJson<GeminiInteractionAffinityBinding>(key)
    : runtimeConfig.runtimeStateDriver === 'redis'
      ? await interactionAffinityStateStore.getJson<GeminiInteractionAffinityBinding>(key)
    : interactionAffinityMemoryCache.get(key)
  if (!isValidBinding(binding, interactionId)) {
    if (binding) await deleteBinding(key)
    return undefined
  }
  await setBinding(key, binding)
  return binding
}

export async function updateGeminiInteractionAffinityAfterSuccessAsync(input: {
  req: Request
  responseBodyText?: string
  responseResourceId?: string
  account: UpstreamAccount
  scope: GeminiInteractionAffinityScope
}): Promise<GeminiInteractionAffinityMutationResult> {
  if (input.account.providerCode !== GEMINI_PROVIDER_CODE) return { action: 'none' }

  if (isGeminiInteractionCreateRequest(input.req)) {
    const interactionId = normalizeInteractionId(input.responseResourceId)
      ?? geminiInteractionIdFromResponseBody(input.responseBodyText)
    return rememberGeminiInteractionAffinityAsync({
      interactionId,
      account: input.account,
      scope: input.scope
    })
  }

  const interactionId = geminiInteractionResourceIdFromRequest(input.req)
  if (!interactionId) return { action: 'none' }
  if (input.req.method.toUpperCase() === 'DELETE') {
    return deleteGeminiInteractionAffinityAsync({ interactionId, scope: input.scope })
  }
  const binding = await resolveGeminiInteractionAffinityAsync({ req: input.req, scope: input.scope })
  return binding ? { action: 'refreshed', interactionId } : { action: 'none' }
}

export async function rememberGeminiInteractionAffinityAsync(input: {
  interactionId: string | undefined
  account: UpstreamAccount
  scope: GeminiInteractionAffinityScope
}): Promise<GeminiInteractionAffinityMutationResult> {
  if (input.account.providerCode !== GEMINI_PROVIDER_CODE) return { action: 'none' }
  const interactionId = normalizeInteractionId(input.interactionId)
  if (!interactionId) return { action: 'none' }
  const binding: GeminiInteractionAffinityBinding = {
    interactionId,
    accountId: input.account.id,
    groupId: input.scope.groupId,
    providerCode: input.account.providerCode,
    providerProtocolProfileId: input.account.providerProtocolProfileId,
    createdAtMs: Date.now()
  }
  try {
    await setBinding(affinityKey(input.scope, interactionId), binding)
  } catch (error) {
    throw new GeminiInteractionAffinityUnavailableError('remember', error)
  }
  return { action: 'remembered', interactionId }
}

export async function deleteGeminiInteractionAffinityAsync(input: {
  interactionId: string | undefined
  scope: GeminiInteractionAffinityScope
}): Promise<GeminiInteractionAffinityMutationResult> {
  const interactionId = normalizeInteractionId(input.interactionId)
  if (!interactionId) return { action: 'none' }
  try {
    await deleteBinding(affinityKey(input.scope, interactionId))
  } catch (error) {
    throw new GeminiInteractionAffinityUnavailableError('delete', error)
  }
  return { action: 'deleted', interactionId }
}

export function clearGeminiInteractionAffinityForTest(): void {
  interactionAffinityMemoryCache.clear()
}

export function setGeminiInteractionAffinityStateStoreForTest(store?: GeminiInteractionAffinityStateStore): void {
  interactionAffinityStateStoreForTest = store
}

export function geminiInteractionIdFromResponseBody(bodyText: string | undefined): string | undefined {
  const normalized = bodyText?.trim()
  if (!normalized) return undefined
  const jsonId = parseInteractionIdFromJson(normalized, true)
  if (jsonId) return jsonId
  for (const line of normalized.split(/\r?\n/)) {
    const trimmed = line.trim()
    if (!trimmed.toLowerCase().startsWith('data:')) continue
    const data = trimmed.slice('data:'.length).trim()
    if (!data || data === '[DONE]') continue
    const id = parseInteractionIdFromJson(data, false)
    if (id) return id
  }
  return undefined
}

export function geminiInteractionIdFromJsonPrefix(rawBody: Buffer): string | undefined {
  let index = skipJsonWhitespace(rawBody, 0)
  if (rawBody[index] !== 0x7b) return undefined
  index += 1

  while (index < rawBody.length) {
    index = skipJsonWhitespace(rawBody, index)
    if (rawBody[index] === 0x7d) return undefined
    if (rawBody[index] === 0x2c) {
      index += 1
      continue
    }
    const key = readJsonString(rawBody, index)
    if (!key) return undefined
    index = skipJsonWhitespace(rawBody, key.nextIndex)
    if (rawBody[index] !== 0x3a) return undefined
    index = skipJsonWhitespace(rawBody, index + 1)
    if (key.value === 'id') {
      const value = readJsonString(rawBody, index)
      return normalizeInteractionId(value?.value)
    }
    const nextIndex = skipJsonValue(rawBody, index)
    if (nextIndex === undefined) return undefined
    index = nextIndex
  }
  return undefined
}

function parseInteractionIdFromJson(value: string, allowRootId: boolean): string | undefined {
  try {
    const parsed = JSON.parse(value) as unknown
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return undefined
    const payload = parsed as Record<string, unknown>
    const interaction = payload.interaction
    if (interaction && typeof interaction === 'object' && !Array.isArray(interaction)) {
      const id = normalizeInteractionId((interaction as Record<string, unknown>).id)
      if (id) return id
    }
    const interactionId = normalizeInteractionId(payload.interaction_id)
    if (interactionId) return interactionId
    return allowRootId ? normalizeInteractionId(payload.id) : undefined
  } catch {
    return undefined
  }
}

function skipJsonWhitespace(rawBody: Buffer, index: number): number {
  while (index < rawBody.length && [0x20, 0x09, 0x0a, 0x0d].includes(rawBody[index] ?? -1)) {
    index += 1
  }
  return index
}

function readJsonString(rawBody: Buffer, index: number): { value: string; nextIndex: number } | undefined {
  if (rawBody[index] !== 0x22) return undefined
  let escaped = false
  for (let cursor = index + 1; cursor < rawBody.length; cursor += 1) {
    const byte = rawBody[cursor]
    if (escaped) {
      escaped = false
      continue
    }
    if (byte === 0x5c) {
      escaped = true
      continue
    }
    if (byte !== 0x22) continue
    const nextIndex = cursor + 1
    try {
      const value = JSON.parse(rawBody.toString('utf8', index, nextIndex)) as unknown
      return typeof value === 'string' ? { value, nextIndex } : undefined
    } catch {
      return undefined
    }
  }
  return undefined
}

function skipJsonValue(rawBody: Buffer, index: number): number | undefined {
  index = skipJsonWhitespace(rawBody, index)
  if (rawBody[index] === 0x22) return readJsonString(rawBody, index)?.nextIndex
  const firstByte = rawBody[index]
  if (firstByte !== 0x7b && firstByte !== 0x5b) {
    while (index < rawBody.length && ![0x2c, 0x7d, 0x5d].includes(rawBody[index] ?? -1)) {
      index += 1
    }
    return index
  }
  const stack = [firstByte]
  for (let cursor = index + 1; cursor < rawBody.length; cursor += 1) {
    const byte = rawBody[cursor]
    if (byte === 0x22) {
      const stringValue = readJsonString(rawBody, cursor)
      if (!stringValue) return undefined
      cursor = stringValue.nextIndex - 1
      continue
    }
    if (byte === 0x7b || byte === 0x5b) {
      stack.push(byte)
      continue
    }
    if (byte !== 0x7d && byte !== 0x5d) continue
    const openByte = stack.pop()
    if ((byte === 0x7d && openByte !== 0x7b) || (byte === 0x5d && openByte !== 0x5b)) return undefined
    if (stack.length === 0) return cursor + 1
  }
  return undefined
}

function normalizedRequestPath(req: Request): string {
  const rawPath = (req.originalUrl || req.path || '').split('?', 1)[0]
  const normalized = rawPath.startsWith('/') ? rawPath : `/${rawPath}`
  return normalized.replace(/^\/v1beta(?=\/|$)/i, '') || '/'
}

function geminiInteractionResourcePathMatch(req: Request): RegExpExecArray | undefined {
  const path = normalizedRequestPath(req)
  const method = req.method.toUpperCase()
  const match = /^\/interactions\/([^/]+)(?:\/cancel)?$/i.exec(path)
  if (!match?.[1]) return undefined
  const cancelPath = /\/cancel$/i.test(path)
  if (cancelPath ? method !== 'POST' : method !== 'GET' && method !== 'DELETE') return undefined
  return match
}

function decodePathSegment(value: string): string {
  try {
    return decodeURIComponent(value)
  } catch {
    return value
  }
}

function normalizeInteractionId(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const normalized = value.trim()
  if (!normalized || normalized.length > 512 || /[\u0000-\u001f\u007f/\\]/.test(normalized)) return undefined
  return normalized
}

function affinityKey(scope: GeminiInteractionAffinityScope, interactionId: string): string {
  const scopeDigest = createHash('sha256')
    .update(JSON.stringify({
      systemAccountId: scope.systemAccountId.trim(),
      apiKeyId: scope.apiKeyId?.trim() || `internal:${scope.groupId.trim()}`,
      interactionId
    }))
    .digest('hex')
  return `interaction:${scopeDigest}`
}

function isValidBinding(
  value: GeminiInteractionAffinityBinding | undefined,
  interactionId: string
): value is GeminiInteractionAffinityBinding {
  return Boolean(
    value
      && value.interactionId === interactionId
      && value.accountId?.trim()
      && value.groupId?.trim()
      && value.providerCode === GEMINI_PROVIDER_CODE
      && Number.isFinite(value.createdAtMs)
  )
}

async function setBinding(key: string, binding: GeminiInteractionAffinityBinding): Promise<void> {
  if (interactionAffinityStateStoreForTest) {
    await interactionAffinityStateStoreForTest.setJson(key, binding, interactionAffinityTtlMs)
    return
  }
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    await interactionAffinityStateStore.setJson(key, binding, interactionAffinityTtlMs)
    return
  }
  interactionAffinityMemoryCache.set(key, binding, { ttlMs: interactionAffinityTtlMs })
}

async function deleteBinding(key: string): Promise<void> {
  if (interactionAffinityStateStoreForTest) {
    await interactionAffinityStateStoreForTest.delete(key)
    return
  }
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    await interactionAffinityStateStore.delete(key)
    return
  }
  interactionAffinityMemoryCache.delete(key)
}

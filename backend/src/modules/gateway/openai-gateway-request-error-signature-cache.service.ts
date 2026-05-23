import { createHash } from 'node:crypto'
import type { Request } from 'express'

import { createAppCache } from '../../shared/cache.js'
import type { ClientVisibleUpstreamErrorResponse, UpstreamFailureSignature } from './openai-gateway-error-helpers.js'
import { headersToObject } from './openai-gateway-usage.js'
import type { GatewayRawBodyRequest } from './openai-gateway-request-body.js'

interface RequestErrorSignatureScope {
  systemAccountId: string
  groupId: string
  apiKeyId?: string
  clientIp?: string
  endpoint: string
}

export interface RequestErrorSignatureCacheHit {
  response: ClientVisibleUpstreamErrorResponse
  failureSignatureLabel: string
  confirmedAccountIds: string[]
  cachedAt: string
}

interface CachedRequestErrorSignatureEntry {
  key: string
  response: {
    statusCode: number
    headers: Record<string, string>
    body: Buffer
    bodyText: string
  }
  failureSignatureLabel: string
  confirmedAccountIds: string[]
  cachedAtMs: number
}

const requestErrorSignatureCache = createAppCache<string, CachedRequestErrorSignatureEntry>({
  name: 'gateway:request-error-signature-cache',
  max: 5_000,
  ttlMs: 5 * 60_000,
  updateAgeOnGet: false
})

const requestErrorSignatureCacheTtlMs = 30_000

export function inspectRequestErrorSignatureCache(
  req: Request,
  scope: RequestErrorSignatureScope
): RequestErrorSignatureCacheHit | undefined {
  const key = requestErrorSignatureCacheKey(req, scope)
  if (!key) {
    return undefined
  }
  const entry = requestErrorSignatureCache.get(key)
  if (!entry) {
    return undefined
  }
  return {
    response: {
      statusCode: entry.response.statusCode,
      headers: headersFromObject(entry.response.headers),
      body: Buffer.from(entry.response.body),
      bodyText: entry.response.bodyText
    },
    failureSignatureLabel: entry.failureSignatureLabel,
    confirmedAccountIds: entry.confirmedAccountIds,
    cachedAt: new Date(entry.cachedAtMs).toISOString()
  }
}

export function rememberRequestErrorSignatureCache(input: {
  req: Request
  scope: RequestErrorSignatureScope
  signature: UpstreamFailureSignature
  confirmedAccountIds: string[]
  response: ClientVisibleUpstreamErrorResponse
}): boolean {
  const key = requestErrorSignatureCacheKey(input.req, input.scope)
  if (!key) {
    return false
  }
  requestErrorSignatureCache.set(key, {
    key,
    response: {
      statusCode: input.response.statusCode,
      headers: headersToObject(input.response.headers),
      body: Buffer.from(input.response.body),
      bodyText: input.response.bodyText
    },
    failureSignatureLabel: input.signature.label,
    confirmedAccountIds: [...new Set(input.confirmedAccountIds)],
    cachedAtMs: Date.now()
  }, { ttlMs: requestErrorSignatureCacheTtlMs })
  return true
}

export function clearRequestErrorSignatureCacheForTest(): void {
  requestErrorSignatureCache.clear()
}

function requestErrorSignatureCacheKey(req: Request, scope: RequestErrorSignatureScope): string | undefined {
  const clientIp = scope.clientIp?.trim()
  if (!clientIp) {
    return undefined
  }
  const rawBody = (req as GatewayRawBodyRequest).rawBody
  if (!rawBody) {
    return undefined
  }
  return JSON.stringify({
    systemAccountId: scope.systemAccountId,
    apiKeyId: scope.apiKeyId?.trim() || 'internal',
    groupId: scope.groupId,
    clientIp,
    endpoint: scope.endpoint,
    bodyHash: createHash('sha256').update(rawBody).digest('hex')
  })
}

function headersFromObject(headers: Record<string, string>): Headers {
  const output = new Headers()
  for (const [name, value] of Object.entries(headers)) {
    output.set(name, value)
  }
  return output
}

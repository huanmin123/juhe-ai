import type { IncomingHttpHeaders } from 'node:http'
import type { Request } from 'express'

import {
  buildGatewayRequestBodySummary
} from '../request/body.js'
import { getGatewayRequestBodyState } from '../request/body.js'
import { normalizeUsageServiceTier, type UsageServiceTier } from './service-tier.js'
import { normalizeUsageReasoningEffort, type UsageReasoningEffort } from './reasoning-effort.js'
import {
  headersToSafeObject,
  sanitizeHeaderRecord,
  sanitizeStringHeaderRecord
} from '../upstream/headers.js'
import type { UpstreamAttempt } from '../upstream/attempt.js'
import type { CodexResponsesGuardUsageSummary } from '../codex-responses/response-guard.js'

export interface UsageRequestSnapshot {
  method: string
  path: string
  originalUrl: string
  clientIp?: string
  traceId: string
  requestedServiceTier?: UsageServiceTier
  requestedReasoningEffort?: UsageReasoningEffort
  headers: Record<string, string | string[]>
  body?: unknown
  bodyOmission?: unknown
}

export interface UsageResponseSnapshot {
  upstreamUrl?: string
  statusCode?: number
  headers?: Record<string, string>
  bodyText?: string
  bodyOmission?: unknown
  errorMessage?: string
  generatedBy?: 'gateway'
  codexResponsesGuard?: CodexResponsesGuardUsageSummary
  lastUpstreamAttempt?: {
    accountId: string
    accountName: string
    upstreamUrl: string
    statusCode?: number
    headers?: Record<string, string>
    bodyText?: string
    errorMessage?: string
  }
}

export function buildUsageRequestSnapshot(req: Request, traceId: string, clientIp?: string): UsageRequestSnapshot {
  const snapshot: UsageRequestSnapshot = {
    method: req.method,
    path: req.path,
    originalUrl: req.originalUrl,
    clientIp,
    traceId,
    requestedServiceTier: normalizeUsageServiceTier(
      getGatewayRequestBodyState(req)?.serviceTier
        ?? (typeof req.body === 'object' && req.body !== null ? (req.body as Record<string, unknown>).service_tier : undefined)
    ),
    requestedReasoningEffort: normalizeUsageReasoningEffort(
      getGatewayRequestBodyState(req)?.reasoningEffort
        ?? requestedReasoningEffortFromBody(req.body)
    ),
    headers: sanitizeRequestHeaders(req.headers)
  }
  const bodySummary = buildGatewayRequestBodySummary(req)
  if (bodySummary) {
    snapshot.body = bodySummary
  } else if (req.body !== undefined) {
    snapshot.body = req.body
  }
  return snapshot
}

function requestedReasoningEffortFromBody(body: unknown): unknown {
  if (typeof body !== 'object' || body === null || Array.isArray(body)) return undefined
  const record = body as Record<string, unknown>
  if (typeof record.reasoning === 'object' && record.reasoning !== null && !Array.isArray(record.reasoning)) {
    const nested = normalizeUsageReasoningEffort((record.reasoning as Record<string, unknown>).effort)
    if (nested) return nested
  }
  return record.reasoning_effort
}

export function sanitizeRequestHeaders(headers: IncomingHttpHeaders): Record<string, string | string[]> {
  const output: Record<string, string | string[]> = {}
  for (const [name, value] of Object.entries(headers)) {
    if (value === undefined) continue
    output[name] = value
  }
  return sanitizeHeaderRecord(output)
}

export function buildUsageResponseSnapshot(input: {
  upstreamUrl?: string
  statusCode?: number
  headers?: Headers | Record<string, string>
  bodyText?: string
  bodyOmission?: unknown
  errorMessage?: string
  generatedBy?: 'gateway'
  codexResponsesGuard?: CodexResponsesGuardUsageSummary
}): UsageResponseSnapshot {
  return {
    upstreamUrl: input.upstreamUrl,
    statusCode: input.statusCode,
    headers: input.headers instanceof Headers
      ? headersToSafeObject(input.headers)
      : input.headers ? sanitizeStringHeaderRecord(input.headers) : undefined,
    bodyText: input.bodyText,
    bodyOmission: input.bodyOmission,
    errorMessage: input.errorMessage,
    generatedBy: input.generatedBy,
    codexResponsesGuard: input.codexResponsesGuard
  }
}

export function buildGatewayErrorResponseSnapshot(
  statusCode: number,
  payload: Record<string, unknown>,
  lastAttempt?: UpstreamAttempt
): UsageResponseSnapshot {
  const snapshot = buildUsageResponseSnapshot({
    statusCode,
    headers: { 'content-type': 'application/json; charset=utf-8' },
    bodyText: JSON.stringify(payload),
    errorMessage: typeof payload.error === 'object' && payload.error !== null
      ? String((payload.error as Record<string, unknown>).message ?? '')
      : undefined,
    generatedBy: 'gateway'
  })

  if (lastAttempt) {
    snapshot.lastUpstreamAttempt = {
      accountId: lastAttempt.accountId,
      accountName: lastAttempt.accountName,
      upstreamUrl: lastAttempt.upstreamUrl,
      statusCode: lastAttempt.status,
      headers: lastAttempt.responseHeaders ? sanitizeStringHeaderRecord(lastAttempt.responseHeaders) : undefined,
      bodyText: lastAttempt.responseBodyText,
      errorMessage: lastAttempt.message
    }
  }

  return snapshot
}

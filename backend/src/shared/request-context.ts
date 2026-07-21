import { AsyncLocalStorage } from 'node:async_hooks'
import { randomUUID } from 'node:crypto'
import { isIP } from 'node:net'
import type { NextFunction, Request, Response } from 'express'
import type { Logger } from 'pino'

import type { SystemAccountRole } from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { LOG_EVENT_VERSION } from './logging/log-event-contract.js'
import { captureExpectedFailureContext, captureUnexpectedFailureContext } from './logging/log-failure-context.js'
import { logger } from './logger.js'

export interface RequestContext {
  traceId: string
  requestId?: string
  startedAt: number
  monotonicStartedAt?: number
  stageSequence?: number
  stageSummaries?: Array<{ sequence: number; stage: string; outcome: string; durationMs: number }>
  stageSummaryDropped?: number
  timingSummaryLogged?: boolean
  terminalExpectedFailure?: boolean
  method: string
  path: string
  originalUrl: string
  clientIp?: string
  systemAccountId?: string
  role?: SystemAccountRole
  apiKeyId?: string
  groupId?: string
  trafficSource?: string
  logger: Logger
}

export interface RequestContextFields {
  systemAccountId?: string
  role?: SystemAccountRole
  apiKeyId?: string
  groupId?: string
  trafficSource?: string
}

export const GATEWAY_REQUEST_STAGES = [
  'runtime_resolution',
  'body.admission',
  'body.speed_first_admission',
  'body.receive',
  'body.capture',
  'request.accepted',
  'route.group_access',
  'client.profile',
  'protocol.bridge',
  'account.load_candidates',
  'model.capability_filter',
  'account.session_affinity',
  'account.runtime_suppression',
  'account.latency_degradation',
  'account.proxy_health',
  'account.client_ip_avoidance',
  'account.codex_turn_avoidance',
  'quota.batch_decision',
  'capacity.account_snapshot',
  'capacity.client_ip_concurrency',
  'account.dispatch_candidates',
  'account.concurrency_acquire',
  'upstream.request_prepare',
  'upstream.fetch_headers',
  'upstream.dispatch.completed',
  'upstream.dispatch.failed',
  'upstream.first_output',
  'upstream.body.completed',
  'downstream.response.completed',
  'downstream.finish',
  'audit.finalize',
  'preflight.completed',
  'preflight.rejected',
  'preflight.failed'
] as const

export type GatewayRequestStage = typeof GATEWAY_REQUEST_STAGES[number]
export type GatewayRequestStageOutcome = 'success' | 'skipped' | 'expected_failure' | 'unexpected_failure' | 'aborted'

const requestContextStorage = new AsyncLocalStorage<RequestContext>()

export function createTraceId(): string {
  return randomUUID()
}

export function requestContextMiddleware(req: Request, res: Response, next: NextFunction): void {
  const traceId = normalizeTraceId(req) ?? createTraceId()
  const requestId = randomUUID()
  const clientIp = extractClientIp(req)
  const context: RequestContext = {
    traceId,
    requestId,
    startedAt: Date.now(),
    monotonicStartedAt: performance.now(),
    method: req.method,
    path: req.path,
    originalUrl: sanitizeUrlForLog(req.originalUrl),
    clientIp,
    logger: logger.child({ traceId, requestId })
  }

  res.setHeader('x-trace-id', traceId)

  requestContextStorage.run(context, () => {
    context.logger.info({
      event: 'http_request_started',
      version: LOG_EVENT_VERSION,
      service: 'juhe-ai',
      role: runtimeConfig.processRole,
      method: req.method,
      path: req.path,
      originalUrl: context.originalUrl,
      clientIp
    }, 'HTTP 请求开始')
    res.once('finish', () => logRequestFinished(req, res, context))
    res.once('close', () => {
      if (!res.writableEnded) {
        logRequestClosed(req, res, context)
      }
    })
    next()
  })
}

export function getRequestContext(): RequestContext | undefined {
  return requestContextStorage.getStore()
}

export function getRequestLogger(): Logger {
  return getRequestContext()?.logger ?? logger
}

export function bindRequestContextFields(fields: RequestContextFields): void {
  const context = getRequestContext()
  if (!context) {
    return
  }

  const nextFields = Object.fromEntries(
    Object.entries(fields).filter(([, value]) => value !== undefined)
  ) as RequestContextFields
  Object.assign(context, nextFields)
  context.logger = logger.child(requestContextLogBindings(context))
}

export function getTraceId(): string | undefined {
  return getRequestContext()?.traceId
}

export function getRequestId(): string | undefined {
  return getRequestContext()?.requestId
}

export function logRequestStage(
  stage: GatewayRequestStage,
  fields: Record<string, unknown> = {},
  outcome: GatewayRequestStageOutcome = 'success',
  stageStartedAt = performance.now()
): void {
  const context = getRequestContext()
  const endedAt = performance.now()
  const effectiveStageStartedAt = normalizeStageStartedAt(context, stageStartedAt)
  const stageFields = buildRequestStageLogFields(
    context,
    stage,
    fields,
    outcome,
    effectiveStageStartedAt,
    endedAt
  )
  const requestLogger = context?.logger ?? logger
  requestLogger.info(stageFields, '请求阶段完成')
  if (context) {
    context.stageSummaries ??= []
    if (context.stageSummaries.length < 64) {
      context.stageSummaries.push({
        sequence: Number(stageFields.sequence ?? context.stageSummaries.length + 1),
        stage,
        outcome,
        durationMs: Number(stageFields.durationMs ?? 0)
      })
    } else {
      context.stageSummaryDropped = (context.stageSummaryDropped ?? 0) + 1
    }
    if (outcome === 'expected_failure' && fields.terminalExpectedFailure === true) {
      context.terminalExpectedFailure = true
    }
  }
  if (outcome === 'unexpected_failure') {
    requestLogger.error({ ...stageFields, event: 'gateway.request.failure' }, '请求阶段发生未预期异常')
  }
}

function normalizeStageStartedAt(context: RequestContext | undefined, stageStartedAt: number): number {
  const requestStartedAt = context?.monotonicStartedAt
  if (requestStartedAt === undefined || !Number.isFinite(stageStartedAt)) {
    return requestStartedAt ?? stageStartedAt
  }

  // A wall-clock timestamp is several orders of magnitude away from performance.now().
  // Keep the event usable if a caller accidentally passes Date.now().
  return Math.abs(stageStartedAt - requestStartedAt) > 86_400_000
    ? requestStartedAt
    : stageStartedAt
}

export function buildRequestStageLogFields(
  context: RequestContext | undefined,
  stage: GatewayRequestStage,
  fields: Record<string, unknown>,
  outcome: GatewayRequestStageOutcome,
  stageStartedAt: number,
  endedAt: number
): Record<string, unknown> {
  const {
    error: failureError,
    failureReason,
    decisionInputs,
    stageSnapshot,
    queueSnapshot,
    retryState,
    terminalExpectedFailure: _terminalExpectedFailure,
    traceId: suppliedTraceIdValue,
    ...businessFields
  } = fields
  const suppliedTraceId = typeof suppliedTraceIdValue === 'string' ? suppliedTraceIdValue : undefined
  const sequence = context ? (context.stageSequence = (context.stageSequence ?? 0) + 1) : undefined
  const failureContext = outcome === 'unexpected_failure'
    ? captureUnexpectedFailureContext(
      failureError ?? new Error(`${stage} failed without original error context`),
      {
        stageSnapshot: recordValue(stageSnapshot) ?? {
          currentStage: stage,
          completedStages: context?.stageSummaries ?? [],
          currentFields: businessFields
        },
        queueSnapshot: recordValue(queueSnapshot),
        retryState: recordValue(retryState),
        decisionInputs: recordValue(decisionInputs)
      }
    )
    : outcome === 'expected_failure'
      ? captureExpectedFailureContext(
        typeof failureReason === 'string' && failureReason.trim() ? failureReason : stage,
        recordValue(decisionInputs) ?? businessFields
      )
      : outcome === 'aborted'
        ? { failureClass: 'aborted' as const }
        : undefined
  return {
    ...omitRequestContextLogFields(businessFields, context),
    event: 'gateway.request.stage',
    version: LOG_EVENT_VERSION,
    service: 'juhe-ai',
    role: runtimeConfig.processRole,
    ...(context ? {} : { traceId: suppliedTraceId }),
    stage,
    ...(sequence !== undefined ? { sequence } : {}),
    outcome,
    durationMs: Math.max(0, endedAt - stageStartedAt),
    ...(context ? {
      startedOffsetMs: Math.max(0, stageStartedAt - (context.monotonicStartedAt ?? stageStartedAt)),
      endedOffsetMs: Math.max(0, endedAt - (context.monotonicStartedAt ?? stageStartedAt))
    } : {}),
    ...(failureContext ?? {})
  }
}

function recordValue(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function requestContextLogBindings(context: RequestContext): Record<string, unknown> {
  return {
    traceId: context.traceId,
    requestId: context.requestId,
    systemAccountId: context.systemAccountId,
    systemAccountRole: context.role,
    apiKeyId: context.apiKeyId,
    groupId: context.groupId,
    trafficSource: context.trafficSource
  }
}

function omitRequestContextLogFields(
  fields: Record<string, unknown>,
  context: RequestContext | undefined
): Record<string, unknown> {
  if (!context) return fields
  const {
    requestId: _requestId,
    systemAccountId: _systemAccountId,
    systemAccountRole: _systemAccountRole,
    apiKeyId: _apiKeyId,
    groupId: _groupId,
    trafficSource: _trafficSource,
    ...remainingFields
  } = fields
  return remainingFields
}

export function withRequestContext<T>(context: RequestContext, handler: () => T): T {
  return requestContextStorage.run(context, handler)
}

export function extractClientIp(req: Request): string | undefined {
  return normalizeClientIp(req.ip) ?? normalizeClientIp(req.socket.remoteAddress)
}

function logRequestFinished(req: Request, res: Response, context: RequestContext): void {
  setImmediate(() => logRequestTimingSummary(context, res.statusCode, resolveRequestSummaryOutcome(context, res.statusCode)))
  const durationMs = Date.now() - context.startedAt
  const fields = {
    event: 'http_request_completed',
    method: req.method,
    path: req.path,
    originalUrl: sanitizeUrlForLog(req.originalUrl),
    statusCode: res.statusCode,
    durationMs,
    clientIp: context.clientIp,
    userAgent: req.header('user-agent')
  }

  if (isHealthPath(req.path) && res.statusCode < 400) {
    context.logger.debug(fields, 'HTTP 请求已结束')
    return
  }

  if (res.statusCode >= 500) {
    context.logger.error(fields, 'HTTP 请求已结束')
  } else if (res.statusCode >= 400) {
    context.logger.warn(fields, 'HTTP 请求已结束')
  } else {
    context.logger.info(fields, 'HTTP 请求已结束')
  }
}

function logRequestClosed(req: Request, res: Response, context: RequestContext): void {
  setImmediate(() => logRequestTimingSummary(context, res.statusCode, 'aborted'))
  context.logger.warn({
    event: 'http_request_closed',
    method: req.method,
    path: req.path,
    originalUrl: sanitizeUrlForLog(req.originalUrl),
    statusCode: res.statusCode,
    durationMs: Date.now() - context.startedAt,
    clientIp: context.clientIp,
    userAgent: req.header('user-agent')
  }, 'HTTP 请求在完成前关闭')
}

function logRequestTimingSummary(context: RequestContext, statusCode: number, outcome: GatewayRequestStageOutcome): void {
  if (context.timingSummaryLogged || !context.stageSummaries?.length) return
  context.timingSummaryLogged = true
  context.logger.info({
    event: 'gateway.request.timing_summary',
    version: LOG_EVENT_VERSION,
    service: 'juhe-ai',
    role: runtimeConfig.processRole,
    outcome,
    statusCode,
    durationMs: Math.max(0, performance.now() - (context.monotonicStartedAt ?? performance.now())),
    stageCount: context.stageSummaries.length,
    droppedStageSummaries: context.stageSummaryDropped ?? 0,
    stages: context.stageSummaries
  }, '网关请求阶段耗时汇总')
}

export function resolveRequestSummaryOutcome(context: RequestContext, statusCode: number): GatewayRequestStageOutcome {
  if (context.stageSummaries?.some((stage) => stage.outcome === 'unexpected_failure')) {
    return 'unexpected_failure'
  }
  if (statusCode >= 500) {
    return context.terminalExpectedFailure ? 'expected_failure' : 'unexpected_failure'
  }
  if (statusCode >= 400) {
    return 'expected_failure'
  }
  return 'success'
}

function normalizeTraceId(req: Request): string | undefined {
  const traceParent = parseTraceParent(req.header('traceparent'))
  if (traceParent) return traceParent
  return normalizeHeaderId(req.header('x-trace-id')) ?? normalizeHeaderId(req.header('x-correlation-id'))
}

export function parseTraceParent(value?: string): string | undefined {
  if (!value) return undefined
  const match = value.trim().match(/^([\da-f]{2})-([\da-f]{32})-([\da-f]{16})-([\da-f]{2})$/i)
  if (!match || match[1]?.toLowerCase() === 'ff') return undefined
  if (isAllZeroHex(match[2]) || isAllZeroHex(match[3])) return undefined
  return match[2]?.toLowerCase()
}

export function normalizeHeaderId(value?: string): string | undefined {
  const text = firstHeaderValue(value)?.trim()
  if (!text || text.length > 128 || !/^[A-Za-z0-9._:-]+$/.test(text)) return undefined
  return text
}

function isAllZeroHex(value: string | undefined): boolean {
  return Boolean(value && /^0+$/.test(value))
}

function firstHeaderValue(value?: string): string | undefined {
  return value?.split(',').map((item) => item.trim()).find(Boolean)
}

function normalizeClientIp(value?: string): string | undefined {
  if (!value) return undefined
  let ip = value.trim()
  if (!ip) return undefined
  if (ip.startsWith('[')) {
    const end = ip.indexOf(']')
    if (end > 0) ip = ip.slice(1, end)
  }
  if (/^\d{1,3}(?:\.\d{1,3}){3}:\d+$/.test(ip)) {
    ip = ip.replace(/:\d+$/, '')
  }
  if (ip.startsWith('::ffff:')) {
    ip = ip.slice('::ffff:'.length)
  }
  return isIP(ip) === 4 ? ip : undefined
}

function isHealthPath(path: string): boolean {
  return path === '/__aisys__/health' || path === '/__aisys__/api/health'
}

export function sanitizeUrlForLog(value: string): string {
  return value
}

export function sanitizeUrlCredentialsForLog(value?: string | null): string | undefined {
  const text = value?.trim()
  return text || undefined
}

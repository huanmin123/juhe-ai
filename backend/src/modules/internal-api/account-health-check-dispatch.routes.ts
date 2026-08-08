import { createHmac, timingSafeEqual } from 'node:crypto'
import { BlockList, isIP } from 'node:net'

import express, {
  Router,
  type Application,
  type NextFunction,
  type Request,
  type RequestHandler,
  type Response
} from 'express'

import { getRequestLogger, getTraceId } from '../../shared/request-context.js'
import { normalizeCodexSourceProbeFence, type CodexSourceProbeFence } from '../accounts/account-health-check-trigger.js'

export const accountHealthCheckDispatchSignatureDomain = 'juhe-ai:account-health-check-dispatch:v1\n'
export const accountHealthCheckDispatchInternalPrefix = '/__aiinternal__'

const accountHealthCheckDispatchRawBodyLimitBytes = 4096
const accountHealthCheckDispatchPath = '/v1/account-health-check/dispatch'
const signaturePattern = /^v1=([0-9a-f]{64})$/
const loopbackAddresses = new BlockList()

loopbackAddresses.addSubnet('127.0.0.0', 8, 'ipv4')
loopbackAddresses.addAddress('::1', 'ipv6')

export type AccountHealthCheckDispatchReason = 'activation' | 'configuration' | 'request_failure'

interface AccountHealthCheckDispatchQueueDetails {
  targetRole?: 'ops-worker'
  queueLength?: number
  queueBytes?: number
  messageBytes?: number
  maxQueueMessages?: number
  maxQueueBytes?: number
}

export type AccountHealthCheckDispatchOutcome = AccountHealthCheckDispatchQueueDetails & (
  | {
    outcome: 'queued'
    decisionCode: 'queued'
    targetRole: 'ops-worker'
  }
  | {
    outcome: 'coalesced'
    decisionCode: 'request_failure_cooldown'
    targetRole: 'ops-worker'
    cooldownRemainingMs: number
  }
  | {
    outcome: 'rejected'
    decisionCode: 'ops_ipc_message_limit' | 'ops_ipc_byte_limit' | 'ops_ipc_unavailable' | 'dispatch_rejected'
  }
)

export interface AccountHealthCheckDispatchRouterOptions {
  secret: string
  dispatch: (accountId: string, reason: AccountHealthCheckDispatchReason, traceId?: string, sourceFence?: CodexSourceProbeFence) => boolean | AccountHealthCheckDispatchOutcome
}

export interface AccountHealthCheckDispatchBridgeOptions extends AccountHealthCheckDispatchRouterOptions {
  corsMiddleware: RequestHandler
  compressionMiddleware: RequestHandler
}

type BodyParserError = Error & {
  status?: number
  statusCode?: number
  type?: string
}

export function createAccountHealthCheckDispatchSignature(secret: string, rawBody: Uint8Array): string {
  return `v1=${createHmac('sha256', secret)
    .update(accountHealthCheckDispatchSignatureDomain, 'utf8')
    .update(rawBody)
    .digest('hex')}`
}

export function isLoopbackRemoteAddress(remoteAddress: string | undefined): boolean {
  if (!remoteAddress) return false
  const version = isIP(remoteAddress)
  if (version === 0) return false
  return loopbackAddresses.check(remoteAddress, version === 4 ? 'ipv4' : 'ipv6')
}

export function mountAccountHealthCheckDispatchBridge(
  app: Application,
  options: AccountHealthCheckDispatchBridgeOptions
): void {
  app.use((req, res, next) => {
    if (isInternalApiPathIgnoringCase(req.path)) {
      next()
      return
    }
    options.corsMiddleware(req, res, next)
  })
  app.use(options.compressionMiddleware)
  app.use(accountHealthCheckDispatchInternalPrefix, createAccountHealthCheckDispatchRouter({
    secret: options.secret,
    dispatch: options.dispatch
  }))
}

export function createAccountHealthCheckDispatchRouter(
  options: AccountHealthCheckDispatchRouterOptions
): Router {
  const router = Router({ caseSensitive: true, strict: true })

  router.use((req, res, next) => {
    res.setHeader('Cache-Control', 'no-store')
    if (req.baseUrl !== accountHealthCheckDispatchInternalPrefix) {
      res.status(404).json({ message: '资源不存在' })
      return
    }
    next()
  })

  router.post(
    accountHealthCheckDispatchPath,
    requireLoopback,
    requireJsonContentType,
    requireIdentityContentEncoding,
    express.raw({
      type: () => true,
      limit: accountHealthCheckDispatchRawBodyLimitBytes,
      inflate: false
    }),
    handleAccountHealthCheckDispatchBodyParserError,
    (req: Request, res: Response, next: NextFunction) => handleDispatchRequest(req, res, next, options)
  )

  router.use((_req, res) => {
    res.status(404).json({ message: '资源不存在' })
  })

  return router
}

export function handleAccountHealthCheckDispatchBodyParserError(
  error: BodyParserError,
  req: Request,
  res: Response,
  next: NextFunction
): void {
  if (error.type === 'request.aborted') {
    return
  }
  const statusCode = accountHealthCheckDispatchClientErrorStatus(error)
  if (statusCode === undefined) {
    next(error)
    return
  }
  if (req.aborted || req.destroyed || res.destroyed || res.writableEnded || res.headersSent) {
    return
  }
  res.status(statusCode).json({
    message: statusCode === 413 ? '请求体过大' : '请求体无效'
  })
}

function accountHealthCheckDispatchClientErrorStatus(error: BodyParserError): number | undefined {
  if (error.type === 'entity.too.large') return 413
  for (const statusCode of [error.statusCode, error.status]) {
    if (Number.isInteger(statusCode) && Number(statusCode) >= 400 && Number(statusCode) <= 499) {
      return Number(statusCode)
    }
  }
  return undefined
}

function isInternalApiPathIgnoringCase(pathname: string): boolean {
  const normalizedPath = pathname.toLowerCase()
  return normalizedPath === accountHealthCheckDispatchInternalPrefix
    || normalizedPath.startsWith(`${accountHealthCheckDispatchInternalPrefix}/`)
}

function requireLoopback(req: Request, res: Response, next: NextFunction): void {
  if (!isLoopbackRemoteAddress(req.socket.remoteAddress)) {
    res.status(403).json({ message: '禁止访问' })
    return
  }
  next()
}

function requireJsonContentType(req: Request, res: Response, next: NextFunction): void {
  const contentType = req.headers['content-type']
  if (typeof contentType !== 'string') {
    res.status(415).json({ message: '仅支持 JSON 请求' })
    return
  }
  const mediaType = contentType.split(';', 1)[0]?.trim().toLowerCase()
  if (mediaType !== 'application/json' && !mediaType?.endsWith('+json')) {
    res.status(415).json({ message: '仅支持 JSON 请求' })
    return
  }
  next()
}

function requireIdentityContentEncoding(req: Request, res: Response, next: NextFunction): void {
  const contentEncoding = req.headers['content-encoding']
  if (
    contentEncoding !== undefined
    && (typeof contentEncoding !== 'string' || contentEncoding.trim().toLowerCase() !== 'identity')
  ) {
    res.status(415).json({ message: '不支持压缩请求体' })
    return
  }
  next()
}

function handleDispatchRequest(
  req: Request,
  res: Response,
  next: NextFunction,
  options: AccountHealthCheckDispatchRouterOptions
): void {
  const rawBody = Buffer.isBuffer(req.body) ? req.body : Buffer.alloc(0)
  if (!hasValidSignature(req, options.secret, rawBody)) {
    res.status(401).json({ message: '认证失败' })
    return
  }

  const payload = parseDispatchPayload(rawBody)
  if (!payload) {
    res.status(400).json({ message: '请求参数无效' })
    return
  }

  try {
    const outcome = normalizeDispatchOutcome(options.dispatch(payload.accountId, payload.reason, getTraceId(), payload.sourceFence))
    const statusCode = outcome.outcome === 'rejected' ? 503 : 202
    logDispatchOutcome(payload.reason, outcome, statusCode)
    if (statusCode === 503) {
      res.status(503).json({ message: '服务暂不可用' })
      return
    }
    res.status(202).end()
  } catch (error) {
    next(error)
  }
}

function normalizeDispatchOutcome(value: boolean | AccountHealthCheckDispatchOutcome): AccountHealthCheckDispatchOutcome {
  if (typeof value !== 'boolean') return value
  if (value) {
    return {
      outcome: 'queued',
      decisionCode: 'queued',
      targetRole: 'ops-worker'
    }
  }
  return {
    outcome: 'rejected',
    decisionCode: 'dispatch_rejected'
  }
}

function logDispatchOutcome(
  triggerReason: AccountHealthCheckDispatchReason,
  outcome: AccountHealthCheckDispatchOutcome,
  statusCode: number
): void {
  getRequestLogger().info({
    event: 'account_health_check_dispatch_decision',
    outcome: outcome.outcome,
    triggerReason,
    decisionCode: outcome.decisionCode,
    targetRole: outcome.targetRole ?? null,
    queueLength: outcome.queueLength ?? null,
    queueBytes: outcome.queueBytes ?? null,
    messageBytes: outcome.messageBytes ?? null,
    maxQueueMessages: outcome.maxQueueMessages ?? null,
    maxQueueBytes: outcome.maxQueueBytes ?? null,
    cooldownRemainingMs: outcome.outcome === 'coalesced' ? outcome.cooldownRemainingMs : null,
    statusCode
  }, '账户健康检查派发决策')
}

function hasValidSignature(req: Request, secret: string, rawBody: Buffer): boolean {
  const signature = req.headers['x-juhe-ai-signature']
  if (typeof signature !== 'string') return false
  const match = signaturePattern.exec(signature)
  if (!match) return false

  const providedDigest = Buffer.from(match[1]!, 'hex')
  const expectedDigest = Buffer.from(
    createAccountHealthCheckDispatchSignature(secret, rawBody).slice(3),
    'hex'
  )
  return timingSafeEqual(providedDigest, expectedDigest)
}

function parseDispatchPayload(rawBody: Buffer): {
  accountId: string
  reason: AccountHealthCheckDispatchReason
  sourceFence?: CodexSourceProbeFence
} | undefined {
  if (rawBody.length === 0) return undefined

  let parsed: unknown
  try {
    parsed = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(rawBody)) as unknown
  } catch {
    return undefined
  }

  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return undefined
  }

  const record = parsed as Record<string, unknown>
  const keys = Object.keys(record)
  if (
    (keys.length !== 2 && keys.length !== 3)
    || !Object.prototype.hasOwnProperty.call(record, 'accountId')
    || !Object.prototype.hasOwnProperty.call(record, 'reason')
    || (keys.length === 3 && !Object.prototype.hasOwnProperty.call(record, 'sourceFence'))
  ) {
    return undefined
  }

  if (typeof record.accountId !== 'string') return undefined
  const accountId = record.accountId.trim()
  if (!accountId) return undefined

  if (
    record.reason !== 'activation'
    && record.reason !== 'configuration'
    && record.reason !== 'request_failure'
  ) {
    return undefined
  }

  const sourceFence = record.sourceFence === undefined ? undefined : normalizeCodexSourceProbeFence(record.sourceFence)
  if (record.sourceFence !== undefined && !sourceFence) return undefined
  if (sourceFence && sourceFence.accountId !== accountId) return undefined

  return {
    accountId,
    reason: record.reason,
    ...(sourceFence ? { sourceFence } : {})
  }
}

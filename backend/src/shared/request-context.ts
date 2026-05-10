import { AsyncLocalStorage } from 'node:async_hooks'
import { randomUUID } from 'node:crypto'
import type { NextFunction, Request, Response } from 'express'
import type { Logger } from 'pino'

import { logger } from './logger.js'

export interface RequestContext {
  traceId: string
  startedAt: number
  method: string
  path: string
  originalUrl: string
  clientIp?: string
  systemAccountId?: string
  role?: 'admin' | 'user'
  apiKeyId?: string
  groupId?: string
  logger: Logger
}

export interface RequestContextFields {
  systemAccountId?: string
  role?: 'admin' | 'user'
  apiKeyId?: string
  groupId?: string
}

const requestContextStorage = new AsyncLocalStorage<RequestContext>()

export function createTraceId(): string {
  return randomUUID()
}

export function requestContextMiddleware(req: Request, res: Response, next: NextFunction): void {
  const traceId = normalizeTraceId(req) ?? createTraceId()
  const clientIp = extractClientIp(req)
  const contextLogger = logger.child({
    traceId
  })
  const context: RequestContext = {
    traceId,
    startedAt: Date.now(),
    method: req.method,
    path: req.path,
    originalUrl: sanitizeUrlForLog(req.originalUrl),
    clientIp,
    logger: contextLogger
  }

  res.setHeader('x-trace-id', traceId)

  requestContextStorage.run(context, () => {
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
  context.logger = context.logger.child(nextFields)
}

export function getTraceId(): string | undefined {
  return getRequestContext()?.traceId
}

export function withRequestContext<T>(context: RequestContext, handler: () => T): T {
  return requestContextStorage.run(context, handler)
}

export function extractClientIp(req: Request): string | undefined {
  const forwarded = firstHeaderValue(req.header('x-forwarded-for'))
  const realIp = firstHeaderValue(req.header('x-real-ip'))
  const cfIp = firstHeaderValue(req.header('cf-connecting-ip'))
  return normalizeClientIp(forwarded ?? realIp ?? cfIp ?? req.ip ?? req.socket.remoteAddress)
}

function logRequestFinished(req: Request, res: Response, context: RequestContext): void {
  const durationMs = Date.now() - context.startedAt
  const fields = {
    event: 'http_request_completed',
    method: req.method,
    path: req.path,
    originalUrl: sanitizeUrlForLog(req.originalUrl),
    statusCode: res.statusCode,
    durationMs,
    clientIp: context.clientIp,
    systemAccountId: context.systemAccountId,
    role: context.role,
    apiKeyId: context.apiKeyId,
    groupId: context.groupId,
    userAgent: req.header('user-agent')
  }

  if (isHealthPath(req.path) && res.statusCode < 400) {
    context.logger.debug(fields, 'HTTP 请求完成')
    return
  }

  if (res.statusCode >= 500) {
    context.logger.error(fields, 'HTTP 请求完成')
  } else if (res.statusCode >= 400) {
    context.logger.warn(fields, 'HTTP 请求完成')
  } else {
    context.logger.info(fields, 'HTTP 请求完成')
  }
}

function logRequestClosed(req: Request, res: Response, context: RequestContext): void {
  context.logger.warn({
    event: 'http_request_closed',
    method: req.method,
    path: req.path,
    originalUrl: sanitizeUrlForLog(req.originalUrl),
    statusCode: res.statusCode,
    durationMs: Date.now() - context.startedAt,
    clientIp: context.clientIp,
    systemAccountId: context.systemAccountId,
    role: context.role,
    apiKeyId: context.apiKeyId,
    groupId: context.groupId,
    userAgent: req.header('user-agent')
  }, 'HTTP 请求在完成前关闭')
}

function normalizeTraceId(req: Request): string | undefined {
  const traceParent = parseTraceParent(req.header('traceparent'))
  if (traceParent) return traceParent
  return normalizeHeaderId(req.header('x-trace-id')) ?? normalizeHeaderId(req.header('x-correlation-id'))
}

function parseTraceParent(value?: string): string | undefined {
  if (!value) return undefined
  const match = value.trim().match(/^[\da-f]{2}-([\da-f]{32})-[\da-f]{16}-[\da-f]{2}$/i)
  return match?.[1]
}

function normalizeHeaderId(value?: string): string | undefined {
  const text = firstHeaderValue(value)?.trim()
  if (!text) return undefined
  return text.length <= 128 ? text : text.slice(0, 128)
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
  return ip === '::1' ? '127.0.0.1' : ip
}

function isHealthPath(path: string): boolean {
  return path === '/health' || path === '/api/health'
}

export function sanitizeUrlForLog(value: string): string {
  const [path, queryString] = value.split('?', 2)
  if (!queryString) return path

  try {
    const params = new URLSearchParams(queryString)
    for (const name of [...params.keys()]) {
      if (sensitiveQueryNames.has(name.toLowerCase())) {
        params.set(name, '[redacted]')
      }
    }
    const sanitizedQuery = params.toString()
    return sanitizedQuery ? `${path}?${sanitizedQuery}` : path
  } catch {
    return path
  }
}

const sensitiveQueryNames = new Set([
  'api_key',
  'apikey',
  'authorization',
  'code',
  'cookie',
  'key',
  'keyword',
  'keywords',
  'password',
  'refresh_token',
  'secret',
  'session',
  'state',
  'token'
])

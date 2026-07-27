import type { NextFunction, Request, RequestHandler, Response } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { getRequestLogger, sanitizeUrlForLog } from '../../shared/request-context.js'

export const systemApiDbAccessModes = ['noDb', 'read', 'readWithSideEffect', 'write', 'longRead'] as const
export type SystemApiDbAccessMode = typeof systemApiDbAccessModes[number]

export const systemApiDbAccessModeLocalKey = 'systemApiDbAccessMode'
export const systemApiDbServiceMaxInFlight = runtimeConfig.systemApi.dbServiceMaxInFlight
export const systemApiLongReadMaxInFlight = Math.max(2, Math.min(8, Math.floor(systemApiDbServiceMaxInFlight / 8)))
export const systemApiLongReadDeadlineMs = 120_000

let systemApiDbServiceWriteInFlight = 0
let systemApiDbServiceLongReadInFlight = 0

interface SystemApiDbAccessRouteRule {
  methods: readonly string[]
  pattern: RegExp
  mode: SystemApiDbAccessMode
}

const explicitSystemApiDbAccessRules: readonly SystemApiDbAccessRouteRule[] = [
  { methods: ['GET'], pattern: /^\/health\/?$/, mode: 'noDb' },
  { methods: ['GET'], pattern: /^\/settings\/public\/?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/auth\/me\/?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/auth\/captcha\/?$/, mode: 'write' },
  { methods: ['POST'], pattern: /^\/auth\/login\/?$/, mode: 'write' },
  { methods: ['POST'], pattern: /^\/auth\/logout\/?$/, mode: 'write' },
  { methods: ['PATCH'], pattern: /^\/auth\/me\/?$/, mode: 'write' },
  { methods: ['POST'], pattern: /^\/auth\/change-password\/?$/, mode: 'write' },
  { methods: ['GET'], pattern: /^\/runtime-logs\/?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/runtime-logs\/facets\/?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/runtime-logs\/grep\/?$/, mode: 'longRead' },
  { methods: ['GET'], pattern: /^\/audit-logs\/search-hot\/?$/, mode: 'longRead' },
  { methods: ['GET'], pattern: /^\/announcements(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/(?:my-)?accounts(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/(?:my-)?groups(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/(?:my-)?route-strategies(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/(?:my-)?api-keys\/[^/]+\/secret\/?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/(?:my-)?api-keys(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/(?:my-)?authorization-options(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/(?:my-)?authorizations(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/(?:my-)?anthropic-oauth(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/(?:my-)?gemini-oauth(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/(?:my-)?grok-oauth(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/(?:my-)?openai-oauth(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/(?:my-)?usage-records\/?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/(?:my-)?model-checks(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/(?:my-)?stats(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/(?:my-)?operation-logs(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/providers(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/response-inspection-policies(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/proxies(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/public-api-logs(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/audit-logs(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/runtime-logs(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/ip-stats(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/external-integration-sources(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/table-monitor(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/settings(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/system-accounts(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/my-teams(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/system-teams(?:\/.*)?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/group\/list\/?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/route-strategy\/list\/?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/api-key\/list\/?$/, mode: 'read' },
  { methods: ['GET'], pattern: /^\/account\/list\/?$/, mode: 'read' },
  { methods: ['POST'], pattern: /^\/(?:my-)?accounts\/import\/preview\/?$/, mode: 'read' },
  { methods: ['POST'], pattern: /^\/(?:my-)?accounts\/import\/confirm\/?$/, mode: 'write' },
  { methods: ['POST'], pattern: /^\/(?:my-)?accounts\/export\/?$/, mode: 'write' }
]

export function systemApiDbAccessModeMiddleware(systemApiPrefix: string): RequestHandler {
  return (req, res, next) => {
    setSystemApiDbAccessMode(res, resolveSystemApiDbAccessMode(req, systemApiPrefix))
    next()
  }
}

export function resolveSystemApiDbAccessMode(req: Pick<Request, 'method' | 'path' | 'originalUrl'>, systemApiPrefix = ''): SystemApiDbAccessMode {
  const method = req.method.toUpperCase()
  const ruleMethod = method === 'HEAD' ? 'GET' : method
  const path = normalizeSystemApiPath(req.path || pathFromOriginalUrl(req.originalUrl), systemApiPrefix)
  const rule = explicitSystemApiDbAccessRules.find((item) => item.methods.includes(ruleMethod) && item.pattern.test(path))
  if (rule) return rule.mode

  // requireAuth touches the session by default, so unmarked authenticated GET routes are not pure reads.
  return method === 'GET' || method === 'HEAD' ? 'readWithSideEffect' : 'write'
}

export function systemApiDbServiceAdmissionControl(req: Request, res: Response, next: NextFunction): void {
  const mode = systemApiDbAccessModeFromResponse(res) ?? resolveSystemApiDbAccessMode(req)
  setSystemApiDbAccessMode(res, mode)

  if (mode === 'noDb' || mode === 'read') {
    next()
    return
  }

  if (mode === 'longRead') {
    applyLongReadDeadline(req, res)
    acquireSystemApiAdmissionSlot({
      req,
      res,
      next,
      mode,
      currentInFlight: () => systemApiDbServiceLongReadInFlight,
      maxInFlight: systemApiLongReadMaxInFlight,
      increment: () => {
        systemApiDbServiceLongReadInFlight += 1
      },
      decrement: () => {
        systemApiDbServiceLongReadInFlight = Math.max(0, systemApiDbServiceLongReadInFlight - 1)
      },
      event: 'system_api_db_service_long_read_rejected',
      message: 'DB service 系统 API 长读请求过多，已拒绝本次管理端请求',
      responseMessage: '系统管理接口长查询繁忙，请稍后重试',
      code: 'system_api_long_read_busy'
    })
    return
  }

  if (runtimeConfig.databaseDriver !== 'sqlite') {
    next()
    return
  }

  acquireSystemApiAdmissionSlot({
    req,
    res,
    next,
    mode,
    currentInFlight: () => systemApiDbServiceWriteInFlight,
    maxInFlight: systemApiDbServiceMaxInFlight,
    increment: () => {
      systemApiDbServiceWriteInFlight += 1
    },
    decrement: () => {
      systemApiDbServiceWriteInFlight = Math.max(0, systemApiDbServiceWriteInFlight - 1)
    },
    event: 'system_api_db_service_in_flight_rejected',
    message: 'DB service 系统 API 在途写入请求过多，已拒绝本次管理端请求',
    responseMessage: '系统管理接口繁忙，请稍后重试',
    code: 'system_api_busy'
  })
}

export function shouldTouchSessionForSystemApiRequest(res: Response): boolean {
  const mode = systemApiDbAccessModeFromResponse(res)
  return mode !== 'noDb' && mode !== 'read' && mode !== 'longRead'
}

export function systemApiDbAccessModeFromResponse(res: Response): SystemApiDbAccessMode | undefined {
  const value = res.locals[systemApiDbAccessModeLocalKey]
  return isSystemApiDbAccessMode(value) ? value : undefined
}

export function setSystemApiDbAccessMode(res: Response, mode: SystemApiDbAccessMode): void {
  res.locals[systemApiDbAccessModeLocalKey] = mode
}

export function isSystemApiDbAccessMode(value: unknown): value is SystemApiDbAccessMode {
  return typeof value === 'string' && (systemApiDbAccessModes as readonly string[]).includes(value)
}

export function clearSystemApiDbAccessAdmissionStateForTest(): void {
  systemApiDbServiceWriteInFlight = 0
  systemApiDbServiceLongReadInFlight = 0
}

export function setSystemApiDbAccessAdmissionStateForTest(input: { writeInFlight?: number; longReadInFlight?: number }): void {
  if (input.writeInFlight !== undefined) {
    systemApiDbServiceWriteInFlight = Math.max(0, Math.floor(input.writeInFlight))
  }
  if (input.longReadInFlight !== undefined) {
    systemApiDbServiceLongReadInFlight = Math.max(0, Math.floor(input.longReadInFlight))
  }
}

function acquireSystemApiAdmissionSlot(options: {
  req: Request
  res: Response
  next: NextFunction
  mode: SystemApiDbAccessMode
  currentInFlight: () => number
  maxInFlight: number
  increment: () => void
  decrement: () => void
  event: string
  message: string
  responseMessage: string
  code: string
}): void {
  if (options.currentInFlight() >= options.maxInFlight) {
    getRequestLogger().warn({
      event: options.event,
      method: options.req.method,
      path: options.req.path,
      originalUrl: sanitizeUrlForLog(options.req.originalUrl),
      accessMode: options.mode,
      inFlight: options.currentInFlight(),
      maxInFlight: options.maxInFlight
    }, options.message)
    options.res.setHeader('Retry-After', '1')
    options.res.status(503).json({
      message: options.responseMessage,
      code: options.code
    })
    return
  }

  options.increment()
  let released = false
  const release = () => {
    if (released) return
    released = true
    options.decrement()
    options.res.off('finish', release)
    options.res.off('close', release)
  }
  options.res.once('finish', release)
  options.res.once('close', release)
  options.next()
}

function applyLongReadDeadline(req: Request, res: Response): void {
  req.setTimeout(systemApiLongReadDeadlineMs)
  res.setTimeout(systemApiLongReadDeadlineMs)
}

function normalizeSystemApiPath(path: string, systemApiPrefix: string): string {
  const normalizedPath = path.startsWith('/') ? path : `/${path}`
  if (!systemApiPrefix) return normalizedPath
  const normalizedPrefix = systemApiPrefix.endsWith('/') ? systemApiPrefix.slice(0, -1) : systemApiPrefix
  if (normalizedPath === normalizedPrefix) return '/'
  if (normalizedPath.startsWith(`${normalizedPrefix}/`)) {
    return normalizedPath.slice(normalizedPrefix.length) || '/'
  }
  return normalizedPath
}

function pathFromOriginalUrl(originalUrl: string): string {
  return originalUrl.split('?', 1)[0] || '/'
}

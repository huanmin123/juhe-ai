import http from 'node:http'
import type { IncomingHttpHeaders } from 'node:http'

import type { NextFunction, Request, Response } from 'express'

import { errorLogFields, logger } from '../../shared/logger.js'
import { getTraceId, sanitizeUrlForLog } from '../../shared/request-context.js'
import { getDbServiceState } from './db-service-ipc.js'

export const dbServiceHttpProxyMaxInFlight = 256
export const dbServiceHttpProxyTimeoutMs = 30_000

export interface DbServiceHttpProxyOptions {
  maxInFlight?: number
  timeoutMs?: number
}

const hopByHopHeaders = new Set([
  'connection',
  'keep-alive',
  'proxy-authenticate',
  'proxy-authorization',
  'te',
  'trailer',
  'transfer-encoding',
  'upgrade'
])

export function createDbServiceHttpProxy(options: DbServiceHttpProxyOptions = {}): (req: Request, res: Response, next: NextFunction) => void {
  const maxInFlight = positiveIntegerOrDefault(options.maxInFlight, dbServiceHttpProxyMaxInFlight)
  const timeoutMs = positiveIntegerOrDefault(options.timeoutMs, dbServiceHttpProxyTimeoutMs)
  let inFlight = 0

  return (req, res, _next) => {
    const endpoint = resolveDbServiceHttpEndpoint()
    if (!endpoint) {
      sendDbServiceUnavailable(res)
      return
    }
    if (inFlight >= maxInFlight) {
      logger.warn({
        event: 'db_service_http_proxy_overloaded',
        method: req.method,
        path: req.path,
        originalUrl: sanitizeUrlForLog(req.originalUrl),
        inFlight,
        maxInFlight
      }, 'DB service 内部 HTTP 代理并发已满')
      sendDbServiceProxyOverloaded(res)
      return
    }

    inFlight += 1
    let released = false
    let timedOut = false
    const release = (): void => {
      if (released) return
      released = true
      inFlight = Math.max(0, inFlight - 1)
    }

    const upstream = http.request({
      host: endpoint.host,
      port: endpoint.port,
      method: req.method,
      path: req.originalUrl,
      headers: buildProxyRequestHeaders(req, endpoint)
    }, (upstreamResponse) => {
      res.statusCode = upstreamResponse.statusCode ?? 502
      for (const [name, value] of Object.entries(upstreamResponse.headers)) {
        if (value === undefined || hopByHopHeaders.has(name.toLowerCase())) {
          continue
        }
        res.setHeader(name, value)
      }
      upstreamResponse.pipe(res)
    })
    upstream.setTimeout(timeoutMs, () => {
      timedOut = true
      logger.warn({
        event: 'db_service_http_proxy_timeout',
        method: req.method,
        path: req.path,
        originalUrl: sanitizeUrlForLog(req.originalUrl),
        dbServiceHost: endpoint.host,
        dbServicePort: endpoint.port,
        timeoutMs
      }, 'DB service 内部 HTTP 代理超时')
      upstream.destroy()
      if (!res.headersSent) {
        sendDbServiceProxyTimedOut(res)
        return
      }
      res.end()
    })

    upstream.on('error', (error) => {
      if (timedOut) {
        if (!res.headersSent) {
          sendDbServiceProxyTimedOut(res)
          return
        }
        res.end()
        return
      }
      logger.warn(errorLogFields(error, {
        event: 'db_service_http_proxy_failed',
        method: req.method,
        path: req.path,
        originalUrl: sanitizeUrlForLog(req.originalUrl),
        dbServiceHost: endpoint.host,
        dbServicePort: endpoint.port
      }), 'DB service 内部 HTTP 代理失败')
      if (!res.headersSent) {
        sendDbServiceUnavailable(res)
        return
      }
      res.end()
    })

    req.on('aborted', () => {
      upstream.destroy()
    })
    res.on('close', () => {
      if (!res.writableEnded) {
        upstream.destroy()
      }
      release()
    })

    req.pipe(upstream)
  }
}

function resolveDbServiceHttpEndpoint(): { host: string; port: number } | undefined {
  const state = getDbServiceState()
  if (!state.ready || !state.httpHost || !state.httpPort) {
    return undefined
  }
  return {
    host: state.httpHost,
    port: state.httpPort
  }
}

function buildProxyRequestHeaders(req: Request, endpoint: { host: string; port: number }): IncomingHttpHeaders {
  const headers: IncomingHttpHeaders = {}
  for (const [name, value] of Object.entries(req.headers)) {
    if (value === undefined || hopByHopHeaders.has(name.toLowerCase())) {
      continue
    }
    headers[name] = value
  }

  headers.host = `${endpoint.host}:${endpoint.port}`
  headers['x-forwarded-host'] = req.headers.host
  headers['x-forwarded-proto'] = req.protocol
  headers['x-forwarded-for'] = appendForwardedFor(req)
  const traceId = getTraceId()
  if (traceId) {
    headers['x-trace-id'] = traceId
  }
  return headers
}

function appendForwardedFor(req: Request): string | undefined {
  const current = headerText(req.headers['x-forwarded-for'])
  const remoteAddress = req.ip || req.socket.remoteAddress
  if (!remoteAddress) {
    return current
  }
  return current ? `${current}, ${remoteAddress}` : remoteAddress
}

function headerText(value: string | string[] | undefined): string | undefined {
  if (Array.isArray(value)) {
    return value.join(', ')
  }
  return value
}

function positiveIntegerOrDefault(value: number | undefined, fallback: number): number {
  return typeof value === 'number' && Number.isInteger(value) && value > 0 ? value : fallback
}

function sendDbServiceUnavailable(res: Response): void {
  res.status(503).json({
    message: '本地数据库服务未就绪或内部系统接口不可用，请稍后重试'
  })
}

function sendDbServiceProxyOverloaded(res: Response): void {
  res.setHeader('Retry-After', '1')
  res.status(503).json({
    message: '本地数据库服务繁忙，请稍后重试'
  })
}

function sendDbServiceProxyTimedOut(res: Response): void {
  res.status(504).json({
    message: '本地数据库服务响应超时，请稍后重试'
  })
}

import { createHmac, createHash, randomUUID, timingSafeEqual } from 'node:crypto'
import http, { type IncomingHttpHeaders } from 'node:http'

import type { NextFunction, Request, Response } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { getTraceId, sanitizeUrlForLog } from '../../shared/request-context.js'
import { systemApiDbAccessModeFromResponse } from './system-api-db-access.js'

const grantHeader = 'x-juhe-control-read-grant'
const proxyHeader = 'x-juhe-control-read-proxy'
const grantVersion = 'v1'
const grantLifetimeMs = 10_000
const maxConsumedGrants = 10_000
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

let nextReplicaIndex = 0
const consumedGrants = new Map<string, number>()

/**
 * Runs only on control-1 after both management API rate limits have committed.
 * The replica receives a short-lived, request-bound grant and still performs
 * the normal authentication and authorization checks locally.
 */
export function controlReadReplicaProxy(req: Request, res: Response, next: NextFunction): void {
  if (!canForwardRead(req, res)) {
    next()
    return
  }

  const origin = nextReplicaOrigin()
  if (!origin) {
    next()
    return
  }
  const target = new URL(origin)
  const grant = createReadGrant(req, target.port)
  let connected = false
  let released = false
  const release = () => {
    if (released) return
    released = true
  }
  const upstream = http.request({
    host: target.hostname,
    port: Number(target.port),
    method: req.method,
    path: req.originalUrl,
    headers: replicaRequestHeaders(req, target, grant)
  }, (upstreamResponse) => {
    connected = true
    res.statusCode = upstreamResponse.statusCode ?? 502
    for (const [name, value] of Object.entries(upstreamResponse.headers)) {
      if (value === undefined || hopByHopHeaders.has(name.toLowerCase())) continue
      res.setHeader(name, value)
    }
    upstreamResponse.pipe(res)
    upstreamResponse.once('close', release)
  })
  upstream.setTimeout(120_000, () => {
    upstream.destroy(new Error('管理读副本响应超时'))
  })
  upstream.once('error', (error) => {
    release()
    if (!connected && !res.headersSent) {
      logger.warn(errorLogFields(error, {
        event: 'control_read_replica_unavailable',
        method: req.method,
        originalUrl: sanitizeUrlForLog(req.originalUrl),
        origin
      }), '管理读副本不可用，本次请求回退主 control 执行')
      next()
      return
    }
    logger.warn(errorLogFields(error, {
      event: 'control_read_replica_proxy_failed',
      method: req.method,
      originalUrl: sanitizeUrlForLog(req.originalUrl),
      origin
    }), '管理读副本转发中断')
    res.end()
  })
  req.once('aborted', () => upstream.destroy())
  res.once('close', () => {
    if (!res.writableEnded) upstream.destroy()
    release()
  })
  upstream.end()
}

export function isAuthorizedControlReadReplicaRequest(req: Request): boolean {
  if (
    runtimeConfig.runtimeMode !== 'performance'
    || runtimeConfig.performanceNodeRole !== 'control-replica'
    || req.headers[proxyHeader] !== '1'
  ) {
    return false
  }
  const grant = headerText(req.headers[grantHeader])
  if (!grant) return false
  const [version, issuedAtText, nonce, signature] = grant.split('.')
  const issuedAtMs = Number(issuedAtText)
  if (
    version !== grantVersion
    || !nonce
    || !signature
    || !Number.isSafeInteger(issuedAtMs)
    || Math.abs(Date.now() - issuedAtMs) > grantLifetimeMs
  ) {
    return false
  }
  const expected = signGrant(issuedAtMs, nonce, controlReplicaAudience(), req)
  const expectedBytes = Buffer.from(expected)
  const actualBytes = Buffer.from(signature)
  if (expectedBytes.length !== actualBytes.length || !timingSafeEqual(expectedBytes, actualBytes)) {
    return false
  }
  return consumeGrant(nonce, issuedAtMs)
}

/**
 * A replica is deliberately not a second management writer.  Nginx never
 * exposes it directly, but this guard also protects the loopback listener
 * from an accidental direct request and prevents it from consuming limiter
 * state before the request is rejected.
 */
export function controlReadReplicaRequestGuard(req: Request, res: Response, next: NextFunction): void {
  if (runtimeConfig.runtimeMode !== 'performance' || runtimeConfig.performanceNodeRole !== 'control-replica') {
    next()
    return
  }
  const mode = systemApiDbAccessModeFromResponse(res)
  if (mode === 'noDb' || ((mode === 'read' || mode === 'longRead') && isAuthorizedControlReadReplicaRequest(req))) {
    next()
    return
  }
  res.status(503).json({
    message: '当前管理副本只接受由主 control 授权的只读请求',
    code: 'control_read_replica_write_rejected'
  })
}

/**
 * Public/delegated OAuth endpoints are always primary-owned. They do not use
 * the signed management-read transport, so a replica must reject them even
 * when somebody reaches its loopback listener directly.
 */
export function controlReadReplicaPrimaryOnlyRequestGuard(_req: Request, res: Response, next: NextFunction): void {
  if (runtimeConfig.runtimeMode !== 'performance' || runtimeConfig.performanceNodeRole !== 'control-replica') {
    next()
    return
  }
  res.status(503).json({
    message: '当前管理副本不承接公开或委派接口请求',
    code: 'control_read_replica_primary_only_rejected'
  })
}

function canForwardRead(req: Request, res: Response): boolean {
  const mode = systemApiDbAccessModeFromResponse(res)
  return runtimeConfig.runtimeMode === 'performance'
    && runtimeConfig.performanceNodeRole === 'control'
    && runtimeConfig.controlReadReplicaOrigins.length > 0
    && (req.method === 'GET' || req.method === 'HEAD')
    && (mode === 'read' || mode === 'longRead')
    && !headerText(req.headers[grantHeader])
}

function nextReplicaOrigin(): string | undefined {
  const origins = runtimeConfig.controlReadReplicaOrigins
  if (!origins.length) return undefined
  const origin = origins[nextReplicaIndex % origins.length]
  nextReplicaIndex = (nextReplicaIndex + 1) % Number.MAX_SAFE_INTEGER
  return origin
}

function createReadGrant(req: Request, audience: string): string {
  const issuedAtMs = Date.now()
  const nonce = randomUUID()
  return `${grantVersion}.${issuedAtMs}.${nonce}.${signGrant(issuedAtMs, nonce, audience, req)}`
}

function signGrant(
  issuedAtMs: number,
  nonce: string,
  audience: string,
  req: Pick<Request, 'method' | 'originalUrl' | 'headers'>
): string {
  return createHmac('sha256', runtimeConfig.secret)
    .update('juhe-ai/control-read-replica/v1')
    .update('\n')
    .update(String(issuedAtMs))
    .update('\n')
    .update(nonce)
    .update('\n')
    .update(audience)
    .update('\n')
    .update(req.method)
    .update('\n')
    .update(req.originalUrl)
    .update('\n')
    .update(requestCredentialHash(req))
    .digest('base64url')
}

function controlReplicaAudience(): string {
  // Replica Origins are required to be loopback HTTP endpoints. The listening
  // port therefore identifies the target control replica without trusting an
  // attacker-controlled Host header.
  return String(runtimeConfig.port)
}

function consumeGrant(nonce: string, issuedAtMs: number): boolean {
  const now = Date.now()
  for (const [knownNonce, expiresAtMs] of consumedGrants) {
    if (expiresAtMs <= now) consumedGrants.delete(knownNonce)
  }
  if (consumedGrants.has(nonce) || consumedGrants.size >= maxConsumedGrants) return false
  consumedGrants.set(nonce, issuedAtMs + grantLifetimeMs)
  return true
}

function requestCredentialHash(req: Pick<Request, 'headers'>): string {
  return createHash('sha256')
    .update(headerText(req.headers.authorization) ?? '')
    .update('\n')
    .update(headerText(req.headers.cookie) ?? '')
    .digest('base64url')
}

function replicaRequestHeaders(req: Request, target: URL, grant: string): IncomingHttpHeaders {
  const headers: IncomingHttpHeaders = {}
  for (const [name, value] of Object.entries(req.headers)) {
    if (value === undefined || hopByHopHeaders.has(name.toLowerCase()) || name.toLowerCase() === grantHeader || name.toLowerCase() === proxyHeader) continue
    headers[name] = value
  }
  headers.host = target.host
  headers[grantHeader] = grant
  headers[proxyHeader] = '1'
  const traceId = getTraceId()
  if (traceId) headers['x-trace-id'] = traceId
  return headers
}

function headerText(value: string | string[] | undefined): string | undefined {
  return Array.isArray(value) ? value.join(', ') : value
}

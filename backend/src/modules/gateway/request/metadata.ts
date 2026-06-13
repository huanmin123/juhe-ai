import { isIP } from 'node:net'
import type { Request } from 'express'

import { getGatewayRequestBodyState } from './body.js'

export function extractBearerToken(authorization?: string): string | undefined {
  if (!authorization) return undefined
  const match = authorization.match(/^Bearer\s+(.+)$/i)
  return match?.[1]?.trim()
}

export function extractClientIp(req: Request): string | undefined {
  return normalizeClientIp(req.ip) ?? normalizeClientIp(req.socket.remoteAddress)
}

export function requestModel(req: Request): string | undefined {
  const bodyState = getGatewayRequestBodyState(req)
  return bodyState?.model ?? (typeof req.body?.model === 'string' ? req.body.model : undefined)
}

export function requestStream(req: Request): boolean {
  const bodyState = getGatewayRequestBodyState(req)
  return bodyState?.stream ?? req.body?.stream === true
}

export function requestEndpoint(req: Request): string {
  return `${req.method.toUpperCase()} ${req.originalUrl.split('?')[0] || req.path}`
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

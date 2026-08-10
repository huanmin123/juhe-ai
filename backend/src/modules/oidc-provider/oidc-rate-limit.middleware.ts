import type { NextFunction, Request, Response } from 'express'

import { getRequestContext } from '../../shared/request-context.js'
import {
  clearPenaltyWindowRateLimitStore,
  consumePenaltyWindowRateLimitAsync,
  createPenaltyWindowRateLimitStore,
  type PenaltyWindowRateLimitRule
} from '../rate-limit/penalty-window-rate-limit.js'

const oauthRateLimitStore = createPenaltyWindowRateLimitStore({
  name: 'oauth_protocol_public',
  maxEntries: 50_000,
  maxIdleMs: 60 * 60_000,
  maxPenaltyMs: 5 * 60_000,
  penaltyMode: 'fixed_window'
})

const protocolRules: Record<string, readonly PenaltyWindowRateLimitRule[]> = {
  token: [{ windowSeconds: 60, maxRequests: 30 }],
  decision: [{ windowSeconds: 60, maxRequests: 30 }],
  authorize: [{ windowSeconds: 60, maxRequests: 120 }],
  userinfo: [{ windowSeconds: 60, maxRequests: 120 }],
  discovery: [{ windowSeconds: 60, maxRequests: 240 }],
  delegated: [{ windowSeconds: 60, maxRequests: 300 }]
}

export async function oauthProtocolRateLimit(req: Request, res: Response, next: NextFunction): Promise<void> {
  await enforceRateLimit(req, res, next, endpointClass(req))
}

export async function delegatedOAuthRateLimit(req: Request, res: Response, next: NextFunction): Promise<void> {
  await enforceRateLimit(req, res, next, 'delegated')
}

export function clearOidcProtocolRateLimitStateForTest(): void {
  clearPenaltyWindowRateLimitStore(oauthRateLimitStore)
}

function endpointClass(req: Request): string {
  if (req.path === '/oauth/token' || req.path === '/oauth/token/renew' || req.path === '/oauth/revoke') return 'token'
  if (req.path === '/oauth/authorize/decision' || req.path === '/oauth/device/decision') return 'decision'
  if (req.path === '/oauth/authorize' || req.path === '/oauth/device' || req.path === '/oauth/device_authorization') return 'authorize'
  if (req.path === '/oauth/userinfo') return 'userinfo'
  return 'discovery'
}

async function enforceRateLimit(req: Request, res: Response, next: NextFunction, endpoint: string): Promise<void> {
  try {
    const decision = await consumePenaltyWindowRateLimitAsync({
      store: oauthRateLimitStore,
      scopeKey: `${endpoint}:${getRequestContext()?.clientIp ?? req.ip ?? req.socket.remoteAddress ?? 'unknown'}`,
      rules: protocolRules[endpoint] ?? protocolRules.discovery
    })
    if (!decision.allowed) {
      const retryAfter = decision.retryAfterSeconds ?? 1
      res.setHeader('Retry-After', String(retryAfter))
      res.status(429).json({
        error: endpoint === 'token' ? 'slow_down' : 'rate_limited',
        error_description: 'OAuth 请求过于频繁，请稍后重试'
      })
      return
    }
    next()
  } catch (error) {
    next(error)
  }
}

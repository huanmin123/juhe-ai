import type { NextFunction, Request, Response } from 'express'

import { validateExternalIntegrationSourceToken } from '../../storage/external-integration-source-auth.repository.js'
import {
  type ExternalIntegrationSourceAuthContext,
  type ExternalIntegrationRateLimitRule
} from '../../storage/external-integration-source-types.js'

const rateLimitStates = new Map<string, { windowStartedAt: number; count: number }>()
const rateLimitCleanupThreshold = 1000
const rateLimitCleanupScanLimit = 256
const rateLimitStateMaxAgeMs = 86_400_000

export function requireExternalIntegrationSource(requiredScope?: string) {
  return (req: Request, res: Response, next: NextFunction): void => {
    const token = parseBearerToken(req.header('Authorization'))
    if (!token) {
      res.status(401).json({
        message: '缺少来源系统 token',
        code: 'external_source_token_missing'
      })
      return
    }

    const result = validateExternalIntegrationSourceToken({ token, requiredScope })
    if (!result.ok) {
      if (result.context) {
        res.locals.externalIntegrationSource = result.context
      }
      res.status(result.statusCode).json({
        message: result.message,
        code: result.code
      })
      return
    }

    const rateLimit = consumeExternalSourceRateLimit(result.context)
    if (!rateLimit.allowed) {
      res.locals.externalIntegrationSource = result.context
      res.setHeader('Retry-After', String(rateLimit.retryAfterSeconds))
      res.status(429).json({
        message: '来源系统调用过于频繁，请稍后重试',
        code: 'external_source_rate_limited',
        details: {
          windowSeconds: rateLimit.rule.windowSeconds,
          maxRequests: rateLimit.rule.maxRequests,
          retryAfterSeconds: rateLimit.retryAfterSeconds
        }
      })
      return
    }

    res.locals.externalIntegrationSource = result.context
    next()
  }
}

export function getExternalIntegrationSourceContext(res: Response): ExternalIntegrationSourceAuthContext {
  const context = res.locals.externalIntegrationSource as ExternalIntegrationSourceAuthContext | undefined
  if (!context) {
    throw new Error('缺少外部来源系统认证上下文')
  }
  return context
}

function parseBearerToken(value: string | undefined): string | undefined {
  if (!value) {
    return undefined
  }
  const match = /^Bearer\s+(.+)$/i.exec(value.trim())
  const token = match?.[1]?.trim()
  return token ? token : undefined
}

function consumeExternalSourceRateLimit(context: ExternalIntegrationSourceAuthContext): { allowed: true } | { allowed: false; rule: ExternalIntegrationRateLimitRule; retryAfterSeconds: number } {
  if (!context.rateLimits.length) {
    return { allowed: true }
  }
  const now = Date.now()
  for (const rule of context.rateLimits) {
    const windowMs = rule.windowSeconds * 1000
    const windowStartedAt = Math.floor(now / windowMs) * windowMs
    const key = `${context.sourceRefId}:${context.tokenId}:${context.tokenPrefix}:${rule.windowSeconds}`
    const state = rateLimitStates.get(key)
    if (!state || state.windowStartedAt !== windowStartedAt) {
      rateLimitStates.set(key, { windowStartedAt, count: 1 })
      continue
    }
    if (state.count >= rule.maxRequests) {
      return {
        allowed: false,
        rule,
        retryAfterSeconds: Math.max(1, Math.ceil((windowStartedAt + windowMs - now) / 1000))
      }
    }
    state.count += 1
  }
  cleanupRateLimitStates(now)
  return { allowed: true }
}

function cleanupRateLimitStates(now: number): void {
  if (rateLimitStates.size < rateLimitCleanupThreshold) {
    return
  }
  let scanned = 0
  for (const [key, state] of rateLimitStates) {
    if (now - state.windowStartedAt > rateLimitStateMaxAgeMs) {
      rateLimitStates.delete(key)
    } else {
      rateLimitStates.delete(key)
      rateLimitStates.set(key, state)
    }
    scanned += 1
    if (scanned >= rateLimitCleanupScanLimit) {
      break
    }
  }
}

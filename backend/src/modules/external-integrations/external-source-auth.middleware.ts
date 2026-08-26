import type { NextFunction, Request, Response } from 'express'

import { validateExternalIntegrationSourceTokenAsync } from '../../storage/external-integration-source-auth.repository.js'
import {
  type ExternalIntegrationSourceAuthContext,
  type ExternalIntegrationRateLimitRule
} from '../../storage/external-integration-source-types.js'
import {
  clearPenaltyWindowRateLimitStore,
  consumePenaltyWindowRateLimitAsync,
  createPenaltyWindowRateLimitStore
} from '../rate-limit/penalty-window-rate-limit.js'

const externalSourceRateLimitStore = createPenaltyWindowRateLimitStore({
  name: 'external_source_public_api',
  maxEntries: 20_000,
  maxIdleMs: 86_400_000,
  maxPenaltyMs: 15 * 60_000
})

export function requireExternalIntegrationSource(requiredScope?: string) {
  return async (req: Request, res: Response, next: NextFunction): Promise<void> => {
    try {
      const token = parseBearerToken(req.header('Authorization'))
      if (!token) {
        res.status(401).json({
          message: '缺少来源系统 token',
          code: 'external_source_token_missing'
        })
        return
      }

      const result = await validateExternalIntegrationSourceTokenAsync({ token, requiredScope })
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

      const rateLimit = await consumeExternalSourceRateLimit(result.context)
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
    } catch (error) {
      next(error)
    }
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

async function consumeExternalSourceRateLimit(context: ExternalIntegrationSourceAuthContext): Promise<{ allowed: true } | { allowed: false; rule: ExternalIntegrationRateLimitRule; retryAfterSeconds: number }> {
  if (!context.rateLimits.length) {
    return { allowed: true }
  }
  const decision = await consumePenaltyWindowRateLimitAsync({
    store: externalSourceRateLimitStore,
    scopeKey: `${context.sourceRefId}:${context.tokenId}:${context.tokenPrefix}`,
    rules: context.rateLimits
  })
  return decision.allowed
    ? { allowed: true }
    : {
        allowed: false,
        rule: decision.rule ?? context.rateLimits[0],
        retryAfterSeconds: decision.retryAfterSeconds ?? 1
      }
}

export function clearExternalSourceRateLimitStateForTest(): void {
  clearPenaltyWindowRateLimitStore(externalSourceRateLimitStore)
}

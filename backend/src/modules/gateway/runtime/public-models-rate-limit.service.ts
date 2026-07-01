import {
  clearPenaltyWindowRateLimitStore,
  consumePenaltyWindowRateLimit,
  createPenaltyWindowRateLimitStore
} from '../../rate-limit/penalty-window-rate-limit.js'

export interface PublicModelsRateLimitDecision {
  allowed: boolean
  limit: number
  remaining: number
  retryAfterSeconds?: number
  resetAtMs: number
}

const publicModelsRateLimitRule = {
  windowSeconds: 60,
  maxRequests: 60
} as const

const publicModelsRateLimitStore = createPenaltyWindowRateLimitStore({
  name: 'gateway_public_models',
  maxEntries: 20_000,
  maxPenaltyMs: 15 * 60_000
})

export function consumePublicModelsRateLimit(input: { clientIp?: string }): PublicModelsRateLimitDecision {
  const decision = consumePenaltyWindowRateLimit({
    store: publicModelsRateLimitStore,
    scopeKey: publicModelsRateLimitKey(input.clientIp),
    rules: [publicModelsRateLimitRule]
  })
  if (!decision.allowed) {
    return {
      allowed: false,
      limit: publicModelsRateLimitRule.maxRequests,
      remaining: 0,
      retryAfterSeconds: decision.retryAfterSeconds,
      resetAtMs: Date.now() + (decision.retryAfterSeconds ?? 1) * 1000
    }
  }
  return {
    allowed: true,
    limit: publicModelsRateLimitRule.maxRequests,
    remaining: publicModelsRateLimitRule.maxRequests,
    resetAtMs: Date.now() + publicModelsRateLimitRule.windowSeconds * 1000
  }
}

export function clearPublicModelsRateLimitForTest(): void {
  clearPenaltyWindowRateLimitStore(publicModelsRateLimitStore)
}

function publicModelsRateLimitKey(clientIp: string | undefined): string {
  const normalized = clientIp?.trim()
  return normalized ? `ip:${normalized}` : 'ip:unknown'
}

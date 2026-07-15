import {
  clearPenaltyWindowRateLimitStore,
  consumePenaltyWindowRateLimitAsync,
  createPenaltyWindowRateLimitStore,
  type PenaltyWindowRateLimitDecision,
  type PenaltyWindowRateLimitRule
} from '../../rate-limit/penalty-window-rate-limit.js'

export interface AuthenticatedModelsRateLimitDecision {
  allowed: boolean
  scope?: 'api_key_ip' | 'api_key'
  limit?: number
  retryAfterSeconds?: number
}

const apiKeyIpRules = [
  { windowSeconds: 10, maxRequests: 20 },
  { windowSeconds: 60, maxRequests: 60 }
] as const satisfies readonly PenaltyWindowRateLimitRule[]

const apiKeyRules = [
  { windowSeconds: 10, maxRequests: 100 },
  { windowSeconds: 60, maxRequests: 300 }
] as const satisfies readonly PenaltyWindowRateLimitRule[]

const apiKeyIpStore = createPenaltyWindowRateLimitStore({
  name: 'gateway_authenticated_models_api_key_ip',
  maxEntries: 100_000,
  maxPenaltyMs: 15 * 60_000
})
const apiKeyStore = createPenaltyWindowRateLimitStore({
  name: 'gateway_authenticated_models_api_key',
  maxEntries: 20_000,
  maxPenaltyMs: 15 * 60_000
})

export async function consumeAuthenticatedModelsRateLimit(input: {
  apiKeyId: string
  clientIp?: string
  nowMs?: number
}): Promise<AuthenticatedModelsRateLimitDecision> {
  const apiKeyId = input.apiKeyId.trim()
  if (!apiKeyId) return { allowed: true }
  const apiKeyIpDecision = await consumePenaltyWindowRateLimitAsync({
    store: apiKeyIpStore,
    scopeKey: `${apiKeyId}:ip:${normalizedClientIp(input.clientIp)}`,
    rules: apiKeyIpRules,
    nowMs: input.nowMs
  })
  if (!apiKeyIpDecision.allowed) {
    return blockedDecision('api_key_ip', apiKeyIpDecision)
  }

  const apiKeyDecision = await consumePenaltyWindowRateLimitAsync({
    store: apiKeyStore,
    scopeKey: apiKeyId,
    rules: apiKeyRules,
    nowMs: input.nowMs
  })
  return apiKeyDecision.allowed
    ? { allowed: true }
    : blockedDecision('api_key', apiKeyDecision)
}

export function clearAuthenticatedModelsRateLimitForTest(): void {
  clearPenaltyWindowRateLimitStore(apiKeyIpStore)
  clearPenaltyWindowRateLimitStore(apiKeyStore)
}

function blockedDecision(
  scope: 'api_key_ip' | 'api_key',
  decision: PenaltyWindowRateLimitDecision
): AuthenticatedModelsRateLimitDecision {
  return {
    allowed: false,
    scope,
    limit: decision.limit ?? decision.rule?.maxRequests,
    retryAfterSeconds: decision.retryAfterSeconds ?? 1
  }
}

function normalizedClientIp(clientIp: string | undefined): string {
  return clientIp?.trim() || 'unknown'
}

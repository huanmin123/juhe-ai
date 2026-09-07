import {
  clearPenaltyWindowRateLimitStore,
  consumePenaltyWindowRateLimitGroupsAsync,
  createPenaltyWindowRateLimitStore,
  type PenaltyWindowRateLimitDecision,
  type PenaltyWindowRateLimitRule
} from '../../rate-limit/penalty-window-rate-limit.js'
import { errorLogFields, logger } from '../../../shared/logger.js'

export interface AuthenticatedModelsRateLimitDecision {
  allowed: boolean
  scope?: 'api_key_ip' | 'api_key'
  limit?: number
  retryAfterSeconds?: number
  unavailable?: boolean
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
  penaltyMode: 'fixed_window'
})
const apiKeyStore = createPenaltyWindowRateLimitStore({
  name: 'gateway_authenticated_models_api_key',
  maxEntries: 20_000,
  penaltyMode: 'fixed_window'
})

export async function consumeAuthenticatedModelsRateLimit(input: {
  apiKeyId: string
  clientIp?: string
  nowMs?: number
}): Promise<AuthenticatedModelsRateLimitDecision> {
  const apiKeyId = input.apiKeyId.trim()
  if (!apiKeyId) return { allowed: true }
  try {
    const decision = await consumePenaltyWindowRateLimitGroupsAsync({
      groups: [
        {
          scope: 'api_key',
          store: apiKeyStore,
          scopeKey: apiKeyId,
          rules: apiKeyRules
        },
        {
          scope: 'api_key_ip',
          store: apiKeyIpStore,
          scopeKey: `${apiKeyId}:ip:${normalizedClientIp(input.clientIp)}`,
          rules: apiKeyIpRules
        }
      ],
      nowMs: input.nowMs
    })
    return decision.allowed
      ? { allowed: true }
      : blockedDecision(decision.scope ?? 'api_key', decision)
  } catch (error) {
    logger.error(errorLogFields(error, {
      event: 'authenticated_models_rate_limit_unavailable',
      apiKeyId
    }), '认证模型列表 Redis 限流不可用，本次请求按 fail-closed 拒绝')
    return {
      allowed: false,
      unavailable: true,
      retryAfterSeconds: 5
    }
  }
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

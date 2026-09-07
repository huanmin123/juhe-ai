import type { UpstreamAttempt } from '../upstream/attempt.js'

export type GatewayDispatchExhaustionReason =
  | 'api_key_pool_unavailable'
  | 'all_accounts_locally_suppressed'
  | 'account_concurrency_exhausted'
  | 'upstream_http_error'
  | 'upstream_transport_error'
  | 'no_available_account'

export interface GatewayDispatchExhaustionClassification {
  failureReason: GatewayDispatchExhaustionReason
  upstreamStatus?: number
}

export function classifyGatewayDispatchExhaustion(
  lastAttempt: UpstreamAttempt | undefined
): GatewayDispatchExhaustionClassification {
  if (!lastAttempt) return { failureReason: 'no_available_account' }
  if (lastAttempt.upstreamUrl === 'account:api_key_pool_unavailable') {
    return { failureReason: 'api_key_pool_unavailable' }
  }
  if (lastAttempt.upstreamUrl === 'account:locally_suppressed') {
    return { failureReason: 'all_accounts_locally_suppressed' }
  }
  if (lastAttempt.upstreamUrl === 'concurrency:limit') {
    return { failureReason: 'account_concurrency_exhausted' }
  }
  if (typeof lastAttempt.status === 'number') {
    return { failureReason: 'upstream_http_error', upstreamStatus: lastAttempt.status }
  }
  return { failureReason: 'upstream_transport_error' }
}

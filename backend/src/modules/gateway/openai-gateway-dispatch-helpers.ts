import type { GatewaySettings } from './account-error-policy.service.js'
import type { UpstreamAccount } from './openai-gateway-route-helpers.js'
import { UpstreamRequestAbortedError } from './openai-gateway-upstream.js'
import { fixedRetryPolicy, waitForRetryDelay, type RetryPolicy } from '../../shared/retry-policy.js'

export function failedProxyDispatchReason(failedProxyDispatchKeys: Map<string, string>, account: UpstreamAccount): string | undefined {
  const key = accountProxyDispatchKey(account)
  return key ? failedProxyDispatchKeys.get(key) : undefined
}

export function rememberFailedProxyForDispatch(failedProxyDispatchKeys: Map<string, string>, account: UpstreamAccount, reason: string): void {
  const key = accountProxyDispatchKey(account)
  if (key) {
    failedProxyDispatchKeys.set(key, reason)
  }
}

function accountProxyDispatchKey(account: UpstreamAccount): string | undefined {
  if (account.proxyProfileId) return `profile:${account.proxyProfileId}`
  if (account.proxyUrl) return `url:${account.proxyUrl}`
  return undefined
}

export async function waitBeforeTemporaryUnschedulableRetry(policy: RetryPolicy, retryNumber = 1): Promise<void> {
  await waitForRetryDelay(policy, retryNumber)
}

export function temporaryUnschedulableRetryPolicy(settings: GatewaySettings): RetryPolicy {
  return fixedRetryPolicy(
    'gateway_temporary_unschedulable_same_account_retry',
    Math.max(0, settings.temporaryUnschedulableRetryIntervalSeconds) * 1000,
    settings.temporaryUnschedulableRetryAttempts
  )
}

export function throwIfRequestAborted(signal?: AbortSignal): void {
  if (signal?.aborted) {
    throw new UpstreamRequestAbortedError('请求已取消')
  }
}

export function shouldRecordAbortedUpstreamAttempt(error: unknown): boolean {
  return error instanceof UpstreamRequestAbortedError && error.upstreamRequestStarted
}

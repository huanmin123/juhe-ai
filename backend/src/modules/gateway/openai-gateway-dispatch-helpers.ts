import type { GatewaySettings } from './account-error-policy.service.js'
import type { UpstreamAccount } from './openai-gateway-route-helpers.js'
import { UpstreamRequestAbortedError } from './openai-gateway-upstream.js'

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

export async function waitBeforeTemporaryUnschedulableRetry(settings: GatewaySettings): Promise<void> {
  const intervalMs = Math.max(0, settings.temporaryUnschedulableRetryIntervalSeconds) * 1000
  if (intervalMs <= 0) {
    return
  }
  await new Promise((resolve) => setTimeout(resolve, intervalMs))
}

export function throwIfRequestAborted(signal?: AbortSignal): void {
  if (signal?.aborted) {
    throw new UpstreamRequestAbortedError('请求已取消')
  }
}

export function shouldRecordAbortedUpstreamAttempt(error: unknown): boolean {
  return error instanceof UpstreamRequestAbortedError && error.upstreamRequestStarted
}

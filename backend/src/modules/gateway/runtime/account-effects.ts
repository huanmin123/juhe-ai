import { errorLogFields, logger } from '../../../shared/logger.js'
import { getTraceId } from '../../../shared/request-context.js'
import { isOpenAIProtocolProfile } from '../../../domain/provider-protocol.js'
import { type AccountErrorPolicyDecision, type GatewaySettings } from '../policy/account-error-policy.service.js'
import {
  clearGatewayAutomaticAccountRuntimeAvailability,
  enqueueGatewayAccountErrorHandlingSideEffect
} from './account-side-effects.service.js'
import { clearGatewayRuntimeCache } from './runtime-cache.service.js'
import { parseOpenAICodexUsageHeaders } from '../adapters/gpt-codex/usage.service.js'
import { type UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import { type StreamFailureContext } from '../response/stream.js'
import { headersToObject } from '../upstream/headers.js'
import type { OpenAIGatewayTrafficSource } from '../usage/traffic-source.js'
import type { GatewayUsageContext } from '../usage/records.js'
import { requestGatewayDbService } from './gateway-db-service-request.js'

export async function applyAccountErrorHandlingWithCacheInvalidation(
  account: UpstreamAccount,
  input: {
    success: boolean
    statusCode?: number
    headers?: Headers | Record<string, string | string[]>
    bodyText?: string
    errorMessage?: string
    settings?: GatewaySettings
    trafficSource?: OpenAIGatewayTrafficSource
    policyDecision?: AccountErrorPolicyDecision
  }
): Promise<void> {
  const normalizedInput = {
    ...input,
    traceId: getTraceId(),
    observedAt: new Date().toISOString(),
    dispatchRevision: account.dispatchRevision,
    headers: input.headers instanceof Headers ? headersToObject(input.headers) : input.headers
  }
  await enqueueGatewayAccountErrorHandlingSideEffect({
    type: 'apply_account_error_handling',
    account,
    input: normalizedInput
  })
}

export function markGatewayAccountTemporaryUnavailableWithCacheInvalidation(
  account: UpstreamAccount,
  reason: string,
  source: string
): Promise<boolean> {
  return requestGatewayDbService({
    type: 'mark_account_temporary_unavailable',
    account,
    reason: reason.slice(0, 1000),
    traceId: getTraceId()
  }, {
    priority: 'low'
  }).then((result) => {
    if (!result.updated) {
      return false
    }
    const cleared = clearGatewayAutomaticAccountRuntimeAvailability(account)
    if (!cleared.cleared) {
      clearGatewayRuntimeCache()
    }
    return true
  }).catch((error) => {
    logger.warn(errorLogFields(error, {
      event: 'gateway_account_temporary_unavailable_side_effect_failed',
      accountId: account.id,
      source
    }), '网关账号临时不可调用副作用写入失败')
    return false
  })
}

export async function handleStreamFailure(
  _account: UpstreamAccount,
  _reason: string,
  _settings: GatewaySettings,
  _errorCode: string | undefined,
  _context: StreamFailureContext,
  _usageContext?: GatewayUsageContext,
  _accountStateMutationEnabled = true
): Promise<void> {
  // Stream framing and protocol observations are request-local. Only the
  // transport circuit or an explicit user policy may authorize shared state.
}

export function clearAccountStreamFailureStateWithCacheInvalidation(account: UpstreamAccount | string): void {
  const accountId = typeof account === 'string' ? account : account.id
  void requestGatewayDbService({
    type: 'clear_account_stream_failure_state',
    accountId,
    account: typeof account === 'string' ? undefined : account
  }, {
    priority: 'low'
  }).then((result) => {
    if (result.changed) {
      clearGatewayRuntimeCache()
    }
  }).catch((error) => {
    logger.warn(errorLogFields(error, {
      event: 'gateway_account_stream_failure_clear_failed',
      accountId
    }), '网关清理账号流式失败计数失败')
  })
}

export function persistOpenAICodexHeadersIfNeeded(account: UpstreamAccount, headers: Headers, source: string): void {
  if (account.type !== 'oauth' || !isOpenAIProtocolProfile(account)) return
  if (!parseOpenAICodexUsageHeaders(headers)) return
  void requestGatewayDbService({
    type: 'persist_openai_codex_usage_headers',
    accountId: account.id,
    headers: headersToObject(headers),
    source
  }, {
    priority: 'low'
  }).catch((error) => {
    logger.warn(errorLogFields(error, {
      event: 'gateway_codex_usage_snapshot_side_effect_failed',
      accountId: account.id,
      source
    }), 'OpenAI Codex 用量快照副作用写入失败')
  })
}

import { errorLogFields, logger } from '../../shared/logger.js'
import { getRequestLogger } from '../../shared/request-context.js'
import { requestDbService } from '../db-service/db-service-ipc.js'
import { type GatewaySettings } from './account-error-policy.service.js'
import {
  enqueueGatewayAccountErrorHandlingSideEffect,
  recordGatewayAccountFailureForPrecheck
} from './gateway-account-side-effects.service.js'
import { clearGatewayRuntimeCache } from './gateway-runtime-cache.service.js'
import { parseOpenAICodexUsageHeaders } from './openai-codex-usage.service.js'
import { type UpstreamAccount } from './openai-gateway-route-helpers.js'
import { type StreamFailureContext } from './openai-gateway-stream.js'
import { headersToObject } from './openai-gateway-usage.js'
import { recordGatewayUpstreamBucketFailure } from './openai-gateway-proxy-health.service.js'
import type { OpenAIGatewayTrafficSource } from './openai-gateway-traffic-source.js'
import type { GatewayUsageContext } from './openai-gateway-usage-records.js'

export function applyAccountErrorHandlingWithCacheInvalidation(
  account: UpstreamAccount,
  input: {
    success: boolean
    statusCode?: number
    headers?: Headers | Record<string, string | string[]>
    bodyText?: string
    errorMessage?: string
    settings?: GatewaySettings
    trafficSource?: OpenAIGatewayTrafficSource
  }
): void {
  const normalizedInput = {
    ...input,
    headers: input.headers instanceof Headers ? headersToObject(input.headers) : input.headers
  }
  enqueueGatewayAccountErrorHandlingSideEffect({
    type: 'apply_account_error_handling',
    account,
    input: normalizedInput
  })
}

export function handleStreamFailure(
  account: UpstreamAccount,
  reason: string,
  settings: GatewaySettings,
  errorCode: string | undefined,
  context: StreamFailureContext,
  usageContext?: GatewayUsageContext,
  accountStateMutationEnabled = true
): void {
  if (!accountStateMutationEnabled) {
    return
  }

  recordGatewayUpstreamBucketFailure(account, '流式响应失败')
  if (usageContext?.trafficSource === 'gateway') {
    recordGatewayAccountFailureForPrecheck(account, settings, {
      systemAccountId: usageContext.systemAccountId,
      groupId: usageContext.groupId,
      apiKeyId: usageContext.apiKeyId,
      clientIp: usageContext.clientIp,
      endpoint: usageContext.endpoint,
      reason: `流式响应失败：${reason}`
    })
  }
  applyAccountErrorHandlingWithCacheInvalidation(account, {
    success: false,
    errorMessage: errorCode ? `${errorCode}；${reason}` : reason,
    settings,
    trafficSource: usageContext?.trafficSource
  })
  if (!context.outputReceived) {
    getRequestLogger().warn({
      event: 'gateway_stream_failure_account_side_effect_enqueued',
      accountId: account.id,
      accountName: account.name,
      errorCode,
      reason,
      downstreamBytesWritten: context.downstreamBytesWritten
    }, '流式失败已写入账号错误处理队列')
  }
}

export function clearAccountStreamFailureStateWithCacheInvalidation(account: UpstreamAccount | string): void {
  const accountId = typeof account === 'string' ? account : account.id
  void requestDbService({
    type: 'clear_account_stream_failure_state',
    accountId,
    account: typeof account === 'string' ? undefined : account
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
  if (account.type !== 'oauth') return
  if (!parseOpenAICodexUsageHeaders(headers)) return
  void requestDbService({
    type: 'persist_openai_codex_usage_headers',
    accountId: account.id,
    headers: headersToObject(headers),
    source
  }).catch((error) => {
    logger.warn(errorLogFields(error, {
      event: 'gateway_codex_usage_snapshot_side_effect_failed',
      accountId: account.id,
      source
    }), 'OpenAI Codex 用量快照副作用写入失败')
  })
}

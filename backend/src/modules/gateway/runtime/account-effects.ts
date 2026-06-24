import { errorLogFields, logger } from '../../../shared/logger.js'
import { getRequestLogger } from '../../../shared/request-context.js'
import { requestDbService } from '../../db-service/db-service-ipc.js'
import { type GatewaySettings } from '../policy/account-error-policy.service.js'
import {
  clearGatewayAccountRuntimeAvailability,
  enqueueGatewayAccountErrorHandlingSideEffect,
  recordGatewayAccountFailureForPrecheck,
  suppressGatewayAccountLocally
} from './account-side-effects.service.js'
import { clearGatewayRuntimeCache } from './runtime-cache.service.js'
import { parseOpenAICodexUsageHeaders } from '../adapters/gpt-codex/usage.service.js'
import { type UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import { type StreamFailureContext } from '../response/stream.js'
import { headersToObject } from '../upstream/headers.js'
import { recordGatewayUpstreamBucketFailure } from './proxy-health.service.js'
import type { OpenAIGatewayTrafficSource } from '../usage/traffic-source.js'
import type { GatewayUsageContext } from '../usage/records.js'
import { recordGatewayAccountApiKeyFailure } from './account-api-key-effects.service.js'

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

export function markGatewayAccountTemporaryUnavailableWithCacheInvalidation(
  account: UpstreamAccount,
  reason: string,
  source: string
): Promise<boolean> {
  return requestDbService({
    type: 'mark_account_temporary_unavailable',
    account,
    reason: reason.slice(0, 1000)
  }).then((result) => {
    if (!result.updated) {
      return false
    }
    const cleared = clearGatewayAccountRuntimeAvailability(account)
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
  const reasonWithCode = errorCode ? `${errorCode}；${reason}` : reason
  const isolateAccountApiKeyFailure = Boolean(account.selectedApiKeyFingerprint)
  if (context.protocolFailureEventReceived) {
    getRequestLogger().info({
      event: 'gateway_stream_protocol_failure_without_account_suppression',
      accountId: account.id,
      accountName: account.name,
      errorCode,
      reason,
      downstreamBytesWritten: context.downstreamBytesWritten,
      outputReceived: context.outputReceived
    }, '流式响应收到协议失败事件，已按本次失败返回客户端但不隔离账号')
    return
  }
  if (usageContext?.trafficSource === 'gateway') {
    if (!shouldApplyGatewayStreamFailureAccountSideEffects(context)) {
      const runtimeReason = `流式响应在可见输出前失败：${reason}`
      const localSuppression = suppressGatewayAccountLocally(account, settings, runtimeReason)
      recordGatewayAccountFailureForPrecheck(account, settings, {
        systemAccountId: usageContext.systemAccountId,
        groupId: usageContext.groupId,
        apiKeyId: usageContext.apiKeyId,
        clientIp: usageContext.clientIp,
        endpoint: usageContext.endpoint,
        reason: runtimeReason,
        forcePrecheck: localSuppression.action === 'precheck_required'
      })
      getRequestLogger().info({
        event: 'gateway_stream_failure_pre_output_runtime_avoidance',
        accountId: account.id,
        accountName: account.name,
        errorCode,
        reason,
        downstreamBytesWritten: context.downstreamBytesWritten,
        outputReceived: context.outputReceived,
        delayMs: localSuppression.delayMs,
        localFailureCount: localSuppression.localFailureCount
      }, '流式失败未产生可见模型输出，已进入本地短期避让但不累计持久流失败')
      return
    }
    recordGatewayAccountApiKeyFailure(account, {
      status: 'temporary_unavailable',
      errorCode,
      errorMessage: reasonWithCode,
      trafficSource: usageContext.trafficSource,
      clientIp: usageContext.clientIp,
      apiKeyId: usageContext.apiKeyId,
      source: 'stream_failure'
    })
    if (isolateAccountApiKeyFailure) {
      return
    }
    const runtimeReason = `流式响应失败：${reason}`
    const localSuppression = suppressGatewayAccountLocally(account, settings, runtimeReason)
    recordGatewayAccountFailureForPrecheck(account, settings, {
      systemAccountId: usageContext.systemAccountId,
      groupId: usageContext.groupId,
      apiKeyId: usageContext.apiKeyId,
      clientIp: usageContext.clientIp,
      endpoint: usageContext.endpoint,
      reason: runtimeReason,
      forcePrecheck: localSuppression.action === 'precheck_required'
    })
  } else {
    recordGatewayAccountApiKeyFailure(account, {
      status: 'temporary_unavailable',
      errorCode,
      errorMessage: reasonWithCode,
      trafficSource: usageContext?.trafficSource,
      source: 'stream_failure'
    })
    if (isolateAccountApiKeyFailure) {
      return
    }
    applyAccountErrorHandlingWithCacheInvalidation(account, {
      success: false,
      errorMessage: reasonWithCode,
      settings,
      trafficSource: usageContext?.trafficSource
    })
  }
  if (!context.outputReceived) {
    getRequestLogger().warn({
      event: 'gateway_stream_failure_account_side_effect_enqueued',
      accountId: account.id,
      accountName: account.name,
      errorCode,
      reason,
      downstreamBytesWritten: context.downstreamBytesWritten
    }, usageContext?.trafficSource === 'gateway' ? '流式失败已进入账号运行态屏障' : '流式失败已写入账号运行态处理队列')
  }
}

function shouldApplyGatewayStreamFailureAccountSideEffects(context: StreamFailureContext): boolean {
  return context.outputReceived
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

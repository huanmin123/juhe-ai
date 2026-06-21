import type { Request } from 'express'

import { getRequestLogger } from '../../../shared/request-context.js'
import type { GatewaySettings } from '../policy/account-error-policy.service.js'
import {
  type GatewayAccountFailurePrecheckInput,
  recordGatewayAccountFailureForPrecheck,
  suppressGatewayAccountLocally
} from '../runtime/account-side-effects.service.js'
import { applyAccountErrorHandlingWithCacheInvalidation } from '../runtime/account-effects.js'
import {
  failedProxyDispatchReason,
  rememberFailedProxyForDispatch,
} from './helpers.js'
import { OpenAIOAuthCodexAdapterError } from '../adapters/gpt-codex/oauth-adapter.js'
import { buildGatewayUpstreamRequestParts, prepareGatewayUpstreamAccount } from '../../providers/drivers/registry.js'
import type { ProviderGatewayRequestContext } from '../../providers/drivers/_shared/types.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import type { UpstreamAttempt } from '../upstream/attempt.js'
import {
  recordFailedUpstreamAttempt,
  type GatewayUsageContext
} from '../usage/records.js'
import { recordGatewayProxyFailure } from '../runtime/proxy-health.service.js'
import { requestEndpoint } from '../request/metadata.js'

export interface PreparedUpstreamRequestParts {
  headers: Headers
  body?: Buffer | string
}

type GatewayAccountFailurePrecheckRecorder = (
  account: UpstreamAccount,
  settings: GatewaySettings | undefined,
  input: GatewayAccountFailurePrecheckInput
) => void

export function skipAccountForFailedProxyDispatch(
  failedProxyDispatchKeys: Map<string, string>,
  account: UpstreamAccount
): UpstreamAttempt | undefined {
  const skippedProxyReason = failedProxyDispatchReason(failedProxyDispatchKeys, account)
  if (!skippedProxyReason) {
    return undefined
  }

  const message = `账户绑定的代理已在本次调度中失败，跳过重复尝试：${skippedProxyReason}`
  getRequestLogger().warn({
    event: 'gateway_proxy_duplicate_skipped',
    accountId: account.id,
    accountType: account.type,
    proxyProfileId: account.proxyProfileId,
    proxyConfigured: Boolean(account.proxyProfileId || account.proxyUrl)
  }, '跳过已失败代理绑定账号')
  return {
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    upstreamUrl: 'proxy:skipped',
    message
  }
}

export function handleUnavailableProxyProfile(
  req: Request,
  usageContext: GatewayUsageContext,
  account: UpstreamAccount,
  settings: GatewaySettings,
  failedProxyDispatchKeys: Map<string, string>,
  accountStateMutationEnabled = true,
  recordPrecheckFailure: GatewayAccountFailurePrecheckRecorder = recordGatewayAccountFailureForPrecheck
): UpstreamAttempt | undefined {
  if (!account.proxyProfileUnavailable) {
    return undefined
  }

  const attemptStartedAt = Date.now()
  const message = account.proxyProfileErrorMessage ?? '账户绑定的代理不可用'
  const lastAttempt = {
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    upstreamUrl: 'proxy:configured',
    message
  }
  recordFailedUpstreamAttempt(req, usageContext, account, {
    upstreamUrl: 'proxy:configured',
      startedAt: attemptStartedAt,
      errorMessage: message
    })
  if (accountStateMutationEnabled && usageContext.trafficSource !== 'gateway') {
    applyAccountErrorHandlingWithCacheInvalidation(account, {
      success: false,
      errorMessage: message,
      settings,
      trafficSource: usageContext.trafficSource
    })
  }
  if (accountStateMutationEnabled) {
    const localSuppression = suppressGatewayAccountLocally(account, settings, message)
    if (usageContext.trafficSource === 'gateway') {
      recordPrecheckFailure(account, settings, {
        systemAccountId: usageContext.systemAccountId,
        groupId: usageContext.groupId,
        apiKeyId: usageContext.apiKeyId,
        clientIp: usageContext.clientIp,
        endpoint: requestEndpoint(req),
        reason: message,
        forcePrecheck: localSuppression.action === 'precheck_required'
      })
    }
    recordGatewayProxyFailure(account, message)
  }
  rememberFailedProxyForDispatch(failedProxyDispatchKeys, account, message)
  return lastAttempt
}

export async function prepareUpstreamAccount(account: UpstreamAccount, signal?: AbortSignal): Promise<UpstreamAccount> {
  return await prepareGatewayUpstreamAccount(account, signal)
}

export async function buildPreparedUpstreamRequestParts(
  req: Request,
  account: UpstreamAccount,
  usageContext: GatewayUsageContext,
  signal?: AbortSignal,
  context?: ProviderGatewayRequestContext
): Promise<PreparedUpstreamRequestParts> {
  try {
    return await buildGatewayUpstreamRequestParts(req, account, {
      systemAccountId: usageContext.systemAccountId,
      apiKeyId: usageContext.apiKeyId,
      groupId: usageContext.groupId
    }, signal, context)
  } catch (error) {
    if (error instanceof OpenAIOAuthCodexAdapterError) {
      const responseBodyText = JSON.stringify({
        error: {
          message: error.message,
          type: error.type,
          code: error.code
        }
      })
      recordFailedUpstreamAttempt(req, usageContext, account, {
        upstreamUrl: account.type === 'oauth' ? 'openai-oauth-codex:local-validation' : 'gateway:local-validation',
        startedAt: Date.now(),
        statusCode: error.statusCode,
        bodyText: responseBodyText,
        errorMessage: error.message
      })
    }
    throw error
  }
}

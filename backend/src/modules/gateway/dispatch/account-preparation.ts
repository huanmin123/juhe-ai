import type { Request } from 'express'

import { errorLogFields, logger } from '../../../shared/logger.js'
import { getRequestLogger } from '../../../shared/request-context.js'
import { assertSafeUpstreamBaseUrl } from '../../../shared/upstream-url-policy.js'
import { runtimeOpenAIAccountCredentials } from '../../../storage/repositories.js'
import { shouldRefreshOpenAIOAuthCredentials } from '../../openai-oauth/openai-oauth.service.js'
import { refreshOpenAIOAuthAccountAccessToken } from '../../openai-oauth/openai-oauth-access-token-refresh.service.js'
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
  throwIfRequestAborted
} from './helpers.js'
import { OpenAIOAuthCodexAdapterError } from '../adapters/gpt-codex/oauth-adapter.js'
import { buildGatewayUpstreamRequestParts } from '../../providers/drivers/registry.js'
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

const oauthBlockingRefreshLeadMs = 5_000
const oauthRefreshPreheatInFlightAccountIds = new Set<string>()

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
  return { accountId: account.id, accountName: account.name, upstreamUrl: 'proxy:skipped', message }
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
  const lastAttempt = { accountId: account.id, accountName: account.name, upstreamUrl: 'proxy:configured', message }
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
  const refreshCredentials = openAIOAuthRefreshCredentials(account)
  if (account.type !== 'oauth' || !shouldRefreshOpenAIOAuthCredentials(refreshCredentials) || !account.refreshToken) {
    return account
  }
  if (!shouldBlockForOpenAIOAuthAccessTokenRefresh(refreshCredentials)) {
    scheduleOpenAIOAuthAccessTokenPreheat(account, refreshCredentials)
    return account
  }
  throwIfRequestAborted(signal)

  const credentialSourceAccount = account.credentialSourceAccountId
    ? { ...account, id: account.credentialSourceAccountId }
    : account
  const updated = await refreshOpenAIOAuthAccountAccessToken({
    ...credentialSourceAccount,
    credentials: refreshCredentials
  }, { signal, force: false, persistMode: 'db-service' })
  const credentials = updated.credentials
  const accessToken = typeof credentials.access_token === 'string' ? credentials.access_token : account.apiKey
  const refreshedBaseUrl = typeof credentials.base_url === 'string' && credentials.base_url ? credentials.base_url : undefined
  if (refreshedBaseUrl) {
    assertSafeUpstreamBaseUrl(refreshedBaseUrl)
  }
  return {
    ...account,
    apiKey: accessToken,
    baseUrl: refreshedBaseUrl ?? account.baseUrl,
    refreshToken: typeof credentials.refresh_token === 'string' ? credentials.refresh_token : account.refreshToken,
    clientId: typeof credentials.client_id === 'string' ? credentials.client_id : account.clientId,
    expiresAt: typeof credentials.expires_at === 'string' ? credentials.expires_at : account.expiresAt,
    credentials: runtimeOpenAIAccountCredentials(credentials)
  }
}

function scheduleOpenAIOAuthAccessTokenPreheat(account: UpstreamAccount, credentials: Record<string, unknown>): void {
  const credentialSourceAccount = account.credentialSourceAccountId
    ? { ...account, id: account.credentialSourceAccountId }
    : account
  if (oauthRefreshPreheatInFlightAccountIds.has(credentialSourceAccount.id)) {
    return
  }
  oauthRefreshPreheatInFlightAccountIds.add(credentialSourceAccount.id)
  const previousExpiresAt = typeof credentials.expires_at === 'string' ? credentials.expires_at : undefined

  void (async () => {
    try {
      const updated = await refreshOpenAIOAuthAccountAccessToken({
        ...credentialSourceAccount,
        credentials
      }, { force: false, persistMode: 'db-service', restoreFailureState: false })
      logger.info({
        event: 'gateway_openai_oauth_access_token_preheated',
        accountId: account.id,
        credentialSourceAccountId: credentialSourceAccount.id !== account.id ? credentialSourceAccount.id : undefined,
        previousExpiresAt,
        nextExpiresAt: typeof updated.credentials.expires_at === 'string' ? updated.credentials.expires_at : undefined
      }, 'OpenAI OAuth Access Token 已在请求热路径外预刷新')
    } catch (error) {
      logger.warn(errorLogFields(error, {
        event: 'gateway_openai_oauth_access_token_preheat_failed',
        accountId: account.id,
        credentialSourceAccountId: credentialSourceAccount.id !== account.id ? credentialSourceAccount.id : undefined,
        previousExpiresAt
      }), 'OpenAI OAuth Access Token 请求热路径外预刷新失败')
    } finally {
      oauthRefreshPreheatInFlightAccountIds.delete(credentialSourceAccount.id)
    }
  })()
}

function shouldBlockForOpenAIOAuthAccessTokenRefresh(credentials: Record<string, unknown>): boolean {
  const accessToken = typeof credentials.access_token === 'string' && credentials.access_token.trim()
    ? credentials.access_token.trim()
    : undefined
  if (!accessToken) return true
  const expiresAt = typeof credentials.expires_at === 'string' ? Date.parse(credentials.expires_at) : Number.NaN
  if (!Number.isFinite(expiresAt)) return true
  return expiresAt - Date.now() <= oauthBlockingRefreshLeadMs
}

function openAIOAuthRefreshCredentials(account: UpstreamAccount): Record<string, unknown> {
  const credentials: Record<string, unknown> = {
    ...account.credentials,
    access_token: account.apiKey,
    base_url: account.baseUrl
  }
  if (account.refreshToken) credentials.refresh_token = account.refreshToken
  if (account.clientId) credentials.client_id = account.clientId
  if (account.expiresAt) credentials.expires_at = account.expiresAt
  return credentials
}

export async function buildPreparedUpstreamRequestParts(
  req: Request,
  account: UpstreamAccount,
  usageContext: GatewayUsageContext,
  signal?: AbortSignal
): Promise<PreparedUpstreamRequestParts> {
  try {
    return await buildGatewayUpstreamRequestParts(req, account, {
      systemAccountId: usageContext.systemAccountId,
      apiKeyId: usageContext.apiKeyId,
      groupId: usageContext.groupId
    }, signal)
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

import { assertSafeUpstreamBaseUrl } from '../../../../shared/upstream-url-policy.js'
import { errorLogFields, logger } from '../../../../shared/logger.js'
import { runtimeOpenAIAccountCredentials } from '../../../../storage/repositories.js'
import type { DispatchAccountSecret } from '../../../../storage/openai-account-selector.types.js'
import { shouldRefreshOpenAIOAuthCredentials } from '../../../openai-oauth/openai-oauth.service.js'
import { refreshOpenAIOAuthAccountAccessToken } from '../../../openai-oauth/openai-oauth-access-token-refresh.service.js'
import { UpstreamRequestAbortedError } from '../../../gateway/upstream/request.js'

const oauthBlockingRefreshLeadMs = 5_000
const oauthRefreshPreheatInFlightAccountIds = new Set<string>()

export async function prepareGptAccountBeforeDispatch(
  account: DispatchAccountSecret,
  signal?: AbortSignal
): Promise<DispatchAccountSecret> {
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

function scheduleOpenAIOAuthAccessTokenPreheat(account: DispatchAccountSecret, credentials: Record<string, unknown>): void {
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

function openAIOAuthRefreshCredentials(account: DispatchAccountSecret): Record<string, unknown> {
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

function throwIfRequestAborted(signal?: AbortSignal): void {
  if (signal?.aborted) {
    throw new UpstreamRequestAbortedError('请求已取消')
  }
}

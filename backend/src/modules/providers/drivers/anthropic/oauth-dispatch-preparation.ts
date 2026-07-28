import { ANTHROPIC_PROVIDER_CODE } from '../../../../domain/provider-protocol.js'
import { assertSafeUpstreamBaseUrl } from '../../../../shared/upstream-url-policy.js'
import { runtimeOpenAIAccountCredentials } from '../../../../storage/openai-account-selector.repository.js'
import type { DispatchAccountSecret } from '../../../../storage/openai-account-selector.types.js'
import {
  AccountConfigRevisionConflictError,
  findAccountForTestAsync,
  updateAccountAsync
} from '../../../../storage/repositories.js'
import {
  buildAnthropicOAuthCredentials,
  refreshAnthropicAuthToken,
  type AnthropicOAuthTokenInfo
} from '../../../anthropic-oauth/anthropic-oauth.service.js'
import { UpstreamRequestAbortedError } from '../../../gateway/upstream/request.js'
import { runWithProviderOAuthRefreshLock } from '../_shared/oauth-refresh-lock.js'

const anthropicOAuthRefreshLeadMs = 60_000
const anthropicOAuthPersistAttempts = 3
const anthropicOAuthRefreshes = new Map<string, Promise<DispatchAccountSecret>>()

interface AnthropicOAuthRefreshSource {
  providerCode: string
  type: string
  configRevision?: number
  credentials: Record<string, unknown>
}

export interface AnthropicOAuthDispatchPreparationDependencies {
  loadAccount(accountId: string): Promise<AnthropicOAuthRefreshSource | undefined>
  refreshToken(input: {
    refreshToken: string
    clientId?: string
    proxyUrl?: string
  }): Promise<AnthropicOAuthTokenInfo>
  persistCredentials(
    accountId: string,
    credentials: Record<string, unknown>,
    expectedConfigRevision: number
  ): Promise<AnthropicOAuthRefreshSource | undefined>
}

const defaultDependencies: AnthropicOAuthDispatchPreparationDependencies = {
  async loadAccount(accountId) {
    return await findAccountForTestAsync(accountId)
  },
  async refreshToken(input) {
    return await refreshAnthropicAuthToken(input)
  },
  async persistCredentials(accountId, credentials, expectedConfigRevision) {
    return await updateAccountAsync(accountId, { credentials }, undefined, { expectedConfigRevision })
  }
}

export async function prepareAnthropicAccountBeforeDispatch(
  account: DispatchAccountSecret,
  signal?: AbortSignal,
  dependencies: AnthropicOAuthDispatchPreparationDependencies = defaultDependencies
): Promise<DispatchAccountSecret> {
  if (!isRefreshableAnthropicOAuthAccount(account)) return account
  if (!shouldRefreshAnthropicOAuthCredentials(account.credentials, account.apiKey, account.expiresAt)) return account
  throwIfRequestAborted(signal)

  const sourceAccountId = account.credentialSourceAccountId ?? account.id
  const existing = anthropicOAuthRefreshes.get(sourceAccountId)
  if (existing) return await waitForRefresh(existing, signal)

  // The shared refresh is independent from any one downstream request. Callers may
  // stop waiting without cancelling a token rotation needed by other requests.
  const refresh = runWithProviderOAuthRefreshLock(
    ANTHROPIC_PROVIDER_CODE,
    sourceAccountId,
    async () => await refreshAndPersistAnthropicOAuthAccount(account, sourceAccountId, dependencies)
  )
  anthropicOAuthRefreshes.set(sourceAccountId, refresh)
  void refresh.finally(() => {
    if (anthropicOAuthRefreshes.get(sourceAccountId) === refresh) {
      anthropicOAuthRefreshes.delete(sourceAccountId)
    }
  }).catch(() => undefined)
  return await waitForRefresh(refresh, signal)
}

export function shouldRefreshAnthropicOAuthCredentials(
  credentials: Record<string, unknown>,
  accessTokenFallback?: string,
  expiresAtFallback?: string
): boolean {
  const accessToken = stringCredential(credentials, 'access_token') || normalizeText(accessTokenFallback)
  if (!accessToken) return true
  const expiresAt = stringCredential(credentials, 'expires_at') || normalizeText(expiresAtFallback)
  const expiresAtMs = expiresAt ? Date.parse(expiresAt) : Number.NaN
  return !Number.isFinite(expiresAtMs) || expiresAtMs - Date.now() <= anthropicOAuthRefreshLeadMs
}

async function refreshAndPersistAnthropicOAuthAccount(
  dispatchAccount: DispatchAccountSecret,
  sourceAccountId: string,
  dependencies: AnthropicOAuthDispatchPreparationDependencies
): Promise<DispatchAccountSecret> {
  const source = await dependencies.loadAccount(sourceAccountId)
  if (!source || source.providerCode !== ANTHROPIC_PROVIDER_CODE || source.type !== 'oauth') {
    throw new Error('Anthropic OAuth 凭据源账户不存在或类型不匹配')
  }
  const currentCredentials = source.credentials ?? {}
  if (!shouldRefreshAnthropicOAuthCredentials(currentCredentials)) {
    return dispatchAccountWithCredentials(dispatchAccount, currentCredentials)
  }
  const refreshToken = stringCredential(currentCredentials, 'refresh_token')
  if (!refreshToken) throw new Error('Anthropic OAuth 账户缺少 Refresh Token')
  const tokenInfo = await dependencies.refreshToken({
    refreshToken,
    clientId: stringCredential(currentCredentials, 'client_id'),
    proxyUrl: dispatchAccount.proxyUrl
  })
  let candidate = source
  let lastConflict: AccountConfigRevisionConflictError | undefined
  for (let attempt = 0; attempt < anthropicOAuthPersistAttempts; attempt += 1) {
    const baseUrl = stringCredential(candidate.credentials, 'base_url') || dispatchAccount.baseUrl
    assertSafeUpstreamBaseUrl(baseUrl)
    const credentials = {
      ...candidate.credentials,
      ...buildAnthropicOAuthCredentials(tokenInfo, { refreshToken }),
      base_url: baseUrl
    }
    try {
      const updated = await dependencies.persistCredentials(
        sourceAccountId,
        credentials,
        candidate.configRevision ?? 1
      )
      if (!updated) throw new Error('Anthropic OAuth 账户不存在或无法更新')
      return dispatchAccountWithCredentials(dispatchAccount, updated.credentials)
    } catch (error) {
      if (!(error instanceof AccountConfigRevisionConflictError)) throw error
      lastConflict = error
      const latest = await dependencies.loadAccount(sourceAccountId)
      if (!latest || latest.providerCode !== ANTHROPIC_PROVIDER_CODE || latest.type !== 'oauth') throw error
      const latestRefreshToken = stringCredential(latest.credentials, 'refresh_token')
      if (latestRefreshToken !== refreshToken) {
        if (shouldRefreshAnthropicOAuthCredentials(latest.credentials)) throw error
        return dispatchAccountWithCredentials(dispatchAccount, latest.credentials)
      }
      candidate = latest
    }
  }
  throw lastConflict ?? new Error('Anthropic OAuth 凭据并发写回失败')
}

function isRefreshableAnthropicOAuthAccount(account: DispatchAccountSecret): boolean {
  return account.providerCode === ANTHROPIC_PROVIDER_CODE
    && account.type === 'oauth'
    && Boolean(account.refreshToken || stringCredential(account.credentials, 'refresh_token'))
}

function dispatchAccountWithCredentials(
  account: DispatchAccountSecret,
  credentials: Record<string, unknown>
): DispatchAccountSecret {
  const baseUrl = stringCredential(credentials, 'base_url') || account.baseUrl
  assertSafeUpstreamBaseUrl(baseUrl)
  return {
    ...account,
    apiKey: stringCredential(credentials, 'access_token') || account.apiKey,
    refreshToken: stringCredential(credentials, 'refresh_token') || account.refreshToken,
    clientId: stringCredential(credentials, 'client_id') || account.clientId,
    expiresAt: stringCredential(credentials, 'expires_at') || account.expiresAt,
    baseUrl,
    credentials: runtimeOpenAIAccountCredentials(credentials)
  }
}

async function waitForRefresh(
  refresh: Promise<DispatchAccountSecret>,
  signal?: AbortSignal
): Promise<DispatchAccountSecret> {
  if (!signal) return await refresh
  throwIfRequestAborted(signal)
  return await new Promise<DispatchAccountSecret>((resolve, reject) => {
    const abort = () => reject(new UpstreamRequestAbortedError('请求已取消'))
    signal.addEventListener('abort', abort, { once: true })
    void refresh.then(resolve, reject).finally(() => signal.removeEventListener('abort', abort))
  })
}

function throwIfRequestAborted(signal?: AbortSignal): void {
  if (signal?.aborted) throw new UpstreamRequestAbortedError('请求已取消')
}

function stringCredential(credentials: Record<string, unknown>, key: string): string | undefined {
  return normalizeText(credentials[key])
}

function normalizeText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

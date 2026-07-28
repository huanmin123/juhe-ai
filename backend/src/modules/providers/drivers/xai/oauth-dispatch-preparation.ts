import { XAI_PROVIDER_CODE } from '../../../../domain/provider-protocol.js'
import { assertSafeUpstreamBaseUrl } from '../../../../shared/upstream-url-policy.js'
import { runtimeOpenAIAccountCredentials } from '../../../../storage/openai-account-selector.repository.js'
import type { DispatchAccountSecret } from '../../../../storage/openai-account-selector.types.js'
import {
  AccountConfigRevisionConflictError,
  findAccountForTestAsync,
  updateAccountAsync
} from '../../../../storage/repositories.js'
import {
  buildGrokOAuthCredentials,
  refreshGrokAuthToken,
  type GrokOAuthTokenInfo
} from '../../../grok-oauth/grok-oauth.service.js'
import { UpstreamRequestAbortedError } from '../../../gateway/upstream/request.js'
import { runWithProviderOAuthRefreshLock } from '../_shared/oauth-refresh-lock.js'

const grokOAuthRefreshLeadMs = 5 * 60_000
const grokOAuthPersistAttempts = 3
const grokOAuthRefreshes = new Map<string, Promise<DispatchAccountSecret>>()

interface GrokOAuthRefreshSource {
  providerCode: string
  type: string
  configRevision?: number
  credentials: Record<string, unknown>
}

export interface XaiOAuthDispatchPreparationDependencies {
  loadAccount(accountId: string): Promise<GrokOAuthRefreshSource | undefined>
  refreshToken(input: {
    refreshToken: string
    clientId?: string
    proxyUrl?: string
    signal?: AbortSignal
  }): Promise<GrokOAuthTokenInfo>
  persistCredentials(
    accountId: string,
    credentials: Record<string, unknown>,
    expectedConfigRevision: number
  ): Promise<GrokOAuthRefreshSource | undefined>
}

const defaultDependencies: XaiOAuthDispatchPreparationDependencies = {
  async loadAccount(accountId) {
    return await findAccountForTestAsync(accountId)
  },
  async refreshToken(input) {
    return await refreshGrokAuthToken(input)
  },
  async persistCredentials(accountId, credentials, expectedConfigRevision) {
    return await updateAccountAsync(accountId, { credentials }, undefined, { expectedConfigRevision })
  }
}

export async function prepareXaiAccountBeforeDispatch(
  account: DispatchAccountSecret,
  signal?: AbortSignal,
  dependencies: XaiOAuthDispatchPreparationDependencies = defaultDependencies
): Promise<DispatchAccountSecret> {
  if (!isRefreshableGrokOAuthAccount(account)) return account
  if (!shouldRefreshGrokOAuthCredentials(account.credentials, account.apiKey, account.expiresAt)) return account
  throwIfRequestAborted(signal)

  const sourceAccountId = account.credentialSourceAccountId ?? account.id
  const existing = grokOAuthRefreshes.get(sourceAccountId)
  if (existing) return await waitForRefresh(existing, signal)

  const refresh = runWithProviderOAuthRefreshLock(
    XAI_PROVIDER_CODE,
    sourceAccountId,
    async (lockSignal, assertLockOwned) => await refreshAndPersistGrokOAuthAccount(
      account, sourceAccountId, dependencies, lockSignal, assertLockOwned
    )
  )
  grokOAuthRefreshes.set(sourceAccountId, refresh)
  void refresh.finally(() => {
    if (grokOAuthRefreshes.get(sourceAccountId) === refresh) {
      grokOAuthRefreshes.delete(sourceAccountId)
    }
  }).catch(() => undefined)
  return await waitForRefresh(refresh, signal)
}

export function shouldRefreshGrokOAuthCredentials(
  credentials: Record<string, unknown>,
  accessTokenFallback?: string,
  expiresAtFallback?: string
): boolean {
  const accessToken = stringCredential(credentials, 'access_token') || normalizeText(accessTokenFallback)
  if (!accessToken) return true
  const expiresAt = stringCredential(credentials, 'expires_at') || normalizeText(expiresAtFallback)
  const expiresAtMs = expiresAt ? Date.parse(expiresAt) : Number.NaN
  return !Number.isFinite(expiresAtMs) || expiresAtMs - Date.now() <= grokOAuthRefreshLeadMs
}

async function refreshAndPersistGrokOAuthAccount(
  dispatchAccount: DispatchAccountSecret,
  sourceAccountId: string,
  dependencies: XaiOAuthDispatchPreparationDependencies,
  signal?: AbortSignal,
  assertLockOwned?: () => Promise<void>
): Promise<DispatchAccountSecret> {
  const source = await dependencies.loadAccount(sourceAccountId)
  if (!source || source.providerCode !== XAI_PROVIDER_CODE || source.type !== 'oauth') {
    throw new Error('Grok OAuth 凭据源账户不存在或类型不匹配')
  }
  const currentCredentials = source.credentials ?? {}
  if (!shouldRefreshGrokOAuthCredentials(currentCredentials)) {
    return dispatchAccountWithCredentials(dispatchAccount, currentCredentials)
  }
  const refreshToken = stringCredential(currentCredentials, 'refresh_token')
  if (!refreshToken) throw new Error('Grok OAuth 账户缺少 Refresh Token')
  const tokenInfo = await dependencies.refreshToken({
    refreshToken,
    clientId: stringCredential(currentCredentials, 'client_id'),
    proxyUrl: dispatchAccount.proxyUrl,
    signal
  })
  let candidate = source
  let lastConflict: AccountConfigRevisionConflictError | undefined
  for (let attempt = 0; attempt < grokOAuthPersistAttempts; attempt += 1) {
    const baseUrl = stringCredential(candidate.credentials, 'base_url') || dispatchAccount.baseUrl
    assertSafeUpstreamBaseUrl(baseUrl)
    const credentials = {
      ...candidate.credentials,
      ...buildGrokOAuthCredentials(tokenInfo, { refreshToken }),
      base_url: baseUrl
    }
    try {
      await assertLockOwned?.()
      const updated = await dependencies.persistCredentials(
        sourceAccountId,
        credentials,
        candidate.configRevision ?? 1
      )
      if (!updated) throw new Error('Grok OAuth 账户不存在或无法更新')
      return dispatchAccountWithCredentials(dispatchAccount, updated.credentials)
    } catch (error) {
      if (!(error instanceof AccountConfigRevisionConflictError)) throw error
      lastConflict = error
      const latest = await dependencies.loadAccount(sourceAccountId)
      if (!latest || latest.providerCode !== XAI_PROVIDER_CODE || latest.type !== 'oauth') throw error
      const latestRefreshToken = stringCredential(latest.credentials, 'refresh_token')
      if (latestRefreshToken !== refreshToken) {
        if (shouldRefreshGrokOAuthCredentials(latest.credentials)) throw error
        return dispatchAccountWithCredentials(dispatchAccount, latest.credentials)
      }
      candidate = latest
    }
  }
  throw lastConflict ?? new Error('Grok OAuth 凭据并发写回失败')
}

function isRefreshableGrokOAuthAccount(account: DispatchAccountSecret): boolean {
  return account.providerCode === XAI_PROVIDER_CODE
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

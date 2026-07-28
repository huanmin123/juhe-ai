import { GEMINI_PROVIDER_CODE } from '../../../../domain/provider-protocol.js'
import { assertSafeUpstreamBaseUrl } from '../../../../shared/upstream-url-policy.js'
import { runtimeOpenAIAccountCredentials } from '../../../../storage/openai-account-selector.repository.js'
import type { DispatchAccountSecret } from '../../../../storage/openai-account-selector.types.js'
import {
  AccountConfigRevisionConflictError,
  findAccountForTestAsync,
  updateAccountAsync
} from '../../../../storage/repositories.js'
import {
  buildGeminiOAuthCredentials,
  refreshGeminiAuthToken,
  type GeminiOAuthTokenInfo,
  type GeminiOAuthType
} from '../../../gemini-oauth/gemini-oauth.service.js'
import { UpstreamRequestAbortedError } from '../../../gateway/upstream/request.js'
import { runWithProviderOAuthRefreshLock } from '../_shared/oauth-refresh-lock.js'

const geminiOAuthRefreshLeadMs = 60_000
const geminiOAuthPersistAttempts = 3
const geminiOAuthRefreshes = new Map<string, Promise<DispatchAccountSecret>>()

interface GeminiOAuthRefreshSource {
  providerCode: string
  type: string
  configRevision?: number
  credentials: Record<string, unknown>
}

export interface GeminiOAuthDispatchPreparationDependencies {
  loadAccount(accountId: string): Promise<GeminiOAuthRefreshSource | undefined>
  refreshToken(input: {
    refreshToken: string
    oauthType?: GeminiOAuthType
    clientId?: string
    clientSecret?: string
    projectId?: string
    tierId?: string
    quotaProjectId?: string
    baseUrl?: string
    scope?: string
    proxyUrl?: string
    signal?: AbortSignal
  }): Promise<GeminiOAuthTokenInfo>
  persistCredentials(
    accountId: string,
    credentials: Record<string, unknown>,
    expectedConfigRevision: number
  ): Promise<GeminiOAuthRefreshSource | undefined>
}

const defaultDependencies: GeminiOAuthDispatchPreparationDependencies = {
  async loadAccount(accountId) {
    return await findAccountForTestAsync(accountId)
  },
  async refreshToken(input) {
    return await refreshGeminiAuthToken(input)
  },
  async persistCredentials(accountId, credentials, expectedConfigRevision) {
    return await updateAccountAsync(accountId, { credentials }, undefined, { expectedConfigRevision })
  }
}

export async function prepareGeminiAccountBeforeDispatch(
  account: DispatchAccountSecret,
  signal?: AbortSignal,
  dependencies: GeminiOAuthDispatchPreparationDependencies = defaultDependencies
): Promise<DispatchAccountSecret> {
  if (!isRefreshableGeminiOAuthAccount(account)) return account
  if (!shouldRefreshGeminiOAuthCredentials(account.credentials, account.apiKey, account.expiresAt)) return account
  throwIfRequestAborted(signal)

  const sourceAccountId = account.credentialSourceAccountId ?? account.id
  const existing = geminiOAuthRefreshes.get(sourceAccountId)
  if (existing) return await waitForRefresh(existing, signal)

  const refresh = runWithProviderOAuthRefreshLock(
    GEMINI_PROVIDER_CODE,
    sourceAccountId,
    async (lockSignal, assertLockOwned) => await refreshAndPersistGeminiOAuthAccount(
      account, sourceAccountId, dependencies, lockSignal, assertLockOwned
    )
  )
  geminiOAuthRefreshes.set(sourceAccountId, refresh)
  void refresh.finally(() => {
    if (geminiOAuthRefreshes.get(sourceAccountId) === refresh) {
      geminiOAuthRefreshes.delete(sourceAccountId)
    }
  }).catch(() => undefined)
  return await waitForRefresh(refresh, signal)
}

export function shouldRefreshGeminiOAuthCredentials(
  credentials: Record<string, unknown>,
  accessTokenFallback?: string,
  expiresAtFallback?: string
): boolean {
  const accessToken = stringCredential(credentials, 'access_token') || normalizeText(accessTokenFallback)
  if (!accessToken) return true
  const expiresAt = stringCredential(credentials, 'expires_at') || normalizeText(expiresAtFallback)
  const expiresAtMs = expiresAt ? Date.parse(expiresAt) : Number.NaN
  return !Number.isFinite(expiresAtMs) || expiresAtMs - Date.now() <= geminiOAuthRefreshLeadMs
}

async function refreshAndPersistGeminiOAuthAccount(
  dispatchAccount: DispatchAccountSecret,
  sourceAccountId: string,
  dependencies: GeminiOAuthDispatchPreparationDependencies,
  signal?: AbortSignal,
  assertLockOwned?: () => Promise<void>
): Promise<DispatchAccountSecret> {
  const source = await dependencies.loadAccount(sourceAccountId)
  if (!source || source.providerCode !== GEMINI_PROVIDER_CODE || source.type !== 'google_oauth') {
    throw new Error('Gemini OAuth 凭据源账户不存在或类型不匹配')
  }
  const currentCredentials = source.credentials ?? {}
  if (!shouldRefreshGeminiOAuthCredentials(currentCredentials)) {
    return dispatchAccountWithCredentials(dispatchAccount, currentCredentials)
  }
  const refreshToken = stringCredential(currentCredentials, 'refresh_token')
  if (!refreshToken) throw new Error('Gemini OAuth 账户缺少 Refresh Token')
  const tokenInfo = await dependencies.refreshToken({
    refreshToken,
    oauthType: accountOAuthType(currentCredentials),
    clientId: stringCredential(currentCredentials, 'client_id'),
    clientSecret: stringCredential(currentCredentials, 'client_secret'),
    projectId: stringCredential(currentCredentials, 'project_id'),
    tierId: stringCredential(currentCredentials, 'tier_id'),
    quotaProjectId: stringCredential(currentCredentials, 'quota_project_id'),
    baseUrl: stringCredential(currentCredentials, 'base_url'),
    scope: stringCredential(currentCredentials, 'scope'),
    proxyUrl: dispatchAccount.proxyUrl,
    signal
  })
  let candidate = source
  let lastConflict: AccountConfigRevisionConflictError | undefined
  for (let attempt = 0; attempt < geminiOAuthPersistAttempts; attempt += 1) {
    const credentials = {
      ...candidate.credentials,
      ...buildGeminiOAuthCredentials(tokenInfo, {
        refreshToken,
        oauthType: accountOAuthType(candidate.credentials),
        projectId: stringCredential(candidate.credentials, 'project_id'),
        tierId: stringCredential(candidate.credentials, 'tier_id'),
        quotaProjectId: stringCredential(candidate.credentials, 'quota_project_id'),
        baseUrl: stringCredential(candidate.credentials, 'base_url') || dispatchAccount.baseUrl,
        scope: stringCredential(candidate.credentials, 'scope')
      })
    }
    assertSafeUpstreamBaseUrl(stringCredential(credentials, 'base_url') || dispatchAccount.baseUrl)
    try {
      await assertLockOwned?.()
      const updated = await dependencies.persistCredentials(
        sourceAccountId,
        credentials,
        candidate.configRevision ?? 1
      )
      if (!updated) throw new Error('Gemini OAuth 账户不存在或无法更新')
      return dispatchAccountWithCredentials(dispatchAccount, updated.credentials)
    } catch (error) {
      if (!(error instanceof AccountConfigRevisionConflictError)) throw error
      lastConflict = error
      const latest = await dependencies.loadAccount(sourceAccountId)
      if (!latest || latest.providerCode !== GEMINI_PROVIDER_CODE || latest.type !== 'google_oauth') throw error
      const latestRefreshToken = stringCredential(latest.credentials, 'refresh_token')
      if (latestRefreshToken !== refreshToken) {
        if (shouldRefreshGeminiOAuthCredentials(latest.credentials)) throw error
        return dispatchAccountWithCredentials(dispatchAccount, latest.credentials)
      }
      candidate = latest
    }
  }
  throw lastConflict ?? new Error('Gemini OAuth 凭据并发写回失败')
}

function isRefreshableGeminiOAuthAccount(account: DispatchAccountSecret): boolean {
  return account.providerCode === GEMINI_PROVIDER_CODE
    && account.type === 'google_oauth'
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

function accountOAuthType(credentials: Record<string, unknown>): GeminiOAuthType {
  const value = stringCredential(credentials, 'oauth_type')
  if (value === 'code_assist' || value === 'google_one' || value === 'ai_studio') return value
  const baseUrl = stringCredential(credentials, 'base_url')
  if (baseUrl?.includes('generativelanguage.googleapis.com')) return 'ai_studio'
  if (stringCredential(credentials, 'project_id') || baseUrl?.includes('cloudcode-pa.googleapis.com')) return 'code_assist'
  const clientId = stringCredential(credentials, 'client_id')
  return clientId && clientId !== '681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com' ? 'ai_studio' : 'code_assist'
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

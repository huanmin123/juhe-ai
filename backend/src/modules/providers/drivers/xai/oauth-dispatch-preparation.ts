import { assertSafeUpstreamBaseUrl } from '../../../../shared/upstream-url-policy.js'
import { runtimeOpenAIAccountCredentials } from '../../../../storage/openai-account-selector.repository.js'
import type { DispatchAccountSecret } from '../../../../storage/openai-account-selector.types.js'
import { updateAccountAsync } from '../../../../storage/repositories.js'
import {
  buildGrokOAuthCredentials,
  refreshGrokAuthToken
} from '../../../grok-oauth/grok-oauth.service.js'
import { UpstreamRequestAbortedError } from '../../../gateway/upstream/request.js'

const grokOAuthRefreshLeadMs = 60_000
const grokOAuthRefreshes = new Map<string, Promise<DispatchAccountSecret>>()

export async function prepareXaiAccountBeforeDispatch(
  account: DispatchAccountSecret,
  signal?: AbortSignal
): Promise<DispatchAccountSecret> {
  if (account.type !== 'oauth' || !shouldRefreshGrokOAuthAccount(account)) return account
  if (signal?.aborted) throw new UpstreamRequestAbortedError('请求已取消')

  const sourceAccountId = account.credentialSourceAccountId ?? account.id
  const existing = grokOAuthRefreshes.get(sourceAccountId)
  if (existing) return await waitForRefresh(existing, signal)

  const refresh = refreshAndPersistGrokOAuthAccount(account, sourceAccountId, signal)
  grokOAuthRefreshes.set(sourceAccountId, refresh)
  void refresh.finally(() => {
    if (grokOAuthRefreshes.get(sourceAccountId) === refresh) {
      grokOAuthRefreshes.delete(sourceAccountId)
    }
  }).catch(() => undefined)
  return await waitForRefresh(refresh, signal)
}

function shouldRefreshGrokOAuthAccount(account: DispatchAccountSecret): boolean {
  if (!account.refreshToken) return false
  if (!account.apiKey.trim()) return true
  const expiresAt = account.expiresAt ? Date.parse(account.expiresAt) : Number.NaN
  return !Number.isFinite(expiresAt) || expiresAt - Date.now() <= grokOAuthRefreshLeadMs
}

async function refreshAndPersistGrokOAuthAccount(
  account: DispatchAccountSecret,
  sourceAccountId: string,
  signal?: AbortSignal
): Promise<DispatchAccountSecret> {
  const tokenInfo = await refreshGrokAuthToken({
    refreshToken: account.refreshToken ?? '',
    clientId: account.clientId,
    proxyUrl: account.proxyUrl,
    signal
  })
  const refreshed = buildGrokOAuthCredentials(tokenInfo, {
    refreshToken: account.refreshToken
  })
  const currentBaseUrl = stringCredential(account.credentials, 'base_url') || account.baseUrl
  const credentials = {
    ...account.credentials,
    ...refreshed,
    base_url: currentBaseUrl
  }
  assertSafeUpstreamBaseUrl(currentBaseUrl)

  const updated = await updateAccountAsync(sourceAccountId, { credentials })
  if (!updated) throw new Error('Grok OAuth 账户不存在或无法更新')
  return {
    ...account,
    apiKey: stringCredential(credentials, 'access_token') ?? account.apiKey,
    refreshToken: stringCredential(credentials, 'refresh_token') ?? account.refreshToken,
    clientId: stringCredential(credentials, 'client_id') ?? account.clientId,
    expiresAt: stringCredential(credentials, 'expires_at') ?? account.expiresAt,
    baseUrl: currentBaseUrl,
    credentials: runtimeOpenAIAccountCredentials(credentials)
  }
}

async function waitForRefresh(
  refresh: Promise<DispatchAccountSecret>,
  signal?: AbortSignal
): Promise<DispatchAccountSecret> {
  if (!signal) return await refresh
  if (signal.aborted) throw new UpstreamRequestAbortedError('请求已取消')
  return await new Promise<DispatchAccountSecret>((resolve, reject) => {
    const abort = () => reject(new UpstreamRequestAbortedError('请求已取消'))
    signal.addEventListener('abort', abort, { once: true })
    void refresh.then(resolve, reject).finally(() => signal.removeEventListener('abort', abort))
  })
}

function stringCredential(credentials: Record<string, unknown>, key: string): string | undefined {
  const value = credentials[key]
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

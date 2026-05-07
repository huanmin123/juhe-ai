import type { AccountSummary } from '../../domain/types.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import {
  getSettings,
  listAccounts,
  markAccountCooldown,
  resolveProxyUrlForProfile,
  updateAccount
} from '../../storage/repositories.js'
import {
  buildOpenAIOAuthCredentials,
  refreshOpenAIOAuthToken
} from './openai-oauth.service.js'

export interface OpenAIOAuthAccessTokenRefreshOptions {
  leadSeconds?: number
  batchSize?: number
  retryBackoffSeconds?: number
  failureCooldownMinutes?: number
}

export interface OpenAIOAuthAccessTokenRefreshResult {
  scanned: number
  due: number
  refreshed: number
  failed: number
  cooldowned: number
  skippedBackoff: number
}

const retryBackoffByAccountId = new Map<string, number>()

export async function refreshOpenAIOAuthAccountAccessToken(account: AccountSummary): Promise<AccountSummary> {
  if (account.providerCode !== 'openai' || account.type !== 'oauth') {
    throw new Error('仅支持刷新 OpenAI OAuth 账户')
  }
  const credentials = account.credentials
  const refreshToken = stringCredential(credentials, 'refresh_token')
  if (!refreshToken) {
    throw new Error('OpenAI OAuth 缺少 refresh_token')
  }

  const tokenInfo = await refreshOpenAIOAuthToken({
    refreshToken,
    clientId: stringCredential(credentials, 'client_id'),
    proxyUrl: account.proxyProfileId ? resolveProxyUrlForProfile(account.proxyProfileId) : undefined
  })
  const nextCredentials = {
    ...credentials,
    ...buildOpenAIOAuthCredentials(tokenInfo, { refreshToken })
  }
  const updated = updateAccount(account.id, {
    credentials: nextCredentials,
    status: 'active',
    clearFailureState: true
  })
  if (!updated) {
    throw new Error('OpenAI OAuth 账户不存在或无法更新')
  }
  return updated
}

export async function refreshDueOpenAIOAuthAccessTokens(
  options: OpenAIOAuthAccessTokenRefreshOptions = {}
): Promise<OpenAIOAuthAccessTokenRefreshResult> {
  const settings = getSettings()
  const leadSeconds = boundedNumber(options.leadSeconds ?? settings.oauthAccessTokenRefreshLeadSeconds, 300, 60, 86400)
  const batchSize = boundedNumber(options.batchSize ?? settings.oauthAccessTokenRefreshBatchSize, 20, 1, 200)
  const retryBackoffSeconds = boundedNumber(options.retryBackoffSeconds ?? settings.oauthAccessTokenRefreshRetryBackoffSeconds, 300, 60, 86400)
  const failureCooldownMinutes = boundedNumber(options.failureCooldownMinutes ?? settings.defaultTemporaryUnschedulableMinutes, 5, 1, 1440)
  const now = Date.now()
  const leadMs = leadSeconds * 1000
  const retryBackoffMs = retryBackoffSeconds * 1000
  cleanupRetryBackoff(now)

  const eligibleAccounts = listAccounts()
    .filter(isActiveOpenAIOAuthAccountWithRefreshToken)
  const dueAccounts = eligibleAccounts
    .filter((account) => shouldPreRefreshAccessToken(account.credentials, now, leadMs))

  const result: OpenAIOAuthAccessTokenRefreshResult = {
    scanned: eligibleAccounts.length,
    due: dueAccounts.length,
    refreshed: 0,
    failed: 0,
    cooldowned: 0,
    skippedBackoff: 0
  }

  const candidates: AccountSummary[] = []
  for (const account of dueAccounts) {
    const expiredOrMissing = isAccessTokenExpiredOrMissing(account.credentials, now)
    const backoffUntil = retryBackoffByAccountId.get(account.id)
    if (!expiredOrMissing && backoffUntil !== undefined && backoffUntil > now) {
      result.skippedBackoff += 1
      continue
    }
    candidates.push(account)
    if (candidates.length >= batchSize) {
      break
    }
  }
  for (const account of candidates) {
    try {
      await refreshOpenAIOAuthAccountAccessToken(account)
      retryBackoffByAccountId.delete(account.id)
      result.refreshed += 1
    } catch (error) {
      result.failed += 1
      const expiredOrMissing = isAccessTokenExpiredOrMissing(account.credentials, Date.now())
      const message = errorMessage(error)
      logger.warn(errorLogFields(error, {
        event: 'openai_oauth_access_token_refresh_account_failed',
        accountId: account.id,
        accountName: account.name,
        accessTokenExpiredOrMissing: expiredOrMissing
      }), 'OpenAI OAuth Access Token 刷新失败')

      if (expiredOrMissing) {
        const until = new Date(Date.now() + failureCooldownMinutes * 60_000).toISOString()
        const updated = markAccountCooldown(account.id, until, `OpenAI OAuth Access Token 刷新失败：${message}`)
        if (updated) {
          result.cooldowned += 1
        }
      } else {
        retryBackoffByAccountId.set(account.id, Date.now() + retryBackoffMs)
      }
    }
  }

  return result
}

function isActiveOpenAIOAuthAccountWithRefreshToken(account: AccountSummary): boolean {
  return account.providerCode === 'openai'
    && account.type === 'oauth'
    && account.status === 'active'
    && account.schedulable
    && Boolean(stringCredential(account.credentials, 'refresh_token'))
}

function shouldPreRefreshAccessToken(credentials: Record<string, unknown>, now: number, leadMs: number): boolean {
  if (!stringCredential(credentials, 'access_token')) {
    return true
  }
  const expiresAt = parseCredentialExpiresAt(credentials)
  if (expiresAt === undefined) {
    return true
  }
  return expiresAt - now <= leadMs
}

function isAccessTokenExpiredOrMissing(credentials: Record<string, unknown>, now: number): boolean {
  if (!stringCredential(credentials, 'access_token')) {
    return true
  }
  const expiresAt = parseCredentialExpiresAt(credentials)
  return expiresAt === undefined || expiresAt <= now
}

function parseCredentialExpiresAt(credentials: Record<string, unknown>): number | undefined {
  const expiresAtText = stringCredential(credentials, 'expires_at')
  if (!expiresAtText) {
    return undefined
  }
  const expiresAt = Date.parse(expiresAtText)
  return Number.isFinite(expiresAt) ? expiresAt : undefined
}

function cleanupRetryBackoff(now: number): void {
  for (const [accountId, backoffUntil] of retryBackoffByAccountId.entries()) {
    if (backoffUntil <= now) {
      retryBackoffByAccountId.delete(accountId)
    }
  }
}

function stringCredential(credentials: Record<string, unknown>, key: string): string | undefined {
  const value = credentials[key]
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function errorMessage(error: unknown): string {
  const message = error instanceof Error ? error.message : 'OpenAI OAuth Access Token 刷新失败'
  return message.length > 240 ? `${message.slice(0, 237)}...` : message
}

function boundedNumber(value: unknown, fallback: number, min: number, max: number): number {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  if (!Number.isFinite(number)) return fallback
  return Math.min(Math.max(Math.trunc(number), min), max)
}

import type { AccountSummary } from '../../domain/types.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import {
  clearAccountFailureState,
  findAccountForTest,
  getSettings,
  listAccounts,
  markAccountException,
  resolveProxyUrlForProfile,
  updateAccount
} from '../../storage/repositories.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { requestDbService } from '../db-service/db-service-ipc.js'
import { clearGatewayRuntimeCache } from '../gateway/gateway-runtime-cache.service.js'
import {
  buildOpenAIOAuthCredentials,
  refreshOpenAIOAuthToken,
  type OpenAITokenInfo
} from './openai-oauth.service.js'

export interface OpenAIOAuthAccessTokenRefreshOptions {
  leadSeconds?: number
  batchSize?: number
  retryBackoffSeconds?: number
}

export interface OpenAIOAuthAccessTokenRefreshResult {
  scanned: number
  due: number
  refreshed: number
  failed: number
  exceptioned: number
  cooldowned: number
  skippedBackoff: number
}

export interface RefreshedOpenAIOAuthAccount {
  id: string
  credentials: Record<string, unknown>
  status?: string
}

const oauthTokenRefreshFailureThreshold = 3
const oauthTokenRefreshFailedErrorCode = 'oauth_token_refresh_failed'
const refreshFailureStateByAccountId = new Map<string, { count: number; backoffUntil: number }>()
const refreshQueueByAccountId = new Map<string, Promise<void>>()
let openAIOAuthTokenRefresher: OpenAIOAuthTokenRefresher = refreshOpenAIOAuthToken

type RefreshableOpenAIOAuthAccount = Pick<AccountSummary, 'id' | 'type' | 'credentials'> & Partial<Pick<AccountSummary, 'providerCode' | 'proxyProfileId' | 'status' | 'name'>>
type OpenAIOAuthTokenRefresher = (input: { refreshToken: string; clientId?: string; proxyUrl?: string; signal?: AbortSignal }) => Promise<OpenAITokenInfo>
type OpenAIOAuthAccountRefreshCallOptions = {
  access?: AccessScope
  signal?: AbortSignal
  force?: boolean
  leadSeconds?: number
  persistMode?: 'sync' | 'db-service'
  restoreFailureState?: boolean
}

export function setOpenAIOAuthTokenRefresherForTest(refresher?: OpenAIOAuthTokenRefresher): void {
  openAIOAuthTokenRefresher = refresher ?? refreshOpenAIOAuthToken
}

export async function refreshOpenAIOAuthAccountAccessToken(
  account: RefreshableOpenAIOAuthAccount,
  options: OpenAIOAuthAccountRefreshCallOptions = {}
): Promise<AccountSummary | RefreshedOpenAIOAuthAccount> {
  if ((account.providerCode !== undefined && account.providerCode !== 'openai') || account.type !== 'oauth') {
    throw new Error('仅支持刷新 OpenAI OAuth 账户')
  }
  return runWithAccountRefreshLock(account.id, () => refreshOpenAIOAuthAccountAccessTokenLocked(account, options))
}

async function refreshOpenAIOAuthAccountAccessTokenLocked(
  account: RefreshableOpenAIOAuthAccount,
  options: OpenAIOAuthAccountRefreshCallOptions
): Promise<AccountSummary | RefreshedOpenAIOAuthAccount> {
  let current = findLatestRefreshableOpenAIOAuthAccount(account.id, options.access)
  let retryWithLatestRefreshToken = false

  for (let attempt = 0; attempt < 2; attempt += 1) {
    if (!current) {
      throw new Error('OpenAI OAuth 账户不存在或无法刷新')
    }

    const credentials = current.credentials
    const refreshToken = stringCredential(credentials, 'refresh_token')
    if (!refreshToken) {
      throw new Error('OpenAI OAuth 缺少 refresh_token')
    }

    if (credentialsChanged(account.credentials, credentials) && !isAccessTokenExpiredOrMissing(credentials, Date.now())) {
      clearGatewayRuntimeCache()
      return current
    }

    if (options.force !== true && !retryWithLatestRefreshToken && !shouldPreRefreshAccessToken(credentials, Date.now(), refreshLeadMs(options.leadSeconds))) {
      return current
    }

    try {
      const tokenInfo = await openAIOAuthTokenRefresher({
        refreshToken,
        clientId: stringCredential(credentials, 'client_id'),
        proxyUrl: current.proxyProfileId ? resolveProxyUrlForProfile(current.proxyProfileId) : undefined,
        signal: options.signal
      })
      const nextCredentials = {
        ...credentials,
        ...buildOpenAIOAuthCredentials(tokenInfo, { refreshToken })
      }
      if (options.persistMode === 'db-service') {
        const updated = await persistOpenAIOAuthCredentialsViaDbService(current.id, nextCredentials)
        if (!updated) {
          throw new Error('OpenAI OAuth 账户不存在或无法更新')
        }
        return {
          ...current,
          credentials: nextCredentials
        }
      }
      const updated = updateAccount(current.id, {
        credentials: nextCredentials
      }, options.access)
      if (!updated) {
        throw new Error('OpenAI OAuth 账户不存在或无法更新')
      }
      return finalizeSuccessfulTokenRefresh(updated, options)
    } catch (error) {
      const recovered = tryRecoverOpenAIOAuthRefreshRace(current, options.access)
      if (isRecoverableOpenAIOAuthRefreshRaceError(error) && recovered.result === 'fresh') {
        logger.info({
          event: 'openai_oauth_access_token_refresh_race_recovered',
          accountId: recovered.account.id
        }, 'OpenAI OAuth Access Token 刷新竞争已恢复')
        clearGatewayRuntimeCache()
        return finalizeSuccessfulTokenRefresh(recovered.account, options)
      }
      if (isRecoverableOpenAIOAuthRefreshRaceError(error) && recovered.result === 'retry' && attempt === 0) {
        logger.info({
          event: 'openai_oauth_access_token_refresh_retry_with_latest_refresh_token',
          accountId: recovered.account.id
        }, 'OpenAI OAuth Access Token 使用最新 Refresh Token 重试刷新')
        current = recovered.account
        retryWithLatestRefreshToken = true
        continue
      }
      throw error
    }
  }

  throw new Error('OpenAI OAuth Access Token 刷新失败')
}

async function persistOpenAIOAuthCredentialsViaDbService(accountId: string, credentials: Record<string, unknown>): Promise<boolean> {
  const result = await requestDbService({
    type: 'update_openai_oauth_credentials',
    accountId,
    credentials
  })
  if (result.updated) {
    clearGatewayRuntimeCache()
  }
  return result.updated
}

export async function refreshDueOpenAIOAuthAccessTokens(
  options: OpenAIOAuthAccessTokenRefreshOptions = {}
): Promise<OpenAIOAuthAccessTokenRefreshResult> {
  const settings = getSettings()
  const leadSeconds = boundedNumber(options.leadSeconds ?? settings.oauthAccessTokenRefreshLeadSeconds, 300, 60, 86400)
  const batchSize = boundedNumber(options.batchSize ?? settings.oauthAccessTokenRefreshBatchSize, 20, 1, 200)
  const retryBackoffSeconds = boundedNumber(options.retryBackoffSeconds ?? settings.oauthAccessTokenRefreshRetryBackoffSeconds, 300, 0, 86400)
  const now = Date.now()
  const leadMs = leadSeconds * 1000
  const retryBackoffMs = retryBackoffSeconds * 1000
  cleanupRefreshFailureBackoff(now)

  const eligibleAccounts = listAccounts()
    .filter(isExistingOpenAIOAuthAccountWithRefreshToken)
  const dueAccounts = eligibleAccounts
    .filter((account) => shouldPreRefreshAccessToken(account.credentials, now, leadMs))

  const result: OpenAIOAuthAccessTokenRefreshResult = {
    scanned: eligibleAccounts.length,
    due: dueAccounts.length,
    refreshed: 0,
    failed: 0,
    exceptioned: 0,
    cooldowned: 0,
    skippedBackoff: 0
  }

  const candidates: AccountSummary[] = []
  for (const account of dueAccounts) {
    const failureState = refreshFailureStateByAccountId.get(account.id)
    if (failureState?.backoffUntil !== undefined && failureState.backoffUntil > now) {
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
      await refreshOpenAIOAuthAccountAccessToken(account, { force: false, leadSeconds, restoreFailureState: false })
      refreshFailureStateByAccountId.delete(account.id)
      result.refreshed += 1
    } catch (error) {
      result.failed += 1
      const expiredOrMissing = isAccessTokenExpiredOrMissing(account.credentials, Date.now())
      const message = errorMessage(error)
      const failureState = recordRefreshFailure(account.id, Date.now() + retryBackoffMs)
      logger.warn(errorLogFields(error, {
        event: 'openai_oauth_access_token_refresh_account_failed',
        accountId: account.id,
        accountName: account.name,
        failureCount: failureState.count,
        accessTokenExpiredOrMissing: expiredOrMissing
      }), 'OpenAI OAuth Access Token 刷新失败')

      if (failureState.count >= oauthTokenRefreshFailureThreshold) {
        const updated = markAccountException(
          account.id,
          oauthTokenRefreshFailedErrorCode,
          `OpenAI OAuth Access Token 连续 ${failureState.count} 次刷新失败：${message}`,
          { preserveDisabled: false }
        )
        if (updated) {
          clearGatewayRuntimeCache()
          result.exceptioned += 1
        }
      }
    }
  }

  return result
}

function isExistingOpenAIOAuthAccountWithRefreshToken(account: AccountSummary): boolean {
  return account.providerCode === 'openai'
    && account.type === 'oauth'
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

function recordRefreshFailure(accountId: string, backoffUntil: number): { count: number; backoffUntil: number } {
  const previous = refreshFailureStateByAccountId.get(accountId)
  const next = {
    count: (previous?.count ?? 0) + 1,
    backoffUntil
  }
  refreshFailureStateByAccountId.set(accountId, next)
  return next
}

function cleanupRefreshFailureBackoff(now: number): void {
  for (const [accountId, failureState] of refreshFailureStateByAccountId.entries()) {
    if (failureState.backoffUntil <= now) {
      refreshFailureStateByAccountId.set(accountId, { ...failureState, backoffUntil: 0 })
    }
  }
}

function findLatestRefreshableOpenAIOAuthAccount(accountId: string, access?: AccessScope): AccountSummary | undefined {
  const account = findAccountForTest(accountId, access)
  if (!account || account.providerCode !== 'openai' || account.type !== 'oauth') {
    return undefined
  }
  return account
}

async function runWithAccountRefreshLock<T>(accountId: string, task: () => Promise<T>): Promise<T> {
  const previous = refreshQueueByAccountId.get(accountId) ?? Promise.resolve()
  const ready = previous.catch(() => undefined)
  let release: () => void = () => {}
  const current = ready.then(() => new Promise<void>((resolve) => {
    release = resolve
  }))
  refreshQueueByAccountId.set(accountId, current)
  await ready
  try {
    return await task()
  } finally {
    release()
    if (refreshQueueByAccountId.get(accountId) === current) {
      refreshQueueByAccountId.delete(accountId)
    }
  }
}

function finalizeSuccessfulTokenRefresh(account: AccountSummary, options: OpenAIOAuthAccountRefreshCallOptions = {}): AccountSummary | RefreshedOpenAIOAuthAccount {
  if (options.persistMode === 'db-service') {
    clearGatewayRuntimeCache()
    return account
  }
  if (options.restoreFailureState === false) {
    clearGatewayRuntimeCache()
    return account
  }
  const updated = account.status !== 'disabled' && account.status !== 'error'
    ? clearAccountFailureState(account.id, options.access) ?? account
    : account
  clearGatewayRuntimeCache()
  return updated
}

function tryRecoverOpenAIOAuthRefreshRace(
  usedAccount: AccountSummary,
  access?: AccessScope
): { result: 'fresh' | 'retry'; account: AccountSummary } | { result: 'none' } {
  const latest = findLatestRefreshableOpenAIOAuthAccount(usedAccount.id, access)
  if (!latest) {
    return { result: 'none' }
  }

  const usedRefreshToken = stringCredential(usedAccount.credentials, 'refresh_token')
  const latestRefreshToken = stringCredential(latest.credentials, 'refresh_token')
  const refreshTokenChanged = Boolean(usedRefreshToken && latestRefreshToken && usedRefreshToken !== latestRefreshToken)
  const accessTokenChanged = stringCredential(usedAccount.credentials, 'access_token') !== stringCredential(latest.credentials, 'access_token')
  const latestAccessTokenUsable = !isAccessTokenExpiredOrMissing(latest.credentials, Date.now())

  if (latestAccessTokenUsable && (refreshTokenChanged || accessTokenChanged || isCredentialExpiresAtLater(latest.credentials, usedAccount.credentials))) {
    return { result: 'fresh', account: latest }
  }
  if (refreshTokenChanged) {
    return { result: 'retry', account: latest }
  }
  return { result: 'none' }
}

function isRecoverableOpenAIOAuthRefreshRaceError(error: unknown): boolean {
  const message = errorText(error).toLowerCase()
  return message.includes('refresh_token_reused')
    || message.includes('refresh token has already been used')
    || message.includes('already been used to generate a new access token')
    || message.includes('invalid_grant')
}

function errorText(error: unknown): string {
  if (error instanceof Error) {
    const cause = 'cause' in error ? (error as Error & { cause?: unknown }).cause : undefined
    return `${error.message} ${cause ? errorText(cause) : ''}`
  }
  if (typeof error === 'string') return error
  try {
    return JSON.stringify(error)
  } catch {
    return ''
  }
}

function refreshLeadMs(leadSeconds: unknown): number {
  return boundedNumber(leadSeconds, 60, 0, 86400) * 1000
}

function credentialsChanged(left: Record<string, unknown>, right: Record<string, unknown>): boolean {
  return stringCredential(left, 'access_token') !== stringCredential(right, 'access_token')
    || stringCredential(left, 'refresh_token') !== stringCredential(right, 'refresh_token')
    || stringCredential(left, 'expires_at') !== stringCredential(right, 'expires_at')
}

function isCredentialExpiresAtLater(nextCredentials: Record<string, unknown>, currentCredentials: Record<string, unknown>): boolean {
  const nextExpiresAt = parseCredentialExpiresAt(nextCredentials)
  const currentExpiresAt = parseCredentialExpiresAt(currentCredentials)
  return nextExpiresAt !== undefined && (currentExpiresAt === undefined || nextExpiresAt > currentExpiresAt)
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

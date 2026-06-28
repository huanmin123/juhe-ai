import type { AccountSummary } from '../../domain/types.js'
import { isGptVendorCode, isOpenAIProtocolProfile } from '../../domain/provider-protocol.js'
import { runtimeConfig } from '../../config/runtime.js'
import { createAppCache } from '../../shared/cache.js'
import { registerGatewayRuntimeCacheInvalidator } from '../../shared/gateway-cache-invalidation.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { fixedRetryPolicy, retryAttemptCount, retryDueAtMs, shouldRetryPolicyAttempt } from '../../shared/retry-policy.js'
import {
  clearAccountFailureState,
  clearAccountFailureStateResult,
  findAccountForTest,
  getSettings,
  getSettingsAsync,
  listOpenAIOAuthAccountsDueForAccessTokenRefresh,
  listOpenAIOAuthAccountsDueForAccessTokenRefreshAsync,
  markAccountException,
  resolveProxyUrlForProfile,
  updateAccount
} from '../../storage/repositories.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { requestDbService } from '../db-service/db-service-ipc.js'
import type { DbServiceOpenAIOAuthRefreshAccount, DbServiceOperation, DbServiceOperationResult } from '../db-service/db-service-types.js'
import { requestBackgroundWorkerDbService } from '../background/background-ipc.js'
import { clearGatewayRuntimeCache } from '../gateway/runtime/runtime-cache.service.js'
import {
  buildOpenAIOAuthCredentials,
  refreshOpenAIOAuthToken,
  sanitizeOpenAIOAuthErrorMessage,
  type OpenAITokenInfo
} from './openai-oauth.service.js'

export interface OpenAIOAuthAccessTokenRefreshOptions {
  leadSeconds?: number
  batchSize?: number
  retryBackoffSeconds?: number
  persistMode?: 'sync' | 'db-service'
  accountIds?: string[]
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
export const OPENAI_OAUTH_TOKEN_REFRESH_FAILED_ERROR_CODE = 'oauth_token_refresh_failed'
const openAIOAuthRefreshRaceRetryPolicy = fixedRetryPolicy('openai_oauth_access_token_refresh_race', 0, 1)
const internalOpenAIOAuthRefreshAccess: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
const refreshFailureStateByAccountId = new Map<string, { count: number; backoffUntil: number }>()
const refreshQueueByAccountId = new Map<string, Promise<void>>()
const recentRefreshTtlMs = 30_000
const recentRefreshByAccountId = createAppCache<string, OpenAIOAuthRefreshAccount>({
  name: 'openai-oauth:recent-refresh',
  max: 5000,
  ttlMs: recentRefreshTtlMs
})
let openAIOAuthTokenRefresher: OpenAIOAuthTokenRefresher = refreshOpenAIOAuthToken

type RefreshableOpenAIOAuthAccount = Pick<AccountSummary, 'id' | 'providerCode' | 'type' | 'credentials'> & Partial<Pick<AccountSummary, 'providerProtocolProfileId' | 'protocolCode' | 'protocolVersion' | 'proxyProfileId' | 'status' | 'name' | 'lastErrorCode'>> & {
  proxyUrl?: string
}
type OpenAIOAuthRefreshAccount = RefreshableOpenAIOAuthAccount & Partial<Pick<AccountSummary, 'systemAccountId' | 'concurrencyLimit' | 'currentConcurrency' | 'priority' | 'superPriorityEnabled' | 'fallbackEnabled' | 'schedulable' | 'todayUsage' | 'usage' | 'permissions'>> & {
  proxyUrl?: string
}
type OpenAIOAuthTokenRefresher = (input: { refreshToken: string; clientId?: string; proxyUrl?: string; signal?: AbortSignal }) => Promise<OpenAITokenInfo>
type OpenAIOAuthDbServiceRequester = <T extends DbServiceOperation>(
  operation: T,
  persistMode?: 'sync' | 'db-service'
) => Promise<DbServiceOperationResult<T>>
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

let openAIOAuthDbServiceRequesterForTest: OpenAIOAuthDbServiceRequester | undefined

export function setOpenAIOAuthDbServiceRequesterForTest(requester?: OpenAIOAuthDbServiceRequester): void {
  openAIOAuthDbServiceRequesterForTest = requester
}

export function clearOpenAIOAuthRecentRefreshCache(): void {
  recentRefreshByAccountId.clear()
}

export async function refreshOpenAIOAuthAccountAccessToken(
  account: RefreshableOpenAIOAuthAccount,
  options: OpenAIOAuthAccountRefreshCallOptions = {}
): Promise<AccountSummary | RefreshedOpenAIOAuthAccount> {
  if (!isOpenAIOAuthRefreshAccount(account)) {
    throw new Error('仅支持刷新 OpenAI OAuth 账户')
  }
  return runWithAccountRefreshLock(account.id, () => refreshOpenAIOAuthAccountAccessTokenLocked(account, options))
}

async function refreshOpenAIOAuthAccountAccessTokenLocked(
  account: RefreshableOpenAIOAuthAccount,
  options: OpenAIOAuthAccountRefreshCallOptions
): Promise<AccountSummary | RefreshedOpenAIOAuthAccount> {
  const persistMode = effectivePersistMode(options)
  const recent = readRecentOpenAIOAuthRefresh(account, options, persistMode)
  if (recent) {
    return recent
  }
  let current = await findLatestRefreshableOpenAIOAuthAccount(account, options, persistMode)
  let retryWithLatestRefreshToken = false

  for (let attempt = 0; attempt < retryAttemptCount(openAIOAuthRefreshRaceRetryPolicy); attempt += 1) {
    if (!current) {
      throw new Error('OpenAI OAuth 账户不存在或无法刷新')
    }

    const credentials = current.credentials
    const refreshToken = stringCredential(credentials, 'refresh_token')
    if (!refreshToken) {
      throw new Error('OpenAI OAuth 账户缺少刷新令牌')
    }

    if (credentialsChanged(account.credentials, credentials) && !isAccessTokenExpiredOrMissing(credentials, Date.now())) {
      clearGatewayRuntimeCache()
      rememberRecentOpenAIOAuthRefresh(current, persistMode)
      return current
    }

    if (options.force !== true && !retryWithLatestRefreshToken && !shouldPreRefreshAccessToken(credentials, Date.now(), refreshLeadMs(options.leadSeconds))) {
      return current
    }

    try {
      const tokenInfo = await openAIOAuthTokenRefresher({
        refreshToken,
        clientId: stringCredential(credentials, 'client_id'),
        proxyUrl: resolveRefreshProxyUrl(current, persistMode),
        signal: options.signal
      })
      const nextCredentials = {
        ...credentials,
        ...buildOpenAIOAuthCredentials(tokenInfo, { refreshToken })
      }
      if (persistMode === 'db-service') {
        const updated = await persistOpenAIOAuthCredentialsViaDbService(current.id, nextCredentials, persistMode)
        if (!updated) {
          throw new Error('OpenAI OAuth 账户不存在或无法更新')
        }
        const refreshed = {
          ...current,
          credentials: nextCredentials
        }
        rememberRecentOpenAIOAuthRefresh(refreshed, persistMode)
        return refreshed
      }
      const updated = updateAccount(current.id, {
        credentials: nextCredentials
      }, options.access ?? internalOpenAIOAuthRefreshAccess)
      if (!updated) {
        throw new Error('OpenAI OAuth 账户不存在或无法更新')
      }
      return finalizeSuccessfulTokenRefresh(updated, options)
    } catch (error) {
      const recovered = await tryRecoverOpenAIOAuthRefreshRace(current, options, persistMode)
      if (isRecoverableOpenAIOAuthRefreshRaceError(error) && recovered.result === 'fresh') {
        logger.info({
          event: 'openai_oauth_access_token_refresh_race_recovered',
          accountId: recovered.account.id
        }, 'OpenAI OAuth Access Token 刷新竞争已恢复')
        clearGatewayRuntimeCache()
        rememberRecentOpenAIOAuthRefresh(recovered.account, persistMode)
        return finalizeSuccessfulTokenRefresh(recovered.account, options)
      }
      if (
        isRecoverableOpenAIOAuthRefreshRaceError(error)
        && recovered.result === 'retry'
        && shouldRetryPolicyAttempt(attempt, openAIOAuthRefreshRaceRetryPolicy)
      ) {
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

  throw new Error('OpenAI OAuth 访问令牌刷新失败')
}

async function persistOpenAIOAuthCredentialsViaDbService(accountId: string, credentials: Record<string, unknown>, persistMode: 'sync' | 'db-service'): Promise<boolean> {
  const result = await requestOpenAIOAuthDbService({
    type: 'update_openai_oauth_credentials',
    accountId,
    credentials
  }, persistMode)
  if (result.updated) {
    clearGatewayRuntimeCache()
  }
  return result.updated
}

export async function refreshDueOpenAIOAuthAccessTokens(
  options: OpenAIOAuthAccessTokenRefreshOptions = {}
): Promise<OpenAIOAuthAccessTokenRefreshResult> {
  const persistMode = effectivePersistMode({ persistMode: options.persistMode })
  const settings = runtimeConfig.databaseDriver === 'postgres'
    ? await getSettingsAsync()
    : getSettings()
  const leadSeconds = options.leadSeconds === undefined
    ? settingsInteger(settings, 'oauthAccessTokenRefreshLeadSeconds', 60, 86400)
    : optionInteger(options.leadSeconds, 'leadSeconds', 60, 86400)
  const batchSize = options.batchSize === undefined
    ? settingsInteger(settings, 'oauthAccessTokenRefreshBatchSize', 1, 200)
    : optionInteger(options.batchSize, 'batchSize', 1, 200)
  const retryBackoffSeconds = options.retryBackoffSeconds === undefined
    ? settingsInteger(settings, 'oauthAccessTokenRefreshRetryBackoffSeconds', 0, 86400)
    : optionInteger(options.retryBackoffSeconds, 'retryBackoffSeconds', 0, 86400)
  const now = Date.now()
  const leadMs = leadSeconds * 1000
  const retryBackoffMs = retryBackoffSeconds * 1000
  const retryBackoffPolicy = fixedRetryPolicy('openai_oauth_access_token_refresh_backoff', retryBackoffMs)
  cleanupRefreshFailureBackoff(now)
  const accountIdFilter = normalizedRefreshAccountIdSet(options.accountIds)

  const dueAccounts = (runtimeConfig.databaseDriver === 'postgres'
    ? await listOpenAIOAuthAccountsDueForAccessTokenRefreshAsync({
      leadSeconds,
      limit: batchSize + refreshFailureStateByAccountId.size,
      stoppedErrorCode: OPENAI_OAUTH_TOKEN_REFRESH_FAILED_ERROR_CODE
    })
    : listOpenAIOAuthAccountsDueForAccessTokenRefresh({
    leadSeconds,
    limit: batchSize + refreshFailureStateByAccountId.size,
    stoppedErrorCode: OPENAI_OAUTH_TOKEN_REFRESH_FAILED_ERROR_CODE
    })).filter((account) =>
    (!accountIdFilter || accountIdFilter.has(account.id)) &&
    isExistingOpenAIOAuthAccountWithRefreshToken(account) &&
    shouldPreRefreshAccessToken(account.credentials, now, leadMs)
  )

  const result: OpenAIOAuthAccessTokenRefreshResult = {
    scanned: dueAccounts.length,
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
      await refreshOpenAIOAuthAccountAccessToken(account, { force: false, leadSeconds, restoreFailureState: false, persistMode })
      refreshFailureStateByAccountId.delete(account.id)
      await restoreOpenAIOAuthTokenRefreshFailureIfRecovered(account, persistMode)
      result.refreshed += 1
    } catch (error) {
      result.failed += 1
      const expiredOrMissing = isAccessTokenExpiredOrMissing(account.credentials, Date.now())
      const message = errorMessage(error)
      const failureState = recordRefreshFailure(account.id, retryDueAtMs(retryBackoffPolicy))
      logger.warn(errorLogFields(error, {
        event: 'openai_oauth_access_token_refresh_account_failed',
        accountId: account.id,
        accountName: account.name,
        failureCount: failureState.count,
        accessTokenExpiredOrMissing: expiredOrMissing
      }), 'OpenAI OAuth 访问令牌刷新失败')

      if (failureState.count >= oauthTokenRefreshFailureThreshold && account.status !== 'pending_test') {
        const updated = await requestOpenAIOAuthDbService({
          type: 'mark_account_exception',
          accountId: account.id,
          errorCode: OPENAI_OAUTH_TOKEN_REFRESH_FAILED_ERROR_CODE,
          reason: openAIOAuthTokenRefreshStoppedMessage(failureState.count, message)
        }, persistMode)
        if (updated.updated) {
          clearGatewayRuntimeCache()
          refreshFailureStateByAccountId.delete(account.id)
          result.exceptioned += 1
        }
      }
    }
  }

  return result
}

function isExistingOpenAIOAuthAccountWithRefreshToken(account: AccountSummary): boolean {
  return isOpenAIOAuthRefreshAccount(account)
    && account.accessType !== 'authorized'
    && !shouldStopOpenAIOAuthBackgroundRefresh(account)
    && Boolean(stringCredential(account.credentials, 'refresh_token'))
}

function isOpenAIOAuthRefreshAccount(account: RefreshableOpenAIOAuthAccount | AccountSummary | undefined): boolean {
  return Boolean(account
    && isGptVendorCode(account.providerCode)
    && isOpenAIProtocolProfile(account)
    && account.type === 'oauth')
}

function shouldStopOpenAIOAuthBackgroundRefresh(account: AccountSummary): boolean {
  return account.status === 'error'
    && account.lastErrorCode === OPENAI_OAUTH_TOKEN_REFRESH_FAILED_ERROR_CODE
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

async function findLatestRefreshableOpenAIOAuthAccount(
  account: RefreshableOpenAIOAuthAccount,
  options: OpenAIOAuthAccountRefreshCallOptions,
  persistMode: 'sync' | 'db-service'
): Promise<OpenAIOAuthRefreshAccount | undefined> {
  if (persistMode === 'db-service') {
    const latest = await requestOpenAIOAuthDbService({
      type: 'find_openai_oauth_account_for_refresh',
      accountId: account.id
    })
    return latest ? normalizeDbServiceRefreshAccount(latest) : undefined
  }

  const latest = findAccountForTest(account.id, options.access)
  if (!isOpenAIOAuthRefreshAccount(latest)) {
    return undefined
  }
  return latest
}

async function restoreOpenAIOAuthTokenRefreshFailureIfRecovered(account: AccountSummary, persistMode: 'sync' | 'db-service'): Promise<void> {
  if (account.status !== 'error' || account.lastErrorCode !== OPENAI_OAUTH_TOKEN_REFRESH_FAILED_ERROR_CODE) {
    return
  }
  const restored = await requestOpenAIOAuthDbService({
    type: 'clear_account_failure_state',
    accountId: account.id
  }, persistMode)
  if (restored.changed && restored.accountStatus === 'active') {
    clearGatewayRuntimeCache()
    logger.info({
      event: 'openai_oauth_access_token_refresh_account_restored',
      accountId: account.id,
      accountName: account.name
    }, 'OpenAI OAuth Access Token 后台刷新成功，已自动恢复此前刷新失败异常')
  }
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

function finalizeSuccessfulTokenRefresh(account: OpenAIOAuthRefreshAccount, options: OpenAIOAuthAccountRefreshCallOptions = {}): AccountSummary | RefreshedOpenAIOAuthAccount {
  if (effectivePersistMode(options) === 'db-service') {
    clearGatewayRuntimeCache()
    return account
  }
  if (options.restoreFailureState === false) {
    clearGatewayRuntimeCache()
    return account
  }
  const updated = account.status !== 'pending_test' && account.status !== 'disabled' && (account.status !== 'error' || account.lastErrorCode === OPENAI_OAUTH_TOKEN_REFRESH_FAILED_ERROR_CODE)
    ? clearAccountFailureState(account.id, options.access) ?? account as AccountSummary
    : account
  clearGatewayRuntimeCache()
  return updated
}

async function tryRecoverOpenAIOAuthRefreshRace(
  usedAccount: OpenAIOAuthRefreshAccount,
  options: OpenAIOAuthAccountRefreshCallOptions,
  persistMode: 'sync' | 'db-service'
): Promise<{ result: 'fresh' | 'retry'; account: OpenAIOAuthRefreshAccount } | { result: 'none' }> {
  const latest = await findLatestRefreshableOpenAIOAuthAccount(usedAccount, options, persistMode)
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

function normalizeDbServiceRefreshAccount(account: DbServiceOpenAIOAuthRefreshAccount): OpenAIOAuthRefreshAccount {
  return {
    ...account,
    credentials: { ...account.credentials },
    proxyUrl: account.proxyUrl
  }
}

function readRecentOpenAIOAuthRefresh(
  account: RefreshableOpenAIOAuthAccount,
  options: OpenAIOAuthAccountRefreshCallOptions,
  persistMode: 'sync' | 'db-service'
): OpenAIOAuthRefreshAccount | undefined {
  if (persistMode !== 'db-service' || options.force === true) {
    return undefined
  }
  const cached = recentRefreshByAccountId.get(account.id)
  if (!cached || isAccessTokenExpiredOrMissing(cached.credentials, Date.now())) {
    return undefined
  }
  if (!credentialsChanged(account.credentials, cached.credentials)) {
    return undefined
  }
  return cloneOpenAIOAuthRefreshAccount(cached)
}

function rememberRecentOpenAIOAuthRefresh(account: OpenAIOAuthRefreshAccount, persistMode: 'sync' | 'db-service'): void {
  if (persistMode !== 'db-service' || isAccessTokenExpiredOrMissing(account.credentials, Date.now())) {
    return
  }
  recentRefreshByAccountId.set(account.id, cloneOpenAIOAuthRefreshAccount(account), { ttlMs: recentRefreshTtlMs })
}

function cloneOpenAIOAuthRefreshAccount(account: OpenAIOAuthRefreshAccount): OpenAIOAuthRefreshAccount {
  return {
    ...account,
    credentials: { ...account.credentials }
  }
}

function resolveRefreshProxyUrl(account: OpenAIOAuthRefreshAccount, persistMode: 'sync' | 'db-service'): string | undefined {
  if (account.proxyUrl || persistMode === 'db-service') {
    return account.proxyUrl
  }
  return account.proxyProfileId ? resolveProxyUrlForProfile(account.proxyProfileId) : undefined
}

function effectivePersistMode(options: OpenAIOAuthAccountRefreshCallOptions): 'sync' | 'db-service' {
  return options.persistMode ?? (runtimeConfig.processRole === 'server' || runtimeConfig.processRole === 'worker' ? 'db-service' : 'sync')
}

function normalizedRefreshAccountIdSet(accountIds: string[] | undefined): Set<string> | undefined {
  if (!accountIds) return undefined
  const ids = accountIds.map((id) => id.trim()).filter(Boolean)
  return ids.length ? new Set(ids) : undefined
}

async function requestOpenAIOAuthDbService<T extends DbServiceOperation>(
  operation: T,
  persistMode: 'sync' | 'db-service' = effectivePersistMode({})
): Promise<import('../db-service/db-service-types.js').DbServiceOperationResult<T>> {
  if (openAIOAuthDbServiceRequesterForTest) {
    return openAIOAuthDbServiceRequesterForTest(operation, persistMode)
  }
  if (persistMode === 'sync') {
    return runLocalOpenAIOAuthDbServiceOperation(operation)
  }
  if (runtimeConfig.processRole === 'worker') {
    const result = await requestBackgroundWorkerDbService(operation)
    if (result === undefined) {
      throw new Error(`后台 worker DB service 请求失败：${operation.type}`)
    }
    return result
  }
  return await requestDbService(operation)
}

function runLocalOpenAIOAuthDbServiceOperation<T extends DbServiceOperation>(operation: T): DbServiceOperationResult<T> {
  if (operation.type === 'clear_account_failure_state') {
    if (operation.authorizedBinding) {
      throw new Error('单进程 worker 回归不支持授权账号失败状态清理')
    }
    const result = clearAccountFailureStateResult(operation.accountId, internalOpenAIOAuthRefreshAccess, {
      allowPendingTestRestore: operation.allowPendingTestRestore,
      allowErrorRestore: operation.allowErrorRestore
    })
    if (result.changed) {
      clearGatewayRuntimeCache()
    }
    return { changed: result.changed, accountStatus: result.account?.status } as DbServiceOperationResult<T>
  }
  if (operation.type === 'mark_account_exception') {
    const updated = markAccountException(operation.accountId, operation.errorCode, operation.reason, {
      preserveDisabled: operation.preserveDisabled
    })
    if (updated) {
      clearGatewayRuntimeCache()
    }
    return { updated: Boolean(updated), accountStatus: updated?.status } as DbServiceOperationResult<T>
  }
  throw new Error(`单进程 worker 回归不支持 DB service 操作：${operation.type}`)
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
  return optionalInteger(leadSeconds, 'leadSeconds', 60, 0, 86400) * 1000
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
  const message = sanitizeOpenAIOAuthErrorMessage(error instanceof Error ? error.message : 'OpenAI OAuth 访问令牌刷新失败')
  return message.length > 240 ? `${message.slice(0, 237)}...` : message
}

function openAIOAuthTokenRefreshStoppedMessage(failureCount: number, lastError: string): string {
  return [
    `OpenAI OAuth 访问令牌连续 ${failureCount} 次后台刷新失败，已停止自动刷新。`,
    '该 Refresh Token 可能已失效、被重复使用，或账号授权已被上游撤销。',
    '请在账户页手动刷新、重新登录/重新授权该 OAuth 账号；如果不再使用，建议禁用或删除该账号。',
    `最后错误：${lastError}`
  ].join(' ').slice(0, 1000)
}

function settingsInteger(settings: Record<string, unknown>, key: string, min: number, max: number): number {
  return optionInteger(settings[key], `系统设置 ${key}`, min, max)
}

function optionalInteger(value: unknown, label: string, fallback: number, min: number, max: number): number {
  return value === undefined ? fallback : optionInteger(value, label, min, max)
}

function optionInteger(value: unknown, label: string, min: number, max: number): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value)) {
    throw new Error(`${label} 必须是整数`)
  }
  if (value < min || value > max) {
    throw new Error(`${label} 必须在 ${min} 到 ${max} 之间`)
  }
  return value
}

registerGatewayRuntimeCacheInvalidator(clearOpenAIOAuthRecentRefreshCache)

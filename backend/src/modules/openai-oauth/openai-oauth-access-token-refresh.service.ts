import { createHash, randomBytes } from 'node:crypto'

import type { AccountSummary } from '../../domain/types.js'
import { GPT_VENDOR_CODE, isGptVendorCode, isOpenAIProtocolProfile } from '../../domain/provider-protocol.js'
import { runtimeConfig } from '../../config/runtime.js'
import { createAppCache } from '../../shared/cache.js'
import { registerGatewayRuntimeCacheInvalidator } from '../../shared/gateway-cache-invalidation.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { runRedisOperationWithDeadline, type RedisCommandClient } from '../../shared/redis-client.js'
import { redisNamespacedKey } from '../../shared/redis-namespace.js'
import { fixedRetryPolicy, retryAttemptCount, retryDueAtMs, shouldRetryPolicyAttempt } from '../../shared/retry-policy.js'
import { createRuntimeStateStore } from '../../shared/runtime-state-store.js'
import { runWithGlobalBackgroundConcurrencySlot } from '../../shared/concurrency-governor.js'
import {
  clearAccountFailureState,
  clearAccountFailureStateResult,
  getSettings,
  getSettingsAsync,
  listOpenAIOAuthAccountsDueForAccessTokenRefresh,
  listOpenAIOAuthAccountsDueForAccessTokenRefreshAsync,
  markOpenAIOAuthLocalConfigurationExceptionIfCurrent,
  resolveProxyUrlsForProfiles,
  type OpenAIOAuthRefreshCandidateResult
} from '../../storage/repositories.js'
import { findOAuthCredentialRotationAccountAsync, rotateOAuthCredentialsAsync } from '../../storage/oauth-credential-rotation.repository.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { requestDbService } from '../db-service/db-service-ipc.js'
import type { DbServiceOpenAIOAuthRefreshAccount, DbServiceOperation, DbServiceOperationResult } from '../db-service/db-service-types.js'
import { requestBackgroundWorkerDbService } from '../background/background-ipc.js'
import { clearGatewayRuntimeCache } from '../gateway/runtime/runtime-cache.service.js'
import {
  ProviderOAuthRefreshLockBusyError,
  runWithProviderOAuthRefreshLock
} from '../providers/drivers/_shared/oauth-refresh-lock.js'
import {
  buildOpenAIOAuthCredentials,
  refreshOpenAIOAuthToken,
  sanitizeOpenAIOAuthErrorMessage,
  type OpenAITokenInfo
} from './openai-oauth.service.js'

const openAIOAuthLegacyRefreshLockStore = createRuntimeStateStore('openai-oauth:refresh-locks')

export interface OpenAIOAuthAccessTokenRefreshOptions {
  leadSeconds?: number
  batchSize?: number
  retryBackoffSeconds?: number
  persistMode?: 'sync' | 'db-service'
  accountIds?: string[]
  startAdmissionBudgetMs?: number
  signal?: AbortSignal
}

export interface OpenAIOAuthAccessTokenRefreshResult {
  scanned: number
  due: number
  refreshed: number
  failed: number
  exceptioned: number
  cooldowned: number
  skippedBackoff: number
  started: number
  skippedLocked: number
  deferredBudget: number
}

export interface RefreshedOpenAIOAuthAccount {
  id: string
  credentials: Record<string, unknown>
  status?: string
}

const oauthTokenRefreshFailureThreshold = 3
export const OPENAI_OAUTH_TOKEN_REFRESH_FAILED_ERROR_CODE = 'oauth_token_refresh_failed'
export const OPENAI_OAUTH_TOKEN_REFRESH_LOCAL_CONFIGURATION_ERROR_CODE = 'oauth_token_refresh_local_configuration_invalid'
export const openAIOAuthRefreshManagedErrorCodes = [
  OPENAI_OAUTH_TOKEN_REFRESH_FAILED_ERROR_CODE,
  OPENAI_OAUTH_TOKEN_REFRESH_LOCAL_CONFIGURATION_ERROR_CODE
] as const
const openAIOAuthRefreshRaceRetryPolicy = fixedRetryPolicy('openai_oauth_access_token_refresh_race', 0, 1)
const internalOpenAIOAuthRefreshAccess: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
interface OpenAIOAuthRefreshFailureState {
  count: number
  localConfigurationCount: number
  backoffUntil: number
  configRevision: number
  applied: boolean
  mutationId: string
  snapshot?: string
}

export class OpenAIOAuthRefreshLocalConfigurationError extends Error {
  readonly failureKind = 'local_configuration' as const
  readonly expectedConfigRevision?: number

  constructor(message: string, options: { cause?: unknown; expectedConfigRevision?: number } = {}) {
    super(message, options)
    this.name = 'OpenAIOAuthRefreshLocalConfigurationError'
    this.expectedConfigRevision = normalizedConfigRevision(options.expectedConfigRevision)
  }
}

const refreshFailureStateByAccountId = new Map<string, OpenAIOAuthRefreshFailureState>()
const recentRefreshTtlMs = 30_000
const openAIOAuthRefreshFailureStateTtlMs = 7 * 24 * 60 * 60 * 1000
const openAIOAuthRefreshBatchConcurrency = runtimeConfig.concurrency.globalMax
const openAIOAuthRefreshStartAdmissionBudgetMs = 55_000
const recentRefreshByAccountId = createAppCache<string, OpenAIOAuthRefreshAccount>({
  name: 'openai-oauth:recent-refresh',
  max: 5000,
  ttlMs: recentRefreshTtlMs
})
let openAIOAuthTokenRefresher: OpenAIOAuthTokenRefresher = refreshOpenAIOAuthToken

type RefreshableOpenAIOAuthAccount = Pick<AccountSummary, 'id' | 'providerCode' | 'type' | 'credentials'> & Partial<Pick<AccountSummary, 'providerProtocolProfileId' | 'protocolCode' | 'protocolVersion' | 'proxyProfileId' | 'status' | 'name' | 'lastErrorCode' | 'configRevision'>> & {
  updatedAt?: string
  proxyUrl?: string
  localConfigurationError?: OpenAIOAuthRefreshLocalConfigurationMarker
}
type OpenAIOAuthRefreshAccount = RefreshableOpenAIOAuthAccount & Partial<Pick<AccountSummary, 'systemAccountId' | 'concurrencyLimit' | 'currentConcurrency' | 'priority' | 'superPriorityEnabled' | 'fallbackEnabled' | 'schedulable' | 'todayUsage' | 'usage' | 'permissions'>> & {
  proxyUrl?: string
  localConfigurationError?: OpenAIOAuthRefreshLocalConfigurationMarker
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
  lockMode?: 'wait' | 'skip'
  onLockAcquired?: () => void
}

type OpenAIOAuthRefreshLocalConfigurationMarker = {
  code: 'oauth_proxy_configuration_invalid'
  message: string
}

type OpenAIOAuthRefreshBatchCandidate =
  | {
      kind: 'account'
      account: AccountSummary
      observedFailureState?: OpenAIOAuthRefreshFailureState
    }
  | {
      kind: 'local_configuration_error'
      accountId: string
      accountName: string
      accountStatus: string
      configRevision: number
      errorMessage: string
      accountLocalEvidenceConfirmed: boolean
      observedFailureState?: OpenAIOAuthRefreshFailureState
    }

export interface OpenAIOAuthRefreshFailureRedisClientForTest {
  get(key: string): Promise<string | null>
  eval(script: string, options: { keys: string[]; arguments: string[] }): Promise<unknown>
}

export function setOpenAIOAuthTokenRefresherForTest(refresher?: OpenAIOAuthTokenRefresher): void {
  openAIOAuthTokenRefresher = refresher ?? refreshOpenAIOAuthToken
}

let openAIOAuthDbServiceRequesterForTest: OpenAIOAuthDbServiceRequester | undefined
let openAIOAuthRefreshFailureRedisClientForTest: OpenAIOAuthRefreshFailureRedisClientForTest | undefined

export function setOpenAIOAuthDbServiceRequesterForTest(requester?: OpenAIOAuthDbServiceRequester): void {
  openAIOAuthDbServiceRequesterForTest = requester
}

export function setOpenAIOAuthRefreshFailureRedisClientForTest(
  client?: OpenAIOAuthRefreshFailureRedisClientForTest
): void {
  openAIOAuthRefreshFailureRedisClientForTest = client
  refreshFailureStateByAccountId.clear()
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
  return await runWithGlobalBackgroundConcurrencySlot(async () => await runWithAccountRefreshLock(
    account.id,
    (lockSignal, assertLockOwned) => refreshOpenAIOAuthAccountAccessTokenLocked(
      account,
      { ...options, signal: lockSignal },
      assertLockOwned
    ),
    options.lockMode === 'skip' ? options : { ...options, signal: undefined }
  ))
}

async function refreshOpenAIOAuthAccountAccessTokenLocked(
  account: RefreshableOpenAIOAuthAccount,
  options: OpenAIOAuthAccountRefreshCallOptions,
  assertLockOwned: () => Promise<void>
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
      throw new OpenAIOAuthRefreshLocalConfigurationError('OpenAI OAuth 账户缺少刷新令牌', {
        expectedConfigRevision: current.configRevision
      })
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
      // Once a refresh starts, the provider may rotate the refresh token before
      // persistence completes. Do not let a disconnected client cancel that
      // exchange and leave the account with the old token.
      const tokenInfo = await openAIOAuthTokenRefresher({
        refreshToken,
        clientId: stringCredential(credentials, 'client_id'),
        proxyUrl: resolveRefreshProxyUrlOrThrow(current, persistMode),
        signal: options.signal
      })
      const nextCredentials = {
        ...credentials,
        ...buildOpenAIOAuthCredentials(tokenInfo, { refreshToken })
      }
      await assertLockOwned()
      if (persistMode === 'db-service') {
        const persisted = await persistOpenAIOAuthCredentialsViaDbService(
          current.id,
          nextCredentials,
          current.configRevision ?? 1,
          persistMode
        )
        if (!persisted.updated) {
          throw new Error('OpenAI OAuth 账户不存在或无法更新')
        }
        const refreshed: OpenAIOAuthRefreshAccount = {
          ...current,
          credentials: nextCredentials,
          configRevision: persisted.configRevision ?? (current.configRevision ?? 1) + 1,
          updatedAt: persisted.updatedAt ?? current.updatedAt
        }
        rememberRecentOpenAIOAuthRefresh(refreshed, persistMode)
        return refreshed
      }
      if (!current.providerProtocolProfileId) {
        throw new Error('OpenAI OAuth 账户协议配置缺失')
      }
      const rotation = await rotateOAuthCredentialsAsync({
        accountId: current.id,
        expectedConfigRevision: current.configRevision ?? 1,
        expectedProviderCode: GPT_VENDOR_CODE,
        expectedAccountType: 'oauth',
        expectedProviderProtocolProfileId: current.providerProtocolProfileId,
        credentials: nextCredentials,
        access: options.access ?? internalOpenAIOAuthRefreshAccess
      })
      const updated = rotation ? {
        ...current,
        credentials: rotation.credentials,
        configRevision: rotation.configRevision,
        updatedAt: rotation.updatedAt
      } : undefined
      if (!updated) {
        throw new Error('OpenAI OAuth 账户不存在或无法更新')
      }
      return finalizeSuccessfulTokenRefresh(updated, options)
    } catch (error) {
      const recovered = await tryRecoverOpenAIOAuthRefreshRace(current, options, persistMode)
      if (recovered.result === 'fresh') {
        logger.info({
          event: 'openai_oauth_access_token_refresh_race_recovered',
          accountId: recovered.account.id
        }, 'OpenAI OAuth Access Token 刷新竞争已恢复')
        clearGatewayRuntimeCache()
        rememberRecentOpenAIOAuthRefresh(recovered.account, persistMode)
        return finalizeSuccessfulTokenRefresh(recovered.account, options)
      }
      if (
        recovered.result === 'retry'
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
      throw withOpenAIOAuthRefreshAttemptContext(error, current)
    }
  }

  throw new Error('OpenAI OAuth 访问令牌刷新失败')
}

async function persistOpenAIOAuthCredentialsViaDbService(
  accountId: string,
  credentials: Record<string, unknown>,
  expectedConfigRevision: number,
  persistMode: 'sync' | 'db-service'
): Promise<{ updated: boolean; configRevision?: number; updatedAt?: string }> {
  const result = await requestOpenAIOAuthDbService({
    type: 'update_openai_oauth_credentials',
    accountId,
    credentials,
    expectedConfigRevision
  }, persistMode)
  if (result.updated) {
    clearGatewayRuntimeCache()
  }
  return result
}

export async function refreshDueOpenAIOAuthAccessTokens(
  options: OpenAIOAuthAccessTokenRefreshOptions = {}
): Promise<OpenAIOAuthAccessTokenRefreshResult> {
  if (options.signal?.aborted) {
    return emptyOpenAIOAuthAccessTokenRefreshResult()
  }
  const admissionStartedAt = Date.now()
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
  const startAdmissionBudgetMs = options.startAdmissionBudgetMs === undefined
    ? openAIOAuthRefreshStartAdmissionBudgetMs
    : optionInteger(options.startAdmissionBudgetMs, 'startAdmissionBudgetMs', 1, 300_000)
  const now = Date.now()
  const leadMs = leadSeconds * 1000
  const retryBackoffMs = retryBackoffSeconds * 1000
  const retryBackoffPolicy = fixedRetryPolicy('openai_oauth_access_token_refresh_backoff', retryBackoffMs)
  cleanupRefreshFailureBackoff(now)
  const accountIdFilter = normalizedRefreshAccountIdSet(options.accountIds)
  const candidateFetchLimit = refreshCandidateFetchLimit(batchSize, accountIdFilter?.size)

  const listedCandidates = runtimeConfig.databaseDriver === 'postgres'
    ? await listOpenAIOAuthAccountsDueForAccessTokenRefreshAsync({
      leadSeconds,
      limit: candidateFetchLimit,
      stoppedErrorCode: OPENAI_OAUTH_TOKEN_REFRESH_LOCAL_CONFIGURATION_ERROR_CODE
    })
    : listOpenAIOAuthAccountsDueForAccessTokenRefresh({
    leadSeconds,
    limit: candidateFetchLimit,
    stoppedErrorCode: OPENAI_OAUTH_TOKEN_REFRESH_LOCAL_CONFIGURATION_ERROR_CODE
    })
  // A decryptable sibling proves the local keyring can read this batch. Without
  // that proof, treating every decrypt failure as an account-local defect could
  // turn a process-wide secret/keyring outage into a full-pool account outage.
  const accountLocalCredentialEvidenceConfirmed = listedCandidates.some((candidate) => candidate.kind === 'account')
  const dueAccounts = listedCandidates.filter((candidate) => {
    const accountId = openAIOAuthRefreshCandidateAccountId(candidate)
    if (accountIdFilter && !accountIdFilter.has(accountId)) return false
    if (candidate.kind === 'local_configuration_error') return true
    return isExistingOpenAIOAuthAccountForRefresh(candidate.account)
      && shouldPreRefreshAccessToken(candidate.account.credentials, now, leadMs)
  })

  const result: OpenAIOAuthAccessTokenRefreshResult = {
    scanned: dueAccounts.length,
    due: dueAccounts.length,
    refreshed: 0,
    failed: 0,
    exceptioned: 0,
    cooldowned: 0,
    skippedBackoff: 0,
    started: 0,
    skippedLocked: 0,
    deferredBudget: 0
  }

  const candidates = await selectOpenAIOAuthRefreshBatchCandidates({
    dueAccounts,
    batchSize,
    now,
    accountLocalCredentialEvidenceConfirmed,
    result,
    signal: options.signal,
    admissionDeadlineAtMs: admissionStartedAt + startAdmissionBudgetMs
  })
  let nextCandidateIndex = 0

  const processCandidate = async (candidate: OpenAIOAuthRefreshBatchCandidate): Promise<void> => {
    const accountId = openAIOAuthRefreshBatchCandidateAccountId(candidate)
    const accountName = openAIOAuthRefreshBatchCandidateAccountName(candidate)
    const accountStatus = openAIOAuthRefreshBatchCandidateAccountStatus(candidate)
    const attemptConfigRevision = openAIOAuthRefreshBatchCandidateConfigRevision(candidate)
    let countedAsStarted = false
    const markStarted = (): void => {
      if (countedAsStarted) return
      countedAsStarted = true
      result.started += 1
    }
    try {
      if (candidate.kind === 'local_configuration_error') {
        markStarted()
        if (candidate.accountLocalEvidenceConfirmed) {
          throw new OpenAIOAuthRefreshLocalConfigurationError(candidate.errorMessage, {
            expectedConfigRevision: candidate.configRevision
          })
        }
        throw new Error('OpenAI OAuth 凭据存储当前不可验证，未改变账户调度状态')
      }
      await refreshOpenAIOAuthAccountAccessToken(candidate.account, {
        force: false,
        leadSeconds,
        restoreFailureState: false,
        persistMode,
        lockMode: 'skip',
        signal: options.signal,
        onLockAcquired: markStarted
      })
      await clearRefreshFailureState(accountId, candidate.observedFailureState)
      await restoreOpenAIOAuthTokenRefreshFailureIfRecovered(candidate.account, persistMode)
      result.refreshed += 1
    } catch (error) {
      if (options.signal?.aborted && !countedAsStarted) {
        return
      }
      if (error instanceof ProviderOAuthRefreshLockBusyError) {
        result.skippedLocked += 1
        return
      }
      result.failed += 1
      const expiredOrMissing = candidate.kind === 'account'
        ? isAccessTokenExpiredOrMissing(candidate.account.credentials, Date.now())
        : true
      const message = errorMessage(error)
      const failureKind = isOpenAIOAuthRefreshLocalConfigurationError(error) ? 'local_configuration' : 'untrusted_upstream_or_runtime'
      const errorConfigRevision = isOpenAIOAuthRefreshLocalConfigurationError(error)
        ? error.expectedConfigRevision ?? attemptConfigRevision
        : attemptConfigRevision
      const failureState = await recordRefreshFailure(
        accountId,
        retryDueAtMs(retryBackoffPolicy),
        failureKind,
        errorConfigRevision
      )
      logger.warn(errorLogFields(error, {
        event: 'openai_oauth_access_token_refresh_account_failed',
        accountId,
        accountName,
        failureCount: failureState.count,
        localConfigurationFailureCount: failureState.localConfigurationCount,
        failureKind,
        failureStateApplied: failureState.applied,
        accessTokenExpiredOrMissing: expiredOrMissing
      }), 'OpenAI OAuth 访问令牌刷新失败')

      if (
        failureState.applied
        && failureState.localConfigurationCount >= oauthTokenRefreshFailureThreshold
        && accountStatus === 'active'
      ) {
        const updated = await requestOpenAIOAuthDbService({
          type: 'mark_openai_oauth_local_configuration_exception',
          accountId,
          errorCode: OPENAI_OAUTH_TOKEN_REFRESH_LOCAL_CONFIGURATION_ERROR_CODE,
          reason: openAIOAuthTokenRefreshLocalConfigurationStoppedMessage(failureState.localConfigurationCount, message),
          expectedConfigRevision: failureState.configRevision,
          expectedStatus: 'active'
        }, persistMode)
        if (updated.updated) {
          clearGatewayRuntimeCache()
          await clearRefreshFailureState(accountId, failureState)
          result.exceptioned += 1
        }
      }
    }
  }

  const workers = Array.from(
    { length: Math.min(openAIOAuthRefreshBatchConcurrency, candidates.length) },
    async () => {
      while (nextCandidateIndex < candidates.length) {
        if (
          Date.now() - admissionStartedAt >= startAdmissionBudgetMs
          || options.signal?.aborted === true
        ) {
          return
        }
        const candidateIndex = nextCandidateIndex
        nextCandidateIndex += 1
        const candidate = candidates[candidateIndex]
        if (!candidate) return
        await processCandidate(candidate)
      }
    }
  )
  await Promise.all(workers)
  result.deferredBudget += Math.max(0, candidates.length - nextCandidateIndex)

  return result
}

function emptyOpenAIOAuthAccessTokenRefreshResult(): OpenAIOAuthAccessTokenRefreshResult {
  return {
    scanned: 0,
    due: 0,
    refreshed: 0,
    failed: 0,
    exceptioned: 0,
    cooldowned: 0,
    skippedBackoff: 0,
    started: 0,
    skippedLocked: 0,
    deferredBudget: 0
  }
}

async function selectOpenAIOAuthRefreshBatchCandidates(input: {
  dueAccounts: OpenAIOAuthRefreshCandidateResult[]
  batchSize: number
  now: number
  accountLocalCredentialEvidenceConfirmed: boolean
  result: OpenAIOAuthAccessTokenRefreshResult
  signal?: AbortSignal
  admissionDeadlineAtMs: number
}): Promise<OpenAIOAuthRefreshBatchCandidate[]> {
  const candidates: OpenAIOAuthRefreshBatchCandidate[] = []
  for (let offset = 0; offset < input.dueAccounts.length && candidates.length < input.batchSize;) {
    if (input.signal?.aborted || Date.now() >= input.admissionDeadlineAtMs) {
      input.result.deferredBudget += Math.max(0, input.dueAccounts.length - offset)
      break
    }
    const candidateWindow = input.dueAccounts.slice(offset, offset + input.batchSize - candidates.length)
    offset += candidateWindow.length
    const failureStates = await Promise.all(candidateWindow.map((candidate) => readRefreshFailureState(
      openAIOAuthRefreshCandidateAccountId(candidate),
      input.now,
      openAIOAuthRefreshCandidateConfigRevision(candidate),
      { signal: input.signal, deadlineAtMs: input.admissionDeadlineAtMs }
    )))
    for (let index = 0; index < candidateWindow.length && candidates.length < input.batchSize; index += 1) {
      const candidate = candidateWindow[index]
      if (!candidate) continue
      const failureState = failureStates[index]
      if (failureState?.backoffUntil !== undefined && failureState.backoffUntil > input.now) {
        input.result.skippedBackoff += 1
        continue
      }
      candidates.push(candidate.kind === 'account'
        ? { kind: 'account', account: candidate.account, observedFailureState: failureState }
        : {
            kind: 'local_configuration_error',
            accountId: candidate.accountId,
            accountName: candidate.accountName,
            accountStatus: candidate.accountStatus,
            configRevision: candidate.configRevision,
            errorMessage: candidate.errorMessage,
            accountLocalEvidenceConfirmed: input.accountLocalCredentialEvidenceConfirmed,
            observedFailureState: failureState
          })
    }
  }
  return candidates
}

function isExistingOpenAIOAuthAccountForRefresh(account: AccountSummary): boolean {
  return isOpenAIOAuthRefreshAccount(account)
    && account.accessType !== 'authorized'
    && !shouldStopOpenAIOAuthBackgroundRefresh(account)
}

function openAIOAuthRefreshCandidateAccountId(candidate: OpenAIOAuthRefreshCandidateResult): string {
  return candidate.kind === 'account' ? candidate.account.id : candidate.accountId
}

function openAIOAuthRefreshCandidateConfigRevision(candidate: OpenAIOAuthRefreshCandidateResult): number {
  return candidate.kind === 'account' ? candidate.account.configRevision ?? 1 : candidate.configRevision
}

function openAIOAuthRefreshBatchCandidateAccountId(candidate: OpenAIOAuthRefreshBatchCandidate): string {
  return candidate.kind === 'account' ? candidate.account.id : candidate.accountId
}

function openAIOAuthRefreshBatchCandidateAccountName(candidate: OpenAIOAuthRefreshBatchCandidate): string {
  return candidate.kind === 'account' ? candidate.account.name : candidate.accountName
}

function openAIOAuthRefreshBatchCandidateAccountStatus(candidate: OpenAIOAuthRefreshBatchCandidate): string | undefined {
  return candidate.kind === 'account' ? candidate.account.status : candidate.accountStatus
}

function openAIOAuthRefreshBatchCandidateConfigRevision(candidate: OpenAIOAuthRefreshBatchCandidate): number {
  return candidate.kind === 'account' ? candidate.account.configRevision ?? 1 : candidate.configRevision
}

function isOpenAIOAuthRefreshAccount(account: RefreshableOpenAIOAuthAccount | AccountSummary | undefined): boolean {
  return Boolean(account
    && isGptVendorCode(account.providerCode)
    && isOpenAIProtocolProfile(account)
    && account.type === 'oauth')
}

function shouldStopOpenAIOAuthBackgroundRefresh(account: AccountSummary): boolean {
  return account.status === 'error'
    && account.lastErrorCode === OPENAI_OAUTH_TOKEN_REFRESH_LOCAL_CONFIGURATION_ERROR_CODE
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

async function recordRefreshFailure(
  accountId: string,
  backoffUntil: number,
  failureKind: 'local_configuration' | 'untrusted_upstream_or_runtime',
  configRevision: number
): Promise<OpenAIOAuthRefreshFailureState> {
  const normalizedRevision = normalizedConfigRevision(configRevision) ?? 1
  const mutationId = randomBytes(12).toString('hex')
  if (usesRedisRefreshFailureState()) {
    const result = await runRefreshFailureRedisOperation('OAuth 刷新失败状态写入', (client) => client.eval(redisRecordRefreshFailureScript, {
      keys: [redisRefreshFailureStateKey(accountId)],
      arguments: [
        String(Math.max(0, Math.trunc(backoffUntil))),
        String(openAIOAuthRefreshFailureStateTtlMs),
        failureKind === 'local_configuration' ? '1' : '0',
        String(normalizedRevision),
        mutationId
      ]
    }))
    const values = redisResultArray(result)
    const applied = numericRedisResult(values[4]) === 1
    const storedMutationId = stringRedisResult(values[5]) ?? mutationId
    const snapshot = stringRedisResult(values[6])
    return {
      count: Math.max(1, Math.trunc(numericRedisResult(values[0]) || 1)),
      backoffUntil: Math.max(0, Math.trunc(numericRedisResult(values[1]) || backoffUntil)),
      localConfigurationCount: Math.max(0, Math.trunc(numericRedisResult(values[2]) || (failureKind === 'local_configuration' ? 1 : 0))),
      configRevision: Math.max(1, Math.trunc(numericRedisResult(values[3]) || normalizedRevision)),
      applied,
      mutationId: storedMutationId,
      snapshot
    }
  }
  const previous = refreshFailureStateByAccountId.get(accountId)
  if (previous && previous.configRevision > normalizedRevision) {
    return { ...previous, applied: false }
  }
  const previousForRevision = previous?.configRevision === normalizedRevision ? previous : undefined
  const next = {
    count: (previousForRevision?.count ?? 0) + 1,
    localConfigurationCount: failureKind === 'local_configuration'
      ? (previousForRevision?.localConfigurationCount ?? 0) + 1
      : 0,
    backoffUntil: Math.max(previousForRevision?.backoffUntil ?? 0, backoffUntil),
    configRevision: normalizedRevision,
    applied: true,
    mutationId
  }
  refreshFailureStateByAccountId.set(accountId, next)
  return next
}

async function readRefreshFailureState(
  accountId: string,
  now: number,
  configRevision: number,
  options: { signal?: AbortSignal; deadlineAtMs?: number } = {}
): Promise<OpenAIOAuthRefreshFailureState | undefined> {
  const normalizedRevision = normalizedConfigRevision(configRevision) ?? 1
  if (usesRedisRefreshFailureState()) {
    const rawValue = await runRefreshFailureRedisOperation(
      'OAuth 刷新失败状态读取',
      (client) => client.get(redisRefreshFailureStateKey(accountId)),
      options
    )
    if (!rawValue) return undefined
    try {
      const parsed = JSON.parse(rawValue) as Partial<OpenAIOAuthRefreshFailureState>
      const count = typeof parsed.count === 'number' && Number.isFinite(parsed.count) ? Math.max(0, Math.trunc(parsed.count)) : 0
      const localConfigurationCount = typeof parsed.localConfigurationCount === 'number' && Number.isFinite(parsed.localConfigurationCount)
        ? Math.max(0, Math.trunc(parsed.localConfigurationCount))
        : 0
      const backoffUntil = typeof parsed.backoffUntil === 'number' && Number.isFinite(parsed.backoffUntil) ? Math.max(0, Math.trunc(parsed.backoffUntil)) : 0
      const storedConfigRevision = normalizedConfigRevision(parsed.configRevision)
      const mutationId = typeof parsed.mutationId === 'string' ? parsed.mutationId : ''
      if (!storedConfigRevision) {
        await clearRefreshFailureState(accountId, { snapshot: rawValue })
        return undefined
      }
      if (storedConfigRevision > normalizedRevision) return undefined
      if (storedConfigRevision < normalizedRevision) {
        await clearRefreshFailureState(accountId, { snapshot: rawValue, configRevision: storedConfigRevision })
        return undefined
      }
      return {
        count,
        localConfigurationCount,
        backoffUntil: backoffUntil > now ? backoffUntil : 0,
        configRevision: storedConfigRevision,
        applied: true,
        mutationId,
        snapshot: rawValue
      }
    } catch {
      await clearRefreshFailureState(accountId, { snapshot: rawValue })
      return undefined
    }
  }
  const failureState = refreshFailureStateByAccountId.get(accountId)
  if (!failureState) return undefined
  if (failureState.configRevision > normalizedRevision) return undefined
  if (failureState.configRevision < normalizedRevision) {
    await clearRefreshFailureState(accountId, failureState)
    return undefined
  }
  return {
    count: failureState.count,
    localConfigurationCount: failureState.localConfigurationCount,
    backoffUntil: failureState.backoffUntil > now ? failureState.backoffUntil : 0,
    configRevision: failureState.configRevision,
    applied: true,
    mutationId: failureState.mutationId,
    snapshot: failureState.snapshot
  }
}

type OpenAIOAuthRefreshFailureStateGuard = {
  configRevision?: number
  mutationId?: string
  snapshot?: string
}

async function clearRefreshFailureState(accountId: string, expected?: OpenAIOAuthRefreshFailureStateGuard): Promise<void> {
  if (!expected) return
  if (usesRedisRefreshFailureState()) {
    const snapshot = expected.snapshot
    if (!snapshot) return
    await runRefreshFailureRedisOperation('OAuth 刷新失败状态清理', (client) => client.eval(redisCompareDeleteRefreshFailureScript, {
      keys: [redisRefreshFailureStateKey(accountId)],
      arguments: [snapshot, String(expected.configRevision ?? 0)]
    }).then(() => undefined))
    return
  }
  const current = refreshFailureStateByAccountId.get(accountId)
  if (!current) return
  if (expected.mutationId && current.mutationId !== expected.mutationId) return
  if (expected.configRevision !== undefined && current.configRevision !== expected.configRevision) return
  refreshFailureStateByAccountId.delete(accountId)
}

function cleanupRefreshFailureBackoff(now: number): void {
  if (usesRedisRefreshFailureState()) {
    return
  }
  for (const [accountId, failureState] of refreshFailureStateByAccountId.entries()) {
    if (failureState.backoffUntil <= now) {
      refreshFailureStateByAccountId.set(accountId, {
        ...failureState,
        backoffUntil: 0,
        mutationId: randomBytes(12).toString('hex')
      })
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

  const latest = await findOAuthCredentialRotationAccountAsync(account.id, options.access)
  if (!isOpenAIOAuthRefreshAccount(latest)) {
    return undefined
  }
  return latest
}

async function restoreOpenAIOAuthTokenRefreshFailureIfRecovered(account: AccountSummary, persistMode: 'sync' | 'db-service'): Promise<void> {
  if (account.status !== 'error' || !isManagedOpenAIOAuthRefreshErrorCode(account.lastErrorCode)) {
    return
  }
  const restored = await requestOpenAIOAuthDbService({
    type: 'clear_account_failure_state',
    accountId: account.id,
    expectedLastErrorCodes: [...openAIOAuthRefreshManagedErrorCodes]
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

async function runWithAccountRefreshLock<T>(
  accountId: string,
  task: (signal: AbortSignal, assertLockOwned: () => Promise<void>) => Promise<T>,
  options: Pick<OpenAIOAuthAccountRefreshCallOptions, 'lockMode' | 'onLockAcquired' | 'signal'> = {}
): Promise<T> {
  return await runWithProviderOAuthRefreshLock(GPT_VENDOR_CODE, accountId, task, {
    signal: options.signal,
    failIfLocked: options.lockMode === 'skip',
    onLockAcquired: options.onLockAcquired,
    // Remove after every deployed Node instance uses the shared provider lock namespace.
    compatibilityLock: { lockStore: openAIOAuthLegacyRefreshLockStore, lockKey: accountId }
  })
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
  const updated = account.status === 'error' && isManagedOpenAIOAuthRefreshErrorCode(account.lastErrorCode)
    ? clearAccountFailureState(account.id, options.access, {
      expectedLastErrorCodes: openAIOAuthRefreshManagedErrorCodes
    }) ?? account as AccountSummary
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

function resolveRefreshProxyUrlOrThrow(account: OpenAIOAuthRefreshAccount, persistMode: 'sync' | 'db-service'): string | undefined {
  if (account.localConfigurationError) {
    throw new OpenAIOAuthRefreshLocalConfigurationError(account.localConfigurationError.message, {
      expectedConfigRevision: account.configRevision
    })
  }
  if (account.proxyUrl || persistMode === 'db-service' || !account.proxyProfileId) {
    return account.proxyUrl
  }
  const resolution = resolveProxyUrlsForProfiles([account.proxyProfileId]).get(account.proxyProfileId)
  if (!resolution?.proxyUrl) {
    throw new OpenAIOAuthRefreshLocalConfigurationError(
      resolution?.errorMessage ?? 'OpenAI OAuth 账户配置的代理不可用，请检查代理配置',
      { expectedConfigRevision: account.configRevision }
    )
  }
  return resolution.proxyUrl
}

function effectivePersistMode(options: OpenAIOAuthAccountRefreshCallOptions): 'sync' | 'db-service' {
  if (runtimeConfig.databaseDriver === 'postgres') {
    if (options.persistMode === 'sync') {
      throw new Error('高性能 PostgreSQL 模式禁止 OpenAI OAuth sync persistMode，必须通过 DB service 持久化')
    }
    return 'db-service'
  }
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
      allowErrorRestore: operation.allowErrorRestore,
      expectedLastErrorCodes: operation.expectedLastErrorCodes
    })
    if (result.changed) {
      clearGatewayRuntimeCache()
    }
    return { changed: result.changed, accountStatus: result.account?.status } as DbServiceOperationResult<T>
  }
  if (operation.type === 'mark_openai_oauth_local_configuration_exception') {
    const updated = markOpenAIOAuthLocalConfigurationExceptionIfCurrent(operation)
    if (updated) {
      clearGatewayRuntimeCache()
    }
    return { updated } as DbServiceOperationResult<T>
  }
  throw new Error(`单进程 worker 回归不支持 DB service 操作：${operation.type}`)
}

export function isOpenAIOAuthRefreshLocalConfigurationError(error: unknown): error is OpenAIOAuthRefreshLocalConfigurationError {
  return error instanceof OpenAIOAuthRefreshLocalConfigurationError
}

function withOpenAIOAuthRefreshAttemptContext(
  error: unknown,
  account: OpenAIOAuthRefreshAccount
): unknown {
  if (!isOpenAIOAuthRefreshLocalConfigurationError(error)) return error
  const expectedConfigRevision = normalizedConfigRevision(account.configRevision)
  if (error.expectedConfigRevision === expectedConfigRevision) return error
  return new OpenAIOAuthRefreshLocalConfigurationError(error.message, {
    cause: error,
    expectedConfigRevision
  })
}

function normalizedConfigRevision(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isInteger(value) && value > 0 ? value : undefined
}

export function isManagedOpenAIOAuthRefreshErrorCode(errorCode: string | undefined): boolean {
  return Boolean(errorCode && openAIOAuthRefreshManagedErrorCodes.includes(errorCode as typeof openAIOAuthRefreshManagedErrorCodes[number]))
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

function openAIOAuthTokenRefreshLocalConfigurationStoppedMessage(failureCount: number, lastError: string): string {
  return [
    `OpenAI OAuth 访问令牌连续 ${failureCount} 次因本地配置错误无法启动刷新，已停止自动刷新。`,
    '该结论只来自本地可验证的凭据、代理配置解析或解密失败，不使用上游 HTTP 状态、错误码或响应正文推断账户状态。',
    '请在账户页检查并修正 OAuth 凭据或代理配置，然后重新检查账户。',
    `最后本地错误：${lastError}`
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

function refreshCandidateFetchLimit(batchSize: number, requestedAccountCount?: number): number {
  if (requestedAccountCount !== undefined) {
    return Math.min(500, Math.max(batchSize, requestedAccountCount * 5))
  }
  if (!usesRedisRefreshFailureState()) {
    return Math.min(500, batchSize + refreshFailureStateByAccountId.size)
  }
  return Math.min(200, batchSize * 5)
}

function usesRedisRefreshFailureState(): boolean {
  return Boolean(openAIOAuthRefreshFailureRedisClientForTest) || runtimeConfig.runtimeStateDriver === 'redis'
}

async function runRefreshFailureRedisOperation<T>(
  operationName: string,
  operation: (client: OpenAIOAuthRefreshFailureRedisClientForTest) => Promise<T>,
  options: { signal?: AbortSignal; deadlineAtMs?: number } = {}
): Promise<T> {
  if (openAIOAuthRefreshFailureRedisClientForTest) return await operation(openAIOAuthRefreshFailureRedisClientForTest)
  const redisUrl = runtimeConfig.redis.stateUrl
  if (!redisUrl) {
    throw new Error('JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置')
  }
  return await runRedisOperationWithDeadline(redisUrl, {
    operationName,
    timeoutMs: 3_000,
    signal: options.signal,
    deadlineAtMs: options.deadlineAtMs
  }, (client: RedisCommandClient) => operation(client))
}

function redisRefreshFailureStateKey(accountId: string): string {
  return redisNamespacedKey(`juhe-ai:state:openai-oauth-refresh-failure:${redisKeyHash(accountId)}`)
}

function redisKeyHash(value: string): string {
  return createHash('sha256').update(value).digest('base64url')
}

function redisResultArray(value: unknown): unknown[] {
  if (Array.isArray(value)) {
    return value
  }
  return []
}

function stringRedisResult(value: unknown): string | undefined {
  if (typeof value === 'string' && value.length > 0) return value
  if (Buffer.isBuffer(value)) return value.toString('utf8')
  return value === undefined || value === null ? undefined : String(value)
}

function numericRedisResult(value: unknown): number {
  if (typeof value === 'number' && Number.isFinite(value)) return value
  if (typeof value === 'bigint') return Number(value)
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : 0
}

function delay(ms: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
}

const redisRecordRefreshFailureScript = `
local raw = redis.call('GET', KEYS[1])
local count = 0
local local_configuration_count = 0
local config_revision = tonumber(ARGV[4])
local stored_revision = 0
local stored_backoff_until = 0
local stored_mutation_id = ''
if raw then
  local ok, decoded = pcall(cjson.decode, raw)
  if ok and decoded then
    stored_revision = tonumber(decoded['configRevision']) or 0
    stored_backoff_until = tonumber(decoded['backoffUntil']) or 0
    stored_mutation_id = tostring(decoded['mutationId'] or '')
    if stored_revision > config_revision then
      return {
        tonumber(decoded['count']) or 0,
        stored_backoff_until,
        tonumber(decoded['localConfigurationCount']) or 0,
        stored_revision,
        0,
        stored_mutation_id,
        raw
      }
    end
    if stored_revision == config_revision then
      count = tonumber(decoded['count']) or 0
      local_configuration_count = tonumber(decoded['localConfigurationCount']) or 0
    else
      stored_backoff_until = 0
    end
  end
end
count = count + 1
local backoff_until = math.max(stored_backoff_until, tonumber(ARGV[1]) or 0)
local ttl_ms = tonumber(ARGV[2])
local is_local_configuration = tonumber(ARGV[3]) or 0
if is_local_configuration == 1 then
  local_configuration_count = local_configuration_count + 1
else
  local_configuration_count = 0
end
local mutation_id = ARGV[5]
local payload = cjson.encode({ count = count, localConfigurationCount = local_configuration_count, backoffUntil = backoff_until, configRevision = config_revision, mutationId = mutation_id })
redis.call('SET', KEYS[1], payload, 'PX', ttl_ms)
return {count, backoff_until, local_configuration_count, config_revision, 1, mutation_id, payload}
`

const redisCompareDeleteRefreshFailureScript = `
local raw = redis.call('GET', KEYS[1])
if raw and raw == ARGV[1] then
  local ok, decoded = pcall(cjson.decode, raw)
  if ok and decoded and tonumber(ARGV[2]) > 0 and tonumber(decoded['configRevision']) ~= tonumber(ARGV[2]) then
    return 0
  end
  return redis.call('DEL', KEYS[1])
end
return 0
`

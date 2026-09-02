import { randomUUID } from 'node:crypto'

import { runtimeConfig } from '../../config/runtime.js'
import { runWithGlobalBackgroundConcurrencySlot } from '../../shared/concurrency-governor.js'
import { passiveScheduleDelayMs } from '../../shared/passive-schedule-jitter.js'
import type {
  AccountBalanceBuiltinAdapter,
  AccountBalanceKeySnapshot,
  AccountBalanceQueryConfig,
  AccountBalanceScope,
  AccountBalanceSnapshot
} from './account-balance.types.js'
import type { AccountBalanceRefreshCandidate } from '../../storage/account-balance.repository.js'
import {
  accountBalanceSnapshotMatchesConfiguration,
  commitAccountBalanceRefreshAsync,
  loadAccountBalanceSnapshotRecordsByAccountIdsAsync,
  persistAccountBalanceRefreshWithSnapshotAsync,
  replaceAccountBalanceSnapshotIfCurrentAsync
} from '../../storage/account-balance.repository.js'
import {
  acquireBackgroundJobLeaseAsync,
  releaseBackgroundJobLeaseAsync
} from '../../storage/background-task-runs.repository.js'
import { mainDatabaseRuntimeInfo } from '../../storage/database.js'
import { resolveProxyUrlForProfileAsync } from '../../storage/proxy.repository.js'
import { requestBackgroundWorkerDbService } from '../background/background-ipc.js'
import { requestStatsWriter } from '../background/background-stats-writer.js'
import { requestUpstream, UpstreamRequestAbortedError, UpstreamRequestTimeoutError } from '../gateway/upstream/request.js'
import {
  parseCustomBalance,
  parseLiteLlmBalance,
  parseNewApiBalance,
  parseOpenAiCompatibleBillingBalance,
  parseOpenAiCompatibleBillingStatus,
  parseSub2ApiBalance,
  parseUserBalance
} from './account-balance-adapters.js'
import {
  accountBalanceApiKeyFingerprint,
  effectiveAccountApiKeys,
  maskAccountBalanceApiKey,
  MULTI_KEY_ACCOUNT_BALANCE_QUERY_MESSAGE
} from './account-balance-config.js'

const responseMaxBytes = 256 * 1024
const requestTimeoutMs = 15_000
const balanceRefreshLeaseMs = 30_000
const multiKeyBalanceMaxConcurrent = 4
const multiKeyBalanceDeadlineMs = 25_000
const builtinAdapterOrder: AccountBalanceBuiltinAdapter[] = ['sub2api', 'newapi', 'openai_billing', 'litellm', 'user_balance']

type AccountBalanceQueryCandidate = Pick<AccountBalanceRefreshCandidate, 'id' | 'credentials' | 'config' | 'proxyProfileId'>

interface AccountBalanceRequestContext {
  baseUrl: URL
  apiKey: string
  proxyUrl?: string
  deadlineAtMs: number
  signal?: AbortSignal
}

export interface AccountBalanceBuiltinQueryResult {
  adapter: AccountBalanceBuiltinAdapter
  snapshot: AccountBalanceSnapshot
}

interface AccountBalanceQueryResolution {
  snapshot: AccountBalanceSnapshot
  preferredBuiltinAdapter?: AccountBalanceBuiltinAdapter
}

interface AccountBalanceRefreshAttempt {
  snapshot: AccountBalanceSnapshot
  nextConfig: AccountBalanceQueryConfig
  nextRefreshAfter: string | null
}

type AccountBalanceRefreshMode = 'automatic' | 'manual'
type AccountBalanceFailureKind = 'transient' | 'deterministic' | 'neutral'

export type AccountBalanceRefreshOutcome = 'refreshed' | 'lease_busy' | 'stale' | 'failed' | 'unsupported'

export interface AccountBalanceRefreshResult {
  outcome: AccountBalanceRefreshOutcome
  snapshot: AccountBalanceSnapshot
  persisted: boolean
}

export type AccountBalanceLeaseResult<T> =
  | { acquired: true; value: T }
  | { acquired: false }

interface AccountBalanceRefreshExecutionContext {
  signal?: AbortSignal
  deadlineAtMs?: number
  queryAdapter?: (candidate: AccountBalanceQueryCandidate, adapter: AccountBalanceBuiltinAdapter) => Promise<AccountBalanceSnapshot>
}

interface AccountBalanceRefreshDependencies extends AccountBalanceRefreshExecutionContext {
  mode?: AccountBalanceRefreshMode
  query?: (
    candidate: AccountBalanceRefreshCandidate,
    context: AccountBalanceRefreshExecutionContext
  ) => Promise<AccountBalanceSnapshot>
  resolveProxyUrl?: (proxyProfileId: string) => Promise<string | undefined>
}

class AccountBalanceQueryFailure extends Error {
  constructor(readonly kind: AccountBalanceFailureKind, message: string) {
    super(message)
    this.name = 'AccountBalanceQueryFailure'
  }
}

export async function refreshAccountBalanceCandidate(
  candidate: AccountBalanceRefreshCandidate,
  dependencies: AccountBalanceRefreshDependencies = {}
): Promise<AccountBalanceSnapshot> {
  return (await refreshAccountBalanceCandidateWithOutcome(candidate, dependencies)).snapshot
}

export async function refreshAccountBalanceCandidateWithOutcome(
  candidate: AccountBalanceRefreshCandidate,
  dependencies: AccountBalanceRefreshDependencies = {}
): Promise<AccountBalanceRefreshResult> {
  if (!candidate) throw new Error('余额刷新账户不存在')
  throwIfBalanceRefreshAborted(dependencies.signal)
  const startedAt = new Date().toISOString()
  const lease = await runWithAccountBalanceLease(candidate, async () => {
    throwIfBalanceRefreshAborted(dependencies.signal)
    const attempt = await resolveAccountBalanceRefreshAttempt(candidate, dependencies)
    const persisted = await persistBalanceRefreshIfCurrent(
      candidate,
      attempt.nextConfig,
      attempt.snapshot,
      attempt.nextRefreshAfter,
      dependencies.mode
    )
    return {
      outcome: persisted ? accountBalanceRefreshOutcome(attempt.snapshot) : 'stale',
      snapshot: attempt.snapshot,
      persisted
    } satisfies AccountBalanceRefreshResult
  })
  if (!lease.acquired) {
    return {
      outcome: 'lease_busy',
      snapshot: await loadCurrentGenerationBalanceSnapshot(candidate)
        ?? { status: 'refreshing', lastAttemptAt: startedAt },
      persisted: false
    }
  }
  return lease.value
}

async function resolveAccountBalanceRefreshAttempt(
  candidate: AccountBalanceRefreshCandidate,
  dependencies: AccountBalanceRefreshDependencies
): Promise<AccountBalanceRefreshAttempt> {
  throwIfBalanceRefreshAborted(dependencies.signal)
  const resolvedProxyUrl = dependencies.query || !candidate.proxyProfileId
    ? undefined
    : (await (dependencies.resolveProxyUrl ?? resolveProxyUrlForProfileAsync)(candidate.proxyProfileId) ?? null)
  throwIfBalanceRefreshAborted(dependencies.signal)
  try {
    const resolution = dependencies.query
      ? { snapshot: await dependencies.query(candidate, balanceRefreshExecutionContext(dependencies)) }
      : await queryAccountBalanceResolutionWithGlobalSlot(candidate, resolvedProxyUrl, dependencies)
    const completedAt = new Date().toISOString()
    const successful = isSuccessfulBalanceSnapshot(resolution.snapshot)
    const snapshot: AccountBalanceSnapshot = {
      ...resolution.snapshot,
      configRevision: candidate.configRevision,
      lastAttemptAt: completedAt,
      ...(successful ? { lastSuccessAt: completedAt } : {})
    }
    if (!successful) {
      // Multi-Key aggregation deliberately keeps its failure/unsupported
      // status and per-Key counts so the list can distinguish partial
      // results from an adapter that is not configured. Do not collapse the
      // aggregate into the legacy single-Key unsupported state.
      if (snapshot.keyBalances && snapshot.keyCount && snapshot.keyCount > 1) {
        return {
          snapshot,
          nextConfig: candidate.config,
          nextRefreshAfter: nextBalanceRefreshAfter(candidate.config.intervalMinutes)
        }
      }
      return {
        snapshot: {
          ...snapshot,
          status: 'unsupported',
          errorMessage: snapshot.errorMessage ?? '当前配置未找到可用余额接口'
        },
        nextConfig: resolvedBalanceConfig(candidate.config, undefined),
        nextRefreshAfter: nextBalanceRefreshAfter(candidate.config.intervalMinutes)
      }
    }
    return {
      snapshot,
      nextConfig: resolvedBalanceConfig(candidate.config, resolution.preferredBuiltinAdapter),
      nextRefreshAfter: nextBalanceRefreshAfter(candidate.config.intervalMinutes)
    }
  } catch (error) {
    const failureKind = accountBalanceFailureKind(error)
    if (!failureKind) throw error
    const completedAt = new Date().toISOString()
    const errorMessage = accountBalanceErrorMessage(error)
    const failedSnapshot: AccountBalanceSnapshot = {
      status: 'failed',
      configRevision: candidate.configRevision,
      errorMessage,
      lastAttemptAt: completedAt
    }
    if (dependencies.mode === 'manual') {
      if (failureKind !== 'transient') {
        return {
          snapshot: { ...failedSnapshot, status: 'unsupported' },
          nextConfig: resolvedBalanceConfig(candidate.config, undefined),
          nextRefreshAfter: nextBalanceRefreshAfter(candidate.config.intervalMinutes)
        }
      }
      return {
        snapshot: failedSnapshot,
        nextConfig: candidate.config,
        nextRefreshAfter: nextBalanceRefreshAfter(candidate.config.intervalMinutes)
      }
    }
    if (failureKind !== 'transient') {
      return {
        snapshot: { status: 'unsupported', configRevision: candidate.configRevision, errorMessage, lastAttemptAt: completedAt },
        nextConfig: resolvedBalanceConfig(candidate.config, undefined),
        nextRefreshAfter: nextBalanceRefreshAfter(candidate.config.intervalMinutes)
      }
    }
    const previousSnapshot = await loadPreviousTransientFailureSnapshot(candidate)
    return {
      snapshot: {
        ...nextTransientFailureSnapshot(previousSnapshot, errorMessage, completedAt),
        configRevision: candidate.configRevision
      },
      nextConfig: candidate.config,
      nextRefreshAfter: nextBalanceRefreshAfter(candidate.config.intervalMinutes)
    }
  }
}

export function nextBalanceRefreshAfter(intervalMinutes: number, nowMs = Date.now(), random = Math.random): string {
  const intervalMs = Math.max(1, Math.trunc(Number(intervalMinutes) || 1)) * 60_000
  return new Date(nowMs + passiveScheduleDelayMs(intervalMs, random)).toISOString()
}

export async function queryAccountBalance(
  candidate: AccountBalanceQueryCandidate,
  executionContext: AccountBalanceRefreshExecutionContext = {}
): Promise<AccountBalanceSnapshot> {
  return (await queryAccountBalanceResolutionWithGlobalSlot(candidate, undefined, executionContext)).snapshot
}

export async function testAccountBalanceCandidate(
  candidate: AccountBalanceQueryCandidate,
  dependencies: { query?: (candidate: AccountBalanceQueryCandidate) => Promise<AccountBalanceSnapshot> } = {}
): Promise<AccountBalanceSnapshot> {
  const completedAt = () => new Date().toISOString()
  try {
    const snapshot = dependencies.query
      ? await dependencies.query(candidate)
      : await queryAccountBalance(candidate)
    const timestamp = completedAt()
    return {
      ...snapshot,
      lastAttemptAt: timestamp,
      ...(isSuccessfulBalanceSnapshot(snapshot)
        ? { lastSuccessAt: timestamp }
        : {})
    }
  } catch (error) {
    return {
      status: 'failed',
      errorMessage: accountBalanceErrorMessage(error),
      lastAttemptAt: completedAt()
    }
  }
}

async function queryAccountBalanceResolution(
  candidate: AccountBalanceQueryCandidate,
  resolvedProxyUrl?: string | null,
  executionContext: AccountBalanceRefreshExecutionContext = {}
): Promise<AccountBalanceQueryResolution> {
  const apiKeys = effectiveAccountApiKeys(candidate.credentials)
  if (apiKeys.length > 1) {
    return await queryMultiKeyAccountBalance(candidate, apiKeys, executionContext, resolvedProxyUrl)
  }
  if (candidate.config.adapter === 'builtin') {
    const result = await queryBuiltinAccountBalance(candidate, {
      resolvedProxyUrl,
      signal: executionContext.signal,
      deadlineAtMs: executionContext.deadlineAtMs,
      queryAdapter: executionContext.queryAdapter
    })
    return {
      snapshot: result.snapshot,
      ...(isSuccessfulBalanceSnapshot(result.snapshot) ? { preferredBuiltinAdapter: result.adapter } : {})
    }
  }
  const context = await accountBalanceRequestContext(candidate, resolvedProxyUrl, executionContext)
  const customConfig = candidate.config.custom
  if (!customConfig) throw deterministicBalanceError('自定义余额查询配置缺失')
  const target = new URL(customConfig.path, context.baseUrl)
  if (target.origin !== context.baseUrl.origin) throw deterministicBalanceError('自定义余额查询必须与账户 Base URL 同源')
  const response = await requestJson(target, context)
  return { snapshot: parseBalanceResponse(() => parseCustomBalance(response, customConfig)) }
}

/**
 * Acquire one shared upstream-I/O slot per effective API Key. Multi-Key
 * queries acquire slots inside their bounded workers; wrapping the whole pool
 * here would consume one slot while waiting for the child slots and can
 * deadlock when globalMax is 1.
 */
async function queryAccountBalanceResolutionWithGlobalSlot(
  candidate: AccountBalanceQueryCandidate,
  resolvedProxyUrl: string | null | undefined,
  executionContext: AccountBalanceRefreshExecutionContext
): Promise<AccountBalanceQueryResolution> {
  if (effectiveAccountApiKeys(candidate.credentials).length > 1) {
    return await queryAccountBalanceResolution(candidate, resolvedProxyUrl, executionContext)
  }
  return await runWithGlobalBackgroundConcurrencySlot(
    async () => await queryAccountBalanceResolution(candidate, resolvedProxyUrl, executionContext),
    { signal: executionContext.signal }
  )
}

/**
 * Queries a Key pool with a shared deadline and bounded concurrency. A pool
 * result is summed only when every successful response explicitly represents
 * an independent API-Key quota; account/wallet/subscription/unknown balances
 * remain shared and are never added together.
 */
async function queryMultiKeyAccountBalance(
  candidate: AccountBalanceQueryCandidate,
  apiKeys: string[],
  executionContext: AccountBalanceRefreshExecutionContext,
  resolvedProxyUrl?: string | null
): Promise<AccountBalanceQueryResolution> {
  const deadlineAtMs = Math.min(
    executionContext.deadlineAtMs ?? Number.POSITIVE_INFINITY,
    Date.now() + multiKeyBalanceDeadlineMs
  )
  const keyBalances: AccountBalanceKeySnapshot[] = []
  let nextIndex = 0
  const worker = async (): Promise<void> => {
    while (true) {
      const index = nextIndex++
      const apiKey = apiKeys[index]
      if (!apiKey) return
      const keyCandidate: AccountBalanceQueryCandidate = {
        ...candidate,
        credentials: { ...candidate.credentials, api_key: apiKey, api_keys: [apiKey] }
      }
      const keyStartedAt = new Date().toISOString()
      if (Date.now() >= deadlineAtMs) {
        keyBalances[index] = keySnapshotForFailure(apiKey, '上游余额查询超时', keyStartedAt)
        continue
      }
      try {
        const resolution = await runWithGlobalBackgroundConcurrencySlot(
          async () => await queryAccountBalanceResolution(keyCandidate, resolvedProxyUrl, {
            ...executionContext,
            deadlineAtMs
          }),
          { signal: executionContext.signal }
        )
        const completedAt = new Date().toISOString()
        keyBalances[index] = keySnapshotFromResult(apiKey, resolution.snapshot, completedAt)
      } catch (error) {
        // Only classified upstream/transport failures belong to a per-Key
        // diagnostic. Unexpected errors (storage, programming or contract
        // failures) must escape the pool so the caller can alert and retry.
        if (!accountBalanceFailureKind(error)) throw error
        keyBalances[index] = keySnapshotForFailure(apiKey, accountBalanceErrorMessage(error), new Date().toISOString())
      }
    }
  }
  await Promise.all(Array.from({ length: Math.min(multiKeyBalanceMaxConcurrent, apiKeys.length) }, () => worker()))
  const ordered = keyBalances.filter((value): value is AccountBalanceKeySnapshot => Boolean(value))
  return {
    snapshot: aggregateMultiKeyBalance(ordered, apiKeys.length),
    // A preferred adapter is safe only when all Keys selected the same adapter;
    // retaining the configured preference avoids persisting a mixed guess.
    ...(candidate.config.preferredBuiltinAdapter ? { preferredBuiltinAdapter: candidate.config.preferredBuiltinAdapter } : {})
  }
}

function keySnapshotFromResult(apiKey: string, snapshot: AccountBalanceSnapshot, completedAt: string): AccountBalanceKeySnapshot {
  const scope = snapshot.scope ?? balanceScopeFromBasis(snapshot.basis)
  return {
    keyFingerprint: accountBalanceApiKeyFingerprint(apiKey),
    maskedKey: maskAccountBalanceApiKey(apiKey),
    status: snapshot.status,
    ...(snapshot.remainingUsd !== undefined ? { remainingUsd: snapshot.remainingUsd } : {}),
    ...(snapshot.rawUnit ? { rawUnit: snapshot.rawUnit } : {}),
    ...(scope ? { scope } : {}),
    ...(snapshot.basis ? { basis: snapshot.basis } : {}),
    ...(snapshot.errorMessage ? { errorMessage: snapshot.errorMessage } : {}),
    lastAttemptAt: completedAt,
    ...(isSuccessfulBalanceSnapshot(snapshot) ? { lastSuccessAt: completedAt } : {})
  }
}

function keySnapshotForFailure(apiKey: string, errorMessage: string, attemptedAt: string): AccountBalanceKeySnapshot {
  return {
    keyFingerprint: accountBalanceApiKeyFingerprint(apiKey),
    maskedKey: maskAccountBalanceApiKey(apiKey),
    status: 'failed',
    errorMessage,
    lastAttemptAt: attemptedAt
  }
}

function balanceScopeFromBasis(basis: AccountBalanceSnapshot['basis']): AccountBalanceScope {
  return 'unknown'
}

function aggregateMultiKeyBalance(keyBalances: AccountBalanceKeySnapshot[], keyCount: number): AccountBalanceSnapshot {
  const successful = keyBalances.filter((item) => item.status === 'fresh' || item.status === 'unlimited')
  const allSuccessful = successful.length === keyCount
  const firstBasis = successful[0]?.basis
  const firstRawUnit = successful[0]?.rawUnit
  const allKeyQuota = allSuccessful && successful.every((item) => (
    item.scope === 'key'
    && item.status === 'fresh'
    && item.remainingUsd !== undefined
    && item.rawUnit !== undefined
    && item.basis === firstBasis
    && item.rawUnit === firstRawUnit
  ))
  if (allKeyQuota) {
    return {
      status: 'fresh',
      remainingUsd: addDecimalStrings(successful.map((item) => item.remainingUsd!)),
      scope: 'key',
      aggregation: 'sum',
      keyCount,
      queriedKeyCount: successful.length,
      keyBalances
    }
  }
  const allShared = allSuccessful && successful.every((item) => (
    item.scope === 'account'
    && item.status === 'fresh'
    && item.rawUnit !== undefined
    && item.basis === firstBasis
    && item.rawUnit === firstRawUnit
  ))
  const sharedValues = successful.map((item) => item.remainingUsd).filter((value): value is string => Boolean(value))
  if (allShared && sharedValues.length === keyCount && sharedValues.every((value) => value === sharedValues[0])) {
    return {
      status: 'fresh',
      remainingUsd: sharedValues[0],
      scope: 'account',
      aggregation: 'shared',
      keyCount,
      queriedKeyCount: successful.length,
      keyBalances
    }
  }
  return {
    status: allSuccessful && successful.length > 0 ? 'unsupported' : 'failed',
    scope: successful.some((item) => item.scope === 'account') ? 'account' : 'unknown',
    aggregation: successful.some((item) => item.scope === 'account') ? 'shared' : 'unknown',
    keyCount,
    queriedKeyCount: successful.length,
    errorMessage: allSuccessful ? '多 Key 余额口径不一致，无法安全合计' : `多 Key 余额查询部分失败（${successful.length}/${keyCount}）`,
    keyBalances
  }
}

function addDecimalStrings(values: string[]): string {
  let scale = 0
  const parts = values.map((value) => {
    const match = /^(-?)(\d+)(?:\.(\d+))?$/.exec(value.trim())
    if (!match) throw new Error('余额金额格式无效')
    scale = Math.max(scale, (match[3] ?? '').length)
    return { sign: match[1] === '-' ? -1n : 1n, digits: match[2]!, fraction: match[3] ?? '' }
  })
  const total = parts.reduce((sum, part) => sum + part.sign * BigInt(`${part.digits}${part.fraction}`) * 10n ** BigInt(scale - part.fraction.length), 0n)
  const negative = total < 0n
  const digits = (negative ? -total : total).toString().padStart(scale + 1, '0')
  const integer = scale ? digits.slice(0, -scale) : digits
  const fraction = scale ? digits.slice(-scale).replace(/0+$/, '') : ''
  return `${negative ? '-' : ''}${integer}${fraction ? `.${fraction}` : ''}`
}

export async function queryBuiltinAccountBalance(
  candidate: AccountBalanceQueryCandidate,
  dependencies: {
    queryAdapter?: (candidate: AccountBalanceQueryCandidate, adapter: AccountBalanceBuiltinAdapter) => Promise<AccountBalanceSnapshot>
    resolvedProxyUrl?: string | null
    signal?: AbortSignal
    deadlineAtMs?: number
  } = {}
): Promise<AccountBalanceBuiltinQueryResult> {
  if (candidate.config.adapter !== 'builtin') throw deterministicBalanceError('账户未配置内置余额适配')
  const apiKeys = effectiveAccountApiKeys(candidate.credentials)
  if (apiKeys.length > 1) {
    const resolution = await queryMultiKeyAccountBalance(candidate, apiKeys, {
      signal: dependencies.signal,
      deadlineAtMs: dependencies.deadlineAtMs,
      queryAdapter: dependencies.queryAdapter
    }, dependencies.resolvedProxyUrl)
    return {
      adapter: resolution.preferredBuiltinAdapter ?? candidate.config.preferredBuiltinAdapter ?? 'user_balance',
      snapshot: resolution.snapshot
    }
  }
  const adapters = preferredBuiltinAdapterOrder(candidate.config.preferredBuiltinAdapter)
  const context = dependencies.queryAdapter
    ? undefined
    : await accountBalanceRequestContext(candidate, dependencies.resolvedProxyUrl, dependencies)
  const queryAdapter = dependencies.queryAdapter
    ?? ((_candidate: AccountBalanceQueryCandidate, adapter: AccountBalanceBuiltinAdapter) => queryBuiltinAdapter(context as AccountBalanceRequestContext, adapter))
  let transientError: unknown
  let deterministicError: unknown
  let unsupportedResult: AccountBalanceBuiltinQueryResult | undefined
  for (const adapter of adapters) {
    throwIfBalanceRefreshAborted(dependencies.signal)
    try {
      const snapshot = await queryAdapter(candidate, adapter)
      if (snapshot.status === 'fresh' || snapshot.status === 'unlimited') {
        return { adapter, snapshot }
      }
      if (snapshot.status === 'unsupported') unsupportedResult = { adapter, snapshot }
    } catch (error) {
      if (context && Date.now() >= context.deadlineAtMs) {
        throw new UpstreamRequestTimeoutError('上游余额查询超时')
      }
      const failureKind = accountBalanceFailureKind(error)
      if (!failureKind) throw error
      if (failureKind === 'transient') transientError = error
      else deterministicError = error
      // A saved preference can become stale when the relay, key, or proxy changes.
    }
  }
  if (transientError) throw transientError
  if (unsupportedResult) {
    return {
      ...unsupportedResult,
      snapshot: {
        ...unsupportedResult.snapshot,
        errorMessage: unsupportedResult.snapshot.errorMessage ?? '当前配置未找到可用余额接口'
      }
    }
  }
  return {
    adapter: adapters.at(-1) ?? 'user_balance',
    snapshot: {
      status: 'unsupported',
      errorMessage: deterministicError ? accountBalanceErrorMessage(deterministicError) : '内置余额适配器均未匹配'
    }
  }
}

async function accountBalanceRequestContext(
  candidate: Pick<AccountBalanceRefreshCandidate, 'credentials' | 'proxyProfileId'>,
  resolvedProxyUrl?: string | null,
  executionContext: AccountBalanceRefreshExecutionContext = {}
): Promise<AccountBalanceRequestContext> {
  throwIfBalanceRefreshAborted(executionContext.signal)
  const baseUrlText = typeof candidate.credentials.base_url === 'string' ? candidate.credentials.base_url.trim() : ''
  if (!baseUrlText) throw deterministicBalanceError('账户未配置 Base URL')
  let baseUrl: URL
  try {
    baseUrl = new URL(baseUrlText)
  } catch {
    throw deterministicBalanceError('账户 Base URL 无效')
  }
  const apiKey = accountApiKey(candidate.credentials)
  const proxyUrl = resolvedProxyUrl !== undefined
    ? resolvedProxyUrl ?? undefined
    : candidate.proxyProfileId
      ? await resolveProxyUrlForProfileAsync(candidate.proxyProfileId)
      : undefined
  throwIfBalanceRefreshAborted(executionContext.signal)
  const localDeadlineAtMs = Date.now() + requestTimeoutMs
  return {
    baseUrl,
    apiKey,
    proxyUrl,
    deadlineAtMs: executionContext.deadlineAtMs === undefined
      ? localDeadlineAtMs
      : Math.min(localDeadlineAtMs, executionContext.deadlineAtMs),
    signal: executionContext.signal
  }
}

async function queryBuiltinAdapter(context: AccountBalanceRequestContext, adapter: AccountBalanceBuiltinAdapter): Promise<AccountBalanceSnapshot> {
  const { baseUrl } = context
  if (adapter === 'sub2api') {
    const response = await requestJson(new URL('/v1/usage', baseUrl.origin), context)
    return parseBalanceResponse(() => parseSub2ApiBalance(response))
  }
  if (adapter === 'newapi') {
    const usage = await requestJson(new URL('/api/usage/token/', baseUrl.origin), context)
    const usageData = objectValue(objectValue(usage).data)
    if (usageData.unlimited_quota === true) return parseBalanceResponse(() => parseNewApiBalance(usage, { quotaPerUnit: 1 }))
    const status = objectValue(await requestJson(new URL('/api/status', baseUrl.origin), context))
    const statusData = objectValue(status.data)
    return parseBalanceResponse(() => parseNewApiBalance(usage, { quotaPerUnit: statusData.quota_per_unit }))
  }
  if (adapter === 'openai_billing') {
    const billingOptions = parseOpenAiCompatibleBillingStatus(await requestJson(new URL('/api/status', baseUrl.origin), context))
    if ('snapshot' in billingOptions) return billingOptions.snapshot
    const subscription = await requestJson(new URL('/dashboard/billing/subscription', baseUrl.origin), context)
    const usage = await requestJson(new URL('/dashboard/billing/usage', baseUrl.origin), context)
    return parseOpenAiCompatibleBillingBalance(subscription, usage, billingOptions)
  }
  if (adapter === 'litellm') {
    const response = await requestJson(new URL('/key/info', baseUrl.origin), context)
    return parseBalanceResponse(() => parseLiteLlmBalance(response))
  }
  const response = await requestJson(new URL('/user/balance', baseUrl.origin), context)
  return parseBalanceResponse(() => parseUserBalance(response))
}

function preferredBuiltinAdapterOrder(preferred: AccountBalanceBuiltinAdapter | undefined): AccountBalanceBuiltinAdapter[] {
  return preferred
    ? [preferred, ...builtinAdapterOrder.filter((adapter) => adapter !== preferred)]
    : [...builtinAdapterOrder]
}

function resolvedBalanceConfig(
  config: AccountBalanceQueryConfig,
  preferredBuiltinAdapter: AccountBalanceBuiltinAdapter | undefined
): AccountBalanceQueryConfig {
  if (config.adapter === 'custom') return config
  return {
    adapter: 'builtin',
    intervalMinutes: config.intervalMinutes,
    ...(preferredBuiltinAdapter ? { preferredBuiltinAdapter } : {})
  }
}

async function persistBalanceRefreshIfCurrent(
  candidate: AccountBalanceRefreshCandidate,
  nextConfig: AccountBalanceQueryConfig,
  snapshot: AccountBalanceSnapshot,
  nextRefreshAfter: string | null,
  mode: AccountBalanceRefreshMode | undefined
): Promise<boolean> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return await persistAccountBalanceRefreshWithSnapshotAsync({
      accountId: candidate.id,
      systemAccountId: candidate.systemAccountId,
      expectedConfigRevision: candidate.configRevision,
      expectedConfig: candidate.config,
      expectedNextRefreshAt: mode === 'manual' ? undefined : candidate.nextRefreshAt,
      nextConfig,
      nextRefreshAt: nextRefreshAfter,
      snapshot
    })
  }
  const changed = await commitBalanceRefresh({
    accountId: candidate.id,
    expectedConfigRevision: candidate.configRevision,
    expectedConfig: candidate.config,
    expectedNextRefreshAt: mode === 'manual' ? undefined : candidate.nextRefreshAt,
    nextConfig,
    nextRefreshAt: nextRefreshAfter
  })
  if (!changed) return false
  const input = {
    accountId: candidate.id,
    systemAccountId: candidate.systemAccountId,
    expectedConfigRevision: candidate.configRevision,
    expectedConfig: nextConfig,
    snapshot,
    ...(nextRefreshAfter ? { nextRefreshAfter } : {})
  }
  if (!mainDatabaseRuntimeInfo('stats').queryOnly) {
    try {
      return await replaceAccountBalanceSnapshotIfCurrentAsync(input)
    } catch (error) {
      await rescheduleBalanceRefreshAfterSnapshotWriteFailure(candidate, nextConfig, nextRefreshAfter)
      throw error
    }
  }
  try {
    const result = await requestStatsWriter({ type: 'replace_account_balance_snapshot_if_current', input })
    return result.written
  } catch (error) {
    await rescheduleBalanceRefreshAfterSnapshotWriteFailure(candidate, nextConfig, nextRefreshAfter)
    throw error
  }
}

export async function deferAccountBalanceRefreshCandidate(
  candidate: AccountBalanceRefreshCandidate,
  retryAt = new Date(Date.now() + passiveScheduleDelayMs(60_000)).toISOString()
): Promise<boolean> {
  return await commitBalanceRefresh({
    accountId: candidate.id,
    expectedConfigRevision: candidate.configRevision,
    expectedConfig: candidate.config,
    expectedNextRefreshAt: candidate.nextRefreshAt,
    nextConfig: candidate.config,
    nextRefreshAt: retryAt
  })
}

async function rescheduleBalanceRefreshAfterSnapshotWriteFailure(
  candidate: AccountBalanceRefreshCandidate,
  nextConfig: AccountBalanceQueryConfig,
  nextRefreshAfter: string | null
): Promise<void> {
  if (!nextRefreshAfter) return
  await commitBalanceRefresh({
    accountId: candidate.id,
    expectedConfigRevision: candidate.configRevision,
    expectedConfig: nextConfig,
    expectedNextRefreshAt: nextRefreshAfter,
    nextConfig,
    nextRefreshAt: new Date().toISOString()
  }).catch(() => undefined)
}

async function loadCurrentGenerationBalanceSnapshot(
  candidate: Pick<AccountBalanceRefreshCandidate, 'id' | 'nextRefreshAt' | 'configRevision'>
): Promise<AccountBalanceSnapshot | undefined> {
  const record = (await loadAccountBalanceSnapshotRecordsByAccountIdsAsync([candidate.id])).get(candidate.id)
  return accountBalanceSnapshotMatchesConfiguration({
    nextRefreshAt: candidate.nextRefreshAt ?? undefined,
    configRevision: candidate.configRevision
  }, record)
    ? record.snapshot
    : undefined
}

/**
 * A configuration change must never revive the old amount, but retaining the
 * bounded transient-failure counter keeps retry diagnostics continuous across
 * a save that invalidated the previous snapshot generation.
 */
async function loadPreviousTransientFailureSnapshot(
  candidate: Pick<AccountBalanceRefreshCandidate, 'id' | 'nextRefreshAt' | 'configRevision'>
): Promise<AccountBalanceSnapshot | undefined> {
  const current = await loadCurrentGenerationBalanceSnapshot(candidate)
  if (current) return current
  const record = (await loadAccountBalanceSnapshotRecordsByAccountIdsAsync([candidate.id])).get(candidate.id)
  const snapshot = record?.snapshot
  if (!snapshot || snapshot.consecutiveTransientFailures === undefined) return undefined
  return {
    status: snapshot.status === 'failed' ? 'failed' : 'pending',
    consecutiveTransientFailures: snapshot.consecutiveTransientFailures,
    ...(snapshot.lastTransientErrorMessage ? { lastTransientErrorMessage: snapshot.lastTransientErrorMessage } : {}),
    ...(snapshot.lastTransientFailureAt ? { lastTransientFailureAt: snapshot.lastTransientFailureAt } : {})
  }
}

async function commitBalanceRefresh(input: Parameters<typeof commitAccountBalanceRefreshAsync>[0]): Promise<boolean> {
  if (runtimeConfig.databaseDriver === 'postgres' || !mainDatabaseRuntimeInfo('business').queryOnly) {
    return await commitAccountBalanceRefreshAsync(input)
  }
  const result = await requestBackgroundWorkerDbService({ type: 'commit_account_balance_refresh', input })
  return result?.changed === true
}

/**
 * Serializes every upstream balance query for one account, including first
 * detection, automatic refresh, and manual refresh. The lease is deliberately
 * acquired and released outside the caller's upstream I/O transaction.
 */
export async function runWithAccountBalanceLease<T>(
  candidate: Pick<AccountBalanceRefreshCandidate, 'id' | 'systemAccountId'>,
  run: () => Promise<T>
): Promise<AccountBalanceLeaseResult<T>> {
  const leaseKey = `account-balance:${candidate.id}`
  const ownerId = `account-balance-${randomUUID()}`
  const startedAt = new Date().toISOString()
  const acquired = await acquireBalanceLease({
    leaseKey,
    jobName: 'account-balance-refresh',
    shardKey: candidate.systemAccountId,
    ownerId,
    leaseUntil: new Date(Date.now() + balanceRefreshLeaseMs).toISOString(),
    now: startedAt
  })
  if (!acquired) return { acquired: false }
  try {
    return { acquired: true, value: await run() }
  } finally {
    await releaseBalanceLease(leaseKey, ownerId)
  }
}

async function acquireBalanceLease(input: Parameters<typeof acquireBackgroundJobLeaseAsync>[0]): Promise<boolean> {
  if (runtimeConfig.databaseDriver === 'postgres' || !mainDatabaseRuntimeInfo('stats').queryOnly) return await acquireBackgroundJobLeaseAsync(input)
  const result = await requestStatsWriter({ type: 'acquire_account_balance_lease', input })
  return result.acquired
}

async function releaseBalanceLease(leaseKey: string, ownerId: string): Promise<void> {
  if (runtimeConfig.databaseDriver === 'postgres' || !mainDatabaseRuntimeInfo('stats').queryOnly) {
    await releaseBackgroundJobLeaseAsync(leaseKey, ownerId)
    return
  }
  await requestStatsWriter({ type: 'release_account_balance_lease', leaseKey, ownerId })
}

async function requestJson(url: URL, context: AccountBalanceRequestContext): Promise<unknown> {
  throwIfBalanceRefreshAborted(context.signal)
  const remainingMs = context.deadlineAtMs - Date.now()
  if (remainingMs <= 0) throw new UpstreamRequestTimeoutError('上游余额查询超时')
  const deadlineSignal = AbortSignal.timeout(remainingMs)
  let response: Awaited<ReturnType<typeof requestUpstream>>
  try {
    response = await requestUpstream(url.toString(), {
      method: 'GET',
      headers: new Headers({ authorization: `Bearer ${context.apiKey}`, accept: 'application/json' }),
      proxyUrl: context.proxyUrl,
      timeoutMs: remainingMs,
      requestTimeoutMs: remainingMs,
      signal: context.signal ? AbortSignal.any([context.signal, deadlineSignal]) : deadlineSignal
    })
  } catch (error) {
    if (accountBalanceFailureKind(error)) throw error
    if (isGenericUpstreamNetworkError(error)) throw transientBalanceError('上游余额接口网络请求失败')
    throw error
  }
  if (!response.ok) throw neutralBalanceError(`上游余额接口返回非成功响应（HTTP ${response.status}）`)
  if (!response.body) throw deterministicBalanceError('上游余额接口响应为空')
  const chunks: Buffer[] = []
  let totalBytes = 0
  const bodyIterator = response.body[Symbol.asyncIterator]()
  while (true) {
    let nextChunk: IteratorResult<Uint8Array>
    try {
      nextChunk = await bodyIterator.next()
    } catch (error) {
      if (accountBalanceFailureKind(error)) throw error
      if (isGenericUpstreamNetworkError(error)) throw transientBalanceError('上游余额接口网络请求失败')
      throw error
    }
    if (nextChunk.done) break
    const chunk = nextChunk.value
    totalBytes += chunk.byteLength
    if (totalBytes > responseMaxBytes) throw deterministicBalanceError('上游余额接口响应超过 256 KiB')
    chunks.push(Buffer.from(chunk))
  }
  try {
    return JSON.parse(Buffer.concat(chunks, totalBytes).toString('utf8'))
  } catch {
    throw deterministicBalanceError('上游余额接口返回的 JSON 无效')
  }
}

function accountApiKey(credentials: Record<string, unknown>): string {
  const keys = effectiveAccountApiKeys(credentials)
  if (keys.length > 1) throw deterministicBalanceError(MULTI_KEY_ACCOUNT_BALANCE_QUERY_MESSAGE)
  if (keys.length === 1) return keys[0]
  throw deterministicBalanceError('账户没有可用的单 API Key')
}

function objectValue(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw deterministicBalanceError('上游余额接口响应结构无效')
  return value as Record<string, unknown>
}

function parseBalanceResponse(parse: () => AccountBalanceSnapshot): AccountBalanceSnapshot {
  try {
    return parse()
  } catch (error) {
    if (error instanceof AccountBalanceQueryFailure) throw error
    const message = error instanceof Error ? error.message : '上游余额接口响应结构无效'
    throw deterministicBalanceError(message)
  }
}

function accountBalanceErrorMessage(error: unknown): string {
  if (error instanceof UpstreamRequestTimeoutError || error instanceof UpstreamRequestAbortedError || (error instanceof DOMException && error.name === 'TimeoutError')) {
    return '上游余额查询超时'
  }
  if (error instanceof AccountBalanceQueryFailure) return error.message.slice(0, 200)
  return '上游余额查询失败'
}

function nextTransientFailureSnapshot(
  previous: AccountBalanceSnapshot | undefined,
  errorMessage: string,
  attemptedAt: string
): AccountBalanceSnapshot {
  const consecutiveTransientFailures = Math.min(3, (previous?.consecutiveTransientFailures ?? 0) + 1)
  if (consecutiveTransientFailures >= 3) {
    return {
      status: 'failed',
      errorMessage,
      lastAttemptAt: attemptedAt,
      consecutiveTransientFailures,
      lastTransientErrorMessage: errorMessage,
      lastTransientFailureAt: attemptedAt
    }
  }
  if (previous?.status === 'fresh' || previous?.status === 'unlimited') {
    return {
      ...previous,
      lastAttemptAt: attemptedAt,
      consecutiveTransientFailures,
      lastTransientErrorMessage: errorMessage,
      lastTransientFailureAt: attemptedAt
    }
  }
  if (previous?.status === 'failed') {
    return {
      ...previous,
      lastAttemptAt: attemptedAt,
      consecutiveTransientFailures,
      lastTransientErrorMessage: errorMessage,
      lastTransientFailureAt: attemptedAt
    }
  }
  return {
    status: 'pending',
    lastAttemptAt: attemptedAt,
    consecutiveTransientFailures,
    lastTransientErrorMessage: errorMessage,
    lastTransientFailureAt: attemptedAt
  }
}

function isSuccessfulBalanceSnapshot(snapshot: AccountBalanceSnapshot): boolean {
  return snapshot.status === 'fresh' || snapshot.status === 'unlimited'
}

function deterministicBalanceError(message: string): AccountBalanceQueryFailure {
  return new AccountBalanceQueryFailure('deterministic', message)
}

function neutralBalanceError(message: string): AccountBalanceQueryFailure {
  return new AccountBalanceQueryFailure('neutral', message)
}

function transientBalanceError(message: string): AccountBalanceQueryFailure {
  return new AccountBalanceQueryFailure('transient', message)
}

function accountBalanceFailureKind(error: unknown): AccountBalanceFailureKind | undefined {
  if (error instanceof AccountBalanceQueryFailure) return error.kind
  if (
    error instanceof UpstreamRequestTimeoutError
    || error instanceof UpstreamRequestAbortedError
    || (error instanceof DOMException && (error.name === 'TimeoutError' || error.name === 'AbortError'))
  ) {
    return 'transient'
  }
  return undefined
}

function isGenericUpstreamNetworkError(error: unknown): boolean {
  const visited = new Set<unknown>()
  let current = error
  while (current instanceof Error && !visited.has(current)) {
    if (current instanceof TypeError || typeof (current as NodeJS.ErrnoException).code === 'string') return true
    visited.add(current)
    current = current.cause
  }
  return false
}

function balanceRefreshExecutionContext(
  dependencies: AccountBalanceRefreshExecutionContext
): AccountBalanceRefreshExecutionContext {
  return {
    ...(dependencies.signal ? { signal: dependencies.signal } : {}),
    ...(dependencies.deadlineAtMs === undefined ? {} : { deadlineAtMs: dependencies.deadlineAtMs })
  }
}

function throwIfBalanceRefreshAborted(signal: AbortSignal | undefined): void {
  if (signal?.aborted) throw new UpstreamRequestAbortedError('上游余额刷新已取消')
}

function accountBalanceRefreshOutcome(snapshot: AccountBalanceSnapshot): AccountBalanceRefreshOutcome {
  if (snapshot.status === 'unsupported') return 'unsupported'
  if (snapshot.status === 'failed') return 'failed'
  if (snapshot.lastTransientFailureAt && snapshot.lastTransientFailureAt === snapshot.lastAttemptAt) return 'stale'
  if (snapshot.status === 'pending' || snapshot.status === 'refreshing') return 'stale'
  return 'refreshed'
}

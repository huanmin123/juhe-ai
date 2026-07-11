import { randomUUID } from 'node:crypto'

import { runtimeConfig } from '../../config/runtime.js'
import type { AccountBalanceBuiltinAdapter, AccountBalanceQueryConfig, AccountBalanceSnapshot } from './account-balance.types.js'
import type { AccountBalanceRefreshCandidate } from '../../storage/account-balance.repository.js'
import {
  commitAccountBalanceRefreshAsync,
  loadAccountBalanceSnapshotsByAccountIdsAsync,
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
import { parseCustomBalance, parseLiteLlmBalance, parseNewApiBalance, parseSub2ApiBalance, parseUserBalance } from './account-balance-adapters.js'

const responseMaxBytes = 256 * 1024
const requestTimeoutMs = 8_000
const balanceRefreshLeaseMs = 30_000
const builtinAdapterOrder: AccountBalanceBuiltinAdapter[] = ['sub2api', 'newapi', 'litellm', 'user_balance']

type AccountBalanceQueryCandidate = Pick<AccountBalanceRefreshCandidate, 'id' | 'credentials' | 'config' | 'proxyProfileId'>

interface AccountBalanceRequestContext {
  baseUrl: URL
  apiKey: string
  proxyUrl?: string
  deadlineAtMs: number
}

export interface AccountBalanceBuiltinQueryResult {
  adapter: AccountBalanceBuiltinAdapter
  snapshot: AccountBalanceSnapshot
}

interface AccountBalanceQueryResolution {
  snapshot: AccountBalanceSnapshot
  preferredBuiltinAdapter?: AccountBalanceBuiltinAdapter
}

export async function refreshAccountBalanceCandidate(
  candidate: AccountBalanceRefreshCandidate,
  dependencies: { query?: (candidate: AccountBalanceRefreshCandidate) => Promise<AccountBalanceSnapshot> } = {}
): Promise<AccountBalanceSnapshot> {
  if (!candidate) throw new Error('余额刷新账户不存在')
  const leaseKey = `account-balance:${candidate.id}`
  const ownerId = `account-balance-${randomUUID()}`
  const startedAt = new Date().toISOString()
  const leaseInput = {
    leaseKey,
    jobName: 'account-balance-refresh',
    shardKey: candidate.systemAccountId,
    ownerId,
    leaseUntil: new Date(Date.now() + balanceRefreshLeaseMs).toISOString(),
    now: startedAt
  }
  const acquired = await acquireBalanceLease(leaseInput)
  if (!acquired) {
    return (await loadAccountBalanceSnapshotsByAccountIdsAsync([candidate.id])).get(candidate.id)
      ?? { status: 'refreshing', lastAttemptAt: startedAt }
  }

  try {
    const resolution = dependencies.query
      ? { snapshot: await dependencies.query(candidate) }
      : await queryAccountBalanceResolution(candidate)
    const completedAt = new Date().toISOString()
    const nextRefreshAfter = new Date(Date.now() + candidate.config.intervalMinutes * 60_000).toISOString()
    const snapshot: AccountBalanceSnapshot = {
      ...resolution.snapshot,
      lastAttemptAt: completedAt,
      ...(resolution.snapshot.status === 'fresh' || resolution.snapshot.status === 'unlimited' || resolution.snapshot.status === 'unsupported'
        ? { lastSuccessAt: completedAt }
        : {})
    }
    const nextConfig = resolvedBalanceConfig(candidate.config, resolution.preferredBuiltinAdapter)
    await persistBalanceRefreshIfCurrent(candidate, nextConfig, snapshot, nextRefreshAfter)
    return snapshot
  } catch (error) {
    const completedAt = new Date().toISOString()
    const nextRefreshAfter = new Date(Date.now() + candidate.config.intervalMinutes * 60_000).toISOString()
    const snapshot: AccountBalanceSnapshot = {
      status: 'failed',
      errorMessage: accountBalanceErrorMessage(error),
      lastAttemptAt: completedAt
    }
    const nextConfig = resolvedBalanceConfig(candidate.config, undefined)
    await persistBalanceRefreshIfCurrent(candidate, nextConfig, snapshot, nextRefreshAfter)
    return snapshot
  } finally {
    await releaseBalanceLease(leaseKey, ownerId)
  }
}

export async function queryAccountBalance(candidate: AccountBalanceQueryCandidate): Promise<AccountBalanceSnapshot> {
  return (await queryAccountBalanceResolution(candidate)).snapshot
}

async function queryAccountBalanceResolution(candidate: AccountBalanceQueryCandidate): Promise<AccountBalanceQueryResolution> {
  if (candidate.config.adapter === 'builtin') {
    const result = await queryBuiltinAccountBalance(candidate)
    return { snapshot: result.snapshot, preferredBuiltinAdapter: result.adapter }
  }
  const context = await accountBalanceRequestContext(candidate)
  if (!candidate.config.custom) throw new Error('自定义余额查询配置缺失')
  const target = new URL(candidate.config.custom.path, context.baseUrl)
  if (target.origin !== context.baseUrl.origin) throw new Error('自定义余额查询必须与账户 Base URL 同源')
  return { snapshot: parseCustomBalance(await requestJson(target, context), candidate.config.custom) }
}

export async function queryBuiltinAccountBalance(
  candidate: AccountBalanceQueryCandidate,
  dependencies: {
    queryAdapter?: (candidate: AccountBalanceQueryCandidate, adapter: AccountBalanceBuiltinAdapter) => Promise<AccountBalanceSnapshot>
  } = {}
): Promise<AccountBalanceBuiltinQueryResult> {
  if (candidate.config.adapter !== 'builtin') throw new Error('账户未配置内置余额适配')
  const adapters = preferredBuiltinAdapterOrder(candidate.config.preferredBuiltinAdapter)
  const context = dependencies.queryAdapter ? undefined : await accountBalanceRequestContext(candidate)
  const queryAdapter = dependencies.queryAdapter
    ?? ((_candidate: AccountBalanceQueryCandidate, adapter: AccountBalanceBuiltinAdapter) => queryBuiltinAdapter(context as AccountBalanceRequestContext, adapter))
  for (const adapter of adapters) {
    try {
      const snapshot = await queryAdapter(candidate, adapter)
      if (snapshot.status === 'fresh' || snapshot.status === 'unlimited') {
        return { adapter, snapshot }
      }
    } catch {
      if (context && Date.now() >= context.deadlineAtMs) {
        throw new UpstreamRequestTimeoutError('上游余额查询超时')
      }
      // A saved preference can become stale when the relay, key, or proxy changes.
    }
  }
  throw new Error('内置余额适配器均未匹配')
}

async function accountBalanceRequestContext(candidate: Pick<AccountBalanceRefreshCandidate, 'credentials' | 'proxyProfileId'>): Promise<AccountBalanceRequestContext> {
  const baseUrlText = typeof candidate.credentials.base_url === 'string' ? candidate.credentials.base_url.trim() : ''
  if (!baseUrlText) throw new Error('账户未配置 Base URL')
  const baseUrl = new URL(baseUrlText)
  const apiKey = accountApiKey(candidate.credentials)
  const proxyUrl = candidate.proxyProfileId ? await resolveProxyUrlForProfileAsync(candidate.proxyProfileId) : undefined
  return { baseUrl, apiKey, proxyUrl, deadlineAtMs: Date.now() + requestTimeoutMs }
}

async function queryBuiltinAdapter(context: AccountBalanceRequestContext, adapter: AccountBalanceBuiltinAdapter): Promise<AccountBalanceSnapshot> {
  const { baseUrl } = context
  if (adapter === 'sub2api') {
    return parseSub2ApiBalance(await requestJson(new URL('/v1/usage', baseUrl.origin), context))
  }
  if (adapter === 'newapi') {
    const usage = await requestJson(new URL('/api/usage/token/', baseUrl.origin), context)
    const usageData = objectValue(objectValue(usage).data)
    if (usageData.unlimited_quota === true) return parseNewApiBalance(usage, { quotaPerUnit: 1 })
    const status = objectValue(await requestJson(new URL('/api/status', baseUrl.origin), context))
    const statusData = objectValue(status.data)
    return parseNewApiBalance(usage, { quotaPerUnit: statusData.quota_per_unit })
  }
  if (adapter === 'litellm') {
    return parseLiteLlmBalance(await requestJson(new URL('/key/info', baseUrl.origin), context))
  }
  return parseUserBalance(await requestJson(new URL('/user/balance', baseUrl.origin), context))
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
  nextRefreshAfter: string
): Promise<boolean> {
  const changed = await commitBalanceRefresh({
    accountId: candidate.id,
    expectedConfigRevision: candidate.configRevision,
    expectedConfig: candidate.config,
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
    nextRefreshAfter
  }
  if (runtimeConfig.databaseDriver === 'postgres' || !mainDatabaseRuntimeInfo('stats').queryOnly) {
    return await replaceAccountBalanceSnapshotIfCurrentAsync(input)
  }
  const result = await requestStatsWriter({ type: 'replace_account_balance_snapshot_if_current', input })
  return result.written
}

async function commitBalanceRefresh(input: Parameters<typeof commitAccountBalanceRefreshAsync>[0]): Promise<boolean> {
  if (runtimeConfig.databaseDriver === 'postgres' || !mainDatabaseRuntimeInfo('business').queryOnly) {
    return await commitAccountBalanceRefreshAsync(input)
  }
  const result = await requestBackgroundWorkerDbService({ type: 'commit_account_balance_refresh', input })
  return result?.changed === true
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
  const remainingMs = context.deadlineAtMs - Date.now()
  if (remainingMs <= 0) throw new UpstreamRequestTimeoutError('上游余额查询超时')
  const response = await requestUpstream(url.toString(), {
    method: 'GET',
    headers: new Headers({ authorization: `Bearer ${context.apiKey}`, accept: 'application/json' }),
    proxyUrl: context.proxyUrl,
    timeoutMs: remainingMs,
    requestTimeoutMs: remainingMs,
    signal: AbortSignal.timeout(remainingMs)
  })
  if (response.status === 401 || response.status === 403) throw new Error(`上游鉴权失败（HTTP ${response.status}）`)
  if (response.status >= 300 && response.status < 400) throw new Error(`上游余额接口禁止重定向（HTTP ${response.status}）`)
  if (!response.ok) throw new Error(`上游余额接口请求失败（HTTP ${response.status}）`)
  if (!response.body) throw new Error('上游余额接口响应为空')
  const chunks: Buffer[] = []
  let totalBytes = 0
  for await (const chunk of response.body) {
    totalBytes += chunk.byteLength
    if (totalBytes > responseMaxBytes) throw new Error('上游余额接口响应超过 256 KiB')
    chunks.push(Buffer.from(chunk))
  }
  try {
    return JSON.parse(Buffer.concat(chunks, totalBytes).toString('utf8'))
  } catch {
    throw new Error('上游余额接口返回的 JSON 无效')
  }
}

function accountApiKey(credentials: Record<string, unknown>): string {
  if (Array.isArray(credentials.api_keys)) {
    const keys = credentials.api_keys.filter((value): value is string => typeof value === 'string' && value.trim().length > 0)
    if (keys.length === 1) return keys[0].trim()
  }
  if (typeof credentials.api_key === 'string' && credentials.api_key.trim()) return credentials.api_key.trim()
  throw new Error('账户没有可用的单 API Key')
}

function objectValue(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('上游余额接口响应结构无效')
  return value as Record<string, unknown>
}

function accountBalanceErrorMessage(error: unknown): string {
  if (error instanceof UpstreamRequestTimeoutError || error instanceof UpstreamRequestAbortedError || (error instanceof DOMException && error.name === 'TimeoutError')) {
    return '上游余额查询超时'
  }
  const message = error instanceof Error ? error.message : '上游余额查询失败'
  if (/上游鉴权失败|禁止重定向|HTTP \d{3}|响应超过|JSON 无效|响应为空|配置缺失|同源|字段|余额|预算|quota/i.test(message)) {
    return message.slice(0, 200)
  }
  return '上游余额查询失败'
}

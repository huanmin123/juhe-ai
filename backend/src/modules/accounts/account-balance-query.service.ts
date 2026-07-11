import { randomUUID } from 'node:crypto'

import type { AccountBalanceSnapshot } from './account-balance.types.js'
import type { AccountBalanceRefreshCandidate } from '../../storage/account-balance.repository.js'
import {
  loadAccountBalanceSnapshotsByAccountIdsAsync,
  replaceAccountBalanceSnapshotAsync,
  updateAccountBalanceNextRefreshAsync
} from '../../storage/account-balance.repository.js'
import {
  acquireBackgroundJobLeaseAsync,
  releaseBackgroundJobLeaseAsync
} from '../../storage/background-task-runs.repository.js'
import { resolveProxyUrlForProfileAsync } from '../../storage/proxy.repository.js'
import { requestUpstream, UpstreamRequestAbortedError, UpstreamRequestTimeoutError } from '../gateway/upstream/request.js'
import { parseCustomBalance, parseLiteLlmBalance, parseNewApiBalance, parseSub2ApiBalance } from './account-balance-adapters.js'

const responseMaxBytes = 256 * 1024
const requestTimeoutMs = 8_000

export async function refreshAccountBalanceCandidate(
  candidate: AccountBalanceRefreshCandidate,
  dependencies: { query?: (candidate: AccountBalanceRefreshCandidate) => Promise<AccountBalanceSnapshot> } = {}
): Promise<AccountBalanceSnapshot> {
  if (!candidate) throw new Error('余额刷新账户不存在')
  const leaseKey = `account-balance:${candidate.id}`
  const ownerId = `account-balance-${randomUUID()}`
  const startedAt = new Date().toISOString()
  const acquired = await acquireBackgroundJobLeaseAsync({
    leaseKey,
    jobName: 'account-balance-refresh',
    shardKey: candidate.systemAccountId,
    ownerId,
    leaseUntil: new Date(Date.now() + 15_000).toISOString(),
    now: startedAt
  })
  if (!acquired) {
    return (await loadAccountBalanceSnapshotsByAccountIdsAsync([candidate.id])).get(candidate.id)
      ?? { status: 'refreshing', lastAttemptAt: startedAt }
  }

  await replaceAccountBalanceSnapshotAsync({
    accountId: candidate.id,
    systemAccountId: candidate.systemAccountId,
    snapshot: { status: 'refreshing', lastAttemptAt: startedAt }
  })
  try {
    const result = await (dependencies.query ?? queryAccountBalance)(candidate)
    const completedAt = new Date().toISOString()
    const nextRefreshAfter = new Date(Date.now() + candidate.config.intervalMinutes * 60_000).toISOString()
    const snapshot: AccountBalanceSnapshot = {
      ...result,
      lastAttemptAt: completedAt,
      ...(result.status === 'fresh' || result.status === 'unlimited' || result.status === 'unsupported'
        ? { lastSuccessAt: completedAt }
        : {})
    }
    await replaceAccountBalanceSnapshotAsync({
      accountId: candidate.id,
      systemAccountId: candidate.systemAccountId,
      snapshot,
      nextRefreshAfter
    })
    await updateAccountBalanceNextRefreshAsync(candidate.id, nextRefreshAfter)
    return snapshot
  } catch (error) {
    const completedAt = new Date().toISOString()
    const nextRefreshAfter = new Date(Date.now() + candidate.config.intervalMinutes * 60_000).toISOString()
    const snapshot: AccountBalanceSnapshot = {
      status: 'failed',
      errorMessage: accountBalanceErrorMessage(error),
      lastAttemptAt: completedAt
    }
    await replaceAccountBalanceSnapshotAsync({
      accountId: candidate.id,
      systemAccountId: candidate.systemAccountId,
      snapshot,
      nextRefreshAfter
    })
    await updateAccountBalanceNextRefreshAsync(candidate.id, nextRefreshAfter)
    return snapshot
  } finally {
    await releaseBackgroundJobLeaseAsync(leaseKey, ownerId)
  }
}

async function queryAccountBalance(candidate: AccountBalanceRefreshCandidate): Promise<AccountBalanceSnapshot> {
  const baseUrlText = typeof candidate.credentials.base_url === 'string' ? candidate.credentials.base_url.trim() : ''
  if (!baseUrlText) throw new Error('账户未配置 Base URL')
  const baseUrl = new URL(baseUrlText)
  const apiKey = accountApiKey(candidate.credentials)
  const proxyUrl = candidate.proxyProfileId ? await resolveProxyUrlForProfileAsync(candidate.proxyProfileId) : undefined
  if (candidate.config.adapter === 'sub2api') {
    return parseSub2ApiBalance(await requestJson(new URL('/v1/usage', baseUrl.origin), apiKey, proxyUrl))
  }
  if (candidate.config.adapter === 'newapi') {
    const usage = await requestJson(new URL('/api/usage/token/', baseUrl.origin), apiKey, proxyUrl)
    const usageData = objectValue(objectValue(usage).data)
    if (usageData.unlimited_quota === true) return parseNewApiBalance(usage, { quotaPerUnit: 1 })
    const status = objectValue(await requestJson(new URL('/api/status', baseUrl.origin), apiKey, proxyUrl))
    const statusData = objectValue(status.data)
    return parseNewApiBalance(usage, { quotaPerUnit: statusData.quota_per_unit })
  }
  if (candidate.config.adapter === 'litellm') {
    return parseLiteLlmBalance(await requestJson(new URL('/key/info', baseUrl.origin), apiKey, proxyUrl))
  }
  if (!candidate.config.custom) throw new Error('自定义余额查询配置缺失')
  const target = new URL(candidate.config.custom.path, baseUrl)
  if (target.origin !== baseUrl.origin) throw new Error('自定义余额查询必须与账户 Base URL 同源')
  return parseCustomBalance(await requestJson(target, apiKey, proxyUrl), candidate.config.custom)
}

async function requestJson(url: URL, apiKey: string, proxyUrl?: string): Promise<unknown> {
  const response = await requestUpstream(url.toString(), {
    method: 'GET',
    headers: new Headers({ authorization: `Bearer ${apiKey}`, accept: 'application/json' }),
    proxyUrl,
    timeoutMs: requestTimeoutMs,
    requestTimeoutMs,
    signal: AbortSignal.timeout(requestTimeoutMs)
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

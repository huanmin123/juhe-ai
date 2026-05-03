import { existsSync, statSync } from 'node:fs'
import { request as httpsRequest } from 'node:https'
import { cpus, freemem, totalmem } from 'node:os'

import type { AccountSummary } from '../../domain/types.js'
import { runtimeConfig } from '../../config/runtime.js'
import {
  aggregateUsageStatsBatch,
  getSettings,
  insertSystemMetricsSample,
  latestUsageStatsLagSeconds,
  listAccounts,
  markAccountCooldown,
  resolveProxyUrlForProfileForSystemAccount,
  updateAccount,
  updateAccountUsageSnapshotRefreshState
} from '../../storage/repositories.js'
import {
  calculateOpenAICodexRateLimitResetAt,
  persistOpenAICodexUsageHeaders
} from '../gateway/openai-codex-usage.service.js'
import {
  buildOpenAIOAuthCredentials,
  createProxyAgent,
  refreshOpenAIOAuthToken,
  shouldRefreshOpenAIOAuthCredentials
} from '../openai-oauth/openai-oauth.service.js'

const codexUsageProbeUrl = 'https://chatgpt.com/backend-api/codex/responses'
const codexUsageProbeModel = 'gpt-5.5'
const codexUsageProbePrompt = 'hi'

let started = false
let previousCpuSnapshot = cpuSnapshot()
let lastMetricsExpectedAt = Date.now()

export function startBackgroundJobs(): void {
  if (started) return
  started = true

  void runUsageStatsAggregation()
  void runSystemMetricsSample()
  void runOpenAIOAuthUsageRefresh()

  setInterval(() => { void runUsageStatsAggregation() }, settingsNumber('statsAggregationIntervalSeconds', 60, 5, 3600) * 1000)
  setInterval(() => { void runSystemMetricsSample() }, settingsNumber('systemMetricsSampleIntervalSeconds', 30, 5, 3600) * 1000)
  setInterval(() => { void runOpenAIOAuthUsageRefresh() }, settingsNumber('oauthUsageSnapshotRefreshIntervalSeconds', 300, 60, 86400) * 1000)
}

async function runUsageStatsAggregation(): Promise<void> {
  try {
    aggregateUsageStatsBatch(2000)
  } catch (error) {
    console.error('[background] usage stats aggregation failed', error)
  }
}

async function runSystemMetricsSample(): Promise<void> {
  const now = Date.now()
  const lagMs = Math.max(0, now - lastMetricsExpectedAt)
  lastMetricsExpectedAt = now + settingsNumber('systemMetricsSampleIntervalSeconds', 30, 5, 3600) * 1000
  const memoryTotalBytes = totalmem()
  const memoryFreeBytes = freemem()
  const memoryUsedPercent = memoryTotalBytes > 0 ? ((memoryTotalBytes - memoryFreeBytes) / memoryTotalBytes) * 100 : undefined
  const memoryUsage = process.memoryUsage()
  try {
    insertSystemMetricsSample({
      cpuPercent: currentCpuPercent(),
      memoryUsedPercent,
      memoryTotalBytes,
      memoryFreeBytes,
      processRssBytes: memoryUsage.rss,
      processHeapUsedBytes: memoryUsage.heapUsed,
      processHeapTotalBytes: memoryUsage.heapTotal,
      eventLoopLagMs: lagMs,
      dbFileBytes: databaseFileBytes(),
      statsLagSeconds: latestUsageStatsLagSeconds()
    })
  } catch (error) {
    console.error('[background] system metrics sample failed', error)
  }
}

async function runOpenAIOAuthUsageRefresh(): Promise<void> {
  const candidates = openAIOAuthUsageRefreshCandidates()
  for (const account of candidates) {
    await refreshOpenAIOAuthUsageSnapshot(account)
  }
}

function openAIOAuthUsageRefreshCandidates(): AccountSummary[] {
  const now = Date.now()
  const ttlMs = settingsNumber('oauthUsageSnapshotTtlSeconds', 900, 60, 86400) * 1000
  const limit = settingsNumber('oauthUsageSnapshotPerAccountConcurrency', 1, 1, 20) * Math.max(1, listSystemAccountCount())
  return listAccounts()
    .filter((account) => account.providerCode === 'openai' && account.type === 'oauth')
    .filter((account) => account.status !== 'disabled' && account.schedulable)
    .filter((account) => isOAuthUsageSnapshotDue(account, now, ttlMs))
    .slice(0, limit)
}

function isOAuthUsageSnapshotDue(account: AccountSummary, now: number, ttlMs: number): boolean {
  const nextRefreshAt = account.oauthUsage?.nextRefreshAfter ? Date.parse(account.oauthUsage.nextRefreshAfter) : NaN
  if (Number.isFinite(nextRefreshAt) && nextRefreshAt > now) return false
  const updatedAt = account.oauthUsage?.updatedAt ? Date.parse(account.oauthUsage.updatedAt) : NaN
  return !Number.isFinite(updatedAt) || now - updatedAt >= ttlMs
}

async function refreshOpenAIOAuthUsageSnapshot(account: AccountSummary): Promise<void> {
  const attemptedAt = new Date().toISOString()
  updateAccountUsageSnapshotRefreshState({ accountId: account.id, kind: 'openai_codex', status: 'pending', attemptedAt })
  try {
    const prepared = await prepareOAuthProbeAccount(account)
    const response = await requestCodexUsageProbe(prepared)
    const persisted = persistOpenAICodexUsageHeaders(account.id, response.headers, 'background_probe')
    if (response.statusCode === 429) {
      const resetAt = calculateOpenAICodexRateLimitResetAt(response.headers, response.bodyText)
      if (resetAt) {
        markAccountCooldown(account.id, resetAt, 'OpenAI OAuth 额度达到上游限制', 'rate_limited')
        updateAccountUsageSnapshotRefreshState({
          accountId: account.id,
          kind: 'openai_codex',
          status: 'rate_limited',
          attemptedAt,
          nextRefreshAfter: resetAt,
          errorMessage: 'OpenAI OAuth usage limit reached'
        })
      }
      return
    }
    if (!persisted) {
      updateAccountUsageSnapshotRefreshState({
        accountId: account.id,
        kind: 'openai_codex',
        status: 'failed',
        attemptedAt,
        nextRefreshAfter: nextBackoffAt(),
        errorMessage: `Codex usage headers missing; HTTP ${response.statusCode}`
      })
    }
  } catch (error) {
    updateAccountUsageSnapshotRefreshState({
      accountId: account.id,
      kind: 'openai_codex',
      status: 'failed',
      attemptedAt,
      nextRefreshAfter: nextBackoffAt(),
      errorMessage: error instanceof Error ? error.message : 'OpenAI OAuth usage refresh failed'
    })
  }
}

async function prepareOAuthProbeAccount(account: AccountSummary): Promise<{
  accessToken: string
  chatgptAccountId?: string
  proxyUrl?: string
}> {
  const refreshToken = stringValue(account.credentials.refresh_token)
  const clientId = stringValue(account.credentials.client_id)
  const proxyUrl = resolveProxyUrlForProfileForSystemAccount(account.proxyProfileId, account.systemAccountId ?? 'sys_admin')
  let credentials = account.credentials
  if (shouldRefreshOpenAIOAuthCredentials(credentials)) {
    if (!refreshToken) {
      throw new Error('OAuth 账户缺少 refresh_token，无法刷新 access_token')
    }
    const tokenInfo = await refreshOpenAIOAuthToken({ refreshToken, clientId, proxyUrl })
    credentials = {
      ...credentials,
      ...buildOpenAIOAuthCredentials(tokenInfo, { refreshToken })
    }
    updateAccount(account.id, { credentials, status: 'active' })
  }
  const accessToken = stringValue(credentials.access_token)
  if (!accessToken) {
    throw new Error('OAuth 账户缺少 access_token')
  }
  return {
    accessToken,
    chatgptAccountId: stringValue(credentials.chatgpt_account_id) || stringValue(credentials.account_id),
    proxyUrl
  }
}

function requestCodexUsageProbe(input: {
  accessToken: string
  chatgptAccountId?: string
  proxyUrl?: string
}): Promise<{ statusCode: number; headers: Record<string, string | string[]>; bodyText: string }> {
  const bodyText = JSON.stringify({
    model: codexUsageProbeModel,
    input: [
      {
        role: 'user',
        content: [{ type: 'input_text', text: codexUsageProbePrompt }]
      }
    ],
    instructions: 'You are ChatGPT, a helpful assistant.',
    stream: true,
    store: false
  })
  const headers: Record<string, string> = {
    authorization: `Bearer ${input.accessToken}`,
    accept: 'text/event-stream',
    'content-type': 'application/json',
    'content-length': String(Buffer.byteLength(bodyText)),
    'user-agent': 'codex_cli_rs/0.125.0',
    originator: 'codex_cli_rs',
    version: '0.125.0',
    'openai-beta': 'responses=experimental'
  }
  if (input.chatgptAccountId) headers['chatgpt-account-id'] = input.chatgptAccountId

  return new Promise((resolve, reject) => {
    const request = httpsRequest(codexUsageProbeUrl, {
      method: 'POST',
      headers,
      agent: input.proxyUrl ? createProxyAgent(input.proxyUrl) : undefined,
      timeout: 120000
    }, (response) => {
      const chunks: Buffer[] = []
      response.on('data', (chunk: Buffer) => chunks.push(chunk))
      response.on('end', () => {
        resolve({
          statusCode: response.statusCode ?? 0,
          headers: normalizeHeaders(response.headers),
          bodyText: Buffer.concat(chunks).toString('utf8')
        })
      })
    })
    request.on('error', reject)
    request.on('timeout', () => request.destroy(new Error('OpenAI OAuth usage refresh timed out')))
    request.end(bodyText)
  })
}

function normalizeHeaders(headers: Record<string, string | string[] | number | undefined>): Record<string, string | string[]> {
  const output: Record<string, string | string[]> = {}
  for (const [key, value] of Object.entries(headers)) {
    if (typeof value === 'string' || Array.isArray(value)) {
      output[key] = value
    } else if (typeof value === 'number') {
      output[key] = String(value)
    }
  }
  return output
}

function settingsNumber(key: string, fallback: number, min: number, max: number): number {
  const value = getSettings()[key]
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? Math.min(Math.max(Math.trunc(number), min), max) : fallback
}

function nextBackoffAt(): string {
  return new Date(Date.now() + settingsNumber('oauthUsageSnapshotRetryBackoffSeconds', 300, 60, 86400) * 1000).toISOString()
}

function listSystemAccountCount(): number {
  return Math.max(1, new Set(listAccounts().map((account) => account.systemAccountId ?? 'sys_admin')).size)
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.length > 0 ? value : undefined
}

function databaseFileBytes(): number | undefined {
  try {
    return existsSync(runtimeConfig.databasePath) ? statSync(runtimeConfig.databasePath).size : undefined
  } catch {
    return undefined
  }
}

interface CpuSnapshot {
  idle: number
  total: number
}

function cpuSnapshot(): CpuSnapshot {
  let idle = 0
  let total = 0
  for (const cpu of cpus()) {
    idle += cpu.times.idle
    total += Object.values(cpu.times).reduce((sum, value) => sum + value, 0)
  }
  return { idle, total }
}

function currentCpuPercent(): number | undefined {
  const next = cpuSnapshot()
  const idleDelta = next.idle - previousCpuSnapshot.idle
  const totalDelta = next.total - previousCpuSnapshot.total
  previousCpuSnapshot = next
  if (totalDelta <= 0) return undefined
  return Math.min(100, Math.max(0, (1 - idleDelta / totalDelta) * 100))
}

import type { AccountSummary } from '../../domain/types.js'
import {
  getSettings,
  updateAccountUsageSnapshotRefreshState
} from '../../storage/repositories.js'
import {
  calculateOpenAICodexRateLimitResetAt,
  parseOpenAICodexUsageHeaders
} from '../gateway/openai-codex-usage.service.js'
import { testOpenAIAccount } from '../accounts/account-test.service.js'

export interface OpenAIOAuthUsageRefreshOptions {
  source?: string
  requestTimeoutMs?: number
}

export interface OpenAIOAuthUsageRefreshResult {
  status: 'fresh' | 'failed' | 'rate_limited'
  statusCode?: number
  persisted: boolean
  errorMessage?: string
  nextRefreshAfter?: string
}

export function isOpenAIOAuthUsageSnapshotDue(account: AccountSummary, now: number, ttlMs: number): boolean {
  const nextRefreshAt = account.oauthUsage?.nextRefreshAfter ? Date.parse(account.oauthUsage.nextRefreshAfter) : NaN
  if (Number.isFinite(nextRefreshAt) && nextRefreshAt > now) return false
  const updatedAt = account.oauthUsage?.updatedAt ? Date.parse(account.oauthUsage.updatedAt) : NaN
  return !Number.isFinite(updatedAt) || now - updatedAt >= ttlMs
}

export async function refreshOpenAIOAuthUsageSnapshot(
  account: AccountSummary,
  options: OpenAIOAuthUsageRefreshOptions = {}
): Promise<OpenAIOAuthUsageRefreshResult> {
  void options.source
  void options.requestTimeoutMs
  const attemptedAt = new Date().toISOString()
  updateAccountUsageSnapshotRefreshState({ accountId: account.id, kind: 'openai_codex', status: 'pending', attemptedAt })
  try {
    const response = await testOpenAIAccount(account, {
      model: 'gpt-5.5',
      prompt: 'hi',
      includeUnavailable: true
    })
    const persisted = account.type === 'oauth' && Boolean(parseOpenAICodexUsageHeaders(response.responseHeaders))
    if (response.statusCode === 429) {
      const resetAt = calculateOpenAICodexRateLimitResetAt(response.responseHeaders, response.responseText)
      if (resetAt) {
        updateAccountUsageSnapshotRefreshState({
          accountId: account.id,
          kind: 'openai_codex',
          status: 'rate_limited',
          attemptedAt,
          nextRefreshAfter: resetAt,
          errorMessage: 'OpenAI OAuth usage limit reached'
        })
        return { status: 'rate_limited', statusCode: response.statusCode, persisted, nextRefreshAfter: resetAt }
      }
      const nextRefreshAfter = nextBackoffAt()
      const errorMessage = `OpenAI OAuth usage refresh rate limited; HTTP ${response.statusCode}`
      updateAccountUsageSnapshotRefreshState({
        accountId: account.id,
        kind: 'openai_codex',
        status: 'rate_limited',
        attemptedAt,
        nextRefreshAfter,
        errorMessage
      })
      return { status: 'rate_limited', statusCode: response.statusCode, persisted, errorMessage, nextRefreshAfter }
    }
    if (!response.success || !persisted) {
      const errorMessage = response.success
        ? `Codex usage headers missing; HTTP ${response.statusCode ?? 'unknown'}`
        : response.message || `OpenAI OAuth usage refresh failed; HTTP ${response.statusCode ?? 'unknown'}`
      const nextRefreshAfter = nextBackoffAt()
      updateAccountUsageSnapshotRefreshState({
        accountId: account.id,
        kind: 'openai_codex',
        status: 'failed',
        attemptedAt,
        nextRefreshAfter,
        errorMessage
      })
      return { status: 'failed', statusCode: response.statusCode, persisted, errorMessage, nextRefreshAfter }
    }
    updateAccountUsageSnapshotRefreshState({
      accountId: account.id,
      kind: 'openai_codex',
      status: 'fresh',
      attemptedAt,
      successAt: new Date().toISOString()
    })
    return { status: 'fresh', statusCode: response.statusCode, persisted }
  } catch (error) {
    const errorMessage = error instanceof Error ? error.message : 'OpenAI OAuth usage refresh failed'
    const nextRefreshAfter = nextBackoffAt()
    updateAccountUsageSnapshotRefreshState({
      accountId: account.id,
      kind: 'openai_codex',
      status: 'failed',
      attemptedAt,
      nextRefreshAfter,
      errorMessage
    })
    return { status: 'failed', persisted: false, errorMessage, nextRefreshAfter }
  }
}

function nextBackoffAt(): string {
  return new Date(Date.now() + settingsNumber('oauthUsageSnapshotRetryBackoffSeconds', 300, 60, 86400) * 1000).toISOString()
}

function settingsNumber(key: string, fallback: number, min: number, max: number): number {
  const value = getSettings()[key]
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? Math.min(Math.max(Math.trunc(number), min), max) : fallback
}

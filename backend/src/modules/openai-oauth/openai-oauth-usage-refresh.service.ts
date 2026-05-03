import type { Agent } from 'node:http'
import { request as httpsRequest } from 'node:https'

import type { AccountSummary } from '../../domain/types.js'
import {
  getSettings,
  markAccountCooldown,
  resolveAccountSystemAccountId,
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
} from './openai-oauth.service.js'

const codexUsageProbeUrl = 'https://chatgpt.com/backend-api/codex/responses'
const codexUsageProbeModel = 'gpt-5.5'
const codexUsageProbePrompt = 'hi'

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
  const attemptedAt = new Date().toISOString()
  updateAccountUsageSnapshotRefreshState({ accountId: account.id, kind: 'openai_codex', status: 'pending', attemptedAt })
  try {
    const prepared = await prepareOAuthProbeAccount(account)
    const response = await requestCodexUsageProbe(prepared, options.requestTimeoutMs)
    const persisted = persistOpenAICodexUsageHeaders(account.id, response.headers, options.source ?? 'background_probe')
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
    if (!persisted) {
      const errorMessage = `Codex usage headers missing; HTTP ${response.statusCode}`
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

async function prepareOAuthProbeAccount(account: AccountSummary): Promise<{
  accessToken: string
  chatgptAccountId?: string
  proxyUrl?: string
}> {
  const refreshToken = stringValue(account.credentials.refresh_token)
  const clientId = stringValue(account.credentials.client_id)
  const systemAccountId = account.systemAccountId ?? resolveAccountSystemAccountId(account.id) ?? 'sys_admin'
  const proxyUrl = resolveProxyUrlForProfileForSystemAccount(account.proxyProfileId, systemAccountId)
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
}, timeoutMs?: number): Promise<{ statusCode: number; headers: Record<string, string | string[]>; bodyText: string }> {
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
      agent: input.proxyUrl ? createProxyAgent(input.proxyUrl) as Agent : undefined,
      timeout: timeoutMs ?? 120000
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

function nextBackoffAt(): string {
  return new Date(Date.now() + settingsNumber('oauthUsageSnapshotRetryBackoffSeconds', 300, 60, 86400) * 1000).toISOString()
}

function settingsNumber(key: string, fallback: number, min: number, max: number): number {
  const value = getSettings()[key]
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? Math.min(Math.max(Math.trunc(number), min), max) : fallback
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.length > 0 ? value : undefined
}


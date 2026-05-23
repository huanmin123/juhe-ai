import { createAppCache } from '../../shared/cache.js'
import { getRequestLogger } from '../../shared/request-context.js'
import type { UpstreamAccount } from './openai-gateway-route-helpers.js'

export interface GatewayProxyHealthOrderResult {
  accounts: UpstreamAccount[]
  applied: boolean
  avoidedProxyKeys: string[]
  avoidedAccountIds: string[]
  bypassedAllAvoided: boolean
}

export interface GatewayProxyFailureDecision {
  recorded: boolean
  proxyKey?: string
  suspected: boolean
  distinctAccountCount?: number
}

interface GatewayProxyFailureEntry {
  key: string
  reason: string
  accountSamples: Array<[string, number]>
  failureCount: number
  firstFailedAtMs: number
  lastFailedAtMs: number
  avoidUntilMs?: number
}

const proxyFailureCache = createAppCache<string, GatewayProxyFailureEntry>({
  name: 'gateway:proxy-health',
  max: 2_000,
  ttlMs: 10 * 60_000,
  updateAgeOnGet: false
})

const proxyFailureWindowMs = 60_000
const proxyFailureAvoidTtlMs = 60_000
const proxyFailureDistinctAccountThreshold = 2

export function orderOpenAIAccountsByGatewayProxyHealth(accounts: UpstreamAccount[]): GatewayProxyHealthOrderResult {
  if (accounts.length === 0) {
    return {
      accounts,
      applied: false,
      avoidedProxyKeys: [],
      avoidedAccountIds: [],
      bypassedAllAvoided: false
    }
  }

  const now = Date.now()
  const freshAccounts: UpstreamAccount[] = []
  const avoidedAccounts: UpstreamAccount[] = []
  const avoidedProxyKeys = new Set<string>()
  for (const account of accounts) {
    const key = gatewayProxyKey(account)
    const entry = key ? proxyFailureCache.get(key) : undefined
    const avoided = Boolean(entry?.avoidUntilMs && entry.avoidUntilMs > now)
    if (avoided && key) {
      avoidedAccounts.push(account)
      avoidedProxyKeys.add(key)
    } else {
      freshAccounts.push(account)
    }
  }

  if (avoidedAccounts.length === 0) {
    return {
      accounts,
      applied: false,
      avoidedProxyKeys: [],
      avoidedAccountIds: [],
      bypassedAllAvoided: false
    }
  }

  if (freshAccounts.length === 0) {
    return {
      accounts,
      applied: false,
      avoidedProxyKeys: [...avoidedProxyKeys],
      avoidedAccountIds: avoidedAccounts.map((account) => account.id),
      bypassedAllAvoided: true
    }
  }

  return {
    accounts: [...freshAccounts, ...avoidedAccounts],
    applied: true,
    avoidedProxyKeys: [...avoidedProxyKeys],
    avoidedAccountIds: avoidedAccounts.map((account) => account.id),
    bypassedAllAvoided: false
  }
}

export function recordGatewayProxyFailure(account: UpstreamAccount, reason: string): GatewayProxyFailureDecision {
  const key = gatewayProxyKey(account)
  if (!key) {
    return { recorded: false, suspected: false }
  }
  const now = Date.now()
  const current = proxyFailureCache.get(key)
  const accountSamples = pruneAccountSamples([...(current?.accountSamples ?? []), [account.id, now]], now)
  const distinctAccountCount = new Set(accountSamples.map(([accountId]) => accountId)).size
  const suspected = distinctAccountCount >= proxyFailureDistinctAccountThreshold
  const entry: GatewayProxyFailureEntry = {
    key,
    reason,
    accountSamples,
    failureCount: (current?.failureCount ?? 0) + 1,
    firstFailedAtMs: current?.firstFailedAtMs ?? now,
    lastFailedAtMs: now,
    avoidUntilMs: suspected ? now + proxyFailureAvoidTtlMs : current?.avoidUntilMs
  }
  proxyFailureCache.set(key, entry, { ttlMs: proxyFailureAvoidTtlMs + proxyFailureWindowMs })
  if (suspected && (!current?.avoidUntilMs || current.avoidUntilMs <= now)) {
    getRequestLogger().warn({
      event: 'gateway_proxy_failure_bucket_opened',
      proxyKey: key,
      accountId: account.id,
      distinctAccountCount,
      reason,
      avoidUntil: new Date(entry.avoidUntilMs ?? now).toISOString()
    }, '同代理多个账号短窗失败，网关已进入代理运行态避让')
  }
  return {
    recorded: true,
    proxyKey: key,
    suspected,
    distinctAccountCount
  }
}

export function recordGatewayProxySuccess(account: UpstreamAccount): boolean {
  const key = gatewayProxyKey(account)
  if (!key) {
    return false
  }
  const existed = Boolean(proxyFailureCache.get(key))
  proxyFailureCache.delete(key)
  return existed
}

export function clearGatewayProxyHealthForTest(): void {
  proxyFailureCache.clear()
}

export function gatewayProxyKey(account: Pick<UpstreamAccount, 'proxyProfileId' | 'proxyUrl'>): string | undefined {
  if (account.proxyProfileId) {
    return `profile:${account.proxyProfileId}`
  }
  if (account.proxyUrl) {
    return `url:${account.proxyUrl}`
  }
  return undefined
}

export function isHighConfidenceProxyRequestError(error: unknown): boolean {
  const code = objectStringProperty(error, 'code').toUpperCase()
  if (!code) {
    return false
  }
  return new Set([
    'ECONNREFUSED',
    'ECONNRESET',
    'ETIMEDOUT',
    'EHOSTUNREACH',
    'ENETUNREACH',
    'EAI_AGAIN',
    'UND_ERR_CONNECT_TIMEOUT',
    'UND_ERR_HEADERS_TIMEOUT',
    'UND_ERR_SOCKET'
  ]).has(code)
}

function pruneAccountSamples(samples: Array<[string, number]>, now: number): Array<[string, number]> {
  return samples.filter(([, failedAtMs]) => now - failedAtMs <= proxyFailureWindowMs)
}

function objectStringProperty(value: unknown, key: string): string {
  if (typeof value !== 'object' || value === null) {
    return ''
  }
  const property = (value as Record<string, unknown>)[key]
  return typeof property === 'string' && property.trim() ? property.trim() : ''
}

import { runtimeConfig } from '../../../config/runtime.js'
import { createRuntimeStateStore } from '../../../shared/runtime-state-store.js'
import { getRequestLogger, sanitizeUrlCredentialsForLog } from '../../../shared/request-context.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import { preserveGatewayAccountDispatchPriorityTiers } from './account-dispatch-priority-order.js'
import type { GatewayAccountModelPriority } from '../dispatch/model-filter.js'

export interface GatewayProxyHealthOrderResult {
  accounts: UpstreamAccount[]
  applied: boolean
  avoidedBucketKeys: string[]
  avoidedProxyKeys: string[]
  avoidedAccountIds: string[]
  halfOpenBucketKeys: string[]
  halfOpenAccountIds: string[]
  bypassedAllAvoided: boolean
}

export interface GatewayProxyFailureDecision {
  recorded: boolean
  proxyKey?: string
  bucketKeys?: string[]
  suspectedBucketKeys?: string[]
  suspected: boolean
  distinctAccountCount?: number
}

interface GatewayUpstreamBucketFailureEntry {
  key: string
  reason: string
  accountSamples: Array<[string, number]>
  failureCount: number
  firstFailedAtMs: number
  lastFailedAtMs: number
  avoidUntilMs?: number
  halfOpenStartedAtMs?: number
  halfOpenUntilMs?: number
  halfOpenAccountId?: string
}

interface MemoryBucketEntry {
  value: GatewayUpstreamBucketFailureEntry
  expiresAt: number
}

const upstreamBucketFailureMemoryEntries = new Map<string, MemoryBucketEntry>()
const upstreamBucketFailureStateStore = createRuntimeStateStore('gateway-upstream-bucket-health')

const upstreamBucketFailureMaxEntries = 2_000
const upstreamBucketFailureWindowMs = 60_000
const upstreamBucketFailureAvoidTtlMs = 60_000
const upstreamBucketHalfOpenLeaseMs = 60_000
const upstreamBucketFailureDistinctAccountThreshold = 2
let gatewayUpstreamBucketHealthNowForTest: number | undefined

export function orderGatewayAccountsByUpstreamBucketHealth(
  accounts: UpstreamAccount[],
  modelPriority?: GatewayAccountModelPriority
): GatewayProxyHealthOrderResult {
  if (accounts.length === 0) {
    return {
      accounts,
      applied: false,
      avoidedBucketKeys: [],
      avoidedProxyKeys: [],
      avoidedAccountIds: [],
      halfOpenBucketKeys: [],
      halfOpenAccountIds: [],
      bypassedAllAvoided: false
    }
  }

  const now = gatewayUpstreamBucketHealthNow()
  const specificOrder = orderAccountsByActiveBucketScope(accounts, now, 'specific')
  if (specificOrder.avoidedAccounts.length > 0 && specificOrder.freshAccounts.length > 0) {
    return {
      accounts: preserveGatewayAccountDispatchPriorityTiers(accounts, orderedAccountsForBucketScope(specificOrder), {
        modelRankByAccountId: modelPriority?.rankByAccountId
      }),
      applied: true,
      avoidedBucketKeys: bucketKeysForLog([...specificOrder.avoidedBucketKeys]),
      avoidedProxyKeys: bucketKeysForLog([...specificOrder.avoidedProxyKeys]),
      avoidedAccountIds: specificOrder.avoidedAccounts.map((account) => account.id),
      halfOpenBucketKeys: bucketKeysForLog([...specificOrder.halfOpenBucketKeys]),
      halfOpenAccountIds: specificOrder.halfOpenAccounts.map((account) => account.id),
      bypassedAllAvoided: false
    }
  }

  const providerOrder = orderAccountsByActiveBucketScope(accounts, now, 'provider')
  if (providerOrder.avoidedAccounts.length === 0 && specificOrder.avoidedAccounts.length === 0) {
    return {
      accounts,
      applied: false,
      avoidedBucketKeys: [],
      avoidedProxyKeys: [],
      avoidedAccountIds: [],
      halfOpenBucketKeys: [],
      halfOpenAccountIds: [],
      bypassedAllAvoided: false
    }
  }

  if (providerOrder.avoidedAccounts.length > 0 && providerOrder.freshAccounts.length > 0) {
    return {
      accounts: preserveGatewayAccountDispatchPriorityTiers(accounts, orderedAccountsForBucketScope(providerOrder), {
        modelRankByAccountId: modelPriority?.rankByAccountId
      }),
      applied: true,
      avoidedBucketKeys: bucketKeysForLog([...providerOrder.avoidedBucketKeys]),
      avoidedProxyKeys: bucketKeysForLog([...providerOrder.avoidedProxyKeys]),
      avoidedAccountIds: providerOrder.avoidedAccounts.map((account) => account.id),
      halfOpenBucketKeys: bucketKeysForLog([...providerOrder.halfOpenBucketKeys]),
      halfOpenAccountIds: providerOrder.halfOpenAccounts.map((account) => account.id),
      bypassedAllAvoided: false
    }
  }

  return {
    accounts,
    applied: false,
    avoidedBucketKeys: bucketKeysForLog([...new Set([...specificOrder.avoidedBucketKeys, ...providerOrder.avoidedBucketKeys])]),
    avoidedProxyKeys: bucketKeysForLog([...new Set([...specificOrder.avoidedProxyKeys, ...providerOrder.avoidedProxyKeys])]),
    avoidedAccountIds: [...new Set([...specificOrder.avoidedAccounts, ...providerOrder.avoidedAccounts].map((account) => account.id))],
    halfOpenBucketKeys: bucketKeysForLog([...new Set([...specificOrder.halfOpenBucketKeys, ...providerOrder.halfOpenBucketKeys])]),
    halfOpenAccountIds: [...new Set([...specificOrder.halfOpenAccounts, ...providerOrder.halfOpenAccounts].map((account) => account.id))],
    bypassedAllAvoided: true
  }
}

export function orderOpenAIAccountsByGatewayProxyHealth(
  accounts: UpstreamAccount[],
  modelPriority?: GatewayAccountModelPriority
): GatewayProxyHealthOrderResult {
  return orderGatewayAccountsByUpstreamBucketHealth(accounts, modelPriority)
}

export async function orderGatewayAccountsByUpstreamBucketHealthAsync(
  accounts: UpstreamAccount[],
  modelPriority?: GatewayAccountModelPriority
): Promise<GatewayProxyHealthOrderResult> {
  if (!shouldUseRedisUpstreamBucketHealthState()) {
    return orderGatewayAccountsByUpstreamBucketHealth(accounts, modelPriority)
  }
  if (accounts.length === 0) {
    return {
      accounts,
      applied: false,
      avoidedBucketKeys: [],
      avoidedProxyKeys: [],
      avoidedAccountIds: [],
      halfOpenBucketKeys: [],
      halfOpenAccountIds: [],
      bypassedAllAvoided: false
    }
  }

  const entries = await loadRedisBucketEntriesForAccounts(accounts)
  const pendingWrites: Promise<void>[] = []
  const persistEntry = (entry: GatewayUpstreamBucketFailureEntry, ttlMs: number) => {
    entries.set(entry.key, entry)
    pendingWrites.push(setRedisBucketFailureEntry(entry.key, entry, ttlMs))
  }
  const result = orderGatewayAccountsByUpstreamBucketHealthWithEntries(accounts, entries, persistEntry, modelPriority)
  await Promise.all(pendingWrites)
  return result
}

export async function orderOpenAIAccountsByGatewayProxyHealthAsync(
  accounts: UpstreamAccount[],
  modelPriority?: GatewayAccountModelPriority
): Promise<GatewayProxyHealthOrderResult> {
  return orderGatewayAccountsByUpstreamBucketHealthAsync(accounts, modelPriority)
}

function orderGatewayAccountsByUpstreamBucketHealthWithEntries(
  accounts: UpstreamAccount[],
  entries: Map<string, GatewayUpstreamBucketFailureEntry>,
  persistEntry: (entry: GatewayUpstreamBucketFailureEntry, ttlMs: number) => void | Promise<void>,
  modelPriority?: GatewayAccountModelPriority
): GatewayProxyHealthOrderResult {
  if (accounts.length === 0) {
    return {
      accounts,
      applied: false,
      avoidedBucketKeys: [],
      avoidedProxyKeys: [],
      avoidedAccountIds: [],
      halfOpenBucketKeys: [],
      halfOpenAccountIds: [],
      bypassedAllAvoided: false
    }
  }

  const now = gatewayUpstreamBucketHealthNow()
  const specificOrder = orderAccountsByActiveBucketScopeWithEntries(accounts, now, 'specific', entries, persistEntry)
  if (specificOrder.avoidedAccounts.length > 0 && specificOrder.freshAccounts.length > 0) {
    return {
      accounts: preserveGatewayAccountDispatchPriorityTiers(accounts, orderedAccountsForBucketScope(specificOrder), {
        modelRankByAccountId: modelPriority?.rankByAccountId
      }),
      applied: true,
      avoidedBucketKeys: bucketKeysForLog([...specificOrder.avoidedBucketKeys]),
      avoidedProxyKeys: bucketKeysForLog([...specificOrder.avoidedProxyKeys]),
      avoidedAccountIds: specificOrder.avoidedAccounts.map((account) => account.id),
      halfOpenBucketKeys: bucketKeysForLog([...specificOrder.halfOpenBucketKeys]),
      halfOpenAccountIds: specificOrder.halfOpenAccounts.map((account) => account.id),
      bypassedAllAvoided: false
    }
  }

  const providerOrder = orderAccountsByActiveBucketScopeWithEntries(accounts, now, 'provider', entries, persistEntry)
  if (providerOrder.avoidedAccounts.length === 0 && specificOrder.avoidedAccounts.length === 0) {
    return {
      accounts,
      applied: false,
      avoidedBucketKeys: [],
      avoidedProxyKeys: [],
      avoidedAccountIds: [],
      halfOpenBucketKeys: [],
      halfOpenAccountIds: [],
      bypassedAllAvoided: false
    }
  }

  if (providerOrder.avoidedAccounts.length > 0 && providerOrder.freshAccounts.length > 0) {
    return {
      accounts: preserveGatewayAccountDispatchPriorityTiers(accounts, orderedAccountsForBucketScope(providerOrder), {
        modelRankByAccountId: modelPriority?.rankByAccountId
      }),
      applied: true,
      avoidedBucketKeys: bucketKeysForLog([...providerOrder.avoidedBucketKeys]),
      avoidedProxyKeys: bucketKeysForLog([...providerOrder.avoidedProxyKeys]),
      avoidedAccountIds: providerOrder.avoidedAccounts.map((account) => account.id),
      halfOpenBucketKeys: bucketKeysForLog([...providerOrder.halfOpenBucketKeys]),
      halfOpenAccountIds: providerOrder.halfOpenAccounts.map((account) => account.id),
      bypassedAllAvoided: false
    }
  }

  return {
    accounts,
    applied: false,
    avoidedBucketKeys: bucketKeysForLog([...new Set([...specificOrder.avoidedBucketKeys, ...providerOrder.avoidedBucketKeys])]),
    avoidedProxyKeys: bucketKeysForLog([...new Set([...specificOrder.avoidedProxyKeys, ...providerOrder.avoidedProxyKeys])]),
    avoidedAccountIds: [...new Set([...specificOrder.avoidedAccounts, ...providerOrder.avoidedAccounts].map((account) => account.id))],
    halfOpenBucketKeys: bucketKeysForLog([...new Set([...specificOrder.halfOpenBucketKeys, ...providerOrder.halfOpenBucketKeys])]),
    halfOpenAccountIds: [...new Set([...specificOrder.halfOpenAccounts, ...providerOrder.halfOpenAccounts].map((account) => account.id))],
    bypassedAllAvoided: true
  }
}

function orderAccountsByActiveBucketScope(
  accounts: UpstreamAccount[],
  now: number,
  scope: 'specific' | 'provider'
): {
  freshAccounts: UpstreamAccount[]
  halfOpenAccounts: UpstreamAccount[]
  avoidedAccounts: UpstreamAccount[]
  avoidedBucketKeys: Set<string>
  avoidedProxyKeys: Set<string>
  halfOpenBucketKeys: Set<string>
} {
  const entries = new Map<string, GatewayUpstreamBucketFailureEntry>()
  for (const account of accounts) {
    for (const key of gatewayUpstreamBucketKeys(account)) {
      const entry = getMemoryBucketFailureEntry(key)
      if (entry) {
        entries.set(key, entry)
      }
    }
  }
  const persistEntry = (entry: GatewayUpstreamBucketFailureEntry, ttlMs: number) => {
    entries.set(entry.key, entry)
    setMemoryBucketFailureEntry(entry.key, entry, ttlMs)
  }
  return orderAccountsByActiveBucketScopeWithEntries(accounts, now, scope, entries, persistEntry)
}

function orderAccountsByActiveBucketScopeWithEntries(
  accounts: UpstreamAccount[],
  now: number,
  scope: 'specific' | 'provider',
  entries: Map<string, GatewayUpstreamBucketFailureEntry>,
  persistEntry: (entry: GatewayUpstreamBucketFailureEntry, ttlMs: number) => void | Promise<void>
): {
  freshAccounts: UpstreamAccount[]
  halfOpenAccounts: UpstreamAccount[]
  avoidedAccounts: UpstreamAccount[]
  avoidedBucketKeys: Set<string>
  avoidedProxyKeys: Set<string>
  halfOpenBucketKeys: Set<string>
} {
  const freshAccounts: UpstreamAccount[] = []
  const halfOpenAccounts: UpstreamAccount[] = []
  const avoidedAccounts: UpstreamAccount[] = []
  const avoidedBucketKeys = new Set<string>()
  const avoidedProxyKeys = new Set<string>()
  const halfOpenBucketKeys = new Set<string>()
  const candidateAccountIds = new Set(accounts.map((account) => account.id))
  for (const account of accounts) {
    const blockedKeys: string[] = []
    const probeKeys: string[] = []
    for (const key of gatewayUpstreamBucketKeys(account)
      .filter((key) => scope === 'provider' ? isProviderBucketKey(key) : !isProviderBucketKey(key))) {
      const state = upstreamBucketAccountStateWithEntries(key, account, now, candidateAccountIds, entries, persistEntry)
      if (state === 'blocked') {
        blockedKeys.push(key)
      } else if (state === 'half_open_probe') {
        probeKeys.push(key)
      }
    }
    if (blockedKeys.length > 0) {
      avoidedAccounts.push(account)
      for (const key of blockedKeys) {
        avoidedBucketKeys.add(key)
        if (isProxyBucketKey(key)) {
          avoidedProxyKeys.add(key)
        }
      }
    } else if (probeKeys.length > 0) {
      halfOpenAccounts.push(account)
      for (const key of probeKeys) {
        halfOpenBucketKeys.add(key)
      }
    } else {
      freshAccounts.push(account)
    }
  }
  return { freshAccounts: [...halfOpenAccounts, ...freshAccounts], halfOpenAccounts, avoidedAccounts, avoidedBucketKeys, avoidedProxyKeys, halfOpenBucketKeys }
}

function orderedAccountsForBucketScope(order: {
  freshAccounts: UpstreamAccount[]
  avoidedAccounts: UpstreamAccount[]
  halfOpenBucketKeys: Set<string>
}): UpstreamAccount[] {
  if (order.halfOpenBucketKeys.size > 0) {
    return order.freshAccounts
  }
  return [...order.freshAccounts, ...order.avoidedAccounts]
}

function upstreamBucketAccountState(
  key: string,
  account: UpstreamAccount,
  now: number,
  candidateAccountIds: Set<string>
): 'normal' | 'blocked' | 'half_open_probe' {
  const entry = getMemoryBucketFailureEntry(key)
  const persistEntry = (nextEntry: GatewayUpstreamBucketFailureEntry, ttlMs: number) => {
    setMemoryBucketFailureEntry(nextEntry.key, nextEntry, ttlMs)
  }
  return upstreamBucketAccountStateWithEntries(key, account, now, candidateAccountIds, new Map(entry ? [[key, entry]] : []), persistEntry)
}

function upstreamBucketAccountStateWithEntries(
  key: string,
  account: UpstreamAccount,
  now: number,
  candidateAccountIds: Set<string>,
  entries: Map<string, GatewayUpstreamBucketFailureEntry>,
  persistEntry: (entry: GatewayUpstreamBucketFailureEntry, ttlMs: number) => void
): 'normal' | 'blocked' | 'half_open_probe' {
  const entry = entries.get(key)
  if (!entry?.avoidUntilMs) {
    return 'normal'
  }
  if (entry.avoidUntilMs > now) {
    return 'blocked'
  }
  const halfOpenEntry = ensureHalfOpenProbe(entry, account, now, candidateAccountIds, persistEntry)
  return halfOpenEntry.halfOpenAccountId === account.id ? 'half_open_probe' : 'blocked'
}

function ensureHalfOpenProbe(
  entry: GatewayUpstreamBucketFailureEntry,
  account: UpstreamAccount,
  now: number,
  candidateAccountIds: Set<string>,
  persistEntry?: (entry: GatewayUpstreamBucketFailureEntry, ttlMs: number) => void
): GatewayUpstreamBucketFailureEntry {
  if (
    entry.halfOpenAccountId
    && entry.halfOpenUntilMs
    && entry.halfOpenUntilMs > now
    && candidateAccountIds.has(entry.halfOpenAccountId)
  ) {
    return entry
  }

  const halfOpenUntilMs = now + upstreamBucketHalfOpenLeaseMs
  const nextEntry: GatewayUpstreamBucketFailureEntry = {
    ...entry,
    halfOpenStartedAtMs: now,
    halfOpenUntilMs,
    halfOpenAccountId: account.id
  }
  if (persistEntry) {
    persistEntry(nextEntry, upstreamBucketFailureAvoidTtlMs + upstreamBucketFailureWindowMs)
  } else {
    setMemoryBucketFailureEntry(entry.key, nextEntry, upstreamBucketFailureAvoidTtlMs + upstreamBucketFailureWindowMs)
  }
  getRequestLogger().warn({
    event: 'gateway_upstream_failure_bucket_half_opened',
    bucketKey: bucketKeyForLog(entry.key),
    bucketType: upstreamBucketType(entry.key),
    accountId: account.id,
    halfOpenUntil: new Date(halfOpenUntilMs).toISOString()
  }, '上游桶运行态避让 TTL 到期，已放行一个半开探测账号')
  return nextEntry
}

export function recordGatewayUpstreamBucketFailure(
  account: UpstreamAccount,
  reason: string,
  options: { bucketScope?: 'all' | 'proxy' | 'upstream' } = {}
): GatewayProxyFailureDecision {
  const bucketKeys = gatewayUpstreamBucketKeys(account, options.bucketScope)
  if (bucketKeys.length === 0) {
    return { recorded: false, suspected: false }
  }
  const decisions = bucketKeys.map((key) => recordGatewayUpstreamBucketFailureKey(account, key, reason))
  const suspectedDecisions = decisions.filter((decision) => decision.suspected)
  const proxyKey = bucketKeys.find(isProxyBucketKey)
  return {
    recorded: true,
    proxyKey: bucketKeyForLog(proxyKey),
    bucketKeys: bucketKeysForLog(bucketKeys),
    suspectedBucketKeys: bucketKeysForLog(suspectedDecisions.map((decision) => decision.bucketKey)),
    suspected: suspectedDecisions.length > 0,
    distinctAccountCount: Math.max(0, ...decisions.map((decision) => decision.distinctAccountCount))
  }
}

export async function recordGatewayUpstreamBucketFailureAsync(
  account: UpstreamAccount,
  reason: string,
  options: { bucketScope?: 'all' | 'proxy' | 'upstream' } = {}
): Promise<GatewayProxyFailureDecision> {
  if (!shouldUseRedisUpstreamBucketHealthState()) {
    return recordGatewayUpstreamBucketFailure(account, reason, options)
  }
  const bucketKeys = gatewayUpstreamBucketKeys(account, options.bucketScope)
  if (bucketKeys.length === 0) {
    return { recorded: false, suspected: false }
  }
  const decisions = await Promise.all(bucketKeys.map((key) => recordGatewayUpstreamBucketFailureKeyAsync(account, key, reason)))
  const suspectedDecisions = decisions.filter((decision) => decision.suspected)
  const proxyKey = bucketKeys.find(isProxyBucketKey)
  return {
    recorded: true,
    proxyKey: bucketKeyForLog(proxyKey),
    bucketKeys: bucketKeysForLog(bucketKeys),
    suspectedBucketKeys: bucketKeysForLog(suspectedDecisions.map((decision) => decision.bucketKey)),
    suspected: suspectedDecisions.length > 0,
    distinctAccountCount: Math.max(0, ...decisions.map((decision) => decision.distinctAccountCount))
  }
}

export function suppressGatewayUpstreamBucketLocallyForSeconds(
  account: UpstreamAccount,
  ttlSeconds: number,
  reason: string,
  options: { bucketScope?: 'all' | 'proxy' | 'upstream' } = {}
): GatewayProxyFailureDecision {
  const bucketKeys = gatewayUpstreamBucketKeys(account, options.bucketScope)
  if (bucketKeys.length === 0) {
    return { recorded: false, suspected: false }
  }
  const now = gatewayUpstreamBucketHealthNow()
  const ttlMs = Math.max(1, Math.trunc(ttlSeconds)) * 1000
  const avoidUntilMs = now + ttlMs
  for (const key of bucketKeys) {
    const current = getMemoryBucketFailureEntry(key)
    const accountSamples = pruneAccountSamples([...(current?.accountSamples ?? []), [account.id, now]], now)
    setMemoryBucketFailureEntry(key, {
      key,
      reason,
      accountSamples,
      failureCount: (current?.failureCount ?? 0) + 1,
      firstFailedAtMs: current?.firstFailedAtMs ?? now,
      lastFailedAtMs: now,
      avoidUntilMs
    }, ttlMs + upstreamBucketFailureWindowMs)
  }
  const proxyKey = bucketKeys.find(isProxyBucketKey)
  const safeBucketKeys = bucketKeysForLog(bucketKeys)
  const safeProxyKey = bucketKeyForLog(proxyKey)
  getRequestLogger().warn({
    event: 'gateway_upstream_bucket_locally_suppressed',
    bucketKeys: safeBucketKeys,
    proxyKey: safeProxyKey,
    accountId: account.id,
    ttlSeconds: Math.max(1, Math.trunc(ttlSeconds)),
    avoidUntil: new Date(avoidUntilMs).toISOString(),
    reason
  }, '网关按策略短期避让上游桶')
  return {
    recorded: true,
    proxyKey: safeProxyKey,
    bucketKeys: safeBucketKeys,
    suspectedBucketKeys: safeBucketKeys,
    suspected: true,
    distinctAccountCount: 1
  }
}

export async function suppressGatewayUpstreamBucketForSecondsAsync(
  account: UpstreamAccount,
  ttlSeconds: number,
  reason: string,
  options: { bucketScope?: 'all' | 'proxy' | 'upstream' } = {}
): Promise<GatewayProxyFailureDecision> {
  if (!shouldUseRedisUpstreamBucketHealthState()) {
    return suppressGatewayUpstreamBucketLocallyForSeconds(account, ttlSeconds, reason, options)
  }
  const bucketKeys = gatewayUpstreamBucketKeys(account, options.bucketScope)
  if (bucketKeys.length === 0) {
    return { recorded: false, suspected: false }
  }
  const now = gatewayUpstreamBucketHealthNow()
  const ttlMs = Math.max(1, Math.trunc(ttlSeconds)) * 1000
  const avoidUntilMs = now + ttlMs
  await Promise.all(bucketKeys.map(async (key) => {
    const current = await getRedisBucketFailureEntry(key)
    const accountSamples = pruneAccountSamples([...(current?.accountSamples ?? []), [account.id, now]], now)
    await setRedisBucketFailureEntry(key, {
      key,
      reason,
      accountSamples,
      failureCount: (current?.failureCount ?? 0) + 1,
      firstFailedAtMs: current?.firstFailedAtMs ?? now,
      lastFailedAtMs: now,
      avoidUntilMs
    }, ttlMs + upstreamBucketFailureWindowMs)
  }))
  const proxyKey = bucketKeys.find(isProxyBucketKey)
  const safeBucketKeys = bucketKeysForLog(bucketKeys)
  const safeProxyKey = bucketKeyForLog(proxyKey)
  getRequestLogger().warn({
    event: 'gateway_upstream_bucket_locally_suppressed',
    bucketKeys: safeBucketKeys,
    proxyKey: safeProxyKey,
    accountId: account.id,
    ttlSeconds: Math.max(1, Math.trunc(ttlSeconds)),
    avoidUntil: new Date(avoidUntilMs).toISOString(),
    reason
  }, '网关按策略短期避让上游桶')
  return {
    recorded: true,
    proxyKey: safeProxyKey,
    bucketKeys: safeBucketKeys,
    suspectedBucketKeys: safeBucketKeys,
    suspected: true,
    distinctAccountCount: 1
  }
}

export function recordGatewayProxyFailure(
  account: UpstreamAccount,
  reason: string,
  options: { bucketScope?: 'all' | 'proxy' | 'upstream' } = {}
): GatewayProxyFailureDecision {
  return recordGatewayUpstreamBucketFailure(account, reason, { bucketScope: options.bucketScope ?? 'proxy' })
}

export async function recordGatewayProxyFailureAsync(
  account: UpstreamAccount,
  reason: string,
  options: { bucketScope?: 'all' | 'proxy' | 'upstream' } = {}
): Promise<GatewayProxyFailureDecision> {
  return recordGatewayUpstreamBucketFailureAsync(account, reason, { bucketScope: options.bucketScope ?? 'proxy' })
}

function recordGatewayUpstreamBucketFailureKey(
  account: UpstreamAccount,
  key: string,
  reason: string
): {
  bucketKey: string
  suspected: boolean
  distinctAccountCount: number
} {
  const now = gatewayUpstreamBucketHealthNow()
  const current = getMemoryBucketFailureEntry(key)
  const accountSamples = pruneAccountSamples([...(current?.accountSamples ?? []), [account.id, now]], now)
  const distinctAccountCount = new Set(accountSamples.map(([accountId]) => accountId)).size
  const halfOpenProbeFailed = isHalfOpenProbeForAccount(current, account.id, now)
  const suspected = halfOpenProbeFailed || distinctAccountCount >= upstreamBucketFailureDistinctAccountThreshold
  const entry: GatewayUpstreamBucketFailureEntry = {
    key,
    reason,
    accountSamples,
    failureCount: (current?.failureCount ?? 0) + 1,
    firstFailedAtMs: current?.firstFailedAtMs ?? now,
    lastFailedAtMs: now,
    avoidUntilMs: suspected ? now + upstreamBucketFailureAvoidTtlMs : current?.avoidUntilMs
  }
  setMemoryBucketFailureEntry(key, entry, upstreamBucketFailureAvoidTtlMs + upstreamBucketFailureWindowMs)
  if (suspected && (!current?.avoidUntilMs || current.avoidUntilMs <= now)) {
    getRequestLogger().warn({
      event: 'gateway_upstream_failure_bucket_opened',
      bucketKey: bucketKeyForLog(key),
      bucketType: upstreamBucketType(key),
      accountId: account.id,
      distinctAccountCount,
      reason,
      halfOpenProbeFailed,
      avoidUntil: new Date(entry.avoidUntilMs ?? now).toISOString()
    }, '同上游桶多个账号短窗失败，网关已进入上游桶运行态避让')
  }
  return {
    suspected,
    bucketKey: key,
    distinctAccountCount
  }
}

async function recordGatewayUpstreamBucketFailureKeyAsync(
  account: UpstreamAccount,
  key: string,
  reason: string
): Promise<{
  bucketKey: string
  suspected: boolean
  distinctAccountCount: number
}> {
  const now = gatewayUpstreamBucketHealthNow()
  const current = await getRedisBucketFailureEntry(key)
  const accountSamples = pruneAccountSamples([...(current?.accountSamples ?? []), [account.id, now]], now)
  const distinctAccountCount = new Set(accountSamples.map(([accountId]) => accountId)).size
  const halfOpenProbeFailed = isHalfOpenProbeForAccount(current, account.id, now)
  const suspected = halfOpenProbeFailed || distinctAccountCount >= upstreamBucketFailureDistinctAccountThreshold
  const entry: GatewayUpstreamBucketFailureEntry = {
    key,
    reason,
    accountSamples,
    failureCount: (current?.failureCount ?? 0) + 1,
    firstFailedAtMs: current?.firstFailedAtMs ?? now,
    lastFailedAtMs: now,
    avoidUntilMs: suspected ? now + upstreamBucketFailureAvoidTtlMs : current?.avoidUntilMs
  }
  await setRedisBucketFailureEntry(key, entry, upstreamBucketFailureAvoidTtlMs + upstreamBucketFailureWindowMs)
  if (suspected && (!current?.avoidUntilMs || current.avoidUntilMs <= now)) {
    getRequestLogger().warn({
      event: 'gateway_upstream_failure_bucket_opened',
      bucketKey: bucketKeyForLog(key),
      bucketType: upstreamBucketType(key),
      accountId: account.id,
      distinctAccountCount,
      reason,
      halfOpenProbeFailed,
      avoidUntil: new Date(entry.avoidUntilMs ?? now).toISOString()
    }, '同上游桶多个账号短窗失败，网关已进入上游桶运行态避让')
  }
  return {
    suspected,
    bucketKey: key,
    distinctAccountCount
  }
}

function isHalfOpenProbeForAccount(
  entry: GatewayUpstreamBucketFailureEntry | undefined,
  accountId: string,
  now: number
): boolean {
  return Boolean(
    entry?.halfOpenAccountId === accountId
    && entry.halfOpenUntilMs
    && entry.halfOpenUntilMs > now
    && entry.avoidUntilMs
    && entry.avoidUntilMs <= now
  )
}

export function recordGatewayUpstreamBucketSuccess(account: UpstreamAccount): boolean {
  let existed = false
  for (const key of gatewayUpstreamBucketKeys(account)) {
    existed = Boolean(getMemoryBucketFailureEntry(key)) || existed
    upstreamBucketFailureMemoryEntries.delete(key)
  }
  return existed
}

export async function recordGatewayUpstreamBucketSuccessAsync(account: UpstreamAccount): Promise<boolean> {
  if (!shouldUseRedisUpstreamBucketHealthState()) {
    return recordGatewayUpstreamBucketSuccess(account)
  }
  let existed = false
  for (const key of gatewayUpstreamBucketKeys(account)) {
    existed = Boolean(await getRedisBucketFailureEntry(key)) || existed
    await upstreamBucketFailureStateStore.delete(redisBucketStateKey(key))
  }
  return existed
}

export function recordGatewayProxySuccess(account: UpstreamAccount): boolean {
  return recordGatewayUpstreamBucketSuccess(account)
}

export async function recordGatewayProxySuccessAsync(account: UpstreamAccount): Promise<boolean> {
  return recordGatewayUpstreamBucketSuccessAsync(account)
}

export function clearGatewayProxyHealthForTest(): void {
  upstreamBucketFailureMemoryEntries.clear()
  gatewayUpstreamBucketHealthNowForTest = undefined
}

export function setGatewayProxyHealthNowForTest(nowMs: number | undefined): void {
  gatewayUpstreamBucketHealthNowForTest = typeof nowMs === 'number' && Number.isFinite(nowMs)
    ? Math.max(0, Math.trunc(nowMs))
    : undefined
}

function getMemoryBucketFailureEntry(key: string): GatewayUpstreamBucketFailureEntry | undefined {
  const entry = upstreamBucketFailureMemoryEntries.get(key)
  if (!entry) {
    return undefined
  }
  if (entry.expiresAt <= gatewayUpstreamBucketHealthNow()) {
    upstreamBucketFailureMemoryEntries.delete(key)
    return undefined
  }
  return entry.value
}

function setMemoryBucketFailureEntry(key: string, value: GatewayUpstreamBucketFailureEntry, ttlMs: number): void {
  upstreamBucketFailureMemoryEntries.set(key, {
    value,
    expiresAt: gatewayUpstreamBucketHealthNow() + Math.max(1, Math.trunc(ttlMs))
  })
  evictOldestBucketFailureEntries()
}

function evictOldestBucketFailureEntries(): void {
  while (upstreamBucketFailureMemoryEntries.size > upstreamBucketFailureMaxEntries) {
    const oldestKey = upstreamBucketFailureMemoryEntries.keys().next().value
    if (typeof oldestKey !== 'string') {
      return
    }
    upstreamBucketFailureMemoryEntries.delete(oldestKey)
  }
}

function shouldUseRedisUpstreamBucketHealthState(): boolean {
  return runtimeConfig.runtimeStateDriver === 'redis'
}

async function loadRedisBucketEntriesForAccounts(accounts: UpstreamAccount[]): Promise<Map<string, GatewayUpstreamBucketFailureEntry>> {
  const keys = [...new Set(accounts.flatMap((account) => gatewayUpstreamBucketKeys(account)))]
  const entries = new Map<string, GatewayUpstreamBucketFailureEntry>()
  await Promise.all(keys.map(async (key) => {
    const entry = await getRedisBucketFailureEntry(key)
    if (entry) {
      entries.set(key, entry)
    }
  }))
  return entries
}

function redisBucketStateKey(key: string): string {
  return `bucket:${key}`
}

async function getRedisBucketFailureEntry(key: string): Promise<GatewayUpstreamBucketFailureEntry | undefined> {
  return upstreamBucketFailureStateStore.getJson<GatewayUpstreamBucketFailureEntry>(redisBucketStateKey(key))
}

async function setRedisBucketFailureEntry(
  key: string,
  entry: GatewayUpstreamBucketFailureEntry,
  ttlMs: number
): Promise<void> {
  await upstreamBucketFailureStateStore.setJson(redisBucketStateKey(key), entry, ttlMs)
}

export function gatewayProxyKey(account: Pick<UpstreamAccount, 'proxyProfileId' | 'proxyUrl'>): string | undefined {
  if (account.proxyProfileId) {
    return `proxy:profile:${account.proxyProfileId}`
  }
  if (account.proxyUrl) {
    return `proxy:url:${account.proxyUrl}`
  }
  return undefined
}

export function gatewayUpstreamBucketKeys(
  account: Pick<UpstreamAccount, 'providerCode' | 'baseUrl' | 'type' | 'proxyProfileId' | 'proxyUrl'>,
  scope: 'all' | 'proxy' | 'upstream' = 'all'
): string[] {
  const keys: string[] = []
  const proxyKey = gatewayProxyKey(account)
  if (proxyKey && scope !== 'upstream') {
    keys.push(proxyKey)
  }
  if (scope !== 'proxy') {
    const baseUrlKey = gatewayBaseUrlKey(account)
    if (baseUrlKey) {
      keys.push(baseUrlKey)
    }
    keys.push(gatewayProviderKey(account))
  }
  return [...new Set(keys)]
}

function gatewayBaseUrlKey(account: Pick<UpstreamAccount, 'baseUrl' | 'type'>): string | undefined {
  if (account.type === 'oauth') {
    return 'baseUrl:https://chatgpt.com/backend-api/codex'
  }
  const normalized = normalizeOpenAIBaseUrlForBucket(account.baseUrl)
  return normalized ? `baseUrl:${normalized}` : undefined
}

function gatewayProviderKey(account: Pick<UpstreamAccount, 'providerCode'>): string {
  return `provider:${account.providerCode}`
}

function normalizeOpenAIBaseUrlForBucket(value: unknown): string | undefined {
  if (typeof value !== 'string' || !value.trim()) {
    return undefined
  }
  const trimmed = value.trim().replace(/\/+$/, '')
  const normalizedBase = trimmed.endsWith('/v1') ? trimmed : `${trimmed}/v1`
  try {
    const url = new URL(normalizedBase)
    url.username = ''
    url.password = ''
    url.search = ''
    url.hash = ''
    const pathname = url.pathname.replace(/\/+$/, '') || '/v1'
    return `${url.protocol.toLowerCase()}//${url.host.toLowerCase()}${pathname}`
  } catch {
    return normalizedBase.toLowerCase()
  }
}

function isProxyBucketKey(key: string): boolean {
  return key.startsWith('proxy:')
}

function isProviderBucketKey(key: string): boolean {
  return key.startsWith('provider:')
}

function upstreamBucketType(key: string): string {
  const separatorIndex = key.indexOf(':')
  return separatorIndex > 0 ? key.slice(0, separatorIndex) : 'unknown'
}

function bucketKeysForLog(keys: string[]): string[] {
  return keys.map((key) => bucketKeyForLog(key) ?? key)
}

function bucketKeyForLog(key: string | undefined): string | undefined {
  if (!key?.startsWith('proxy:url:')) {
    return key
  }
  const proxyUrl = key.slice('proxy:url:'.length)
  return `proxy:url:${sanitizeUrlCredentialsForLog(proxyUrl) ?? '[configured]'}`
}

function pruneAccountSamples(samples: Array<[string, number]>, now: number): Array<[string, number]> {
  return samples.filter(([, failedAtMs]) => now - failedAtMs <= upstreamBucketFailureWindowMs)
}

function gatewayUpstreamBucketHealthNow(): number {
  return gatewayUpstreamBucketHealthNowForTest ?? Date.now()
}

function stringProperty(value: unknown, key: string): string {
  if (typeof value !== 'object' || value === null) {
    return ''
  }
  const property = (value as Record<string, unknown>)[key]
  return typeof property === 'string' && property.trim() ? property.trim() : ''
}

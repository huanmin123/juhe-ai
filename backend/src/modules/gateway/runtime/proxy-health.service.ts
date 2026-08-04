import { createHash, randomBytes } from 'node:crypto'

import { runtimeConfig } from '../../../config/runtime.js'
import { isOpenAIProtocolProfile } from '../../../domain/provider-protocol.js'
import { createRuntimeStateStore, type RuntimeStateStore } from '../../../shared/runtime-state-store.js'
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

type GatewayUpstreamBucketScope = 'all' | 'proxy' | 'upstream'

interface GatewayUpstreamBucketFailureEntry {
  key: string
  reason: string
  accountSamples: Array<[string, number]>
  failureCount: number
  firstFailedAtMs: number
  lastFailedAtMs: number
  lastFailureGeneration?: GatewayUpstreamBucketMutationGeneration
  avoidUntilMs?: number
  halfOpenStartedAtMs?: number
  halfOpenUntilMs?: number
  halfOpenAccountId?: string
}

interface GatewayUpstreamBucketMutationGeneration {
  instanceId: string
  sequence: number
}

interface GatewayUpstreamBucketMutationObservation {
  observedAtMs: number
  generation: GatewayUpstreamBucketMutationGeneration
}

interface MemoryBucketEntry {
  value: GatewayUpstreamBucketFailureEntry
  expiresAt: number
}

const upstreamBucketFailureMemoryEntries = new Map<string, MemoryBucketEntry>()
const upstreamBucketFailureStateStore = createRuntimeStateStore('gateway-upstream-bucket-health')
export type GatewayProxyHealthRuntimeStateStore = Pick<
  RuntimeStateStore,
  'getJson' | 'compareSetJson' | 'compareDeleteJson'
>
let upstreamBucketFailureStateStoreForTest: GatewayProxyHealthRuntimeStateStore | undefined

const upstreamBucketFailureMaxEntries = runtimeConfig.gateway.proxyHealthFailureMaxEntries
const upstreamBucketFailureWindowMs = runtimeConfig.gateway.proxyHealthFailureWindowMs
const upstreamBucketFailureAvoidTtlMs = runtimeConfig.gateway.proxyHealthAvoidTtlMs
const upstreamBucketHalfOpenLeaseMs = runtimeConfig.gateway.proxyHealthHalfOpenLeaseMs
const upstreamBucketFailureDistinctAccountThreshold = runtimeConfig.gateway.proxyHealthDistinctAccountThreshold
const upstreamBucketFailureCasMaxAttempts = runtimeConfig.gateway.proxyHealthCasMaxAttempts
const upstreamBucketFailureMaxAccountSamples = runtimeConfig.gateway.proxyHealthMaxAccountSamples
const upstreamBucketMutationInstanceId = randomBytes(12).toString('hex')
let upstreamBucketMutationSequence = 0
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

  const stateStore = gatewayProxyHealthRuntimeStateStore()
  const now = gatewayUpstreamBucketHealthNow()
  const entries = await loadRedisBucketEntriesForAccounts(accounts, stateStore)
  const persistEntry = (entry: GatewayUpstreamBucketFailureEntry, ttlMs: number) => {
    entries.set(entry.key, entry)
    void ttlMs
  }
  await claimRedisHalfOpenLeasesForAccounts(accounts, entries, now, 'specific', stateStore)
  const specificOrder = orderAccountsByActiveBucketScopeWithEntries(
    accounts,
    now,
    'specific',
    entries,
    persistEntry
  )
  if (!(specificOrder.avoidedAccounts.length > 0 && specificOrder.freshAccounts.length > 0)) {
    await claimRedisHalfOpenLeasesForAccounts(accounts, entries, now, 'provider', stateStore)
  }
  return orderGatewayAccountsByUpstreamBucketHealthWithEntries(accounts, entries, persistEntry, modelPriority, now)
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
  modelPriority?: GatewayAccountModelPriority,
  now = gatewayUpstreamBucketHealthNow()
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
  for (const account of accounts) {
    const blockedKeys: string[] = []
    const probeKeys: string[] = []
    for (const key of gatewayUpstreamBucketKeys(account)
      .filter((key) => scope === 'provider' ? isProviderBucketKey(key) : !isProviderBucketKey(key))) {
      const state = upstreamBucketAccountStateWithEntries(key, account, now, entries, persistEntry)
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
    return [...order.freshAccounts, ...order.avoidedAccounts]
  }
  return [...order.freshAccounts, ...order.avoidedAccounts]
}

function upstreamBucketAccountStateWithEntries(
  key: string,
  account: UpstreamAccount,
  now: number,
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
  const halfOpenEntry = ensureHalfOpenProbe(entry, account, now, persistEntry)
  return halfOpenEntry.halfOpenAccountId === account.id ? 'half_open_probe' : 'blocked'
}

function ensureHalfOpenProbe(
  entry: GatewayUpstreamBucketFailureEntry,
  account: UpstreamAccount,
  now: number,
  persistEntry?: (entry: GatewayUpstreamBucketFailureEntry, ttlMs: number) => void
): GatewayUpstreamBucketFailureEntry {
  if (
    entry.halfOpenAccountId
    && entry.halfOpenUntilMs
    && entry.halfOpenUntilMs > now
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

async function claimRedisHalfOpenLeasesForAccounts(
  accounts: UpstreamAccount[],
  entries: Map<string, GatewayUpstreamBucketFailureEntry>,
  now: number,
  scope: 'specific' | 'provider',
  stateStore: GatewayProxyHealthRuntimeStateStore
): Promise<void> {
  const probeAccountByBucketKey = new Map<string, UpstreamAccount>()
  for (const account of accounts) {
    for (const key of gatewayUpstreamBucketKeys(account)
      .filter((key) => scope === 'provider' ? isProviderBucketKey(key) : !isProviderBucketKey(key))) {
      if (!probeAccountByBucketKey.has(key)) {
        probeAccountByBucketKey.set(key, account)
      }
    }
  }
  await Promise.all([...probeAccountByBucketKey.entries()].map(async ([key, account]) => {
    const current = entries.get(key)
    if (!current?.avoidUntilMs || current.avoidUntilMs > now) return
    const claimed = await ensureRedisHalfOpenProbe(
      key,
      current,
      account,
      now,
      stateStore
    )
    if (claimed) {
      entries.set(key, claimed)
    } else {
      entries.delete(key)
    }
  }))
}

async function ensureRedisHalfOpenProbe(
  key: string,
  initialEntry: GatewayUpstreamBucketFailureEntry | undefined,
  account: UpstreamAccount,
  now: number,
  stateStore: GatewayProxyHealthRuntimeStateStore
): Promise<GatewayUpstreamBucketFailureEntry | undefined> {
  let current = initialEntry
  for (let attempt = 0; attempt < upstreamBucketFailureCasMaxAttempts; attempt += 1) {
    if (!current?.avoidUntilMs || current.avoidUntilMs > now) {
      return current
    }
    if (
      current.halfOpenAccountId
      && current.halfOpenUntilMs
      && current.halfOpenUntilMs > now
    ) {
      return current
    }

    const halfOpenUntilMs = now + upstreamBucketHalfOpenLeaseMs
    const nextEntry: GatewayUpstreamBucketFailureEntry = {
      ...current,
      halfOpenStartedAtMs: now,
      halfOpenUntilMs,
      halfOpenAccountId: account.id
    }
    const applied = await stateStore.compareSetJson(
      redisBucketStateKey(key),
      current,
      nextEntry,
      upstreamBucketFailureAvoidTtlMs + upstreamBucketFailureWindowMs
    )
    if (applied) {
      getRequestLogger().warn({
        event: 'gateway_upstream_failure_bucket_half_opened',
        bucketKey: bucketKeyForLog(key),
        bucketType: upstreamBucketType(key),
        accountId: account.id,
        halfOpenUntil: new Date(halfOpenUntilMs).toISOString()
      }, '上游桶运行态避让 TTL 到期，已放行一个半开探测账号')
      return nextEntry
    }
    current = await getRedisBucketFailureEntry(key, stateStore)
  }
  throw redisBucketCasExhaustedError(key, 'half_open_lease')
}

export function recordGatewayUpstreamBucketFailure(
  account: UpstreamAccount,
  reason: string,
  options: { bucketScope?: GatewayUpstreamBucketScope } = {}
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
  options: { bucketScope?: GatewayUpstreamBucketScope } = {}
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
  options: { bucketScope?: GatewayUpstreamBucketScope } = {}
): GatewayProxyFailureDecision {
  const bucketKeys = gatewayUpstreamBucketKeys(account, options.bucketScope)
  if (bucketKeys.length === 0) {
    return { recorded: false, suspected: false }
  }
  const now = gatewayUpstreamBucketHealthNow()
  const ttlMs = Math.max(1, Math.trunc(ttlSeconds)) * 1000
  const avoidUntilMs = now + ttlMs
  let effectiveAvoidUntilMs = avoidUntilMs
  for (const key of bucketKeys) {
    const current = getMemoryBucketFailureEntry(key)
    const accountSamples = pruneAccountSamples([
      ...(current?.accountSamples ?? []),
      [gatewayFailureEvidenceAccountId(account), now]
    ], now)
    const entry: GatewayUpstreamBucketFailureEntry = {
      key,
      reason,
      accountSamples,
      failureCount: (current?.failureCount ?? 0) + 1,
      firstFailedAtMs: current?.firstFailedAtMs ?? now,
      lastFailedAtMs: now,
      avoidUntilMs: Math.max(current?.avoidUntilMs ?? 0, avoidUntilMs)
    }
    effectiveAvoidUntilMs = Math.max(effectiveAvoidUntilMs, entry.avoidUntilMs ?? avoidUntilMs)
    setMemoryBucketFailureEntry(key, entry, ttlMs + upstreamBucketFailureWindowMs)
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
    avoidUntil: new Date(effectiveAvoidUntilMs).toISOString(),
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
  options: { bucketScope?: GatewayUpstreamBucketScope } = {}
): Promise<GatewayProxyFailureDecision> {
  if (!shouldUseRedisUpstreamBucketHealthState()) {
    return suppressGatewayUpstreamBucketLocallyForSeconds(account, ttlSeconds, reason, options)
  }
  const bucketKeys = gatewayUpstreamBucketKeys(account, options.bucketScope)
  if (bucketKeys.length === 0) {
    return { recorded: false, suspected: false }
  }
  const now = gatewayUpstreamBucketHealthNow()
  const mutationObservation = nextGatewayUpstreamBucketMutationObservation(now)
  const ttlMs = Math.max(1, Math.trunc(ttlSeconds)) * 1000
  const avoidUntilMs = now + ttlMs
  const stateStore = gatewayProxyHealthRuntimeStateStore()
  const mutations = await Promise.all(bucketKeys.map((key) =>
    mutateRedisBucketFailureEntry(key, stateStore, (current) => {
      const latestFailure = latestGatewayUpstreamBucketFailureObservation(current, mutationObservation)
      const entry: GatewayUpstreamBucketFailureEntry = {
        key,
        reason,
        accountSamples: pruneAccountSamples([
          ...(current?.accountSamples ?? []),
          [gatewayFailureEvidenceAccountId(account), now]
        ], now),
        failureCount: (current?.failureCount ?? 0) + 1,
        firstFailedAtMs: current?.firstFailedAtMs ?? now,
        lastFailedAtMs: latestFailure.observedAtMs,
        lastFailureGeneration: latestFailure.generation,
        avoidUntilMs: Math.max(current?.avoidUntilMs ?? 0, avoidUntilMs)
      }
      return {
        entry,
        ttlMs: redisBucketFailureEntryTtlMs(entry, now, ttlMs + upstreamBucketFailureWindowMs),
        result: undefined
      }
    })
  ))
  const effectiveAvoidUntilMs = Math.max(
    avoidUntilMs,
    ...mutations.map((mutation) => mutation.entry.avoidUntilMs ?? avoidUntilMs)
  )
  const proxyKey = bucketKeys.find(isProxyBucketKey)
  const safeBucketKeys = bucketKeysForLog(bucketKeys)
  const safeProxyKey = bucketKeyForLog(proxyKey)
  getRequestLogger().warn({
    event: 'gateway_upstream_bucket_locally_suppressed',
    bucketKeys: safeBucketKeys,
    proxyKey: safeProxyKey,
    accountId: account.id,
    ttlSeconds: Math.max(1, Math.trunc(ttlSeconds)),
    avoidUntil: new Date(effectiveAvoidUntilMs).toISOString(),
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
  options: { bucketScope?: GatewayUpstreamBucketScope } = {}
): GatewayProxyFailureDecision {
  return recordGatewayUpstreamBucketFailure(account, reason, { bucketScope: options.bucketScope ?? 'proxy' })
}

export async function recordGatewayProxyFailureAsync(
  account: UpstreamAccount,
  reason: string,
  options: { bucketScope?: GatewayUpstreamBucketScope } = {}
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
  const accountSamples = pruneAccountSamples([
    ...(current?.accountSamples ?? []),
    [gatewayFailureEvidenceAccountId(account), now]
  ], now)
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
    avoidUntilMs: suspected
      ? Math.max(current?.avoidUntilMs ?? 0, now + upstreamBucketFailureAvoidTtlMs)
      : current?.avoidUntilMs
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
  const mutationObservation = nextGatewayUpstreamBucketMutationObservation(now)
  const stateStore = gatewayProxyHealthRuntimeStateStore()
  const mutation = await mutateRedisBucketFailureEntry(key, stateStore, (current) => {
    const accountSamples = pruneAccountSamples([
      ...(current?.accountSamples ?? []),
      [gatewayFailureEvidenceAccountId(account), now]
    ], now)
    const distinctAccountCount = new Set(accountSamples.map(([accountId]) => accountId)).size
    const halfOpenProbeFailed = isHalfOpenProbeForAccount(current, account.id, now)
    const suspected = halfOpenProbeFailed || distinctAccountCount >= upstreamBucketFailureDistinctAccountThreshold
    const latestFailure = latestGatewayUpstreamBucketFailureObservation(current, mutationObservation)
    const entry: GatewayUpstreamBucketFailureEntry = {
      key,
      reason,
      accountSamples,
      failureCount: (current?.failureCount ?? 0) + 1,
      firstFailedAtMs: current?.firstFailedAtMs ?? now,
      lastFailedAtMs: latestFailure.observedAtMs,
      lastFailureGeneration: latestFailure.generation,
      avoidUntilMs: suspected
        ? Math.max(current?.avoidUntilMs ?? 0, now + upstreamBucketFailureAvoidTtlMs)
        : current?.avoidUntilMs
    }
    return {
      entry,
      ttlMs: redisBucketFailureEntryTtlMs(
        entry,
        now,
        upstreamBucketFailureAvoidTtlMs + upstreamBucketFailureWindowMs
      ),
      result: {
        distinctAccountCount,
        halfOpenProbeFailed,
        suspected,
        opened: suspected && (!current?.avoidUntilMs || current.avoidUntilMs <= now)
      }
    }
  })
  if (mutation.result.opened) {
    getRequestLogger().warn({
      event: 'gateway_upstream_failure_bucket_opened',
      bucketKey: bucketKeyForLog(key),
      bucketType: upstreamBucketType(key),
      accountId: account.id,
      distinctAccountCount: mutation.result.distinctAccountCount,
      reason,
      halfOpenProbeFailed: mutation.result.halfOpenProbeFailed,
      avoidUntil: new Date(mutation.entry.avoidUntilMs ?? now).toISOString()
    }, '同上游桶多个账号短窗失败，网关已进入上游桶运行态避让')
  }
  return {
    suspected: mutation.result.suspected,
    bucketKey: key,
    distinctAccountCount: mutation.result.distinctAccountCount
  }
}

async function mutateRedisBucketFailureEntry<TResult>(
  key: string,
  stateStore: GatewayProxyHealthRuntimeStateStore,
  mutation: (current: GatewayUpstreamBucketFailureEntry | undefined) => {
    entry: GatewayUpstreamBucketFailureEntry
    ttlMs: number
    result: TResult
  }
): Promise<{
  previous: GatewayUpstreamBucketFailureEntry | undefined
  entry: GatewayUpstreamBucketFailureEntry
  result: TResult
}> {
  let current = await getRedisBucketFailureEntry(key, stateStore)
  for (let attempt = 0; attempt < upstreamBucketFailureCasMaxAttempts; attempt += 1) {
    const next = mutation(current)
    const applied = await stateStore.compareSetJson(
      redisBucketStateKey(key),
      current,
      next.entry,
      next.ttlMs
    )
    if (applied) {
      return {
        previous: current,
        entry: next.entry,
        result: next.result
      }
    }
    current = await getRedisBucketFailureEntry(key, stateStore)
  }
  throw redisBucketCasExhaustedError(key, 'mutation')
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

export function recordGatewayUpstreamBucketSuccess(
  account: UpstreamAccount,
  options: { bucketScope?: GatewayUpstreamBucketScope } = {}
): boolean {
  let existed = false
  for (const key of gatewayUpstreamBucketKeys(account, options.bucketScope)) {
    existed = Boolean(getMemoryBucketFailureEntry(key)) || existed
    upstreamBucketFailureMemoryEntries.delete(key)
  }
  return existed
}

export async function recordGatewayUpstreamBucketSuccessAsync(
  account: UpstreamAccount,
  options: { bucketScope?: GatewayUpstreamBucketScope } = {}
): Promise<boolean> {
  if (!shouldUseRedisUpstreamBucketHealthState()) {
    return recordGatewayUpstreamBucketSuccess(account, options)
  }
  const successObservation = nextGatewayUpstreamBucketMutationObservation(gatewayUpstreamBucketHealthNow())
  const stateStore = gatewayProxyHealthRuntimeStateStore()
  let cleared = false
  for (const key of gatewayUpstreamBucketKeys(account, options.bucketScope)) {
    cleared = await clearRedisBucketAfterSuccessObservation(
      key,
      successObservation,
      stateStore
    ) || cleared
  }
  return cleared
}

async function clearRedisBucketAfterSuccessObservation(
  key: string,
  successObservation: GatewayUpstreamBucketMutationObservation,
  stateStore: GatewayProxyHealthRuntimeStateStore
): Promise<boolean> {
  let current = await getRedisBucketFailureEntry(key, stateStore)
  if (!current || gatewayUpstreamBucketFailureOccurredAfterObservation(current, successObservation)) {
    return false
  }
  const observedFailureEvidence = current
  for (let attempt = 0; attempt < upstreamBucketFailureCasMaxAttempts; attempt += 1) {
    if (await stateStore.compareDeleteJson(redisBucketStateKey(key), current)) {
      return true
    }
    current = await getRedisBucketFailureEntry(key, stateStore)
    if (!current) {
      return false
    }
    if (
      gatewayUpstreamBucketFailureOccurredAfterObservation(current, successObservation)
      || !sameGatewayUpstreamBucketFailureEvidence(observedFailureEvidence, current)
    ) {
      return false
    }
  }
  throw redisBucketCasExhaustedError(key, 'success_cleanup')
}

export function recordGatewayProxySuccess(account: UpstreamAccount): boolean {
  return recordGatewayUpstreamBucketSuccess(account, { bucketScope: 'proxy' })
}

export async function recordGatewayProxySuccessAsync(account: UpstreamAccount): Promise<boolean> {
  return recordGatewayUpstreamBucketSuccessAsync(account, { bucketScope: 'proxy' })
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

export function setGatewayProxyHealthRuntimeStateStoreForTest(
  stateStore: GatewayProxyHealthRuntimeStateStore | undefined
): void {
  upstreamBucketFailureStateStoreForTest = stateStore
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
  const now = gatewayUpstreamBucketHealthNow()
  const existingExpiresAt = upstreamBucketFailureMemoryEntries.get(key)?.expiresAt ?? 0
  const effectiveTtlMs = redisBucketFailureEntryTtlMs(value, now, ttlMs)
  upstreamBucketFailureMemoryEntries.set(key, {
    value,
    expiresAt: Math.max(existingExpiresAt, now + effectiveTtlMs)
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
  return Boolean(upstreamBucketFailureStateStoreForTest) || runtimeConfig.runtimeStateDriver === 'redis'
}

function gatewayProxyHealthRuntimeStateStore(): GatewayProxyHealthRuntimeStateStore {
  return upstreamBucketFailureStateStoreForTest ?? upstreamBucketFailureStateStore
}

async function loadRedisBucketEntriesForAccounts(
  accounts: UpstreamAccount[],
  stateStore: GatewayProxyHealthRuntimeStateStore
): Promise<Map<string, GatewayUpstreamBucketFailureEntry>> {
  const keys = [...new Set(accounts.flatMap((account) => gatewayUpstreamBucketKeys(account)))]
  const entries = new Map<string, GatewayUpstreamBucketFailureEntry>()
  await Promise.all(keys.map(async (key) => {
    const entry = await getRedisBucketFailureEntry(key, stateStore)
    if (entry) {
      entries.set(key, entry)
    }
  }))
  return entries
}

function redisBucketStateKey(key: string): string {
  return `bucket:${key}`
}

async function getRedisBucketFailureEntry(
  key: string,
  stateStore: GatewayProxyHealthRuntimeStateStore
): Promise<GatewayUpstreamBucketFailureEntry | undefined> {
  return stateStore.getJson<GatewayUpstreamBucketFailureEntry>(redisBucketStateKey(key))
}

function redisBucketCasExhaustedError(key: string, operation: string): Error {
  return new Error(
    `上游桶 Redis CAS 重试耗尽（${upstreamBucketFailureCasMaxAttempts} 次）：${operation}:${bucketKeyForLog(key) ?? 'unknown'}`
  )
}

function redisBucketFailureEntryTtlMs(
  entry: GatewayUpstreamBucketFailureEntry,
  now: number,
  minimumTtlMs: number
): number {
  const avoidRetentionMs = entry.avoidUntilMs
    ? Math.max(0, entry.avoidUntilMs - now) + upstreamBucketFailureWindowMs
    : 0
  const halfOpenRetentionMs = entry.halfOpenUntilMs
    ? Math.max(0, entry.halfOpenUntilMs - now) + upstreamBucketFailureWindowMs
    : 0
  return Math.max(1, Math.trunc(minimumTtlMs), avoidRetentionMs, halfOpenRetentionMs)
}

function nextGatewayUpstreamBucketMutationObservation(observedAtMs: number): GatewayUpstreamBucketMutationObservation {
  upstreamBucketMutationSequence += 1
  return {
    observedAtMs,
    generation: {
      instanceId: upstreamBucketMutationInstanceId,
      sequence: upstreamBucketMutationSequence
    }
  }
}

function latestGatewayUpstreamBucketFailureObservation(
  current: GatewayUpstreamBucketFailureEntry | undefined,
  incoming: GatewayUpstreamBucketMutationObservation
): { observedAtMs: number; generation?: GatewayUpstreamBucketMutationGeneration } {
  if (!current || current.lastFailedAtMs < incoming.observedAtMs) {
    return incoming
  }
  if (current.lastFailedAtMs > incoming.observedAtMs) {
    return {
      observedAtMs: current.lastFailedAtMs,
      generation: validGatewayUpstreamBucketMutationGeneration(current.lastFailureGeneration)
    }
  }
  const currentGeneration = validGatewayUpstreamBucketMutationGeneration(current.lastFailureGeneration)
  if (
    currentGeneration?.instanceId === incoming.generation.instanceId
    && currentGeneration.sequence > incoming.generation.sequence
  ) {
    return { observedAtMs: current.lastFailedAtMs, generation: currentGeneration }
  }
  return incoming
}

function gatewayUpstreamBucketFailureOccurredAfterObservation(
  entry: GatewayUpstreamBucketFailureEntry,
  observation: GatewayUpstreamBucketMutationObservation
): boolean {
  if (entry.lastFailedAtMs > observation.observedAtMs) return true
  if (entry.lastFailedAtMs < observation.observedAtMs) return false
  const failureGeneration = validGatewayUpstreamBucketMutationGeneration(entry.lastFailureGeneration)
  if (!failureGeneration || failureGeneration.instanceId !== observation.generation.instanceId) {
    return true
  }
  return failureGeneration.sequence > observation.generation.sequence
}

function validGatewayUpstreamBucketMutationGeneration(
  value: GatewayUpstreamBucketMutationGeneration | undefined
): GatewayUpstreamBucketMutationGeneration | undefined {
  if (
    !value
    || typeof value.instanceId !== 'string'
    || !value.instanceId
    || !Number.isSafeInteger(value.sequence)
    || value.sequence <= 0
  ) {
    return undefined
  }
  return value
}

function sameGatewayUpstreamBucketFailureEvidence(
  left: GatewayUpstreamBucketFailureEntry,
  right: GatewayUpstreamBucketFailureEntry
): boolean {
  return left.key === right.key
    && left.reason === right.reason
    && left.failureCount === right.failureCount
    && left.firstFailedAtMs === right.firstFailedAtMs
    && left.lastFailedAtMs === right.lastFailedAtMs
    && left.avoidUntilMs === right.avoidUntilMs
    && sameGatewayUpstreamBucketMutationGeneration(left.lastFailureGeneration, right.lastFailureGeneration)
    && sameGatewayUpstreamBucketAccountSamples(left.accountSamples, right.accountSamples)
}

function sameGatewayUpstreamBucketMutationGeneration(
  left: GatewayUpstreamBucketMutationGeneration | undefined,
  right: GatewayUpstreamBucketMutationGeneration | undefined
): boolean {
  if (left === undefined && right === undefined) return true
  const normalizedLeft = validGatewayUpstreamBucketMutationGeneration(left)
  const normalizedRight = validGatewayUpstreamBucketMutationGeneration(right)
  return Boolean(
    normalizedLeft
    && normalizedRight
    && normalizedLeft.instanceId === normalizedRight.instanceId
    && normalizedLeft.sequence === normalizedRight.sequence
  )
}

function sameGatewayUpstreamBucketAccountSamples(
  left: Array<[string, number]>,
  right: Array<[string, number]>
): boolean {
  return Array.isArray(left)
    && Array.isArray(right)
    && left.length === right.length
    && left.every(([accountId, failedAtMs], index) => {
      const candidate = right[index]
      return candidate?.[0] === accountId && candidate[1] === failedAtMs
    })
}

export function gatewayProxyKey(account: Pick<UpstreamAccount, 'proxyProfileId' | 'proxyUrl'>): string | undefined {
  if (account.proxyProfileId) {
    return `proxy:profile:${account.proxyProfileId}`
  }
  if (account.proxyUrl) {
    return `proxy:url:${proxyUrlKeyHash(account.proxyUrl)}`
  }
  return undefined
}

function proxyUrlKeyHash(value: string): string {
  return createHash('sha256').update(value).digest('base64url')
}

export function gatewayUpstreamBucketKeys(
  account: Pick<UpstreamAccount, 'id' | 'systemAccountId' | 'accountOwnerSystemAccountId' | 'providerCode' | 'providerProtocolProfileId' | 'protocolCode' | 'protocolVersion' | 'baseUrl' | 'type' | 'proxyProfileId' | 'proxyUrl'>,
  scope: GatewayUpstreamBucketScope = 'all'
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
  const ownerScope = account.accountOwnerSystemAccountId || account.systemAccountId || account.id
  return [...new Set(keys.map((key) => `${key}:owner:${ownerScope}`))]
}

function gatewayBaseUrlKey(account: Pick<UpstreamAccount, 'baseUrl' | 'type' | 'providerCode' | 'providerProtocolProfileId' | 'protocolCode' | 'protocolVersion'>): string | undefined {
  if (account.type === 'oauth' && isOpenAIProtocolProfile(account)) {
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
  const latestByAccountId = new Map<string, [string, number]>()
  for (const [accountId, failedAtMs] of samples) {
    if (now - failedAtMs > upstreamBucketFailureWindowMs) continue
    const previous = latestByAccountId.get(accountId)
    if (!previous || failedAtMs >= previous[1]) {
      latestByAccountId.set(accountId, [accountId, failedAtMs])
    }
  }
  return [...latestByAccountId.values()]
    .sort((left, right) => left[1] - right[1])
    .slice(-upstreamBucketFailureMaxAccountSamples)
}

function gatewayFailureEvidenceAccountId(
  account: Pick<UpstreamAccount, 'id' | 'credentialSourceAccountId'>
): string {
  return account.credentialSourceAccountId?.trim() || account.id
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

import { runtimeConfig } from '../../../config/runtime.js'
import { createAppCache } from '../../../shared/cache.js'
import { errorLogFields, logger } from '../../../shared/logger.js'
import { getRedisClient, type RedisCommandClient } from '../../../shared/redis-client.js'
import { redisNamespacedKey } from '../../../shared/redis-namespace.js'
import {
  getAccountCurrentConcurrency,
  loadAccountCurrentConcurrencyByIdsAsync,
  loadAccountInFlightStatsByIds,
  loadAccountInFlightStatsByIdsAsync
} from '../../../shared/account-concurrency.js'
import { DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY, effectiveImageLaneConcurrencyLimit, effectiveSoftConcurrencyLimit, resolveGroupSchedulingPolicy } from '../../../domain/group-scheduling.js'
import type { GroupSchedulingPolicy, GroupType } from '../../../domain/types.js'
import type { OpenAIAccountSecret } from '../../../storage/repositories.js'
import type { OpenAIGatewayRequestLane } from '../protocols/openai-v1/request-lane.js'
import {
  compareGatewayAccountModelPriority,
  type GatewayAccountModelPriority
} from '../dispatch/model-filter.js'
import {
  gatewayAccountConcurrencyAccountId,
  gatewayAccountConcurrencyAccountIds
} from '../dispatch/account-concurrency-identity.js'
import {
  deriveGatewaySessionAffinityKey,
  type GatewaySessionIdentity
} from '../session-identity/index.js'
import type { GatewayClientSourceIdentity } from '../client-profiles/source-identity.js'

interface SessionBinding {
  accountId: string
  scope?: OpenAIGatewaySessionAffinityScope
  trafficMigrationPreferred?: boolean
}

interface RedisSessionBindingRecord {
  binding: SessionBinding
  rawValue: string
}

let sessionAffinityRedisClientForTest: RedisCommandClient | undefined

export function setOpenAISessionAffinityRedisClientForTest(client?: RedisCommandClient): void {
  sessionAffinityRedisClientForTest = client
}

interface TrafficMigrationPreference {
  sourceAccountId: string
  targetAccountId: string
}

interface HighConcurrencyCandidate {
  account: OpenAIAccountSecret
  index: number
  currentConcurrency: number
  hardLimit: number
  softLimit: number
  slowInFlightCount: number
  firstOutputSlowCount: number
  oldestInFlightMs: number
  affinityAllowed: boolean
  trafficMigrationPreferred: boolean
  hardBusy: boolean
  softBusy: boolean
}

const sessionAffinityTtlMs = 60 * 60 * 1000
const trafficMigrationPreferenceTtlMs = sessionAffinityTtlMs
const redisSessionAffinityIndexTtlPaddingMs = 60_000
const redisMissingBindingExpectedValue = ''
const redisSetSessionAffinityBindingScript = `
local current = redis.call('GET', KEYS[1])
local expected = ARGV[1]
if expected == '' then
  if current then
    return 0
  end
elseif current ~= expected then
  return 0
end
local new_value = ARGV[2]
local binding_ttl_ms = ARGV[3]
local index_ttl_ms = ARGV[4]
local expires_at = ARGV[5]
local old_index_count = tonumber(ARGV[6])
local session_key = ARGV[7]
for i = 1, old_index_count do
  redis.call('ZREM', KEYS[1 + i], session_key)
end
redis.call('SET', KEYS[1], new_value, 'PX', binding_ttl_ms)
for i = old_index_count + 2, #KEYS do
  redis.call('ZADD', KEYS[i], expires_at, session_key)
  redis.call('PEXPIRE', KEYS[i], index_ttl_ms)
end
return 1
`
const redisDeleteSessionAffinityBindingScript = `
local current = redis.call('GET', KEYS[1])
if current ~= ARGV[1] then
  return 0
end
redis.call('DEL', KEYS[1])
for i = 2, #KEYS do
  redis.call('ZREM', KEYS[i], ARGV[2])
end
return 1
`
const redisRefreshSessionAffinityBindingScript = `
local current = redis.call('GET', KEYS[1])
if current ~= ARGV[1] then
  return 0
end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
for i = 2, #KEYS do
  redis.call('ZADD', KEYS[i], ARGV[3], ARGV[4])
  redis.call('PEXPIRE', KEYS[i], ARGV[5])
end
return 1
`

const sessionAffinityCache = createAppCache<string, SessionBinding>({
  name: 'gateway:openai-session-affinity',
  max: 5000,
  ttlMs: sessionAffinityTtlMs,
  updateAgeOnGet: true,
  dispose: (binding, key) => {
    removeSessionAffinityIndex(key, binding)
  },
  onClear: () => {
    clearSessionAffinityIndexes()
  }
})
const sessionAffinityBindingByKey = new Map<string, SessionBinding>()
const sessionAffinityKeysByAccountId = new Map<string, Set<string>>()
const sessionAffinityKeysByAccountSystemScope = new Map<string, Set<string>>()
const sessionAffinityKeysByAccountSystemApiKeyScope = new Map<string, Set<string>>()
const trafficMigrationPreferenceCache = createAppCache<string, TrafficMigrationPreference>({
  name: 'gateway:openai-traffic-migration-preference',
  max: 1000,
  ttlMs: trafficMigrationPreferenceTtlMs
})

export interface OpenAIGatewaySessionAffinityScope {
  systemAccountId: string
  apiKeyId?: string
  groupId: string
}

export interface OpenAIAccountDispatchOrderingOptions {
  groupType?: GroupType
  schedulingPolicy?: GroupSchedulingPolicy
  modelPriority?: GatewayAccountModelPriority
  trafficMigrationScope?: Partial<OpenAIGatewaySessionAffinityScope>
}

interface TrafficMigrationPreferenceWriteOptions {
  throwOnRedisError?: boolean
}

export function resolveOpenAIGatewaySessionAffinityKey(identity: Pick<GatewaySessionIdentity, 'conversationKey'> | undefined, input: {
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  routeStrategyId?: string
  providerProtocolProfileId?: string
}): string | undefined {
  return identity
    ? deriveGatewaySessionAffinityKey(identity, input)
    : undefined
}

/**
 * Uses the unified client-source resolver without allowing the IP/API-Key
 * fallback to create affinity. Official sessions preserve the existing
 * conversation key; protocol resources gain the same shared affinity storage.
 */
export function resolveOpenAIGatewaySessionAffinityKeyFromClientSource(
  source: Pick<GatewayClientSourceIdentity, 'affinityKey'> | undefined,
  input: {
    systemAccountId: string
    apiKeyId?: string
    groupId: string
    routeStrategyId?: string
    providerProtocolProfileId?: string
  }
): string | undefined {
  const affinityKey = source?.affinityKey?.trim()
  return affinityKey
    ? deriveGatewaySessionAffinityKey({ conversationKey: affinityKey }, input)
    : undefined
}

export function orderOpenAIAccountsBySessionAffinity(
  accounts: OpenAIAccountSecret[],
  sessionAffinityKey?: string,
  options: OpenAIAccountDispatchOrderingOptions = {}
): OpenAIAccountSecret[] {
  const modelOrderedAccounts = orderOpenAIAccountsByModelPriority(accounts, options.modelPriority)
  const trafficMigrationPreference = trafficMigrationPreferenceForAccounts(modelOrderedAccounts, options.trafficMigrationScope)
  const sessionTrafficMigrationTargetAccountId = trafficMigrationPreference
    ? undefined
    : sessionTrafficMigrationTargetForAccounts(modelOrderedAccounts, sessionAffinityKey)
  const trafficMigrationTargetAccountId = trafficMigrationPreference?.targetAccountId ?? sessionTrafficMigrationTargetAccountId
  const preferenceOrderedAccounts = orderOpenAIAccountsByTrafficMigrationPreference(
    modelOrderedAccounts,
    trafficMigrationTargetAccountId,
    options.modelPriority
  )
  if (options.groupType === 'high_concurrency') {
    return orderOpenAIHighConcurrencyAccounts(
      preferenceOrderedAccounts,
      sessionAffinityKey,
      options.schedulingPolicy,
      trafficMigrationTargetAccountId,
      options.modelPriority
    )
  }
  if (trafficMigrationTargetAccountId) {
    return preferenceOrderedAccounts
  }
  return orderOpenAIPersonalAccountsBySessionAffinity(preferenceOrderedAccounts, sessionAffinityKey, options.modelPriority)
}

export async function orderOpenAIAccountsBySessionAffinityAsync(
  accounts: OpenAIAccountSecret[],
  sessionAffinityKey?: string,
  options: OpenAIAccountDispatchOrderingOptions = {}
): Promise<OpenAIAccountSecret[]> {
  if (runtimeConfig.cacheDriver !== 'redis' && options.groupType !== 'high_concurrency') {
    return orderOpenAIAccountsBySessionAffinity(accounts, sessionAffinityKey, options)
  }
  const modelOrderedAccounts = orderOpenAIAccountsByModelPriority(accounts, options.modelPriority)
  const [trafficMigrationPreference, sessionBinding] = runtimeConfig.cacheDriver === 'redis'
    ? await Promise.all([
        trafficMigrationPreferenceForAccountsAsync(modelOrderedAccounts, options.trafficMigrationScope),
        getRedisSessionAffinityBindingForOrdering(sessionAffinityKey)
      ])
    : [
        trafficMigrationPreferenceForAccounts(modelOrderedAccounts, options.trafficMigrationScope),
        sessionAffinityKey ? sessionAffinityCache.get(sessionAffinityKey) : undefined
      ]
  const sessionTrafficMigrationTargetAccountId = trafficMigrationPreference
    ? undefined
    : sessionTrafficMigrationTargetForAccountsFromBinding(modelOrderedAccounts, sessionBinding)
  const trafficMigrationTargetAccountId = trafficMigrationPreference?.targetAccountId ?? sessionTrafficMigrationTargetAccountId
  const preferenceOrderedAccounts = orderOpenAIAccountsByTrafficMigrationPreference(
    modelOrderedAccounts,
    trafficMigrationTargetAccountId,
    options.modelPriority
  )
  if (options.groupType === 'high_concurrency') {
    return orderOpenAIHighConcurrencyAccountsAsync(
      preferenceOrderedAccounts,
      sessionAffinityKey,
      options.schedulingPolicy,
      trafficMigrationTargetAccountId,
      options.modelPriority,
      sessionBinding
    )
  }
  if (trafficMigrationTargetAccountId) {
    return preferenceOrderedAccounts
  }
  return orderOpenAIPersonalAccountsBySessionBinding(preferenceOrderedAccounts, sessionBinding, options.modelPriority)
}

export function areOpenAIHighConcurrencyAccountsHardBusy(accounts: OpenAIAccountSecret[], options: OpenAIAccountDispatchOrderingOptions = {}): boolean {
  return options.groupType === 'high_concurrency'
    && accounts.length > 0
    && accounts.every((account) => accountCurrentConcurrency(account) >= accountHardConcurrencyLimit(account))
}

export function areOpenAIHighConcurrencyAccountsBusyForLane(
  accounts: OpenAIAccountSecret[],
  options: OpenAIAccountDispatchOrderingOptions & { requestLane?: OpenAIGatewayRequestLane } = {}
): boolean {
  if (options.groupType !== 'high_concurrency' || accounts.length === 0) {
    return false
  }
  return accounts.every((account) => {
    const hardLimit = accountHardConcurrencyLimit(account)
    const currentConcurrency = accountCurrentConcurrency(account)
    if (currentConcurrency >= hardLimit) {
      return true
    }
    if (options.requestLane !== 'image') {
      return false
    }
    const imageLaneLimit = effectiveImageLaneConcurrencyLimit({
      accountConcurrencyLimit: hardLimit,
      policy: options.schedulingPolicy
    })
    return getAccountCurrentConcurrency(gatewayAccountConcurrencyAccountId(account), 'image') >= imageLaneLimit
  })
}

export async function areOpenAIHighConcurrencyAccountsBusyForLaneAsync(
  accounts: OpenAIAccountSecret[],
  options: OpenAIAccountDispatchOrderingOptions & { requestLane?: OpenAIGatewayRequestLane } = {}
): Promise<boolean> {
  if (runtimeConfig.runtimeStateDriver !== 'redis') {
    return areOpenAIHighConcurrencyAccountsBusyForLane(accounts, options)
  }
  if (options.groupType !== 'high_concurrency' || accounts.length === 0) {
    return false
  }
  const accountIds = gatewayAccountConcurrencyAccountIds(accounts)
  const [totalConcurrency, imageLaneConcurrency] = await Promise.all([
    loadAccountCurrentConcurrencyByIdsAsync(accountIds),
    options.requestLane === 'image'
      ? loadAccountCurrentConcurrencyByIdsAsync(accountIds, 'image')
      : Promise.resolve(undefined)
  ])
  return accounts.every((account) => {
    const hardLimit = accountHardConcurrencyLimit(account)
    const concurrencyAccountId = gatewayAccountConcurrencyAccountId(account)
    const currentConcurrency = accountCurrentConcurrency(account, totalConcurrency.get(concurrencyAccountId))
    if (currentConcurrency >= hardLimit) {
      return true
    }
    if (options.requestLane !== 'image') {
      return false
    }
    const imageLaneLimit = effectiveImageLaneConcurrencyLimit({
      accountConcurrencyLimit: hardLimit,
      policy: options.schedulingPolicy
    })
    return (imageLaneConcurrency?.get(concurrencyAccountId) ?? 0) >= imageLaneLimit
  })
}

function orderOpenAIPersonalAccountsBySessionAffinity(
  accounts: OpenAIAccountSecret[],
  sessionAffinityKey?: string,
  modelPriority?: GatewayAccountModelPriority
): OpenAIAccountSecret[] {
  if (accounts.some((account) => account.superPriorityEnabled)) {
    return accounts
  }
  if (!sessionAffinityKey || accounts.length < 2) {
    return accounts
  }
  const binding = sessionAffinityCache.get(sessionAffinityKey)
  return orderOpenAIPersonalAccountsBySessionBinding(accounts, binding, modelPriority)
}

function orderOpenAIPersonalAccountsBySessionBinding(
  accounts: OpenAIAccountSecret[],
  binding: SessionBinding | undefined,
  modelPriority?: GatewayAccountModelPriority
): OpenAIAccountSecret[] {
  if (accounts.some((account) => account.superPriorityEnabled)) {
    return accounts
  }
  if (accounts.length < 2) {
    return accounts
  }
  if (!binding) {
    return accounts
  }
  const boundIndex = accounts.findIndex((account) => account.id === binding.accountId)
  if (boundIndex <= 0) {
    return accounts
  }
  const boundAccount = accounts[boundIndex]
  let targetIndex = boundIndex
  for (let index = boundIndex - 1; index >= 0; index -= 1) {
    if (!canSessionAffinityPromoteOver(boundAccount, accounts[index], modelPriority)) {
      break
    }
    targetIndex = index
  }
  if (targetIndex === boundIndex) {
    return accounts
  }
  let rotationEndIndex = boundIndex + 1
  for (; rotationEndIndex < accounts.length; rotationEndIndex += 1) {
    if (!canSessionAffinityRotateWithinSameTier(boundAccount, accounts[rotationEndIndex], modelPriority)) {
      break
    }
  }
  return [
    ...accounts.slice(0, targetIndex),
    boundAccount,
    ...accounts.slice(boundIndex + 1, rotationEndIndex),
    ...accounts.slice(targetIndex, boundIndex),
    ...accounts.slice(rotationEndIndex)
  ]
}

function canSessionAffinityRotateWithinSameTier(
  boundAccount: OpenAIAccountSecret,
  currentAccount: OpenAIAccountSecret,
  modelPriority?: GatewayAccountModelPriority
): boolean {
  return canSessionAffinityPromoteOver(boundAccount, currentAccount, modelPriority)
    && canSessionAffinityPromoteOver(currentAccount, boundAccount, modelPriority)
}

function orderOpenAIHighConcurrencyAccounts(
  accounts: OpenAIAccountSecret[],
  sessionAffinityKey: string | undefined,
  policyInput: GroupSchedulingPolicy | undefined,
  trafficMigrationTargetAccountId?: string,
  modelPriority?: GatewayAccountModelPriority
): OpenAIAccountSecret[] {
  if (accounts.length < 2) {
    return accounts
  }
  const policy = resolveGroupSchedulingPolicy('high_concurrency', policyInput) ?? DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY
  if (policy.fastFirstEnabled === false) {
    return trafficMigrationTargetAccountId
      ? orderOpenAIHighConcurrencyHardBusyLast(accounts)
      : orderOpenAIPersonalAccountsBySessionAffinity(accounts, sessionAffinityKey, modelPriority)
  }
  const binding = sessionAffinityKey ? sessionAffinityCache.get(sessionAffinityKey) : undefined
  const inFlightStats = loadAccountInFlightStatsByIds(gatewayAccountConcurrencyAccountIds(accounts), {
    slowRequestThresholdMs: policy.slowRequestThresholdMs ?? DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.slowRequestThresholdMs,
    firstOutputSlowThresholdMs: policy.firstOutputSlowThresholdMs ?? DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.firstOutputSlowThresholdMs
  })
  const candidates = accounts.map((account, index) => {
    const runtimeStats = inFlightStats.get(gatewayAccountConcurrencyAccountId(account))
    const currentConcurrency = accountCurrentConcurrency(account, runtimeStats?.currentConcurrency)
    const hardLimit = accountHardConcurrencyLimit(account)
    const softLimit = effectiveSoftConcurrencyLimit({
      accountConcurrencyLimit: hardLimit,
      policy
    })
    const boundToSession = binding?.accountId === account.id
    const affinityAllowed = boundToSession
      && currentConcurrency < hardLimit
      && (policy.breakAffinityOnSoftLimit === false || currentConcurrency < softLimit)
    return {
      account,
      index,
      currentConcurrency,
      hardLimit,
      softLimit,
      slowInFlightCount: runtimeStats?.slowInFlightCount ?? 0,
      firstOutputSlowCount: runtimeStats?.firstOutputSlowCount ?? 0,
      oldestInFlightMs: runtimeStats?.oldestInFlightMs ?? 0,
      affinityAllowed,
      trafficMigrationPreferred: account.id === trafficMigrationTargetAccountId,
      hardBusy: currentConcurrency >= hardLimit,
      softBusy: policy.breakAffinityOnSoftLimit === false && boundToSession
        ? false
        : currentConcurrency >= softLimit
    }
  })
  const primarySoftAvailable = candidates.some((candidate) => !candidate.account.fallbackEnabled && !candidate.hardBusy && !candidate.softBusy)
  return [...candidates]
    .sort((left, right) => compareHighConcurrencyCandidates(left, right, policy, primarySoftAvailable, modelPriority))
    .map((candidate) => candidate.account)
}

async function orderOpenAIHighConcurrencyAccountsAsync(
  accounts: OpenAIAccountSecret[],
  sessionAffinityKey: string | undefined,
  policyInput: GroupSchedulingPolicy | undefined,
  trafficMigrationTargetAccountId?: string,
  modelPriority?: GatewayAccountModelPriority,
  sessionBinding?: SessionBinding
): Promise<OpenAIAccountSecret[]> {
  if (runtimeConfig.runtimeStateDriver !== 'redis') {
    return orderOpenAIHighConcurrencyAccounts(accounts, sessionAffinityKey, policyInput, trafficMigrationTargetAccountId, modelPriority)
  }
  if (accounts.length < 2) {
    return accounts
  }
  const policy = resolveGroupSchedulingPolicy('high_concurrency', policyInput) ?? DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY
  if (policy.fastFirstEnabled === false) {
    return trafficMigrationTargetAccountId
      ? await orderOpenAIHighConcurrencyHardBusyLastAsync(accounts)
      : orderOpenAIPersonalAccountsBySessionBinding(
          accounts,
          sessionBinding ?? (runtimeConfig.cacheDriver !== 'redis' && sessionAffinityKey ? sessionAffinityCache.get(sessionAffinityKey) : undefined),
          modelPriority
        )
  }
  const binding = sessionBinding ?? (runtimeConfig.cacheDriver !== 'redis' && sessionAffinityKey ? sessionAffinityCache.get(sessionAffinityKey) : undefined)
  const inFlightStats = await loadAccountInFlightStatsByIdsAsync(gatewayAccountConcurrencyAccountIds(accounts), {
    slowRequestThresholdMs: policy.slowRequestThresholdMs ?? DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.slowRequestThresholdMs,
    firstOutputSlowThresholdMs: policy.firstOutputSlowThresholdMs ?? DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.firstOutputSlowThresholdMs
  })
  const candidates = accounts.map((account, index) => {
    const runtimeStats = inFlightStats.get(gatewayAccountConcurrencyAccountId(account))
    const currentConcurrency = accountCurrentConcurrency(account, runtimeStats?.currentConcurrency)
    const hardLimit = accountHardConcurrencyLimit(account)
    const softLimit = effectiveSoftConcurrencyLimit({
      accountConcurrencyLimit: hardLimit,
      policy
    })
    const boundToSession = binding?.accountId === account.id
    const affinityAllowed = boundToSession
      && currentConcurrency < hardLimit
      && (policy.breakAffinityOnSoftLimit === false || currentConcurrency < softLimit)
    return {
      account,
      index,
      currentConcurrency,
      hardLimit,
      softLimit,
      slowInFlightCount: runtimeStats?.slowInFlightCount ?? 0,
      firstOutputSlowCount: runtimeStats?.firstOutputSlowCount ?? 0,
      oldestInFlightMs: runtimeStats?.oldestInFlightMs ?? 0,
      affinityAllowed,
      trafficMigrationPreferred: account.id === trafficMigrationTargetAccountId,
      hardBusy: currentConcurrency >= hardLimit,
      softBusy: policy.breakAffinityOnSoftLimit === false && boundToSession
        ? false
        : currentConcurrency >= softLimit
    }
  })
  const primarySoftAvailable = candidates.some((candidate) => !candidate.account.fallbackEnabled && !candidate.hardBusy && !candidate.softBusy)
  return [...candidates]
    .sort((left, right) => compareHighConcurrencyCandidates(left, right, policy, primarySoftAvailable, modelPriority))
    .map((candidate) => candidate.account)
}

function orderOpenAIHighConcurrencyHardBusyLast(accounts: OpenAIAccountSecret[]): OpenAIAccountSecret[] {
  if (accounts.length < 2) {
    return accounts
  }
  const inFlightStats = loadAccountInFlightStatsByIds(gatewayAccountConcurrencyAccountIds(accounts), {
    slowRequestThresholdMs: DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.slowRequestThresholdMs,
    firstOutputSlowThresholdMs: DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.firstOutputSlowThresholdMs
  })
  const available: OpenAIAccountSecret[] = []
  const hardBusy: OpenAIAccountSecret[] = []
  for (const account of accounts) {
    const runtimeStats = inFlightStats.get(gatewayAccountConcurrencyAccountId(account))
    if (accountCurrentConcurrency(account, runtimeStats?.currentConcurrency) >= accountHardConcurrencyLimit(account)) {
      hardBusy.push(account)
    } else {
      available.push(account)
    }
  }
  return available.length > 0 && hardBusy.length > 0
    ? [...available, ...hardBusy]
    : accounts
}

async function orderOpenAIHighConcurrencyHardBusyLastAsync(accounts: OpenAIAccountSecret[]): Promise<OpenAIAccountSecret[]> {
  if (runtimeConfig.runtimeStateDriver !== 'redis') {
    return orderOpenAIHighConcurrencyHardBusyLast(accounts)
  }
  if (accounts.length < 2) {
    return accounts
  }
  const currentConcurrencyByAccount = await loadAccountCurrentConcurrencyByIdsAsync(gatewayAccountConcurrencyAccountIds(accounts))
  const available: OpenAIAccountSecret[] = []
  const hardBusy: OpenAIAccountSecret[] = []
  for (const account of accounts) {
    if (accountCurrentConcurrency(account, currentConcurrencyByAccount.get(gatewayAccountConcurrencyAccountId(account))) >= accountHardConcurrencyLimit(account)) {
      hardBusy.push(account)
    } else {
      available.push(account)
    }
  }
  return available.length > 0 && hardBusy.length > 0
    ? [...available, ...hardBusy]
    : accounts
}

function compareHighConcurrencyCandidates(
  left: HighConcurrencyCandidate,
  right: HighConcurrencyCandidate,
  policy: GroupSchedulingPolicy,
  primarySoftAvailable: boolean,
  modelPriority?: GatewayAccountModelPriority
): number {
  if (left.hardBusy !== right.hardBusy) return left.hardBusy ? 1 : -1
  const modelPriorityDelta = compareGatewayAccountModelPriority(left.account, right.account, modelPriority)
  if (modelPriorityDelta !== 0) return modelPriorityDelta
  if (left.trafficMigrationPreferred !== right.trafficMigrationPreferred) {
    return left.trafficMigrationPreferred ? -1 : 1
  }
  if (policy.fallbackOnQueueEnabled === false || primarySoftAvailable) {
    const fallbackDelta = accountFallbackRank(left.account) - accountFallbackRank(right.account)
    if (fallbackDelta !== 0) return fallbackDelta
  }
  if (left.softBusy !== right.softBusy) return left.softBusy ? 1 : -1
  if (policy.fallbackOnQueueEnabled !== false && !primarySoftAvailable) {
    const fallbackDelta = accountFallbackRank(left.account) - accountFallbackRank(right.account)
    if (fallbackDelta !== 0) return fallbackDelta
  }
  if (left.account.superPriorityEnabled !== right.account.superPriorityEnabled) {
    return left.account.superPriorityEnabled ? -1 : 1
  }
  if (left.account.priority !== right.account.priority) return left.account.priority - right.account.priority
  const loadRatioDelta = (left.currentConcurrency / left.softLimit) - (right.currentConcurrency / right.softLimit)
  if (Math.abs(loadRatioDelta) > 0.000001) return loadRatioDelta
  if (left.currentConcurrency !== right.currentConcurrency) return left.currentConcurrency - right.currentConcurrency
  if (left.firstOutputSlowCount !== right.firstOutputSlowCount) return left.firstOutputSlowCount - right.firstOutputSlowCount
  if (left.slowInFlightCount !== right.slowInFlightCount) return left.slowInFlightCount - right.slowInFlightCount
  if (left.oldestInFlightMs !== right.oldestInFlightMs) return left.oldestInFlightMs - right.oldestInFlightMs
  const qualityDelta = compareAccountQualityRank(left.account, right.account)
  if (qualityDelta !== 0) return qualityDelta
  if (left.affinityAllowed !== right.affinityAllowed) return left.affinityAllowed ? -1 : 1
  return left.index - right.index
}

export function rememberOpenAIAccountForSession(sessionAffinityKey: string | undefined, accountId: string, scope?: OpenAIGatewaySessionAffinityScope): void {
  if (!sessionAffinityKey) {
    return
  }
  if (shouldUseRedisSessionAffinity()) {
    void rememberOpenAIAccountForSessionAsync(sessionAffinityKey, accountId, scope)
    return
  }
  if (!canUseProcessLocalSessionAffinity()) return
  rememberOpenAIAccountForSessionLocal(sessionAffinityKey, accountId, scope)
}

export async function rememberOpenAIAccountForSessionAsync(sessionAffinityKey: string | undefined, accountId: string, scope?: OpenAIGatewaySessionAffinityScope): Promise<void> {
  await claimOpenAIAccountForSessionAsync(sessionAffinityKey, accountId, scope)
}

export async function claimOpenAIAccountForSessionAsync(
  sessionAffinityKey: string | undefined,
  accountId: string,
  scope?: OpenAIGatewaySessionAffinityScope
): Promise<string | undefined> {
  if (!sessionAffinityKey) {
    return undefined
  }
  if (!shouldUseRedisSessionAffinity()) {
    if (!canUseProcessLocalSessionAffinity()) return undefined
    return claimOpenAIAccountForSessionLocal(sessionAffinityKey, accountId, scope)
  }
  try {
    let previous = await getRedisSessionAffinityRecord(sessionAffinityKey, { refreshTtl: false })
    for (let attempt = 0; attempt < 2; attempt += 1) {
      if (previous) {
        if (previous.binding.accountId === accountId) {
          await refreshRedisSessionAffinityBinding(await redisSessionAffinityClient(), sessionAffinityKey, previous)
        }
        return previous.binding.accountId
      }
      const written = await setRedisSessionAffinityBinding(sessionAffinityKey, {
        accountId,
        scope
      }, previous)
      if (written) {
        return accountId
      }
      previous = await getRedisSessionAffinityRecord(sessionAffinityKey, { refreshTtl: false })
    }
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'redis_openai_session_affinity_remember_failed',
      accountId
    }), 'Redis 会话亲和绑定写入失败，已跳过本次亲和记录')
  }
  return undefined
}

function rememberOpenAIAccountForSessionLocal(sessionAffinityKey: string, accountId: string, scope?: OpenAIGatewaySessionAffinityScope): void {
  claimOpenAIAccountForSessionLocal(sessionAffinityKey, accountId, scope)
}

function claimOpenAIAccountForSessionLocal(sessionAffinityKey: string, accountId: string, scope?: OpenAIGatewaySessionAffinityScope): string {
  const previous = sessionAffinityCache.get(sessionAffinityKey)
  if (previous) {
    return previous.accountId
  }
  setSessionAffinityBinding(sessionAffinityKey, {
    accountId,
    scope
  })
  return accountId
}

export function rememberOpenAIAccountTrafficMigrationPreference(
  sourceAccountId: string,
  targetAccountId: string,
  scope?: Partial<OpenAIGatewaySessionAffinityScope>
): void {
  const source = stringValue(sourceAccountId)
  const target = stringValue(targetAccountId)
  const key = trafficMigrationPreferenceScopeKey(scope)
  if (!source || !target || !key || source === target) {
    return
  }
  if (shouldUseRedisSessionAffinity()) {
    void rememberOpenAIAccountTrafficMigrationPreferenceAsync(source, target, scope)
    return
  }
  if (!canUseProcessLocalSessionAffinity()) return
  trafficMigrationPreferenceCache.set(key, {
    sourceAccountId: source,
    targetAccountId: target
  })
}

export async function rememberOpenAIAccountTrafficMigrationPreferenceAsync(
  sourceAccountId: string,
  targetAccountId: string,
  scope?: Partial<OpenAIGatewaySessionAffinityScope>,
  options: TrafficMigrationPreferenceWriteOptions = {}
): Promise<void> {
  const source = stringValue(sourceAccountId)
  const target = stringValue(targetAccountId)
  const key = trafficMigrationPreferenceScopeKey(scope)
  if (!source || !target || !key || source === target) {
    return
  }
  if (!shouldUseRedisSessionAffinity()) {
    rememberOpenAIAccountTrafficMigrationPreference(source, target, scope)
    return
  }
  try {
    await setRedisTrafficMigrationPreference(key, {
      sourceAccountId: source,
      targetAccountId: target
    })
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'redis_openai_traffic_migration_preference_write_failed',
      sourceAccountId: source,
      targetAccountId: target
    }), 'Redis 流量迁移偏向写入失败，已跳过本次偏向记录')
    if (options.throwOnRedisError === true) {
      throw error
    }
  }
}

export function forgetOpenAIAccountForSession(sessionAffinityKey: string | undefined, accountId?: string): void {
  if (!sessionAffinityKey) {
    return
  }
  if (shouldUseRedisSessionAffinity()) {
    logger.warn({
      event: 'redis_openai_session_affinity_sync_forget_ignored',
      accountId
    }, 'Redis cache driver 下必须使用异步会话亲和清理入口')
    return
  }
  if (!canUseProcessLocalSessionAffinity()) return
  const binding = sessionAffinityCache.get(sessionAffinityKey)
  if (!binding) {
    return
  }
  if (accountId && binding.accountId !== accountId) {
    return
  }
  sessionAffinityCache.delete(sessionAffinityKey)
}

export async function forgetOpenAIAccountForSessionAsync(sessionAffinityKey: string | undefined, accountId?: string): Promise<void> {
  if (!sessionAffinityKey) {
    return
  }
  if (!shouldUseRedisSessionAffinity()) {
    forgetOpenAIAccountForSession(sessionAffinityKey, accountId)
    return
  }
  try {
    const record = await getRedisSessionAffinityRecord(sessionAffinityKey, { refreshTtl: false })
    if (!record) {
      return
    }
    if (accountId && record.binding.accountId !== accountId) {
      return
    }
    await deleteRedisSessionAffinityBinding(sessionAffinityKey, record)
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'redis_openai_session_affinity_forget_failed',
      accountId
    }), 'Redis 会话亲和绑定清理失败，已跳过本次清理')
  }
}

export function migrateOpenAIAccountSessionAffinity(
  sourceAccountId: string,
  targetAccountId: string,
  scope?: Partial<OpenAIGatewaySessionAffinityScope>,
  options: { preferMigratedSessions?: boolean } = {}
): { migratedSessionCount: number } {
  if (!canUseProcessLocalSessionAffinity()) {
    return { migratedSessionCount: 0 }
  }
  let migratedSessionCount = 0
  for (const key of sessionAffinityMigrationCandidateKeys(sourceAccountId, scope)) {
    const binding = sessionAffinityCache.get(key)
    if (!binding) {
      continue
    }
    if (binding.accountId !== sourceAccountId) {
      continue
    }
    if (scope && !sessionBindingMatchesScope(binding, scope)) {
      continue
    }
    setSessionAffinityBinding(key, {
      accountId: targetAccountId,
      scope: binding.scope,
      ...(options.preferMigratedSessions === true ? { trafficMigrationPreferred: true } : {})
    })
    migratedSessionCount += 1
  }
  return { migratedSessionCount }
}

export async function migrateOpenAIAccountSessionAffinityAsync(
  sourceAccountId: string,
  targetAccountId: string,
  scope?: Partial<OpenAIGatewaySessionAffinityScope>,
  options: { preferMigratedSessions?: boolean } = {}
): Promise<{ migratedSessionCount: number }> {
  if (!shouldUseRedisSessionAffinity()) {
    return migrateOpenAIAccountSessionAffinity(sourceAccountId, targetAccountId, scope, options)
  }
  try {
    return await migrateRedisOpenAIAccountSessionAffinity(sourceAccountId, targetAccountId, scope, options)
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'redis_openai_session_affinity_migration_failed',
      sourceAccountId,
      targetAccountId
    }), 'Redis 会话亲和迁移失败')
    throw error
  }
}

function setSessionAffinityBinding(key: string, binding: SessionBinding): void {
  if (!canUseProcessLocalSessionAffinity()) return
  const previous = sessionAffinityBindingByKey.get(key)
  if (previous) {
    removeSessionAffinityIndex(key, previous)
  }
  sessionAffinityCache.set(key, binding)
  addSessionAffinityIndex(key, binding)
}

function addSessionAffinityIndex(key: string, binding: SessionBinding): void {
  sessionAffinityBindingByKey.set(key, binding)
  addSetValue(sessionAffinityKeysByAccountId, binding.accountId, key)
  if (binding.scope?.systemAccountId) {
    addSetValue(sessionAffinityKeysByAccountSystemScope, accountSystemScopeIndexKey(binding.accountId, binding.scope.systemAccountId), key)
    if (binding.scope.apiKeyId) {
      addSetValue(sessionAffinityKeysByAccountSystemApiKeyScope, accountSystemApiKeyScopeIndexKey(binding.accountId, binding.scope.systemAccountId, binding.scope.apiKeyId), key)
    }
  }
}

function removeSessionAffinityIndex(key: string, binding: SessionBinding): void {
  if (sessionAffinityBindingByKey.get(key) !== binding) {
    return
  }
  sessionAffinityBindingByKey.delete(key)
  deleteSetValue(sessionAffinityKeysByAccountId, binding.accountId, key)
  if (binding.scope?.systemAccountId) {
    deleteSetValue(sessionAffinityKeysByAccountSystemScope, accountSystemScopeIndexKey(binding.accountId, binding.scope.systemAccountId), key)
    if (binding.scope.apiKeyId) {
      deleteSetValue(sessionAffinityKeysByAccountSystemApiKeyScope, accountSystemApiKeyScopeIndexKey(binding.accountId, binding.scope.systemAccountId, binding.scope.apiKeyId), key)
    }
  }
}

function clearSessionAffinityIndexes(): void {
  sessionAffinityBindingByKey.clear()
  sessionAffinityKeysByAccountId.clear()
  sessionAffinityKeysByAccountSystemScope.clear()
  sessionAffinityKeysByAccountSystemApiKeyScope.clear()
}

function sessionAffinityMigrationCandidateKeys(sourceAccountId: string, scope?: Partial<OpenAIGatewaySessionAffinityScope>): string[] {
  if (!canUseProcessLocalSessionAffinity()) return []
  const systemAccountId = scope?.systemAccountId
  const apiKeyId = scope?.apiKeyId
  if (systemAccountId && apiKeyId) {
    return [...(sessionAffinityKeysByAccountSystemApiKeyScope.get(accountSystemApiKeyScopeIndexKey(sourceAccountId, systemAccountId, apiKeyId)) ?? [])]
  }
  if (systemAccountId) {
    return [...(sessionAffinityKeysByAccountSystemScope.get(accountSystemScopeIndexKey(sourceAccountId, systemAccountId)) ?? [])]
  }
  return [...(sessionAffinityKeysByAccountId.get(sourceAccountId) ?? [])]
}

async function migrateRedisOpenAIAccountSessionAffinity(
  sourceAccountId: string,
  targetAccountId: string,
  scope?: Partial<OpenAIGatewaySessionAffinityScope>,
  options: { preferMigratedSessions?: boolean } = {}
): Promise<{ migratedSessionCount: number }> {
  const source = stringValue(sourceAccountId)
  const target = stringValue(targetAccountId)
  if (!source || !target || source === target) {
    return { migratedSessionCount: 0 }
  }
  let migratedSessionCount = 0
  const candidateKeys = await redisSessionAffinityMigrationCandidateKeys(source, scope)
  for (const key of candidateKeys) {
    const record = await getRedisSessionAffinityRecord(key, { refreshTtl: false })
    if (!record) {
      continue
    }
    const binding = record.binding
    if (binding.accountId !== source) {
      continue
    }
    if (scope && !sessionBindingMatchesScope(binding, scope)) {
      continue
    }
    const migrated = await setRedisSessionAffinityBinding(key, {
      accountId: target,
      scope: binding.scope,
      ...(options.preferMigratedSessions === true ? { trafficMigrationPreferred: true } : {})
    }, record)
    if (migrated) {
      migratedSessionCount += 1
    }
  }
  return { migratedSessionCount }
}

async function redisSessionAffinityMigrationCandidateKeys(
  sourceAccountId: string,
  scope?: Partial<OpenAIGatewaySessionAffinityScope>
): Promise<string[]> {
  const systemAccountId = scope?.systemAccountId
  const apiKeyId = scope?.apiKeyId
  const indexKey = systemAccountId && apiKeyId
    ? redisSessionAffinityAccountSystemApiKeyIndexKey(sourceAccountId, systemAccountId, apiKeyId)
    : systemAccountId
      ? redisSessionAffinityAccountSystemIndexKey(sourceAccountId, systemAccountId)
      : redisSessionAffinityAccountIndexKey(sourceAccountId)
  const now = Date.now()
  const client = await redisSessionAffinityClient()
  await client.sendCommand(['ZREMRANGEBYSCORE', indexKey, '-inf', String(now - 1)])
  return stringArrayRedisResult(await client.sendCommand(['ZRANGEBYSCORE', indexKey, String(now), '+inf']))
}

async function getRedisSessionAffinityBindingForOrdering(sessionAffinityKey: string | undefined): Promise<SessionBinding | undefined> {
  if (!sessionAffinityKey) {
    return undefined
  }
  try {
    return await getRedisSessionAffinityBinding(sessionAffinityKey, { refreshTtl: true })
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'redis_openai_session_affinity_read_failed'
    }), 'Redis 会话亲和绑定读取失败，已跳过本次亲和排序')
    return undefined
  }
}

async function getRedisSessionAffinityBinding(
  sessionAffinityKey: string,
  options: { refreshTtl: boolean }
): Promise<SessionBinding | undefined> {
  return (await getRedisSessionAffinityRecord(sessionAffinityKey, options))?.binding
}

async function getRedisSessionAffinityRecord(
  sessionAffinityKey: string,
  options: { refreshTtl: boolean }
): Promise<RedisSessionBindingRecord | undefined> {
  const client = await redisSessionAffinityClient()
  const key = redisSessionAffinityBindingKey(sessionAffinityKey)
  const rawValue = await client.get(key)
  if (rawValue === null) {
    return undefined
  }
  const binding = parseRedisSessionBinding(rawValue)
  if (!binding) {
    await client.del(key)
    return undefined
  }
  const record = { binding, rawValue }
  if (options.refreshTtl) {
    await refreshRedisSessionAffinityBinding(client, sessionAffinityKey, record)
  }
  return record
}

async function setRedisSessionAffinityBinding(
  sessionAffinityKey: string,
  binding: SessionBinding,
  previous?: RedisSessionBindingRecord
): Promise<boolean> {
  const client = await redisSessionAffinityClient()
  const priorRecord = previous ?? await getRedisSessionAffinityRecord(sessionAffinityKey, { refreshTtl: false })
  const oldIndexKeys = priorRecord ? redisSessionAffinityIndexKeysForBinding(priorRecord.binding) : []
  const newIndexKeys = redisSessionAffinityIndexKeysForBinding(binding)
  const keys = [
    redisSessionAffinityBindingKey(sessionAffinityKey),
    ...oldIndexKeys,
    ...newIndexKeys
  ]
  const result = await client.eval(redisSetSessionAffinityBindingScript, {
    keys,
    arguments: [
      priorRecord?.rawValue ?? redisMissingBindingExpectedValue,
      JSON.stringify(binding),
      String(sessionAffinityTtlMs),
      String(sessionAffinityTtlMs + redisSessionAffinityIndexTtlPaddingMs),
      String(Date.now() + sessionAffinityTtlMs),
      String(oldIndexKeys.length),
      sessionAffinityKey
    ]
  })
  return redisBooleanResult(result)
}

async function deleteRedisSessionAffinityBinding(
  sessionAffinityKey: string,
  record: RedisSessionBindingRecord
): Promise<boolean> {
  const client = await redisSessionAffinityClient()
  const result = await client.eval(redisDeleteSessionAffinityBindingScript, {
    keys: [
      redisSessionAffinityBindingKey(sessionAffinityKey),
      ...redisSessionAffinityIndexKeysForBinding(record.binding)
    ],
    arguments: [record.rawValue, sessionAffinityKey]
  })
  return redisBooleanResult(result)
}

async function refreshRedisSessionAffinityBinding(
  client: RedisCommandClient,
  sessionAffinityKey: string,
  record: RedisSessionBindingRecord
): Promise<boolean> {
  const result = await client.eval(redisRefreshSessionAffinityBindingScript, {
    keys: [
      redisSessionAffinityBindingKey(sessionAffinityKey),
      ...redisSessionAffinityIndexKeysForBinding(record.binding)
    ],
    arguments: [
      record.rawValue,
      String(sessionAffinityTtlMs),
      String(Date.now() + sessionAffinityTtlMs),
      sessionAffinityKey,
      String(sessionAffinityTtlMs + redisSessionAffinityIndexTtlPaddingMs)
    ]
  })
  return redisBooleanResult(result)
}

function redisSessionAffinityIndexKeysForBinding(binding: SessionBinding): string[] {
  const keys = [redisSessionAffinityAccountIndexKey(binding.accountId)]
  if (binding.scope?.systemAccountId) {
    keys.push(redisSessionAffinityAccountSystemIndexKey(binding.accountId, binding.scope.systemAccountId))
    if (binding.scope.apiKeyId) {
      keys.push(redisSessionAffinityAccountSystemApiKeyIndexKey(binding.accountId, binding.scope.systemAccountId, binding.scope.apiKeyId))
    }
  }
  return keys
}

function parseRedisSessionBinding(rawValue: string): SessionBinding | undefined {
  try {
    const parsed = JSON.parse(rawValue) as Record<string, unknown>
    const accountId = stringValue(parsed.accountId)
    if (!accountId) {
      return undefined
    }
    const scope = parseRedisSessionBindingScope(parsed.scope)
    return {
      accountId,
      ...(scope ? { scope } : {}),
      ...(parsed.trafficMigrationPreferred === true ? { trafficMigrationPreferred: true } : {})
    }
  } catch {
    return undefined
  }
}

function parseRedisSessionBindingScope(value: unknown): OpenAIGatewaySessionAffinityScope | undefined {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return undefined
  }
  const record = value as Record<string, unknown>
  const systemAccountId = stringValue(record.systemAccountId)
  const groupId = stringValue(record.groupId)
  if (!systemAccountId || !groupId) {
    return undefined
  }
  const apiKeyId = stringValue(record.apiKeyId)
  return {
    systemAccountId,
    ...(apiKeyId ? { apiKeyId } : {}),
    groupId
  }
}

async function setRedisTrafficMigrationPreference(scopeKey: string, preference: TrafficMigrationPreference): Promise<void> {
  await (await redisSessionAffinityClient()).set(
    redisTrafficMigrationPreferenceKey(scopeKey),
    JSON.stringify(preference),
    { PX: trafficMigrationPreferenceTtlMs }
  )
}

async function getRedisTrafficMigrationPreference(scopeKey: string): Promise<TrafficMigrationPreference | undefined> {
  const client = await redisSessionAffinityClient()
  const key = redisTrafficMigrationPreferenceKey(scopeKey)
  const rawValue = await client.get(key)
  if (rawValue === null) {
    return undefined
  }
  const preference = parseRedisTrafficMigrationPreference(rawValue)
  if (!preference) {
    await client.del(key)
    return undefined
  }
  await client.sendCommand(['PEXPIRE', key, String(trafficMigrationPreferenceTtlMs)])
  return preference
}

async function deleteRedisTrafficMigrationPreference(scopeKey: string): Promise<void> {
  await (await redisSessionAffinityClient()).del(redisTrafficMigrationPreferenceKey(scopeKey))
}

function parseRedisTrafficMigrationPreference(rawValue: string): TrafficMigrationPreference | undefined {
  try {
    const parsed = JSON.parse(rawValue) as Record<string, unknown>
    const sourceAccountId = stringValue(parsed.sourceAccountId)
    const targetAccountId = stringValue(parsed.targetAccountId)
    return sourceAccountId && targetAccountId
      ? { sourceAccountId, targetAccountId }
      : undefined
  } catch {
    return undefined
  }
}

function redisSessionAffinityClient(): Promise<RedisCommandClient> {
  if (sessionAffinityRedisClientForTest) {
    return Promise.resolve(sessionAffinityRedisClientForTest)
  }
  const redisUrl = runtimeConfig.redis.cacheUrl
  if (!redisUrl) {
    throw new Error('JUHE_AI_REDIS_CACHE_URL 在 Redis cache driver 下必须配置')
  }
  return getRedisClient(redisUrl)
}

function redisSessionAffinityBindingKey(sessionAffinityKey: string): string {
  return `${redisNamespacedKey('juhe-ai:session-affinity:binding:')}${redisSessionAffinityKeyPart(sessionAffinityKey)}`
}

function redisSessionAffinityAccountIndexKey(accountId: string): string {
  return `${redisNamespacedKey('juhe-ai:session-affinity:index:account:')}${redisSessionAffinityKeyPart(accountId)}`
}

function redisSessionAffinityAccountSystemIndexKey(accountId: string, systemAccountId: string): string {
  return `${redisNamespacedKey('juhe-ai:session-affinity:index:account-system:')}${redisSessionAffinityKeyPart(accountId)}:${redisSessionAffinityKeyPart(systemAccountId)}`
}

function redisSessionAffinityAccountSystemApiKeyIndexKey(accountId: string, systemAccountId: string, apiKeyId: string): string {
  return `${redisNamespacedKey('juhe-ai:session-affinity:index:account-system-api-key:')}${redisSessionAffinityKeyPart(accountId)}:${redisSessionAffinityKeyPart(systemAccountId)}:${redisSessionAffinityKeyPart(apiKeyId)}`
}

function redisTrafficMigrationPreferenceKey(scopeKey: string): string {
  return `${redisNamespacedKey('juhe-ai:traffic-migration-preference:')}${redisSessionAffinityKeyPart(scopeKey)}`
}

function redisSessionAffinityKeyPart(value: string): string {
  return encodeURIComponent(value.trim()) || 'default'
}

function stringArrayRedisResult(value: unknown): string[] {
  return Array.isArray(value)
    ? value.map((item) => String(item ?? '')).filter(Boolean)
    : []
}

function redisBooleanResult(value: unknown): boolean {
  return value === 1 || value === '1' || value === true
}

function addSetValue(map: Map<string, Set<string>>, key: string, value: string): void {
  let values = map.get(key)
  if (!values) {
    values = new Set<string>()
    map.set(key, values)
  }
  values.add(value)
}

function deleteSetValue(map: Map<string, Set<string>>, key: string, value: string): void {
  const values = map.get(key)
  if (!values) {
    return
  }
  values.delete(value)
  if (values.size === 0) {
    map.delete(key)
  }
}

function accountSystemScopeIndexKey(accountId: string, systemAccountId: string): string {
  return `${accountId}:${systemAccountId}`
}

function accountSystemApiKeyScopeIndexKey(accountId: string, systemAccountId: string, apiKeyId: string): string {
  return `${accountId}:${systemAccountId}:${apiKeyId}`
}

function sessionBindingMatchesScope(binding: SessionBinding, scope: Partial<OpenAIGatewaySessionAffinityScope>): boolean {
  if (!binding.scope) {
    return false
  }
  if (scope.systemAccountId && binding.scope.systemAccountId !== scope.systemAccountId) {
    return false
  }
  if (scope.apiKeyId && binding.scope.apiKeyId !== scope.apiKeyId) {
    return false
  }
  return true
}

function trafficMigrationPreferenceForAccounts(
  accounts: OpenAIAccountSecret[],
  scope?: Partial<OpenAIGatewaySessionAffinityScope>
): TrafficMigrationPreference | undefined {
  if (!canUseProcessLocalSessionAffinity()) return undefined
  if (accounts.length < 2) {
    return undefined
  }
  const scopedPreference = trafficMigrationPreferenceForScope(scope)
  if (!scopedPreference) {
    return undefined
  }
  const { key, preference } = scopedPreference
  if (accounts.some((account) => account.id === preference.sourceAccountId)) {
    trafficMigrationPreferenceCache.delete(key)
    return undefined
  }
  return accounts.some((account) => account.id === preference.targetAccountId)
    ? preference
    : undefined
}

async function trafficMigrationPreferenceForAccountsAsync(
  accounts: OpenAIAccountSecret[],
  scope?: Partial<OpenAIGatewaySessionAffinityScope>
): Promise<TrafficMigrationPreference | undefined> {
  if (accounts.length < 2) {
    return undefined
  }
  try {
    const scopedPreference = await trafficMigrationPreferenceForScopeAsync(scope)
    if (!scopedPreference) {
      return undefined
    }
    const { key, preference } = scopedPreference
    if (accounts.some((account) => account.id === preference.sourceAccountId)) {
      await deleteRedisTrafficMigrationPreference(key)
      return undefined
    }
    return accounts.some((account) => account.id === preference.targetAccountId)
      ? preference
      : undefined
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'redis_openai_traffic_migration_preference_read_failed'
    }), 'Redis 流量迁移偏向读取失败，已跳过本次偏向排序')
    return undefined
  }
}

function trafficMigrationPreferenceForScope(
  scope?: Partial<OpenAIGatewaySessionAffinityScope>
): { key: string; preference: TrafficMigrationPreference } | undefined {
  if (!canUseProcessLocalSessionAffinity()) return undefined
  for (const key of trafficMigrationPreferenceScopeKeys(scope)) {
    const preference = trafficMigrationPreferenceCache.get(key)
    if (preference) {
      return { key, preference }
    }
  }
  return undefined
}

async function trafficMigrationPreferenceForScopeAsync(
  scope?: Partial<OpenAIGatewaySessionAffinityScope>
): Promise<{ key: string; preference: TrafficMigrationPreference } | undefined> {
  for (const key of trafficMigrationPreferenceScopeKeys(scope)) {
    const preference = await getRedisTrafficMigrationPreference(key)
    if (preference) {
      return { key, preference }
    }
  }
  return undefined
}

function sessionTrafficMigrationTargetForAccounts(
  accounts: OpenAIAccountSecret[],
  sessionAffinityKey?: string
): string | undefined {
  if (!canUseProcessLocalSessionAffinity()) return undefined
  if (!sessionAffinityKey || accounts.length < 2) {
    return undefined
  }
  const binding = sessionAffinityCache.get(sessionAffinityKey)
  if (!binding?.trafficMigrationPreferred) {
    return undefined
  }
  return sessionTrafficMigrationTargetForAccountsFromBinding(accounts, binding)
}

function sessionTrafficMigrationTargetForAccountsFromBinding(
  accounts: OpenAIAccountSecret[],
  binding?: SessionBinding
): string | undefined {
  if (!binding?.trafficMigrationPreferred) {
    return undefined
  }
  return accounts.some((account) => account.id === binding.accountId)
    ? binding.accountId
    : undefined
}

function orderOpenAIAccountsByTrafficMigrationPreference(
  accounts: OpenAIAccountSecret[],
  targetAccountId?: string,
  modelPriority?: GatewayAccountModelPriority
): OpenAIAccountSecret[] {
  if (!targetAccountId || accounts.length < 2) {
    return accounts
  }
  const originalTargetIndex = accounts.findIndex((account) => account.id === targetAccountId)
  if (originalTargetIndex <= 0) {
    return accounts
  }
  const targetAccount = accounts[originalTargetIndex]
  let targetIndex = originalTargetIndex
  for (let index = originalTargetIndex - 1; index >= 0; index -= 1) {
    if (compareGatewayAccountModelPriority(targetAccount, accounts[index], modelPriority) > 0) {
      break
    }
    targetIndex = index
  }
  if (targetIndex === originalTargetIndex) {
    return accounts
  }
  return [
    ...accounts.slice(0, targetIndex),
    targetAccount,
    ...accounts.slice(targetIndex, originalTargetIndex),
    ...accounts.slice(originalTargetIndex + 1)
  ]
}

function trafficMigrationPreferenceScopeKeys(scope?: Partial<OpenAIGatewaySessionAffinityScope>): string[] {
  const systemAccountId = stringValue(scope?.systemAccountId)
  const groupId = stringValue(scope?.groupId)
  if (!systemAccountId || !groupId) {
    return []
  }
  const apiKeyId = stringValue(scope?.apiKeyId)
  return apiKeyId
    ? [
        `${systemAccountId}:${apiKeyId}:${groupId}`,
        trafficMigrationGroupPreferenceScopeKey(systemAccountId, groupId)
      ]
    : [trafficMigrationGroupPreferenceScopeKey(systemAccountId, groupId)]
}

function trafficMigrationPreferenceScopeKey(scope?: Partial<OpenAIGatewaySessionAffinityScope>): string | undefined {
  const systemAccountId = stringValue(scope?.systemAccountId)
  const groupId = stringValue(scope?.groupId)
  if (!systemAccountId || !groupId) {
    return undefined
  }
  const apiKeyId = stringValue(scope?.apiKeyId)
  return apiKeyId
    ? `${systemAccountId}:${apiKeyId}:${groupId}`
    : trafficMigrationGroupPreferenceScopeKey(systemAccountId, groupId)
}

function trafficMigrationGroupPreferenceScopeKey(systemAccountId: string, groupId: string): string {
  return `${systemAccountId}:*:${groupId}`
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function canUseProcessLocalSessionAffinity(): boolean {
  if (runtimeConfig.cacheDriver !== 'redis') return true
  clearSessionAffinityIndexes()
  return false
}

function shouldUseRedisSessionAffinity(): boolean {
  return runtimeConfig.cacheDriver === 'redis'
}

function canSessionAffinityPromoteOver(
  boundAccount: OpenAIAccountSecret,
  currentAccount: OpenAIAccountSecret,
  modelPriority?: GatewayAccountModelPriority
): boolean {
  const modelPriorityDelta = compareGatewayAccountModelPriority(boundAccount, currentAccount, modelPriority)
  if (modelPriorityDelta > 0) {
    return false
  }
  if (modelPriorityDelta < 0) {
    return true
  }
  if (boundAccount.superPriorityEnabled !== currentAccount.superPriorityEnabled) {
    return false
  }
  if (boundAccount.fallbackEnabled !== currentAccount.fallbackEnabled) {
    return false
  }
  if (boundAccount.priority !== currentAccount.priority) {
    return false
  }
  return accountQualityRank(boundAccount) <= accountQualityRank(currentAccount)
}

function accountCurrentConcurrency(account: OpenAIAccountSecret, runtimeCurrentConcurrency?: number): number {
  return Math.max(0, Math.trunc(runtimeCurrentConcurrency ?? account.currentConcurrency ?? 0))
}

function accountHardConcurrencyLimit(account: OpenAIAccountSecret): number {
  return Number.isFinite(account.concurrencyLimit) ? Math.max(1, Math.trunc(account.concurrencyLimit)) : 1
}

function accountFallbackRank(account: OpenAIAccountSecret): number {
  return account.fallbackEnabled ? 1 : 0
}

function compareAccountQualityRank(left: OpenAIAccountSecret, right: OpenAIAccountSecret): number {
  const leftRank = accountQualityRank(left)
  const rightRank = accountQualityRank(right)
  if (leftRank === rightRank) return 0
  if (!Number.isFinite(leftRank) && !Number.isFinite(rightRank)) return 0
  return leftRank < rightRank ? -1 : 1
}

function accountQualityRank(account: OpenAIAccountSecret): number {
  return typeof account.qualityScore === 'number' ? account.qualityScore : Number.POSITIVE_INFINITY
}

function orderOpenAIAccountsByModelPriority(
  accounts: OpenAIAccountSecret[],
  modelPriority?: GatewayAccountModelPriority
): OpenAIAccountSecret[] {
  if (!modelPriority || accounts.length < 2) {
    return accounts
  }
  return [...accounts].sort((left, right) => compareGatewayAccountModelPriority(left, right, modelPriority))
}

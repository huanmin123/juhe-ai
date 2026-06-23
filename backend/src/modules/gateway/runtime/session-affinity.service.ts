import { createHash } from 'node:crypto'
import type { Request } from 'express'

import { createAppCache } from '../../../shared/cache.js'
import { getAccountCurrentConcurrency, loadAccountInFlightStatsByIds } from '../../../shared/account-concurrency.js'
import { DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY, effectiveImageLaneConcurrencyLimit, effectiveSoftConcurrencyLimit, resolveGroupSchedulingPolicy } from '../../../domain/group-scheduling.js'
import type { GroupSchedulingPolicy, GroupType } from '../../../domain/types.js'
import type { OpenAIAccountSecret } from '../../../storage/repositories.js'
import type { OpenAIGatewayRequestLane } from '../protocols/openai-v1/request-lane.js'
import {
  compareGatewayAccountModelPriority,
  type GatewayAccountModelPriority
} from '../dispatch/model-filter.js'

interface SessionBinding {
  accountId: string
  scope?: OpenAIGatewaySessionAffinityScope
  trafficMigrationPreferred?: boolean
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

export function resolveOpenAIGatewaySessionAffinityKey(req: Request, input: {
  systemAccountId: string
  apiKeyId?: string
  groupId: string
}): string | undefined {
  const session = extractSessionIdentity(req)
  if (!session) {
    return undefined
  }
  return createHash('sha256')
    .update(JSON.stringify({
      systemAccountId: input.systemAccountId,
      apiKeyId: input.apiKeyId ?? 'internal',
      session
    }))
    .digest('hex')
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
    return getAccountCurrentConcurrency(account.id, 'image') >= imageLaneLimit
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
  return [
    ...accounts.slice(0, targetIndex),
    boundAccount,
    ...accounts.slice(targetIndex, boundIndex),
    ...accounts.slice(boundIndex + 1)
  ]
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
  const inFlightStats = loadAccountInFlightStatsByIds(accounts.map((account) => account.id), {
    slowRequestThresholdMs: policy.slowRequestThresholdMs ?? DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.slowRequestThresholdMs,
    firstOutputSlowThresholdMs: policy.firstOutputSlowThresholdMs ?? DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.firstOutputSlowThresholdMs
  })
  const candidates = accounts.map((account, index) => {
    const runtimeStats = inFlightStats.get(account.id)
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
  const available: OpenAIAccountSecret[] = []
  const hardBusy: OpenAIAccountSecret[] = []
  for (const account of accounts) {
    if (accountCurrentConcurrency(account) >= accountHardConcurrencyLimit(account)) {
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
  const previous = sessionAffinityCache.get(sessionAffinityKey)
  setSessionAffinityBinding(sessionAffinityKey, {
    accountId,
    scope,
    ...(previous?.accountId === accountId && previous.trafficMigrationPreferred ? { trafficMigrationPreferred: true } : {})
  })
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
  trafficMigrationPreferenceCache.set(key, {
    sourceAccountId: source,
    targetAccountId: target
  })
}

export function forgetOpenAIAccountForSession(sessionAffinityKey: string | undefined, accountId?: string): void {
  if (!sessionAffinityKey) {
    return
  }
  const binding = sessionAffinityCache.get(sessionAffinityKey)
  if (!binding) {
    return
  }
  if (accountId && binding.accountId !== accountId) {
    return
  }
  sessionAffinityCache.delete(sessionAffinityKey)
}

export function migrateOpenAIAccountSessionAffinity(
  sourceAccountId: string,
  targetAccountId: string,
  scope?: Partial<OpenAIGatewaySessionAffinityScope>,
  options: { preferMigratedSessions?: boolean } = {}
): { migratedSessionCount: number } {
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

function setSessionAffinityBinding(key: string, binding: SessionBinding): void {
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

function trafficMigrationPreferenceForScope(
  scope?: Partial<OpenAIGatewaySessionAffinityScope>
): { key: string; preference: TrafficMigrationPreference } | undefined {
  for (const key of trafficMigrationPreferenceScopeKeys(scope)) {
    const preference = trafficMigrationPreferenceCache.get(key)
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
  if (!sessionAffinityKey || accounts.length < 2) {
    return undefined
  }
  const binding = sessionAffinityCache.get(sessionAffinityKey)
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

function extractSessionIdentity(req: Request): { source: string; value: string } | undefined {
  for (const name of sessionHeaderNames) {
    const value = stringValue(req.header(name))
    if (value) {
      return { source: `header:${name.toLowerCase()}`, value }
    }
  }

  for (const path of sessionBodyPaths) {
    const value = stringValue(valueAtPath(req.body, path))
    if (value) {
      return { source: `body:${path.join('.')}`, value }
    }
  }

  return undefined
}

function valueAtPath(value: unknown, path: string[]): unknown {
  let current = value
  for (const key of path) {
    if (typeof current !== 'object' || current === null) {
      return undefined
    }
    current = (current as Record<string, unknown>)[key]
  }
  return current
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
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
  return Math.max(0, Math.trunc(account.currentConcurrency ?? runtimeCurrentConcurrency ?? 0))
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

const sessionHeaderNames = [
  'session_id',
  'session-id',
  'x-session-id',
  'conversation_id',
  'conversation-id',
  'x-conversation-id',
  'prompt_cache_key',
  'x-prompt-cache-key',
  'previous_response_id',
  'previous-response-id',
  'x-previous-response-id',
  'x-client-request-id'
]

const sessionBodyPaths = [
  ['previous_response_id'],
  ['session_id'],
  ['conversation_id'],
  ['prompt_cache_key'],
  ['metadata', 'session_id'],
  ['metadata', 'conversation_id'],
  ['metadata', 'user_id']
]

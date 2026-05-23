import { createHash } from 'node:crypto'
import type { Request } from 'express'

import { createAppCache } from '../../shared/cache.js'
import { loadAccountInFlightStatsByIds } from '../../shared/account-concurrency.js'
import { DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY, effectiveSoftConcurrencyLimit, resolveGroupSchedulingPolicy } from '../../domain/group-scheduling.js'
import type { GroupSchedulingPolicy, GroupType } from '../../domain/types.js'
import type { OpenAIAccountSecret } from '../../storage/repositories.js'

interface SessionBinding {
  accountId: string
  scope?: OpenAIGatewaySessionAffinityScope
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
  hardBusy: boolean
  softBusy: boolean
}

const sessionAffinityTtlMs = 60 * 60 * 1000

const sessionAffinityCache = createAppCache<string, SessionBinding>({
  name: 'gateway:openai-session-affinity',
  max: 5000,
  ttlMs: sessionAffinityTtlMs,
  updateAgeOnGet: true
})

export interface OpenAIGatewaySessionAffinityScope {
  systemAccountId: string
  apiKeyId?: string
  groupId: string
}

export interface OpenAIAccountDispatchOrderingOptions {
  groupType?: GroupType
  schedulingPolicy?: GroupSchedulingPolicy
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
      groupId: input.groupId,
      session
    }))
    .digest('hex')
}

export function orderOpenAIAccountsBySessionAffinity(
  accounts: OpenAIAccountSecret[],
  sessionAffinityKey?: string,
  options: OpenAIAccountDispatchOrderingOptions = {}
): OpenAIAccountSecret[] {
  if (options.groupType === 'high_concurrency') {
    return orderOpenAIHighConcurrencyAccounts(accounts, sessionAffinityKey, options.schedulingPolicy)
  }
  return orderOpenAIPersonalAccountsBySessionAffinity(accounts, sessionAffinityKey)
}

export function areOpenAIHighConcurrencyAccountsHardBusy(accounts: OpenAIAccountSecret[], options: OpenAIAccountDispatchOrderingOptions = {}): boolean {
  return options.groupType === 'high_concurrency'
    && accounts.length > 0
    && accounts.every((account) => accountCurrentConcurrency(account) >= accountHardConcurrencyLimit(account))
}

function orderOpenAIPersonalAccountsBySessionAffinity(
  accounts: OpenAIAccountSecret[],
  sessionAffinityKey?: string
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
    if (!canSessionAffinityPromoteOver(boundAccount, accounts[index])) {
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
  policyInput: GroupSchedulingPolicy | undefined
): OpenAIAccountSecret[] {
  if (accounts.length < 2) {
    return accounts
  }
  const policy = resolveGroupSchedulingPolicy('high_concurrency', policyInput) ?? DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY
  if (policy.fastFirstEnabled === false) {
    return orderOpenAIPersonalAccountsBySessionAffinity(accounts, sessionAffinityKey)
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
      hardBusy: currentConcurrency >= hardLimit,
      softBusy: policy.breakAffinityOnSoftLimit === false && boundToSession
        ? false
        : currentConcurrency >= softLimit
    }
  })
  const primarySoftAvailable = candidates.some((candidate) => !candidate.account.fallbackEnabled && !candidate.hardBusy && !candidate.softBusy)
  return [...candidates]
    .sort((left, right) => compareHighConcurrencyCandidates(left, right, policy, primarySoftAvailable))
    .map((candidate) => candidate.account)
}

function compareHighConcurrencyCandidates(
  left: HighConcurrencyCandidate,
  right: HighConcurrencyCandidate,
  policy: GroupSchedulingPolicy,
  primarySoftAvailable: boolean
): number {
  if (left.hardBusy !== right.hardBusy) return left.hardBusy ? 1 : -1
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
  sessionAffinityCache.set(sessionAffinityKey, { accountId, scope })
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

export function migrateOpenAIAccountSessionAffinity(sourceAccountId: string, targetAccountId: string, scope?: Partial<OpenAIGatewaySessionAffinityScope>): { migratedSessionCount: number } {
  let migratedSessionCount = 0
  for (const [key, binding] of sessionAffinityCache.entries()) {
    if (binding.accountId !== sourceAccountId) {
      continue
    }
    if (scope && !sessionBindingMatchesScope(binding, scope)) {
      continue
    }
    sessionAffinityCache.set(key, { accountId: targetAccountId, scope: binding.scope })
    migratedSessionCount += 1
  }
  return { migratedSessionCount }
}

function sessionBindingMatchesScope(binding: SessionBinding, scope: Partial<OpenAIGatewaySessionAffinityScope>): boolean {
  if (!binding.scope) {
    return false
  }
  if (scope.systemAccountId && binding.scope.systemAccountId !== scope.systemAccountId) {
    return false
  }
  if (scope.groupId && binding.scope.groupId !== scope.groupId) {
    return false
  }
  if (scope.apiKeyId && binding.scope.apiKeyId !== scope.apiKeyId) {
    return false
  }
  return true
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

function canSessionAffinityPromoteOver(boundAccount: OpenAIAccountSecret, currentAccount: OpenAIAccountSecret): boolean {
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

import { randomUUID, createHmac } from 'node:crypto'

import { runtimeConfig } from '../../../config/runtime.js'
import { logger } from '../../../shared/logger.js'
import { createRuntimeStateStore, type RuntimeStateStore } from '../../../shared/runtime-state-store.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import type { GatewayAccountModelPriority } from '../dispatch/model-filter.js'
import { preserveGatewayAccountDispatchPriorityTiers } from '../runtime/account-dispatch-priority-order.js'
import type { OpenAIGatewayClientStrategyContext } from './strategy.js'

export type CodexTurnFailureEvidence =
  | 'retryable_failure'
  | 'committed_retry_signal'
  | 'incomplete_downstream_abort'

export interface CodexTurnAccountAvoidanceResult {
  accounts: UpstreamAccount[]
  applied: boolean
  thresholdReached: boolean
  failureCount: number
  avoidedAccountIds: string[]
  bypassedAllAvoided: boolean
}

export interface CodexTurnFailureRecordResult {
  stateKey: string
  failureCount: number
  failedAccountIds: string[]
  avoidanceActivatedAccountIds: string[]
  duplicateObservation: boolean
  activation?: {
    accountId: string
    sourceGeneration: number
    sourceFenceId: string
  }
}

interface CodexTurnFailedAccount {
  accountId: string
  failureCount: number
  lastErrorCode?: string
  lastErrorMessage?: string
  lastFailedAtMs: number
  retryableFailureCount?: number
  committedRetrySignalCount?: number
  incompleteDownstreamAbortCount?: number
  incompleteDownstreamAbortWindowStartedAtMs?: number
  lastIncompleteDownstreamAbortAtMs?: number
  avoidanceGeneration?: number
  avoidanceFenceId?: string
  recentObservationIds?: string[]
}

interface CodexTurnRetryState {
  stateKey: string
  failureCount: number
  failedAccounts: Record<string, CodexTurnFailedAccount>
  createdAtMs: number
  updatedAtMs: number
}

interface MemoryCodexTurnRetryEntry {
  value: CodexTurnRetryState
  expiresAt: number
}

const codexTurnRetryTtlMs = 30 * 60_000
const codexTurnAccountAvoidanceFailureThreshold = 2
const codexTurnIncompleteAbortWindowMs = 60_000
const codexTurnRecentObservationLimit = 32
const codexTurnRedisMutationMaxAttempts = 16
const codexTurnRetryMaxEntries = 5000
const codexTurnRetryMemoryEntries = new Map<string, MemoryCodexTurnRetryEntry>()
interface AvoidanceGenerationTombstone {
  generation: number
  expiresAtMs: number
}

const codexTurnAvoidanceGenerations = new Map<string, AvoidanceGenerationTombstone>()
const codexTurnRetryStateStore = createRuntimeStateStore('gateway-codex-turn-retry')
let codexTurnRetryStateStoreForTest: RuntimeStateStore | undefined
const codexTurnRedisMutationTails = new Map<string, Promise<void>>()

export function orderOpenAIAccountsByCodexTurnAvoidance(
  accounts: UpstreamAccount[],
  strategy: OpenAIGatewayClientStrategyContext,
  modelPriority?: GatewayAccountModelPriority
): CodexTurnAccountAvoidanceResult {
  const stateKey = strategy.clientSourceAvoidanceStateKey
  const state = stateKey
    ? combineCodexTurnRetryStates(stateKey, accounts.map((account) => getMemoryCodexTurnRetryState(codexTurnAccountStateKey(stateKey, account.id))))
    : undefined
  return orderOpenAIAccountsByCodexTurnAvoidanceWithState(accounts, strategy, state, modelPriority)
}

export async function orderOpenAIAccountsByCodexTurnAvoidanceAsync(
  accounts: UpstreamAccount[],
  strategy: OpenAIGatewayClientStrategyContext,
  modelPriority?: GatewayAccountModelPriority
): Promise<CodexTurnAccountAvoidanceResult> {
  if (!shouldUseRedisCodexTurnRetryState()) {
    return orderOpenAIAccountsByCodexTurnAvoidance(accounts, strategy, modelPriority)
  }
  const stateKey = strategy.clientSourceAvoidanceStateKey
  if (stateKey) {
    await Promise.all(accounts.map(async (account) => await (codexTurnRedisMutationTails.get(codexTurnAccountStateKey(stateKey, account.id)) ?? Promise.resolve()).catch(() => undefined)))
  }
  const state = stateKey
    ? combineCodexTurnRetryStates(stateKey, await Promise.all(accounts.map(async (account) => await getRedisCodexTurnRetryState(stateKey, account.id))))
    : undefined
  return orderOpenAIAccountsByCodexTurnAvoidanceWithState(accounts, strategy, state, modelPriority)
}

function orderOpenAIAccountsByCodexTurnAvoidanceWithState(
  accounts: UpstreamAccount[],
  strategy: OpenAIGatewayClientStrategyContext,
  state: CodexTurnRetryState | undefined,
  modelPriority?: GatewayAccountModelPriority
): CodexTurnAccountAvoidanceResult {
  if (!strategy.allowClientSourceAccountAvoidance || !state) {
    return {
      accounts,
      applied: false,
      thresholdReached: false,
      failureCount: state?.failureCount ?? 0,
      avoidedAccountIds: [],
      bypassedAllAvoided: false
    }
  }

  const activatedAccountIds = new Set(
    Object.values(state.failedAccounts)
      .filter((accountState) => codexTurnAccountAvoidanceActivated(accountState))
      .map((accountState) => accountState.accountId)
  )
  if (activatedAccountIds.size === 0) {
    return {
      accounts,
      applied: false,
      thresholdReached: false,
      failureCount: state.failureCount,
      avoidedAccountIds: [],
      bypassedAllAvoided: false
    }
  }
  const freshAccounts = accounts.filter((account) => !activatedAccountIds.has(account.id))
  const failedAccounts = accounts.filter((account) => activatedAccountIds.has(account.id))
  if (freshAccounts.length === 0 || failedAccounts.length === 0) {
    return {
      accounts,
      applied: false,
      thresholdReached: true,
      failureCount: state.failureCount,
      avoidedAccountIds: accounts.filter((account) => activatedAccountIds.has(account.id)).map((account) => account.id),
      bypassedAllAvoided: freshAccounts.length === 0 && accounts.length > 0
    }
  }

  const reorderedAccounts = preserveGatewayAccountDispatchPriorityTiers(
    accounts,
    [
      ...accounts.filter((account) => !activatedAccountIds.has(account.id)),
      ...accounts.filter((account) => activatedAccountIds.has(account.id))
    ],
    { modelRankByAccountId: modelPriority?.rankByAccountId }
  )
  return {
    accounts: reorderedAccounts,
    applied: accounts.some((account, index) => account.id !== reorderedAccounts[index]?.id),
    thresholdReached: true,
    failureCount: state.failureCount,
    avoidedAccountIds: failedAccounts.map((account) => account.id),
    bypassedAllAvoided: false
  }
}

export function rememberCodexTurnStreamFailure(
  strategy: OpenAIGatewayClientStrategyContext,
  accountId: string,
  input: {
    errorCode?: string
    message?: string
    evidence?: CodexTurnFailureEvidence
    observationId?: string
  } = {}
): CodexTurnFailureRecordResult | undefined {
  const stateKey = strategy.clientSourceAvoidanceStateKey
  if (!strategy.allowClientSourceAccountAvoidance || !stateKey) {
    return undefined
  }
  const now = Date.now()
  const accountStateKey = codexTurnAccountStateKey(stateKey, accountId)
  const current = getMemoryCodexTurnRetryState(accountStateKey) ?? {
    stateKey,
    failureCount: 0,
    failedAccounts: {},
    createdAtMs: now,
    updatedAtMs: now
  }
  const mutation = mutateCodexTurnRetryState(current, accountId, input, now)
  if (mutation.duplicateObservation) {
    return codexTurnFailureRecordResult(current, true)
  }
  applyCodexTurnAvoidanceGeneration(mutation, accountStateKey)
  setMemoryCodexTurnRetryState(accountStateKey, mutation.state)
  return codexTurnFailureRecordResult(mutation.state, mutation.duplicateObservation, mutation.activation)
}

export async function rememberCodexTurnStreamFailureAsync(
  strategy: OpenAIGatewayClientStrategyContext,
  accountId: string,
  input: {
    errorCode?: string
    message?: string
    evidence?: CodexTurnFailureEvidence
    observationId?: string
  } = {}
): Promise<CodexTurnFailureRecordResult | undefined> {
  if (!shouldUseRedisCodexTurnRetryState()) {
    return rememberCodexTurnStreamFailure(strategy, accountId, input)
  }
  const stateKey = strategy.clientSourceAvoidanceStateKey
  if (!strategy.allowClientSourceAccountAvoidance || !stateKey) {
    return undefined
  }
  const accountStateKey = codexTurnAccountStateKey(stateKey, accountId)
  return serializeCodexTurnRedisMutation(accountStateKey, async () => {
    for (let attempt = 0; attempt < codexTurnRedisMutationMaxAttempts; attempt += 1) {
      const current = await getRedisCodexTurnRetryState(stateKey, accountId)
      const now = Date.now()
      const base = current ?? {
        stateKey,
        failureCount: 0,
        failedAccounts: {},
        createdAtMs: now,
        updatedAtMs: now
      }
      const mutation = mutateCodexTurnRetryState(base, accountId, input, now)
      if (mutation.duplicateObservation) {
        return codexTurnFailureRecordResult(base, true)
      }
      if (mutation.activation) {
        const generation = await currentCodexTurnRetryStateStore().incr(redisCodexTurnAvoidanceGenerationKey(stateKey, accountId), {
          ttlMs: codexTurnRetryTtlMs
        })
        applyCodexTurnAvoidanceGeneration(mutation, accountStateKey, generation)
      }
      if (await currentCodexTurnRetryStateStore().compareSetJson(
        redisCodexTurnRetryStateKey(stateKey, accountId),
        current,
        mutation.state,
        codexTurnRetryTtlMs
      )) {
        return codexTurnFailureRecordResult(mutation.state, mutation.duplicateObservation, mutation.activation)
      }
      await codexTurnRedisMutationBackoff()
    }
    logger.warn({
      event: 'gateway_codex_turn_retry_state_cas_exhausted',
      stateKey,
      accountId,
      evidence: input.evidence ?? 'retryable_failure'
    }, 'Codex turn 失败状态并发合并耗尽，按 fail-open 继续')
    return undefined
  })
}

/**
 * A probe success may clear only the exact source/turn/account avoidance it
 * verified. It intentionally does not touch account availability or circuit
 * state, and cannot clear a different source because the state key is scoped.
 */
export function clearCodexTurnAccountAvoidance(
  strategy: OpenAIGatewayClientStrategyContext,
  accountId: string
): boolean {
  const stateKey = strategy.clientSourceAvoidanceStateKey
  const normalizedAccountId = accountId.trim()
  if (!strategy.allowClientSourceAccountAvoidance || !stateKey || !normalizedAccountId) return false
  const accountStateKey = codexTurnAccountStateKey(stateKey, normalizedAccountId)
  const current = getMemoryCodexTurnRetryState(accountStateKey)
  if (!current?.failedAccounts[normalizedAccountId]) return false
  const failedAccounts = { ...current.failedAccounts }
  delete failedAccounts[normalizedAccountId]
  const next = { ...current, failedAccounts, updatedAtMs: Date.now() }
  if (Object.keys(failedAccounts).length === 0) {
    codexTurnRetryMemoryEntries.delete(accountStateKey)
  } else {
    setMemoryCodexTurnRetryState(accountStateKey, next)
  }
  return true
}

export async function clearCodexTurnAccountAvoidanceAsync(
  strategy: OpenAIGatewayClientStrategyContext,
  accountId: string
): Promise<boolean> {
  if (!shouldUseRedisCodexTurnRetryState()) return clearCodexTurnAccountAvoidance(strategy, accountId)
  const stateKey = strategy.clientSourceAvoidanceStateKey
  const normalizedAccountId = accountId.trim()
  if (!strategy.allowClientSourceAccountAvoidance || !stateKey || !normalizedAccountId) return false
  const accountStateKey = codexTurnAccountStateKey(stateKey, normalizedAccountId)
  return await serializeCodexTurnRedisMutation(accountStateKey, async () => {
    for (let attempt = 0; attempt < codexTurnRedisMutationMaxAttempts; attempt += 1) {
      const current = await getRedisCodexTurnRetryState(stateKey, normalizedAccountId)
      if (!current?.failedAccounts[normalizedAccountId]) return false
      const failedAccounts = { ...current.failedAccounts }
      delete failedAccounts[normalizedAccountId]
      const next = { ...current, failedAccounts, updatedAtMs: Date.now() }
      if (await currentCodexTurnRetryStateStore().compareSetJson(
        redisCodexTurnRetryStateKey(stateKey, normalizedAccountId),
        current,
        next,
        codexTurnRetryTtlMs
      )) return true
      await codexTurnRedisMutationBackoff()
    }
    logger.warn({
      event: 'gateway_codex_turn_retry_state_clear_cas_exhausted',
      stateKey,
      accountId: normalizedAccountId
    }, 'Codex turn 精确避让清理并发合并耗尽，保留短期避让')
    return false
  })
}

export async function clearCodexTurnAccountAvoidanceByFenceAsync(input: {
  stateKey: string
  accountId: string
  sourceGeneration: number
  sourceFenceId: string
}): Promise<boolean> {
  const normalizedAccountId = input.accountId.trim()
  if (!input.stateKey || !normalizedAccountId || !Number.isFinite(input.sourceGeneration) || !isSourceFenceId(input.sourceFenceId)) return false
  const accountStateKey = codexTurnAccountStateKey(input.stateKey, normalizedAccountId)
  if (!shouldUseRedisCodexTurnRetryState()) {
    const current = getMemoryCodexTurnRetryState(accountStateKey)
    if (current?.failedAccounts[normalizedAccountId]?.avoidanceGeneration !== input.sourceGeneration
      || current.failedAccounts[normalizedAccountId]?.avoidanceFenceId !== input.sourceFenceId) return false
    return clearCodexTurnAccountAvoidance({
      allowClientSourceAccountAvoidance: true,
      clientSourceAvoidanceStateKey: input.stateKey
    } as OpenAIGatewayClientStrategyContext, normalizedAccountId)
  }
  return await serializeCodexTurnRedisMutation(accountStateKey, async () => {
    for (let attempt = 0; attempt < codexTurnRedisMutationMaxAttempts; attempt += 1) {
      const current = await getRedisCodexTurnRetryState(input.stateKey, normalizedAccountId)
      if (current?.failedAccounts[normalizedAccountId]?.avoidanceGeneration !== input.sourceGeneration
        || current.failedAccounts[normalizedAccountId]?.avoidanceFenceId !== input.sourceFenceId) return false
      const failedAccounts = { ...current.failedAccounts }
      delete failedAccounts[normalizedAccountId]
      const next = { ...current, failedAccounts, updatedAtMs: Date.now() }
      if (await currentCodexTurnRetryStateStore().compareSetJson(
        redisCodexTurnRetryStateKey(input.stateKey, normalizedAccountId), current, next, codexTurnRetryTtlMs
      )) return true
      await codexTurnRedisMutationBackoff()
    }
    return false
  })
}

export function getCodexTurnRetryStateForTest(stateKey: string): {
  failureCount: number
  failedAccountIds: string[]
} | undefined {
  const state = combineCodexTurnRetryStates(stateKey, [...codexTurnRetryMemoryEntries.values()]
    .map((entry) => entry.value)
    .filter((entry) => entry.stateKey === stateKey))
  return state
    ? {
      failureCount: state.failureCount,
      failedAccountIds: Object.keys(state.failedAccounts)
    }
    : undefined
}

export function clearCodexTurnRetryStateForTest(): void {
  codexTurnRetryMemoryEntries.clear()
  codexTurnAvoidanceGenerations.clear()
  codexTurnRedisMutationTails.clear()
}

export function getCodexTurnRetryMemoryStatsForTest(): {
  stateEntries: number
  generationTombstones: number
} {
  pruneCodexTurnAvoidanceGenerationTombstones(Date.now())
  return {
    stateEntries: codexTurnRetryMemoryEntries.size,
    generationTombstones: codexTurnAvoidanceGenerations.size
  }
}

export function setCodexTurnRetryStateStoreForTest(store: RuntimeStateStore | undefined): void {
  codexTurnRetryStateStoreForTest = store
  codexTurnRedisMutationTails.clear()
}

function getMemoryCodexTurnRetryState(stateKey: string): CodexTurnRetryState | undefined {
  const entry = codexTurnRetryMemoryEntries.get(stateKey)
  if (!entry) {
    return undefined
  }
  if (entry.expiresAt <= Date.now()) {
    codexTurnRetryMemoryEntries.delete(stateKey)
    return undefined
  }
  return entry.value
}

function setMemoryCodexTurnRetryState(stateKey: string, state: CodexTurnRetryState): void {
  codexTurnRetryMemoryEntries.delete(stateKey)
  codexTurnRetryMemoryEntries.set(stateKey, {
    value: state,
    expiresAt: Date.now() + codexTurnRetryTtlMs
  })
  while (codexTurnRetryMemoryEntries.size > codexTurnRetryMaxEntries) {
    const oldestKey = codexTurnRetryMemoryEntries.keys().next().value
    if (typeof oldestKey !== 'string') {
      return
    }
    codexTurnRetryMemoryEntries.delete(oldestKey)
  }
}

function shouldUseRedisCodexTurnRetryState(): boolean {
  return runtimeConfig.runtimeStateDriver === 'redis'
}

function redisCodexTurnRetryStateKey(stateKey: string, accountId: string): string {
  return `state:${codexTurnAccountStateKey(stateKey, accountId)}`
}

function codexTurnAccountStateKey(stateKey: string, accountId: string): string {
  const normalizedAccountId = accountId.trim()
  if (!normalizedAccountId) throw new Error('Codex turn retry state requires an account id')
  const digest = createHmac('sha256', runtimeConfig.secret)
    .update('juhe-ai:codex-turn-account-state:v1\n', 'utf8')
    .update(stateKey, 'utf8')
    .update('\n', 'utf8')
    .update(normalizedAccountId, 'utf8')
    .digest('base64url')
  return `${stateKey}:a_${digest}`
}

function combineCodexTurnRetryStates(
  stateKey: string,
  states: Array<CodexTurnRetryState | undefined>
): CodexTurnRetryState | undefined {
  const present = states.filter((state): state is CodexTurnRetryState => Boolean(state))
  if (present.length === 0) return undefined
  return {
    stateKey,
    failureCount: present.reduce((total, state) => total + state.failureCount, 0),
    failedAccounts: Object.assign({}, ...present.map((state) => state.failedAccounts)),
    createdAtMs: Math.min(...present.map((state) => state.createdAtMs)),
    updatedAtMs: Math.max(...present.map((state) => state.updatedAtMs))
  }
}

function applyCodexTurnAvoidanceGeneration(
  mutation: { state: CodexTurnRetryState; activation?: { accountId: string; sourceGeneration: number; sourceFenceId: string } },
  accountStateKey: string,
  explicitGeneration?: number
): void {
  if (!mutation.activation) return
  const nowMs = Date.now()
  pruneCodexTurnAvoidanceGenerationTombstones(nowMs)
  const current = codexTurnAvoidanceGenerations.get(accountStateKey)
  const previousGeneration = current && current.expiresAtMs > nowMs ? current.generation : 0
  const generation = explicitGeneration ?? (previousGeneration + 1)
  codexTurnAvoidanceGenerations.delete(accountStateKey)
  codexTurnAvoidanceGenerations.set(accountStateKey, {
    generation: Math.max(previousGeneration, generation),
    expiresAtMs: nowMs + codexTurnRetryTtlMs
  })
  while (codexTurnAvoidanceGenerations.size > codexTurnRetryMaxEntries) {
    const oldestKey = codexTurnAvoidanceGenerations.keys().next().value
    if (typeof oldestKey !== 'string') break
    codexTurnAvoidanceGenerations.delete(oldestKey)
  }
  mutation.state.failedAccounts[mutation.activation.accountId]!.avoidanceGeneration = generation
  mutation.state.failedAccounts[mutation.activation.accountId]!.avoidanceFenceId = mutation.activation.sourceFenceId
  mutation.activation.sourceGeneration = generation
}

function pruneCodexTurnAvoidanceGenerationTombstones(nowMs: number): void {
  for (const [accountStateKey, entry] of codexTurnAvoidanceGenerations) {
    if (entry.expiresAtMs <= nowMs) codexTurnAvoidanceGenerations.delete(accountStateKey)
  }
}

function redisCodexTurnAvoidanceGenerationKey(stateKey: string, accountId: string): string {
  return `generation:${codexTurnAccountStateKey(stateKey, accountId)}`
}

async function getRedisCodexTurnRetryState(stateKey: string, accountId: string): Promise<CodexTurnRetryState | undefined> {
  return currentCodexTurnRetryStateStore().getJson<CodexTurnRetryState>(redisCodexTurnRetryStateKey(stateKey, accountId))
}

function currentCodexTurnRetryStateStore(): RuntimeStateStore {
  return codexTurnRetryStateStoreForTest ?? codexTurnRetryStateStore
}

function mutateCodexTurnRetryState(
  current: CodexTurnRetryState,
  accountId: string,
  input: {
    errorCode?: string
    message?: string
    evidence?: CodexTurnFailureEvidence
    observationId?: string
  },
  now: number
): { state: CodexTurnRetryState; duplicateObservation: boolean; activation?: { accountId: string; sourceGeneration: number; sourceFenceId: string } } {
  const previousAccountState = current.failedAccounts[accountId]
  const accountState: CodexTurnFailedAccount = previousAccountState
    ? {
        ...previousAccountState,
        recentObservationIds: [...(previousAccountState.recentObservationIds ?? [])]
      }
    : {
        accountId,
        failureCount: 0,
        lastFailedAtMs: now,
        recentObservationIds: []
      }
  const observationId = input.observationId?.trim()
  if (observationId && accountState.recentObservationIds?.includes(observationId)) {
    return { state: current, duplicateObservation: true }
  }

  const wasActivated = codexTurnAccountAvoidanceActivated(accountState)

  const evidence = input.evidence ?? 'retryable_failure'
  accountState.failureCount += 1
  if (evidence === 'committed_retry_signal') {
    accountState.committedRetrySignalCount = (accountState.committedRetrySignalCount ?? 0) + 1
  } else if (evidence === 'incomplete_downstream_abort') {
    const windowExpired = accountState.lastIncompleteDownstreamAbortAtMs === undefined
      || now - accountState.lastIncompleteDownstreamAbortAtMs > codexTurnIncompleteAbortWindowMs
    accountState.incompleteDownstreamAbortCount = windowExpired
      ? 1
      : (accountState.incompleteDownstreamAbortCount ?? 0) + 1
    accountState.incompleteDownstreamAbortWindowStartedAtMs = windowExpired
      ? now
      : accountState.incompleteDownstreamAbortWindowStartedAtMs ?? now
    accountState.lastIncompleteDownstreamAbortAtMs = now
  } else {
    accountState.retryableFailureCount = (accountState.retryableFailureCount ?? 0) + 1
  }
  accountState.lastErrorCode = input.errorCode
  accountState.lastErrorMessage = input.message
  accountState.lastFailedAtMs = now
  if (observationId) {
    accountState.recentObservationIds = [
      ...(accountState.recentObservationIds ?? []),
      observationId
    ].slice(-codexTurnRecentObservationLimit)
  }
  const state: CodexTurnRetryState = {
    ...current,
    failureCount: current.failureCount + 1,
    failedAccounts: {
      ...current.failedAccounts,
      [accountId]: accountState
    },
    updatedAtMs: now
  }
  if (!wasActivated && codexTurnAccountAvoidanceActivated(accountState)) {
    state.failedAccounts[accountId]!.avoidanceGeneration = accountState.failureCount
  }
  return {
    state,
    duplicateObservation: false,
    ...(wasActivated || !codexTurnAccountAvoidanceActivated(accountState) ? {} : {
      activation: { accountId, sourceGeneration: accountState.failureCount, sourceFenceId: randomUUID() }
    })
  }
}

function codexTurnAccountAvoidanceActivated(accountState: CodexTurnFailedAccount): boolean {
  return codexTurnStrongAvoidanceActivated(accountState) || codexTurnWeakAvoidanceActivated(accountState)
}

function codexTurnStrongAvoidanceActivated(accountState: CodexTurnFailedAccount): boolean {
  return (accountState.committedRetrySignalCount ?? 0) > 0
    || (accountState.retryableFailureCount ?? 0) >= codexTurnAccountAvoidanceFailureThreshold
}

function codexTurnWeakAvoidanceActivated(accountState: CodexTurnFailedAccount): boolean {
  return accountState.lastIncompleteDownstreamAbortAtMs !== undefined
    && Date.now() - accountState.lastIncompleteDownstreamAbortAtMs <= codexTurnIncompleteAbortWindowMs
    && (accountState.incompleteDownstreamAbortCount ?? 0) >= codexTurnAccountAvoidanceFailureThreshold
}

function codexTurnFailureRecordResult(
  state: CodexTurnRetryState,
  duplicateObservation: boolean,
  activation?: { accountId: string; sourceGeneration: number; sourceFenceId: string }
): CodexTurnFailureRecordResult {
  return {
    stateKey: state.stateKey,
    failureCount: state.failureCount,
    failedAccountIds: Object.keys(state.failedAccounts),
    avoidanceActivatedAccountIds: Object.values(state.failedAccounts)
      .filter((accountState) => codexTurnAccountAvoidanceActivated(accountState))
      .map((accountState) => accountState.accountId),
    duplicateObservation,
    ...(activation ? { activation } : {})
  }
}

async function serializeCodexTurnRedisMutation<T>(stateKey: string, operation: () => Promise<T>): Promise<T> {
  const previous = codexTurnRedisMutationTails.get(stateKey) ?? Promise.resolve()
  const current = previous.catch(() => undefined).then(operation)
  const tail = current.then(() => undefined, () => undefined)
  codexTurnRedisMutationTails.set(stateKey, tail)
  try {
    return await current
  } finally {
    if (codexTurnRedisMutationTails.get(stateKey) === tail) {
      codexTurnRedisMutationTails.delete(stateKey)
    }
  }
}

async function codexTurnRedisMutationBackoff(): Promise<void> {
  await new Promise<void>((resolve) => setTimeout(resolve, Math.floor(Math.random() * 9)))
}
function isSourceFenceId(value: unknown): value is string {
  return typeof value === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value)
}

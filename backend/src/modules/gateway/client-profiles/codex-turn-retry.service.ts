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
const codexTurnRetryStateStore = createRuntimeStateStore('gateway-codex-turn-retry')
let codexTurnRetryStateStoreForTest: RuntimeStateStore | undefined
const codexTurnRedisMutationTails = new Map<string, Promise<void>>()

export function orderOpenAIAccountsByCodexTurnAvoidance(
  accounts: UpstreamAccount[],
  strategy: OpenAIGatewayClientStrategyContext,
  modelPriority?: GatewayAccountModelPriority
): CodexTurnAccountAvoidanceResult {
  const state = strategy.codexTurn?.stateKey ? getMemoryCodexTurnRetryState(strategy.codexTurn.stateKey) : undefined
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
  const stateKey = strategy.codexTurn?.stateKey
  if (stateKey) {
    await (codexTurnRedisMutationTails.get(stateKey) ?? Promise.resolve()).catch(() => undefined)
  }
  const state = stateKey ? await getRedisCodexTurnRetryState(stateKey) : undefined
  return orderOpenAIAccountsByCodexTurnAvoidanceWithState(accounts, strategy, state, modelPriority)
}

function orderOpenAIAccountsByCodexTurnAvoidanceWithState(
  accounts: UpstreamAccount[],
  strategy: OpenAIGatewayClientStrategyContext,
  state: CodexTurnRetryState | undefined,
  modelPriority?: GatewayAccountModelPriority
): CodexTurnAccountAvoidanceResult {
  if (!strategy.allowCodexTurnAccountAvoidance || !state) {
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

  const strongAccountIds = new Set(
    Object.values(state.failedAccounts)
      .filter((accountState) => codexTurnStrongAvoidanceActivated(accountState))
      .map((accountState) => accountState.accountId)
  )
  const afterStrongAvoidance = strongAccountIds.size > 0
    ? [
        ...accounts.filter((account) => !strongAccountIds.has(account.id)),
        ...accounts.filter((account) => strongAccountIds.has(account.id))
      ]
    : accounts
  const weakAccountIds = new Set(
    Object.values(state.failedAccounts)
      .filter((accountState) => !codexTurnStrongAvoidanceActivated(accountState) && codexTurnWeakAvoidanceActivated(accountState))
      .map((accountState) => accountState.accountId)
  )
  const reorderedAccounts = weakAccountIds.size > 0
    ? preserveGatewayAccountDispatchPriorityTiers(
        afterStrongAvoidance,
        [
          ...afterStrongAvoidance.filter((account) => !weakAccountIds.has(account.id)),
          ...afterStrongAvoidance.filter((account) => weakAccountIds.has(account.id))
        ],
        { modelRankByAccountId: modelPriority?.rankByAccountId }
      )
    : afterStrongAvoidance
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
  const stateKey = strategy.codexTurn?.stateKey
  if (!strategy.allowCodexTurnAccountAvoidance || !stateKey) {
    return undefined
  }
  const now = Date.now()
  const current = getMemoryCodexTurnRetryState(stateKey) ?? {
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
  setMemoryCodexTurnRetryState(stateKey, mutation.state)
  return codexTurnFailureRecordResult(mutation.state, mutation.duplicateObservation)
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
  const stateKey = strategy.codexTurn?.stateKey
  if (!strategy.allowCodexTurnAccountAvoidance || !stateKey) {
    return undefined
  }
  return serializeCodexTurnRedisMutation(stateKey, async () => {
    for (let attempt = 0; attempt < codexTurnRedisMutationMaxAttempts; attempt += 1) {
      const current = await getRedisCodexTurnRetryState(stateKey)
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
      if (await currentCodexTurnRetryStateStore().compareSetJson(
        redisCodexTurnRetryStateKey(stateKey),
        current,
        mutation.state,
        codexTurnRetryTtlMs
      )) {
        return codexTurnFailureRecordResult(mutation.state, mutation.duplicateObservation)
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

export function getCodexTurnRetryStateForTest(stateKey: string): {
  failureCount: number
  failedAccountIds: string[]
} | undefined {
  const state = getMemoryCodexTurnRetryState(stateKey)
  return state
    ? {
      failureCount: state.failureCount,
      failedAccountIds: Object.keys(state.failedAccounts)
    }
    : undefined
}

export function clearCodexTurnRetryStateForTest(): void {
  codexTurnRetryMemoryEntries.clear()
  codexTurnRedisMutationTails.clear()
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

function redisCodexTurnRetryStateKey(stateKey: string): string {
  return `state:${stateKey}`
}

async function getRedisCodexTurnRetryState(stateKey: string): Promise<CodexTurnRetryState | undefined> {
  return currentCodexTurnRetryStateStore().getJson<CodexTurnRetryState>(redisCodexTurnRetryStateKey(stateKey))
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
): { state: CodexTurnRetryState; duplicateObservation: boolean } {
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
  return { state, duplicateObservation: false }
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
  duplicateObservation: boolean
): CodexTurnFailureRecordResult {
  return {
    stateKey: state.stateKey,
    failureCount: state.failureCount,
    failedAccountIds: Object.keys(state.failedAccounts),
    avoidanceActivatedAccountIds: Object.values(state.failedAccounts)
      .filter((accountState) => codexTurnAccountAvoidanceActivated(accountState))
      .map((accountState) => accountState.accountId),
    duplicateObservation
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

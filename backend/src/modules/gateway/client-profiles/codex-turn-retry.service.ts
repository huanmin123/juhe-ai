import { runtimeConfig } from '../../../config/runtime.js'
import { createRuntimeStateStore } from '../../../shared/runtime-state-store.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import { preserveGatewayAccountDispatchPriorityTiers } from '../runtime/account-dispatch-priority-order.js'
import type { GatewayAccountModelPriority } from '../dispatch/model-filter.js'
import type { OpenAIGatewayClientStrategyContext } from './strategy.js'

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
}

interface CodexTurnFailedAccount {
  accountId: string
  failureCount: number
  lastErrorCode?: string
  lastErrorMessage?: string
  lastFailedAtMs: number
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
const codexTurnRetryMaxEntries = 5000
const codexTurnRetryMemoryEntries = new Map<string, MemoryCodexTurnRetryEntry>()
const codexTurnRetryStateStore = createRuntimeStateStore('gateway-codex-turn-retry')

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
  const state = strategy.codexTurn?.stateKey ? await getRedisCodexTurnRetryState(strategy.codexTurn.stateKey) : undefined
  return orderOpenAIAccountsByCodexTurnAvoidanceWithState(accounts, strategy, state, modelPriority)
}

function orderOpenAIAccountsByCodexTurnAvoidanceWithState(
  accounts: UpstreamAccount[],
  strategy: OpenAIGatewayClientStrategyContext,
  state: CodexTurnRetryState | undefined,
  modelPriority?: GatewayAccountModelPriority
): CodexTurnAccountAvoidanceResult {
  if (!strategy.allowCodexTurnAccountAvoidance || !state || state.failureCount < codexTurnAccountAvoidanceFailureThreshold) {
    return {
      accounts,
      applied: false,
      thresholdReached: false,
      failureCount: state?.failureCount ?? 0,
      avoidedAccountIds: [],
      bypassedAllAvoided: false
    }
  }

  const failedAccountIds = new Set(Object.keys(state.failedAccounts))
  const freshAccounts = accounts.filter((account) => !failedAccountIds.has(account.id))
  const failedAccounts = accounts.filter((account) => failedAccountIds.has(account.id))
  if (freshAccounts.length === 0 || failedAccounts.length === 0) {
    return {
      accounts,
      applied: false,
      thresholdReached: true,
      failureCount: state.failureCount,
      avoidedAccountIds: accounts.filter((account) => failedAccountIds.has(account.id)).map((account) => account.id),
      bypassedAllAvoided: freshAccounts.length === 0 && accounts.length > 0
    }
  }

  return {
    accounts: preserveGatewayAccountDispatchPriorityTiers(accounts, [...freshAccounts, ...failedAccounts], {
      modelRankByAccountId: modelPriority?.rankByAccountId
    }),
    applied: true,
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
  const accountState = current.failedAccounts[accountId] ?? {
    accountId,
    failureCount: 0,
    lastFailedAtMs: now
  }
  accountState.failureCount += 1
  accountState.lastErrorCode = input.errorCode
  accountState.lastErrorMessage = input.message
  accountState.lastFailedAtMs = now
  current.failureCount += 1
  current.updatedAtMs = now
  current.failedAccounts[accountId] = accountState
  setMemoryCodexTurnRetryState(stateKey, current)
  return {
    stateKey,
    failureCount: current.failureCount,
    failedAccountIds: Object.keys(current.failedAccounts)
  }
}

export async function rememberCodexTurnStreamFailureAsync(
  strategy: OpenAIGatewayClientStrategyContext,
  accountId: string,
  input: {
    errorCode?: string
    message?: string
  } = {}
): Promise<CodexTurnFailureRecordResult | undefined> {
  if (!shouldUseRedisCodexTurnRetryState()) {
    return rememberCodexTurnStreamFailure(strategy, accountId, input)
  }
  const stateKey = strategy.codexTurn?.stateKey
  if (!strategy.allowCodexTurnAccountAvoidance || !stateKey) {
    return undefined
  }
  const now = Date.now()
  const current = await getRedisCodexTurnRetryState(stateKey) ?? {
    stateKey,
    failureCount: 0,
    failedAccounts: {},
    createdAtMs: now,
    updatedAtMs: now
  }
  const accountState = current.failedAccounts[accountId] ?? {
    accountId,
    failureCount: 0,
    lastFailedAtMs: now
  }
  accountState.failureCount += 1
  accountState.lastErrorCode = input.errorCode
  accountState.lastErrorMessage = input.message
  accountState.lastFailedAtMs = now
  current.failureCount += 1
  current.updatedAtMs = now
  current.failedAccounts[accountId] = accountState
  await setRedisCodexTurnRetryState(stateKey, current)
  return {
    stateKey,
    failureCount: current.failureCount,
    failedAccountIds: Object.keys(current.failedAccounts)
  }
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
  entry.expiresAt = Date.now() + codexTurnRetryTtlMs
  return entry.value
}

function setMemoryCodexTurnRetryState(stateKey: string, state: CodexTurnRetryState): void {
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
  return codexTurnRetryStateStore.getJson<CodexTurnRetryState>(redisCodexTurnRetryStateKey(stateKey))
}

async function setRedisCodexTurnRetryState(stateKey: string, state: CodexTurnRetryState): Promise<void> {
  await codexTurnRetryStateStore.setJson(redisCodexTurnRetryStateKey(stateKey), state, codexTurnRetryTtlMs)
}

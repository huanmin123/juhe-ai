import { createAppCache } from '../../../shared/cache.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
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

const codexTurnRetryTtlMs = 30 * 60_000
const codexTurnAccountAvoidanceFailureThreshold = 2

const codexTurnRetryCache = createAppCache<string, CodexTurnRetryState>({
  name: 'gateway:codex-turn-retry',
  max: 5000,
  ttlMs: codexTurnRetryTtlMs,
  updateAgeOnGet: true
})

export function orderOpenAIAccountsByCodexTurnAvoidance(
  accounts: UpstreamAccount[],
  strategy: OpenAIGatewayClientStrategyContext
): CodexTurnAccountAvoidanceResult {
  const state = strategy.codexTurn?.stateKey ? codexTurnRetryCache.get(strategy.codexTurn.stateKey) : undefined
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
    accounts: [...freshAccounts, ...failedAccounts],
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
  const current = codexTurnRetryCache.get(stateKey) ?? {
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
  codexTurnRetryCache.set(stateKey, current)
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
  const state = codexTurnRetryCache.get(stateKey)
  return state
    ? {
      failureCount: state.failureCount,
      failedAccountIds: Object.keys(state.failedAccounts)
    }
    : undefined
}

export function clearCodexTurnRetryStateForTest(): void {
  codexTurnRetryCache.clear()
}

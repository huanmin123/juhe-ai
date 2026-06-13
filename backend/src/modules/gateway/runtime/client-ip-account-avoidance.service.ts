import { createAppCache } from '../../../shared/cache.js'
import type { GatewaySettings } from '../policy/account-error-policy.service.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'

export interface ClientIpAccountAvoidanceScopeInput {
  systemAccountId: string
  groupId: string
  apiKeyId?: string
  clientIp?: string
}

export interface ClientIpAccountAvoidanceTracker {
  scope?: ClientIpAccountAvoidanceScope
  pendingFailures: ClientIpAccountFailure[]
  pendingFailureIndexByAccountId: Map<string, number>
}

export interface ClientIpAccountAvoidanceOrderResult {
  accounts: UpstreamAccount[]
  applied: boolean
  avoidedAccountIds: string[]
  bypassedAllAvoided: boolean
}

export interface ClientIpAccountFailure {
  accountId: string
  accountName?: string
  statusCode?: number
  errorCode?: string
  errorType?: string
  errorPhase: 'upstream_response' | 'upstream_request' | 'stream'
  errorMessage?: string
  endpoint?: string
}

export interface ClientIpAccountAvoidanceConfirmResult {
  confirmedAccountIds: string[]
  clearedAccountId?: string
  cleared: boolean
}

interface ClientIpAccountAvoidanceScope {
  systemAccountId: string
  apiKeyId: string
  clientIp: string
}

interface ClientIpAccountAvoidanceEntry extends ClientIpAccountFailure {
  entryKey: string
  scopeKey: string
  failureCount: number
  firstFailedAtMs: number
  lastFailedAtMs: number
  avoidUntilMs: number
}

const clientIpAccountAvoidanceMaxEntries = 5_000
const clientIpAccountAvoidanceMaxPendingFailures = 256
const clientIpAccountAvoidanceMaxTtlMs = 10 * 60_000
const clientIpAccountAvoidanceDefaultTtlMs = 5 * 60_000
const clientIpAccountAvoidanceActivationFailureThreshold = 2

const clientIpAccountAvoidanceCache = createAppCache<string, ClientIpAccountAvoidanceEntry>({
  name: 'gateway:client-ip-account-avoidance',
  max: clientIpAccountAvoidanceMaxEntries,
  ttlMs: clientIpAccountAvoidanceMaxTtlMs,
  updateAgeOnGet: false
})

export function createClientIpAccountAvoidanceTracker(
  input: ClientIpAccountAvoidanceScopeInput
): ClientIpAccountAvoidanceTracker {
  return {
    scope: normalizeScope(input),
    pendingFailures: [],
    pendingFailureIndexByAccountId: new Map<string, number>()
  }
}

export function orderOpenAIAccountsByClientIpAccountAvoidance(
  accounts: UpstreamAccount[],
  input: ClientIpAccountAvoidanceScopeInput
): ClientIpAccountAvoidanceOrderResult {
  const scope = normalizeScope(input)
  if (!scope || accounts.length === 0) {
    return {
      accounts,
      applied: false,
      avoidedAccountIds: [],
      bypassedAllAvoided: false
    }
  }

  const freshAccounts: UpstreamAccount[] = []
  const avoidedAccounts: UpstreamAccount[] = []
  for (const account of accounts) {
    const entry = clientIpAccountAvoidanceCache.get(entryKey(scope, account.id))
    if (entry && entry.failureCount >= clientIpAccountAvoidanceActivationFailureThreshold) {
      avoidedAccounts.push(account)
    } else {
      freshAccounts.push(account)
    }
  }

  if (avoidedAccounts.length === 0) {
    return {
      accounts,
      applied: false,
      avoidedAccountIds: [],
      bypassedAllAvoided: false
    }
  }

  if (freshAccounts.length === 0) {
    return {
      accounts,
      applied: false,
      avoidedAccountIds: avoidedAccounts.map((account) => account.id),
      bypassedAllAvoided: true
    }
  }

  return {
    accounts: [...freshAccounts, ...avoidedAccounts],
    applied: true,
    avoidedAccountIds: avoidedAccounts.map((account) => account.id),
    bypassedAllAvoided: false
  }
}

export function rememberClientIpAccountPendingFailure(
  tracker: ClientIpAccountAvoidanceTracker | undefined,
  account: Pick<UpstreamAccount, 'id' | 'name'>,
  input: Omit<ClientIpAccountFailure, 'accountId' | 'accountName'>
): void {
  if (!tracker?.scope) {
    return
  }
  const pendingFailure = {
    ...input,
    accountId: account.id,
    accountName: account.name
  }
  const currentIndex = tracker.pendingFailureIndexByAccountId.get(account.id)
  if (currentIndex !== undefined) {
    tracker.pendingFailures[currentIndex] = pendingFailure
    return
  }
  if (tracker.pendingFailures.length >= clientIpAccountAvoidanceMaxPendingFailures) {
    return
  }
  tracker.pendingFailureIndexByAccountId.set(account.id, tracker.pendingFailures.length)
  tracker.pendingFailures.push(pendingFailure)
}

export function transferClientIpAccountPendingFailures(
  source: ClientIpAccountAvoidanceTracker | undefined,
  target: ClientIpAccountAvoidanceTracker | undefined
): void {
  if (!source?.scope || !target?.scope || source.pendingFailures.length === 0) {
    return
  }
  for (const failure of source.pendingFailures) {
    rememberClientIpAccountPendingFailure(target, {
      id: failure.accountId,
      name: failure.accountName ?? failure.accountId
    }, {
      statusCode: failure.statusCode,
      errorCode: failure.errorCode,
      errorType: failure.errorType,
      errorPhase: failure.errorPhase,
      errorMessage: failure.errorMessage,
      endpoint: failure.endpoint
    })
  }
  clearTrackerPendingFailures(source)
}

export function confirmClientIpAccountAvoidanceAfterSuccess(
  tracker: ClientIpAccountAvoidanceTracker | undefined,
  successAccountId: string,
  settings?: GatewaySettings
): ClientIpAccountAvoidanceConfirmResult {
  if (!tracker?.scope) {
    return { confirmedAccountIds: [], cleared: false }
  }

  const cleared = clearClientIpAccountAvoidanceForAccount(tracker, successAccountId)
  const confirmedAccountIds = confirmTrackerPendingFailures(tracker, settings, successAccountId)

  return {
    confirmedAccountIds: [...new Set(confirmedAccountIds)],
    clearedAccountId: successAccountId,
    cleared
  }
}

export function confirmClientIpAccountAvoidanceAfterFinalFailure(
  tracker: ClientIpAccountAvoidanceTracker | undefined,
  settings?: GatewaySettings
): ClientIpAccountAvoidanceConfirmResult {
  if (!tracker?.scope) {
    return { confirmedAccountIds: [], cleared: false }
  }

  const confirmedAccountIds = confirmTrackerPendingFailures(tracker, settings)
  return {
    confirmedAccountIds: [...new Set(confirmedAccountIds)],
    cleared: false
  }
}

function confirmTrackerPendingFailures(
  tracker: ClientIpAccountAvoidanceTracker,
  settings: GatewaySettings | undefined,
  skipAccountId?: string
): string[] {
  const scope = tracker.scope
  if (!scope) {
    return []
  }
  const ttlMs = avoidanceTtlMs(settings)
  const now = Date.now()
  const confirmedAccountIds: string[] = []
  for (const failure of tracker.pendingFailures) {
    if (failure.accountId === skipAccountId) {
      continue
    }
    const key = entryKey(scope, failure.accountId)
    const current = clientIpAccountAvoidanceCache.get(key)
    const entry: ClientIpAccountAvoidanceEntry = {
      ...failure,
      entryKey: key,
      scopeKey: scopeKey(scope),
      failureCount: (current?.failureCount ?? 0) + 1,
      firstFailedAtMs: current?.firstFailedAtMs ?? now,
      lastFailedAtMs: now,
      avoidUntilMs: now + ttlMs
    }
    clientIpAccountAvoidanceCache.set(key, entry, { ttlMs })
    confirmedAccountIds.push(failure.accountId)
  }
  clearTrackerPendingFailures(tracker)
  return confirmedAccountIds
}

function clearTrackerPendingFailures(tracker: ClientIpAccountAvoidanceTracker): void {
  tracker.pendingFailures = []
  tracker.pendingFailureIndexByAccountId.clear()
}

export function clearClientIpAccountAvoidanceForAccount(
  tracker: ClientIpAccountAvoidanceTracker | undefined,
  accountId: string
): boolean {
  if (!tracker?.scope) {
    return false
  }
  const key = entryKey(tracker.scope, accountId)
  const existed = Boolean(clientIpAccountAvoidanceCache.get(key))
  clientIpAccountAvoidanceCache.delete(key)
  return existed
}

export function clearClientIpAccountAvoidanceForTest(): void {
  clientIpAccountAvoidanceCache.clear()
}

export function getClientIpAccountAvoidanceSnapshotForTest(): Array<{
  accountId: string
  failureCount: number
  active: boolean
  clientIp: string
  apiKeyId: string
  systemAccountId: string
}> {
  return [...clientIpAccountAvoidanceCache.values()].map((entry) => {
    const scope = parseScopeKey(entry.scopeKey)
    return {
      accountId: entry.accountId,
      failureCount: entry.failureCount,
      active: entry.failureCount >= clientIpAccountAvoidanceActivationFailureThreshold,
      clientIp: scope.clientIp,
      apiKeyId: scope.apiKeyId,
      systemAccountId: scope.systemAccountId
    }
  })
}

function normalizeScope(input: ClientIpAccountAvoidanceScopeInput): ClientIpAccountAvoidanceScope | undefined {
  const clientIp = input.clientIp?.trim()
  if (!clientIp) {
    return undefined
  }
  return {
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId?.trim() || 'internal',
    clientIp
  }
}

function entryKey(scope: ClientIpAccountAvoidanceScope, accountId: string): string {
  return `${scopeKey(scope)}:${accountId}`
}

function scopeKey(scope: ClientIpAccountAvoidanceScope): string {
  return JSON.stringify({
    systemAccountId: scope.systemAccountId,
    apiKeyId: scope.apiKeyId,
    clientIp: scope.clientIp
  })
}

function parseScopeKey(value: string): ClientIpAccountAvoidanceScope {
  try {
    const parsed = JSON.parse(value) as ClientIpAccountAvoidanceScope
    return {
      systemAccountId: parsed.systemAccountId,
      apiKeyId: parsed.apiKeyId,
      clientIp: parsed.clientIp
    }
  } catch {
    return {
      systemAccountId: '',
      apiKeyId: '',
      clientIp: ''
    }
  }
}

function avoidanceTtlMs(settings?: GatewaySettings): number {
  const minutes = settings?.defaultTemporaryUnschedulableMinutes
  const numeric = typeof minutes === 'number' && Number.isFinite(minutes)
    ? Math.max(1, Math.trunc(minutes))
    : Math.trunc(clientIpAccountAvoidanceDefaultTtlMs / 60_000)
  return Math.min(numeric * 60_000, clientIpAccountAvoidanceMaxTtlMs)
}

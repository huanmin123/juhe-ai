import { runtimeConfig } from '../../config/runtime.js'
import { accountFilterStatuses } from '../../domain/account-status-classification.js'
import type { AccountListItem } from '../../domain/types.js'
import {
  applyAccountListAvailabilityProjectionDeletionDirtyClaimInClient,
  applyAccountListAvailabilityProjectionDirtyClaimInClient,
  claimAccountListAvailabilityDirtyInClient,
  enqueueDueAccountListAvailabilityProjectionsInClient,
  enqueueMissingAccountListAvailabilityProjectionsInClient,
  enqueueStaleAccountListAvailabilityProjectionsInClient,
  loadAccountListAvailabilityProjectionSearchTermsInClient,
  listAccountListAvailabilityProjectionViewerHealthRefreshCandidatesInClient,
  listAccountListAvailabilityProjectionScopesInClient,
  refreshAccountListAvailabilityProjectionViewerHealthInClient,
  releaseAccountListAvailabilityDirtyForReplayInClient,
  type AccountListAvailabilityDirtyClaim,
  type AccountListAvailabilityProjectionScope,
  type AccountListAvailabilityProjectionWrite
} from '../../storage/account-list-availability-projection.repository.js'
import { loadAccountTagsByAccountIdsAsync } from '../../storage/account-tags.repository.js'
import { listAccountManagementItemsPageAsync } from '../../storage/account-management-list.repository.js'
import { createPostgresDatabaseClient, type DatabaseClient } from '../../storage/database-client.js'
import { getPostgresPool } from '../../storage/postgres-client.js'
import { hydrateAccountListPage } from './account-status-snapshot.service.js'

const maximumProjectionBatchSize = 100
const defaultProjectionLeaseMs = 30_000

export interface AccountListAvailabilityProjectionMaintenanceResult {
  bootstrapped: number
  dueEnqueued: number
  staleEnqueued: number
  claimed: number
  projected: number
  deleted: number
  staleClaims: number
  released: number
}

export interface AccountListAvailabilityProjectionMaintenanceInput {
  ownerId: string
  batchSize?: number
  leaseMs?: number
  maximumProjectionAgeMs?: number
  now?: Date
  signal?: AbortSignal
  loadItems?: (viewerSystemAccountId: string, accountIds: string[]) => Promise<AccountListItem[]>
}

/**
 * PostgreSQL-only materializer for the account management list. It is not a
 * request fallback: a dirty or absent row stays unavailable until this worker
 * has committed a complete replacement payload.
 */
export async function runAccountListAvailabilityProjectionMaintenance(
  input: Omit<AccountListAvailabilityProjectionMaintenanceInput, 'loadItems'>
): Promise<AccountListAvailabilityProjectionMaintenanceResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return emptyProjectionMaintenanceResult()
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return runAccountListAvailabilityProjectionMaintenanceInClient(client, input)
}

export async function runAccountListAvailabilityProjectionMaintenanceInClient(
  client: DatabaseClient,
  input: AccountListAvailabilityProjectionMaintenanceInput
): Promise<AccountListAvailabilityProjectionMaintenanceResult> {
  const ownerId = requiredOwnerId(input.ownerId)
  const batchSize = boundedBatchSize(input.batchSize ?? maximumProjectionBatchSize)
  const leaseMs = boundedLeaseMs(input.leaseMs ?? defaultProjectionLeaseMs)
  const maximumProjectionAgeMs = boundedProjectionAgeMs(input.maximumProjectionAgeMs ?? 30_000)
  const now = input.now ?? new Date()
  const nowMs = now.getTime()
  const loadItems = input.loadItems ?? loadProjectedAccountListItems
  throwIfAborted(input.signal)

  const [bootstrapped, dueEnqueued, staleEnqueued] = await Promise.all([
    enqueueMissingAccountListAvailabilityProjectionsInClient(client, { limit: batchSize, nowMs }),
    enqueueDueAccountListAvailabilityProjectionsInClient(client, { limit: batchSize, nowMs }),
    enqueueStaleAccountListAvailabilityProjectionsInClient(client, { limit: batchSize, maximumProjectionAgeMs, nowMs })
  ])
  const viewersToRefresh = new Set(await listAccountListAvailabilityProjectionViewerHealthRefreshCandidatesInClient(client, {
    limit: batchSize
  }))
  throwIfAborted(input.signal)
  const claims = await claimAccountListAvailabilityDirtyInClient(client, {
    ownerId,
    limit: batchSize,
    leaseMs,
    nowMs
  })
  if (!claims.length) {
    for (const viewerSystemAccountId of viewersToRefresh) {
      await refreshAccountListAvailabilityProjectionViewerHealthInClient(client, {
        viewerSystemAccountId,
        updatedAt: now.toISOString()
      })
    }
    return { ...emptyProjectionMaintenanceResult(), bootstrapped, dueEnqueued, staleEnqueued }
  }

  const scopes = await listAccountListAvailabilityProjectionScopesInClient(client, claims.map((claim) => claim.accountId))
  const scopeByAccountId = new Map(scopes.map((scope) => [scope.accountId, scope]))
  const claimsByViewer = groupClaimsByViewer(claims, scopeByAccountId)
  let projected = 0
  let deleted = 0
  let staleClaims = 0
  let released = 0
  for (const [viewerSystemAccountId, viewerClaims] of claimsByViewer) {
    throwIfAborted(input.signal)
    const scopesForViewer = viewerClaims.map(({ scope }) => scope)
    const claimsForViewer = viewerClaims.map(({ claim }) => claim)
    viewersToRefresh.add(viewerSystemAccountId)
    const completedClaims = new Set<string>()
    try {
      const items = await loadItems(viewerSystemAccountId, scopesForViewer.map((scope) => scope.accountId))
      const itemById = new Map(items.map((item) => [item.id, item]))
      const searchTermsByAccountId = await loadAccountListAvailabilityProjectionSearchTermsInClient(
        client,
        scopesForViewer.map((scope) => scope.accountId)
      )
      for (const { claim, scope } of viewerClaims) {
        throwIfAborted(input.signal)
        const item = itemById.get(claim.accountId)
        if (!item) {
          throw new Error(`账户列表投影账户 ${claim.accountId} 在当前可见范围中缺失`)
        }
        const applied = await applyAccountListAvailabilityProjectionDirtyClaimInClient(client, {
          claim,
          projection: accountListAvailabilityProjectionWrite(
            scope,
            item,
            claim,
            now,
            searchTermsByAccountId.get(item.id) ?? []
          )
        })
        completedClaims.add(claim.claimToken)
        if (applied) projected += 1
        else staleClaims += 1
      }
    } catch (error) {
      if (input.signal?.aborted) throw error
      for (const claim of claimsForViewer) {
        if (completedClaims.has(claim.claimToken)) continue
        const replayed = await releaseAccountListAvailabilityDirtyForReplayInClient(client, {
          accountId: claim.accountId,
          generation: claim.generation,
          claimToken: claim.claimToken,
          reason: 'projection_refresh_failed',
          retryDelayMs: projectionReplayDelayMs(claim.attemptCount),
          nowMs: Date.now()
        })
        if (replayed) released += 1
      }
    }
  }

  const invisibleClaims = claims.filter((claim) => !scopeByAccountId.has(claim.accountId))
  for (const claim of invisibleClaims) {
    throwIfAborted(input.signal)
    const applied = await applyAccountListAvailabilityProjectionDeletionDirtyClaimInClient(client, { claim })
    if (applied) deleted += 1
    else staleClaims += 1
    viewersToRefresh.add(claim.viewerSystemAccountId)
  }
  for (const viewerSystemAccountId of viewersToRefresh) {
    throwIfAborted(input.signal)
    await refreshAccountListAvailabilityProjectionViewerHealthInClient(client, {
      viewerSystemAccountId,
      updatedAt: now.toISOString()
    })
  }

  return {
    bootstrapped,
    dueEnqueued,
    staleEnqueued,
    claimed: claims.length,
    projected,
    deleted,
    staleClaims,
    released
  }
}

async function loadProjectedAccountListItems(viewerSystemAccountId: string, accountIds: string[]): Promise<AccountListItem[]> {
  const page = await listAccountManagementItemsPageAsync({
    systemAccountId: viewerSystemAccountId,
    role: 'user'
  }, {
    ids: accountIds,
    page: 1,
    pageSize: accountIds.length
  })
  const hydrated = await hydrateAccountListPage({
    systemAccountId: viewerSystemAccountId,
    role: 'user'
  }, page)
  const tagsByAccountId = await loadAccountTagsByAccountIdsAsync(accountIds)
  return hydrated.items.map((item) => ({
    ...item,
    // The legacy runtime-filter response includes full tag summaries. Store
    // that superset once so the read query can preserve both legacy shapes.
    tags: tagsByAccountId.get(item.id) ?? []
  }))
}

function groupClaimsByViewer(
  claims: AccountListAvailabilityDirtyClaim[],
  scopeByAccountId: Map<string, AccountListAvailabilityProjectionScope>
): Map<string, Array<{ claim: AccountListAvailabilityDirtyClaim; scope: AccountListAvailabilityProjectionScope }>> {
  const groups = new Map<string, Array<{ claim: AccountListAvailabilityDirtyClaim; scope: AccountListAvailabilityProjectionScope }>>()
  for (const claim of claims) {
    const scope = scopeByAccountId.get(claim.accountId)
    if (!scope) continue
    const group = groups.get(scope.viewerSystemAccountId) ?? []
    group.push({ claim, scope })
    groups.set(scope.viewerSystemAccountId, group)
  }
  return groups
}

function accountListAvailabilityProjectionWrite(
  scope: AccountListAvailabilityProjectionScope,
  item: AccountListItem,
  claim: AccountListAvailabilityDirtyClaim,
  now: Date,
  searchTerms: string[]
): AccountListAvailabilityProjectionWrite {
  const statuses = [...accountFilterStatuses(item)]
  if (statuses.length !== 1) {
    throw new Error(`账户 ${item.id} 无法归类为唯一投影状态`)
  }
  const effectiveStatus = statuses[0]!
  const accountExpiresAtSortKey = item.authorizationExpiresAt
    ?? item.authorizationInstanceSourceAccountExpiresAt
    ?? item.accountExpiresAt
  return {
    viewerSystemAccountId: scope.viewerSystemAccountId,
    accountId: item.id,
    sourceAccountId: item.authorizationInstanceSourceAccountId,
    authorizationId: item.accountAuthorizationId,
    effectiveStatus,
    schedulableBucket: schedulableBucket(item, effectiveStatus),
    providerCode: item.providerCode,
    providerProtocolProfileId: item.providerProtocolProfileId,
    accountType: item.type,
    boundGroupId: item.boundGroupId,
    nameSortKey: item.name,
    prioritySortKey: item.priority,
    superPrioritySortKey: item.superPriorityEnabled ? 1 : 0,
    fallbackSortKey: item.fallbackEnabled ? 1 : 0,
    concurrencySortKey: item.concurrencyLimit,
    accountExpiresAtSortKey,
    lastUsedAtSortKey: item.lastUsedAt,
    createdAtSortKey: scope.createdAt,
    payload: item,
    tagIds: item.tags.map((tag) => tag.id),
    searchTerms,
    sourceGeneration: claim.generation,
    nextTransitionAt: nextTransitionAt(item, now),
    projectedAt: now.toISOString()
  }
}

function schedulableBucket(
  item: AccountListItem,
  effectiveStatus: AccountListAvailabilityProjectionWrite['effectiveStatus']
): AccountListAvailabilityProjectionWrite['schedulableBucket'] {
  if (effectiveStatus === 'rate_limited' || effectiveStatus === 'temporary_unavailable') return 'cooling'
  return item.effectiveAvailability.available ? 'enabled' : 'disabled'
}

function nextTransitionAt(item: AccountListItem, now: Date): string | undefined {
  const nowMs = now.getTime()
  const candidates = [
    item.accountExpiresAt,
    item.cooldownUntil,
    item.authorizationExpiresAt,
    item.authorizationInstanceSourceAccountExpiresAt,
    item.authorizationInstanceSourceAccountCooldownUntil,
    item.apiKeyRuntime?.nextProbeAt
  ]
    .filter((value): value is string => typeof value === 'string')
    .filter((value) => {
      const timestamp = Date.parse(value)
      return Number.isFinite(timestamp) && timestamp > nowMs
    })
  return candidates.sort()[0]
}

function projectionReplayDelayMs(attemptCount: number): number {
  const exponent = Math.min(Math.max(0, attemptCount - 1), 6)
  return Math.min(60_000, 1_000 * 2 ** exponent)
}

function boundedBatchSize(value: number): number {
  if (!Number.isInteger(value) || value < 1 || value > maximumProjectionBatchSize) {
    throw new Error(`账户列表投影批量大小必须在 1-${maximumProjectionBatchSize} 之间`)
  }
  return value
}

function boundedLeaseMs(value: number): number {
  if (!Number.isInteger(value) || value < 1_000 || value > 60 * 60_000) {
    throw new Error('账户列表投影租约必须在 1000-3600000ms 之间')
  }
  return value
}

function boundedProjectionAgeMs(value: number): number {
  if (!Number.isInteger(value) || value < 1_000 || value > 24 * 60 * 60_000) {
    throw new Error('账户列表投影最大新鲜度必须在 1000-86400000ms 之间')
  }
  return value
}

function requiredOwnerId(value: string): string {
  const ownerId = value.trim()
  if (!ownerId || ownerId.length > 128) throw new Error('账户列表投影 ownerId 无效')
  return ownerId
}

function throwIfAborted(signal: AbortSignal | undefined): void {
  if (signal?.aborted) {
    throw signal.reason instanceof Error ? signal.reason : new Error('账户列表投影任务已取消')
  }
}

function emptyProjectionMaintenanceResult(): AccountListAvailabilityProjectionMaintenanceResult {
  return {
    bootstrapped: 0,
    dueEnqueued: 0,
    staleEnqueued: 0,
    claimed: 0,
    projected: 0,
    deleted: 0,
    staleClaims: 0,
    released: 0
  }
}

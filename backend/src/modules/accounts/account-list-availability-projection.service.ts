import { runtimeConfig } from '../../config/runtime.js'
import { accountFilterStatuses } from '../../domain/account-status-classification.js'
import type { AccountListItem } from '../../domain/types.js'
import {
  applyAccountListAvailabilityProjectionDeletionDirtyClaimInClient,
  applyAccountListAvailabilityProjectionDirtyClaimsInClient,
  beginAccountListAvailabilityProjectionRuntimeDependencyRecoveryInClient,
  claimAccountListAvailabilityDirtyInClient,
  completeAccountListAvailabilityProjectionRuntimeDependencyRecoveryInClient,
  enqueueAllAccountListAvailabilityProjectionsForRuntimeRecoveryInClient,
  ensureAccountListAvailabilityProjectionViewerHealthInClient,
  ensureAccountListAvailabilityProjectionRuntimeDependencyInClient,
  enqueueDueAccountListAvailabilityProjectionsInClient,
  enqueueMissingAccountListAvailabilityProjectionsInClient,
  loadAccountListAvailabilityProjectionSearchTermsInClient,
  listAccountListAvailabilityProjectionViewerHealthRefreshCandidatesInClient,
  listAccountListAvailabilityProjectionScopesInClient,
  refreshAccountListAvailabilityProjectionViewerHealthInClient,
  releaseAccountListAvailabilityDirtyForReplayInClient,
  markAccountListAvailabilityProjectionRuntimeDependencyUnavailableInClient,
  touchAccountListAvailabilityProjectionRuntimeDependencyInClient,
  upsertAccountListAvailabilityRuntimeOverlaysInClient,
  type AccountListAvailabilityDirtyClaim,
  type AccountListAvailabilityProjectionScope,
  type AccountListAvailabilityProjectionWrite
} from '../../storage/account-list-availability-projection.repository.js'
import { loadAccountTagsByAccountIdsAsync } from '../../storage/account-tags.repository.js'
import { nextAccountAvailabilityScheduleCheckAt, parseAccountAvailabilityScheduleJson } from '../../storage/account-availability-schedule.js'
import { listAccountCircuitIncidentsByRuntimeKeysInClient } from '../../storage/account-circuit-control-plane.repository.js'
import { listAccountManagementItemsPageAsync } from '../../storage/account-management-list.repository.js'
import { createPostgresDatabaseClient, type DatabaseClient } from '../../storage/database-client.js'
import { getPostgresPool } from '../../storage/postgres-client.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { hydrateAccountListPageWithRuntimeSnapshot } from './account-status-snapshot.service.js'
import { syncAccountAvailabilityScheduleStatusesAsync } from '../../storage/account-availability-schedule-status-sync.repository.js'
import { publicAccountCircuitSummariesFromIncidents } from '../gateway/runtime/account-circuit-control-plane-bridge.js'
import { probeAccountRuntimeState } from '../gateway/runtime/runtime-snapshot.service.js'
import {
  acknowledgeAccountConcurrencyProjectionDirtyEntriesAsync,
  listAccountConcurrencyProjectionDirtyEntriesAsync,
  loadAccountConcurrencyProjectionSnapshotsAsync
} from '../../shared/account-concurrency.js'

const maximumProjectionBatchSize = 100
const defaultProjectionBatchesPerRun = 200
const maximumProjectionBatchesPerRun = 400
const defaultProjectionWorkerConcurrency = 4
const maximumProjectionWorkerConcurrency = 8
const defaultProjectionLeaseMs = 30_000

export interface AccountListAvailabilityProjectionMaintenanceResult {
  runtimeDependencyUnavailable: number
  runtimeRecoveryEnqueued: number
  runtimeRecoveryCompleted: number
  runtimeOverlayReconciled: number
  viewerHealthBootstrapped: number
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
  maxBatchesPerRun?: number
  workerConcurrency?: number
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
  const maxBatchesPerRun = boundedProjectionBatchesPerRun(input.maxBatchesPerRun ?? defaultProjectionBatchesPerRun)
  // SQLite is intentionally single-writer. PostgreSQL claims use SKIP LOCKED,
  // so bounded parallel workers can recover a large dirty viewer without
  // serializing every 100-account hydration batch.
  const workerConcurrency = client.driver === 'postgres'
    ? boundedProjectionWorkerConcurrency(input.workerConcurrency ?? defaultProjectionWorkerConcurrency)
    : 1
  const aggregate = emptyProjectionMaintenanceResult()
  for (let index = 0; index < maxBatchesPerRun; index += workerConcurrency) {
    const batchCount = Math.min(workerConcurrency, maxBatchesPerRun - index)
    const batches = await Promise.all(Array.from({ length: batchCount }, () =>
      runAccountListAvailabilityProjectionMaintenanceBatchInClient(client, input)
    ))
    let progressed = false
    for (const batch of batches) {
      addMaintenanceResult(aggregate, batch)
      if (batch.viewerHealthBootstrapped !== 0
        || batch.bootstrapped !== 0
        || batch.dueEnqueued !== 0
        || batch.staleEnqueued !== 0
        || batch.claimed !== 0) {
        progressed = true
      }
    }
    if (!progressed) {
      break
    }
  }
  return aggregate
}

async function runAccountListAvailabilityProjectionMaintenanceBatchInClient(
  client: DatabaseClient,
  input: AccountListAvailabilityProjectionMaintenanceInput
): Promise<AccountListAvailabilityProjectionMaintenanceResult> {
  const ownerId = requiredOwnerId(input.ownerId)
  const batchSize = boundedBatchSize(input.batchSize ?? maximumProjectionBatchSize)
  const leaseMs = boundedLeaseMs(input.leaseMs ?? defaultProjectionLeaseMs)
  const now = input.now ?? new Date()
  const nowMs = now.getTime()
  const loadItems = input.loadItems ?? ((viewerSystemAccountId: string, accountIds: string[]) =>
    loadProjectedAccountListItems(client, viewerSystemAccountId, accountIds))
  throwIfAborted(input.signal)

  let runtimeOverlayReconciled = 0
  let runtimeRecoveryEnqueued = 0
  if (client.driver === 'postgres') {
    await ensureAccountListAvailabilityProjectionRuntimeDependencyInClient(client, {
      updatedAt: now.toISOString()
    })
    const runtimeDependency = await probeAccountRuntimeState()
    if (!runtimeDependency.accountRuntimeAvailabilityAvailable || !runtimeDependency.accountConcurrencyAvailable) {
      await markAccountListAvailabilityProjectionRuntimeDependencyUnavailableInClient(client, {
        reason: runtimeDependency.accountRuntimeAvailabilityAvailable
          ? 'account_concurrency_runtime_unavailable'
          : 'account_runtime_availability_unavailable',
        updatedAt: now.toISOString()
      })
      return {
        ...emptyProjectionMaintenanceResult(),
        runtimeDependencyUnavailable: 1
      }
    }
    try {
      runtimeOverlayReconciled = await reconcileAccountListAvailabilityRuntimeOverlays(client)
    } catch (error) {
      await markAccountListAvailabilityProjectionRuntimeDependencyUnavailableInClient(client, {
        reason: 'account_concurrency_overlay_reconcile_failed',
        updatedAt: now.toISOString()
      })
      logger.error(errorLogFields(error, {
        event: 'account_list_availability_runtime_overlay_reconcile_failed'
      }), '账户列表运行态并发 overlay 更新失败，已停止读取旧快照')
      return {
        ...emptyProjectionMaintenanceResult(),
        runtimeDependencyUnavailable: 1
      }
    }
    const runtimeRecoveryStarted = await beginAccountListAvailabilityProjectionRuntimeDependencyRecoveryInClient(client, {
      updatedAt: now.toISOString()
    })
    if (!runtimeRecoveryStarted) {
      await touchAccountListAvailabilityProjectionRuntimeDependencyInClient(client, {
        updatedAt: now.toISOString()
      })
    }
    runtimeRecoveryEnqueued = runtimeRecoveryStarted
      ? await enqueueAllAccountListAvailabilityProjectionsForRuntimeRecoveryInClient(client, { nowMs })
      : 0
  }

  // Schedule transitions change persisted status. Apply all currently due
  // transitions before claiming their projection refresh so a boundary can
  // never republish the pre-transition availability decision.
  if (client.driver === 'postgres') {
    await syncAccountAvailabilityScheduleStatusesAsync(now)
  }

  const viewerHealthBootstrapped = await ensureAccountListAvailabilityProjectionViewerHealthInClient(client, {
    limit: batchSize,
    updatedAt: now.toISOString()
  })
  const [bootstrapped, dueEnqueued] = await Promise.all([
    enqueueMissingAccountListAvailabilityProjectionsInClient(client, { limit: batchSize, nowMs }),
    enqueueDueAccountListAvailabilityProjectionsInClient(client, { limit: batchSize, nowMs })
  ])
  const staleEnqueued = 0
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
    const runtimeRecoveryCompleted = client.driver === 'postgres' && await completeAccountListAvailabilityProjectionRuntimeDependencyRecoveryInClient(client, {
      updatedAt: now.toISOString()
    }) ? 1 : 0
    return {
      ...emptyProjectionMaintenanceResult(),
      runtimeRecoveryEnqueued,
      runtimeRecoveryCompleted,
      runtimeOverlayReconciled,
      viewerHealthBootstrapped,
      bootstrapped,
      dueEnqueued,
      staleEnqueued
    }
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
      const writes: Array<{ claim: AccountListAvailabilityDirtyClaim; projection: AccountListAvailabilityProjectionWrite }> = []
      for (const { claim, scope } of viewerClaims) {
        throwIfAborted(input.signal)
        const item = itemById.get(claim.accountId)
        if (!item) {
          throw new Error(`账户列表投影账户 ${claim.accountId} 在当前可见范围中缺失`)
        }
        writes.push({
          claim,
          projection: accountListAvailabilityProjectionWrite(
            scope,
            item,
            claim,
            now,
            searchTermsByAccountId.get(item.id) ?? []
          )
        })
      }
      const appliedByClaimToken = await applyAccountListAvailabilityProjectionDirtyClaimsInClient(client, writes)
      for (const claim of claimsForViewer) {
        const applied = appliedByClaimToken.get(claim.claimToken) === true
        if (applied) {
          completedClaims.add(claim.claimToken)
          projected += 1
        } else {
          staleClaims += 1
        }
      }
    } catch (error) {
      if (input.signal?.aborted) throw error
      await markAccountListAvailabilityProjectionRuntimeDependencyUnavailableInClient(client, {
        reason: 'projection_runtime_materialization_failed',
        updatedAt: new Date().toISOString()
      })
      logger.error(errorLogFields(error, {
        event: 'account_list_availability_projection_batch_failed',
        viewerSystemAccountId,
        claimCount: claimsForViewer.length
      }), '账户列表可用性读模型批量物化失败，已释放为重放')
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

  const runtimeRecoveryCompleted = client.driver === 'postgres' && await completeAccountListAvailabilityProjectionRuntimeDependencyRecoveryInClient(client, {
    updatedAt: now.toISOString()
  }) ? 1 : 0

  return {
    runtimeDependencyUnavailable: 0,
    runtimeRecoveryEnqueued,
    runtimeRecoveryCompleted,
    runtimeOverlayReconciled,
    viewerHealthBootstrapped,
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

/**
 * Redis is read only by this background reconciler. The HTTP list path joins
 * the durable overlay in PostgreSQL and therefore retains a constant query
 * count regardless of page size.
 */
async function reconcileAccountListAvailabilityRuntimeOverlays(client: DatabaseClient): Promise<number> {
  const entries = await listAccountConcurrencyProjectionDirtyEntriesAsync(maximumProjectionBatchSize)
  if (!entries.length) return 0
  // Redis lease release can race a hard account deletion. The projection table
  // has an FK to accounts, so acknowledge only those safely-provable tombstone
  // events instead of turning an unrelated viewer into a global dependency
  // outage. Unknown *existing* accounts still fail closed below.
  const existingRows = await client.query<{ id: string }>(`
    SELECT id
    FROM ${client.driver === 'postgres'
      ? client.dialect.qualifyTable('juhe_business', 'accounts')
      : client.dialect.quoteIdentifier('accounts')}
    WHERE id IN (${client.dialect.bindPlaceholders(entries.length)})
  `, entries.map((entry) => entry.accountId))
  const existingAccountIds = new Set(existingRows.map((row) => row.id))
  const staleEntries = entries.filter((entry) => !existingAccountIds.has(entry.accountId))
  if (staleEntries.length) {
    await acknowledgeAccountConcurrencyProjectionDirtyEntriesAsync(staleEntries)
  }
  const activeEntries = entries.filter((entry) => existingAccountIds.has(entry.accountId))
  if (!activeEntries.length) return entries.length

  const snapshots = await loadAccountConcurrencyProjectionSnapshotsAsync(activeEntries.map((entry) => entry.accountId))
  const snapshotByAccountId = new Map(snapshots.map((snapshot) => [snapshot.accountId, snapshot]))
  const observedAt = new Date().toISOString()
  await upsertAccountListAvailabilityRuntimeOverlaysInClient(client, activeEntries.map((entry) => {
    const snapshot = snapshotByAccountId.get(entry.accountId)
    if (!snapshot) throw new Error(`账户并发 overlay 缺少 Redis 快照: ${entry.accountId}`)
    return {
      accountId: entry.accountId,
      currentConcurrency: snapshot.currentConcurrency,
      observedAt,
      nextReconcileAt: snapshot.nextReconcileAt
    }
  }))
  await acknowledgeAccountConcurrencyProjectionDirtyEntriesAsync(activeEntries.map((entry) => ({
    ...entry,
    nextReconcileAt: snapshotByAccountId.get(entry.accountId)?.nextReconcileAt
  })))
  return entries.length
}

async function loadProjectedAccountListItems(
  client: DatabaseClient,
  viewerSystemAccountId: string,
  accountIds: string[]
): Promise<AccountListItem[]> {
  const page = await listAccountManagementItemsPageAsync({
    systemAccountId: viewerSystemAccountId,
    role: 'user'
  }, {
    ids: accountIds,
    page: 1,
    pageSize: accountIds.length
  })
  const hydrated = await hydrateAccountListPageWithRuntimeSnapshot({
    systemAccountId: viewerSystemAccountId,
    role: 'user'
  }, page, {
    loadCircuitSummaries: async (accountRuntimeKeys) => publicAccountCircuitSummariesFromIncidents(
      accountRuntimeKeys,
      await listAccountCircuitIncidentsByRuntimeKeysInClient(client, accountRuntimeKeys)
    )
  })
  const runtimeSnapshot = hydrated.runtimeSnapshot
  if (!runtimeSnapshot.accountRuntimeAvailabilityAvailable
    || !runtimeSnapshot.accountCircuitSummaryAvailable
    || !runtimeSnapshot.accountConcurrencyAvailable) {
    throw new Error(`账户列表投影运行态快照不可用，拒绝发布未知可用性 (${JSON.stringify(runtimeSnapshot)})`)
  }
  const tagsByAccountId = await loadAccountTagsByAccountIdsAsync(accountIds)
  const sourceAvailabilityScheduleByAccountId = new Map(page.statusSeeds.map((seed) => [
    seed.id,
    parseAccountAvailabilityScheduleJson(seed.source_availability_schedule_json)
  ]))
  const lastUsedAtSortKeyByAccountId = new Map(page.statusSeeds.map((seed) => [
    seed.id,
    seed.last_used_at
  ]))
  return hydrated.result.items.map((item) => ({
    ...item,
    // Legacy pagination sorts on accounts.last_used_at. Authorized rows show
    // authorization usage instead, so keep the ordering key explicitly
    // separate from the public field before persisting the projection.
    accountListProjectionSortLastUsedAt: lastUsedAtSortKeyByAccountId.get(item.id) ?? null,
    ...(hydrated.projectionNextTransitionAtByAccountId.get(item.id)
      ? { accountListProjectionNextTransitionAt: hydrated.projectionNextTransitionAtByAccountId.get(item.id) }
      : {}),
    authorizationInstanceSourceAccountAvailabilitySchedule: sourceAvailabilityScheduleByAccountId.get(item.id),
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
  item: AccountListAvailabilityProjectionMaterializedItem,
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
  const {
    authorizationInstanceSourceAccountAvailabilitySchedule,
    accountListProjectionNextTransitionAt,
    accountListProjectionSortLastUsedAt,
    ...payload
  } = item
  return {
    viewerSystemAccountId: scope.viewerSystemAccountId,
    accountId: item.id,
    concurrencyAccountId: item.authorizationInstanceSourceAccountId ?? item.id,
    currentConcurrency: Math.max(0, Math.trunc(item.currentConcurrency ?? 0)),
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
    lastUsedAtSortKey: Object.hasOwn(item, 'accountListProjectionSortLastUsedAt')
      ? accountListProjectionSortLastUsedAt ?? undefined
      : item.lastUsedAt,
    createdAtSortKey: scope.createdAt,
    payload,
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

type AccountListAvailabilityProjectionMaterializedItem = AccountListItem & {
  authorizationInstanceSourceAccountAvailabilitySchedule?: AccountListItem['availabilitySchedule']
  accountListProjectionNextTransitionAt?: string
  accountListProjectionSortLastUsedAt?: string | null
}

function nextTransitionAt(item: AccountListAvailabilityProjectionMaterializedItem, now: Date): string | undefined {
  const nowMs = now.getTime()
  const candidates = [
    item.accountExpiresAt,
    item.cooldownUntil,
    item.authorizationExpiresAt,
    item.authorizationInstanceSourceAccountExpiresAt,
    item.authorizationInstanceSourceAccountCooldownUntil,
    item.apiKeyRuntime?.nextProbeAt,
    item.runtimeAvailability?.probePresentation?.schedule.nextAttemptAt,
    item.runtimeAvailability?.probePresentation?.recoveryAt,
    item.availabilityPresentation?.statusBoundary?.at,
    item.accountListProjectionNextTransitionAt,
    nextAccountAvailabilityScheduleCheckAt(item.availabilitySchedule, now),
    nextAccountAvailabilityScheduleCheckAt(item.authorizationInstanceSourceAccountAvailabilitySchedule, now)
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

function boundedProjectionBatchesPerRun(value: number): number {
  if (!Number.isInteger(value) || value < 1 || value > maximumProjectionBatchesPerRun) {
    throw new Error(`账户列表投影每轮批次数必须在 1-${maximumProjectionBatchesPerRun} 之间`)
  }
  return value
}

function boundedProjectionWorkerConcurrency(value: number): number {
  if (!Number.isInteger(value) || value < 1 || value > maximumProjectionWorkerConcurrency) {
    throw new Error(`账户列表投影 worker 并发必须在 1-${maximumProjectionWorkerConcurrency} 之间`)
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
    runtimeDependencyUnavailable: 0,
    runtimeRecoveryEnqueued: 0,
    runtimeRecoveryCompleted: 0,
    runtimeOverlayReconciled: 0,
    viewerHealthBootstrapped: 0,
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

function addMaintenanceResult(
  target: AccountListAvailabilityProjectionMaintenanceResult,
  source: AccountListAvailabilityProjectionMaintenanceResult
): void {
  target.runtimeDependencyUnavailable += source.runtimeDependencyUnavailable
  target.runtimeRecoveryEnqueued += source.runtimeRecoveryEnqueued
  target.runtimeRecoveryCompleted += source.runtimeRecoveryCompleted
  target.runtimeOverlayReconciled += source.runtimeOverlayReconciled
  target.viewerHealthBootstrapped += source.viewerHealthBootstrapped
  target.bootstrapped += source.bootstrapped
  target.dueEnqueued += source.dueEnqueued
  target.staleEnqueued += source.staleEnqueued
  target.claimed += source.claimed
  target.projected += source.projected
  target.deleted += source.deleted
  target.staleClaims += source.staleClaims
  target.released += source.released
}

import { accountSummaryWithEffectiveAvailability } from '../../domain/account-effective-availability.js'
import { publicAccountRuntimeAvailability } from '../../domain/account-runtime-availability-public.js'
import type { AccountStatus, AccountStatusSnapshotResult } from '../../domain/types.js'
import type { PublicAccountCircuitSummary } from '../../domain/types.js'
import type { AccessScope } from '../../storage/access-scope.js'
import type {
  AccountManagementListPage,
  AccountManagementListResult
} from '../../storage/account-management-list.repository.js'
import {
  accountBalanceSnapshotMatchesConfiguration,
  loadAccountBalanceSnapshotRecordsByAccountIdsAsync
} from '../../storage/account-balance.repository.js'
import {
  hydrateAccountManagementStatusFilterSeedsAsync,
  hydrateAccountManagementStatusSeedsAsync,
  listAccountStatusProjectionsAsync
} from '../../storage/account-status-snapshot.repository.js'
import { loadAccountApiKeyRuntimeSummariesByAccountIdsAsync } from '../../storage/account-api-key-runtime-state.repository.js'
import { isAccountBalanceSnapshotSuppressed } from './account-balance-snapshot-cleanup.service.js'
import { loadAccountConcurrencyByIds, loadAccountRuntimeAvailabilityByKeys } from '../gateway/runtime/runtime-snapshot.service.js'
import { loadPublicAccountCircuitSummaries } from '../gateway/runtime/account-circuit-control-plane-bridge.js'

const maxSnapshotAccountIds = 100
const maxSnapshotQueryLength = 8192

export interface AccountRuntimeStatusFilterCandidate {
  id: string
  status: AccountStatus
  schedulable: boolean
  authorizationQuotaExceeded?: boolean
  effectiveAvailability: AccountStatusSnapshotResult['items'][number]['effectiveAvailability']
  circuitSummary?: PublicAccountCircuitSummary
}

export async function hydrateAccountListPage(
  access: AccessScope | undefined,
  page: AccountManagementListPage
): Promise<AccountManagementListResult> {
  const { statusSeeds, ...listPage } = page
  if (page.items.length === 0) {
    return { ...listPage, items: [], generatedAt: new Date().toISOString() }
  }
  const snapshot = statusSeeds.length === page.items.length
    ? await getAccountStatusSnapshotFromProjections(await hydrateAccountManagementStatusSeedsAsync(statusSeeds))
    : await getAccountStatusSnapshot(access, page.items.map((item) => item.id))
  const snapshotById = new Map(snapshot.items.map((item) => [item.id, item]))
  return {
    ...listPage,
    generatedAt: snapshot.generatedAt,
    items: page.items.map((item) => ({
      ...item,
      ...snapshotById.get(item.id),
      currentConcurrency: snapshotById.get(item.id)?.currentConcurrency ?? 0
    } as AccountManagementListResult['items'][number]))
  }
}

export async function hydrateAccountRuntimeStatusFilterCandidates(
  page: Pick<AccountManagementListPage, 'statusSeeds'>
): Promise<{
  generatedAt: string
  items: AccountRuntimeStatusFilterCandidate[]
}> {
  const projections = await hydrateAccountManagementStatusFilterSeedsAsync(page.statusSeeds)
  const [runtime, circuits, apiKeyRuntimeByAccountId] = await Promise.all([
    loadAccountRuntimeAvailabilityByKeys(projections.map((item) => item.runtimeKey)),
    loadPublicAccountCircuitSummaries(projections.map((item) => item.runtimeKey))
      .then((values) => ({ available: true, values }))
      .catch(() => ({ available: false, values: {} as Record<string, PublicAccountCircuitSummary> })),
    loadAccountApiKeyRuntimeSummariesByAccountIdsAsync(
      projections.map((item) => item.authorizationInstanceSourceAccountId ?? item.id)
    )
  ])
  return {
    generatedAt: new Date().toISOString(),
    items: projections.map((projection) => {
      const withAvailability = accountSummaryWithEffectiveAvailability({
        ...projection,
        apiKeyRuntime: apiKeyRuntimeByAccountId.get(projection.authorizationInstanceSourceAccountId ?? projection.id),
        runtimeAvailability: runtime.values[projection.runtimeKey]
      })
      return {
        id: projection.id,
        status: projection.status,
        schedulable: projection.schedulable,
        authorizationQuotaExceeded: projection.authorizationQuotaExceeded,
        effectiveAvailability: withAvailability.effectiveAvailability,
        circuitSummary: circuits.values[projection.runtimeKey]
      }
    })
  }
}

export function parseAccountStatusSnapshotAccountIds(value: unknown): string[] {
  const raw = typeof value === 'string' ? value : ''
  if (raw.length > maxSnapshotQueryLength) throw new Error('账户状态快照查询参数过长')
  const ids = [...new Set(raw.split(',').map((item) => item.trim()).filter(Boolean))]
  if (ids.length === 0) throw new Error('账户状态快照至少选择 1 个账户')
  if (ids.length > maxSnapshotAccountIds) throw new Error('账户状态快照最多查询 100 个账户')
  return ids
}

export async function getAccountStatusSnapshot(
  access: AccessScope | undefined,
  accountIds: string[]
): Promise<AccountStatusSnapshotResult> {
  const projections = await listAccountStatusProjectionsAsync(access, accountIds)
  return getAccountStatusSnapshotFromProjections(projections)
}

async function getAccountStatusSnapshotFromProjections(
  projections: Awaited<ReturnType<typeof listAccountStatusProjectionsAsync>>
): Promise<AccountStatusSnapshotResult> {
  const ownerIds = projections
    .filter((item) => item.accessType !== 'authorized' && item.balanceQueryEnabled === true)
    .map((item) => item.id)
  const [runtime, concurrency, circuits, balanceSnapshots, apiKeyRuntimeByAccountId] = await Promise.all([
    loadAccountRuntimeAvailabilityByKeys(projections.map((item) => item.runtimeKey)),
    loadAccountConcurrencyByIds(projections.map((item) => item.concurrencyAccountId)),
    loadPublicAccountCircuitSummaries(projections.map((item) => item.runtimeKey))
      .then((values) => ({ available: true, values }))
      .catch(() => ({ available: false, values: {} as Record<string, PublicAccountCircuitSummary> })),
    loadAccountBalanceSnapshotRecordsByAccountIdsAsync(ownerIds),
    loadAccountApiKeyRuntimeSummariesByAccountIdsAsync(
      projections.map((item) => item.authorizationInstanceSourceAccountId ?? item.id)
    )
  ])
  return {
    generatedAt: new Date().toISOString(),
    runtimeSnapshot: {
      accountConcurrencyAvailable: concurrency.available,
      accountRuntimeAvailabilityAvailable: runtime.available,
      accountCircuitSummaryAvailable: circuits.available
    },
    items: projections.map((projection) => {
      const {
        runtimeKey,
        concurrencyAccountId,
        permissions,
        accessType,
        boundGroupId,
        groupBindStatus,
        balanceQueryEnabled,
        balanceQueryNextRefreshAt,
        sourceAccountProbe: _sourceAccountProbe,
        accountExpiresAt: _accountExpiresAt,
        cooldownUntil: _cooldownUntil,
        lastErrorCode: _lastErrorCode,
        lastErrorMessage: _lastErrorMessage,
        lastErrorTraceId: _lastErrorTraceId,
        cooldownRetestFailureCount: _cooldownRetestFailureCount,
        cooldownRetestObservationStartedAt: _cooldownRetestObservationStartedAt,
        cooldownRetestLastAt: _cooldownRetestLastAt,
        cooldownRetestLastStatusCode: _cooldownRetestLastStatusCode,
        lastHealthCheckAt: _lastHealthCheckAt,
        nextHealthCheckAt: _nextHealthCheckAt,
        lastHealthSuccessAt: _lastHealthSuccessAt,
        healthCheckFailureCount: _healthCheckFailureCount,
        healthCheckFailureStartedAt: _healthCheckFailureStartedAt,
        lastHealthCheckStatusCode: _lastHealthCheckStatusCode,
        lastHealthCheckErrorCode: _lastHealthCheckErrorCode,
        lastHealthCheckErrorMessage: _lastHealthCheckErrorMessage,
        lastHealthCheckTraceId: _lastHealthCheckTraceId,
        streamFailureCount: _streamFailureCount,
        streamFailureWindowStartedAt: _streamFailureWindowStartedAt,
        authorizationInstanceSourceAccountLastErrorCode: _authorizationInstanceSourceAccountLastErrorCode,
        authorizationInstanceSourceAccountLastErrorMessage: _authorizationInstanceSourceAccountLastErrorMessage,
        authorizationInstanceSourceAccountLastErrorTraceId: _authorizationInstanceSourceAccountLastErrorTraceId,
        authorizationInstanceSourceAccountExpiresAt: _authorizationInstanceSourceAccountExpiresAt,
        authorizationInstanceSourceAccountCooldownUntil: _authorizationInstanceSourceAccountCooldownUntil,
        authorizationInstanceSourceAccountStatus: _authorizationInstanceSourceAccountStatus,
        authorizationInstanceSourceAccountSchedulable: _authorizationInstanceSourceAccountSchedulable,
        ...visibleProjection
      } = projection
      const publicRuntimeAvailability = publicAccountRuntimeAvailability(runtime.values[runtimeKey])
      const balanceConfiguration = { nextRefreshAt: balanceQueryNextRefreshAt }
      const balanceSnapshotRecord = balanceSnapshots.get(visibleProjection.id)
      const apiKeyRuntimeAccountId = projection.authorizationInstanceSourceAccountId ?? projection.id
      const withAvailability = accountSummaryWithEffectiveAvailability({
        ...projection,
        permissions,
        accessType,
        boundGroupId,
        groupBindStatus,
        sourceAccountProbe: projection.sourceAccountProbe,
        apiKeyRuntime: apiKeyRuntimeByAccountId.get(apiKeyRuntimeAccountId),
        runtimeAvailability: runtime.values[runtimeKey]
      })
      return {
        ...visibleProjection,
        balanceQueryEnabled: balanceQueryEnabled || undefined,
        balanceQueryNextRefreshAt,
        balanceSnapshot: balanceQueryEnabled
          && !isAccountBalanceSnapshotSuppressed(visibleProjection.id, { configuration: balanceConfiguration, snapshotRecord: balanceSnapshotRecord })
          && accountBalanceSnapshotMatchesConfiguration(balanceConfiguration, balanceSnapshotRecord)
          ? balanceSnapshotRecord.snapshot
          : undefined,
        currentConcurrency: concurrency.values[concurrencyAccountId] ?? 0,
        runtimeAvailability: publicRuntimeAvailability,
        circuitSummary: circuits.values[runtimeKey],
        availabilityPresentation: withAvailability.availabilityPresentation,
        effectiveAvailability: withAvailability.effectiveAvailability
      }
    })
  }
}

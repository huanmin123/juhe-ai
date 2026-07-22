import { accountSummaryWithEffectiveAvailability } from '../../domain/account-effective-availability.js'
import { publicAccountRuntimeAvailability } from '../../domain/account-runtime-availability-public.js'
import type { AccountStatusSnapshotResult } from '../../domain/types.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { listAccountStatusProjectionsAsync } from '../../storage/account-status-snapshot.repository.js'
import { loadAccountConcurrencyByIds, loadAccountRuntimeAvailabilityByKeys } from '../gateway/runtime/runtime-snapshot.service.js'

const maxSnapshotAccountIds = 100
const maxSnapshotQueryLength = 8192

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
  const [runtime, concurrency] = await Promise.all([
    loadAccountRuntimeAvailabilityByKeys(projections.map((item) => item.runtimeKey)),
    loadAccountConcurrencyByIds(projections.map((item) => item.concurrencyAccountId))
  ])
  return {
    generatedAt: new Date().toISOString(),
    runtimeSnapshot: {
      accountConcurrencyAvailable: concurrency.available,
      accountRuntimeAvailabilityAvailable: runtime.available
    },
    items: projections.map(({ runtimeKey, concurrencyAccountId, permissions: _permissions, accessType: _accessType, boundGroupId: _boundGroupId, groupBindStatus: _groupBindStatus, ...projection }) => {
      const publicRuntimeAvailability = publicAccountRuntimeAvailability(runtime.values[runtimeKey])
      const withAvailability = accountSummaryWithEffectiveAvailability({
        ...projection,
        permissions: _permissions,
        accessType: _accessType,
        boundGroupId: _boundGroupId,
        groupBindStatus: _groupBindStatus,
        runtimeAvailability: runtime.values[runtimeKey]
      })
      return {
        ...projection,
        currentConcurrency: concurrency.values[concurrencyAccountId] ?? 0,
        runtimeAvailability: publicRuntimeAvailability,
        availabilityPresentation: withAvailability.availabilityPresentation,
        effectiveAvailability: withAvailability.effectiveAvailability
      }
    })
  }
}

import { isDeepStrictEqual } from 'node:util'

import type { AccessScope } from '../../storage/access-scope.js'
import {
  AccountListAvailabilityProjectionUnavailableError,
  listAccountListAvailabilityProjectionPageInClient,
  type AccountListAvailabilityProjectionPage
} from '../../storage/account-list-availability-projection.repository.js'
import { listAccountManagementItemsPageAsync, type AccountManagementListResult } from '../../storage/account-management-list.repository.js'
import type { AccountListOptions } from '../../storage/account-list-options.js'
import type { DatabaseClient } from '../../storage/database-client.js'
import { AccountRuntimeStatusFilterScanLimitError, accountListNeedsRuntimeStatusFilter, listAccountsPageWithRuntimeStatusFilter } from './account-list-runtime-status-filter.js'
import { hydrateAccountListPage } from './account-status-snapshot.service.js'

export interface AccountListAvailabilityProjectionShadowResult {
  outcome: 'equal' | 'different' | 'projection_unavailable' | 'legacy_unavailable'
  legacyItemIds: string[]
  projectionItemIds: string[]
  legacyHasMore?: boolean
  projectionHasMore?: boolean
  legacyTotal?: number
  projectionTotal?: number
  reason?: string
}

/**
 * The shadow reader never changes the response path. It executes the legacy
 * semantics and the read model independently, then returns an auditable
 * equality result. An unavailable projection is a failed comparison, never a
 * request-side fallback to the legacy candidate scan.
 */
export async function shadowCompareAccountListAvailabilityProjectionPage(
  client: DatabaseClient,
  input: {
    access: AccessScope
    options: AccountListOptions
  }
): Promise<AccountListAvailabilityProjectionShadowResult> {
  let legacy: AccountManagementListResult
  try {
    legacy = await loadLegacyAccountListPage(input.access, input.options)
  } catch (error) {
    const reason = error instanceof Error ? error.message : String(error)
    return {
      outcome: 'legacy_unavailable',
      legacyItemIds: [],
      projectionItemIds: [],
      reason
    }
  }

  let projection: AccountListAvailabilityProjectionPage
  try {
    projection = await listAccountListAvailabilityProjectionPageInClient(client, {
      viewerSystemAccountId: input.access.systemAccountId,
      options: input.options
    })
  } catch (error) {
    if (!(error instanceof AccountListAvailabilityProjectionUnavailableError)) throw error
    return {
      outcome: 'projection_unavailable',
      legacyItemIds: legacy.items.map((item) => item.id),
      projectionItemIds: [],
      legacyHasMore: legacy.hasMore,
      legacyTotal: legacy.total,
      reason: error.message
    }
  }

  const legacyItemIds = legacy.items.map((item) => item.id)
  const projectionItemIds = projection.items.map((item) => item.id)
  const structuralEqual = legacy.hasMore === projection.hasMore
    && legacy.total === projection.total
    && jsonResponseEqual(legacy.items, projection.items)
  return {
    outcome: structuralEqual ? 'equal' : 'different',
    legacyItemIds,
    projectionItemIds,
    legacyHasMore: legacy.hasMore,
    projectionHasMore: projection.hasMore,
    legacyTotal: legacy.total,
    projectionTotal: projection.total,
    reason: structuralEqual ? undefined : projectionComparisonDifference(legacy, projection)
  }
}

function projectionComparisonDifference(
  legacy: AccountManagementListResult,
  projection: AccountListAvailabilityProjectionPage
): string {
  if (legacy.hasMore !== projection.hasMore || legacy.total !== projection.total) {
    return `分页元数据不一致：legacy(hasMore=${legacy.hasMore}, total=${legacy.total}) `
      + `projection(hasMore=${projection.hasMore}, total=${projection.total})`
  }
  const maxLength = Math.max(legacy.items.length, projection.items.length)
  for (let index = 0; index < maxLength; index += 1) {
    const legacyItem = legacy.items[index]
    const projectionItem = projection.items[index]
    if (!legacyItem || !projectionItem) {
      return `列表长度不一致：legacy=${legacy.items.length}, projection=${projection.items.length}`
    }
    if (legacyItem.id !== projectionItem.id) {
      return `第 ${index + 1} 项 ID 不一致：legacy=${legacyItem.id}, projection=${projectionItem.id}`
    }
    if (!jsonResponseEqual(legacyItem, projectionItem)) {
      return `账户 ${legacyItem.id} 快照字段不一致：legacy=${JSON.stringify(legacyItem)}, projection=${JSON.stringify(projectionItem)}`
    }
  }
  return '分页元数据或完整 AccountListItem 快照不一致'
}

/** HTTP JSON omits undefined and does not preserve object key insertion order. */
function jsonResponseEqual(left: unknown, right: unknown): boolean {
  return isDeepStrictEqual(
    JSON.parse(JSON.stringify(left)),
    JSON.parse(JSON.stringify(right))
  )
}

async function loadLegacyAccountListPage(
  access: AccessScope,
  options: AccountListOptions
): Promise<AccountManagementListResult> {
  try {
    if (accountListNeedsRuntimeStatusFilter(options)) {
      const filtered = await listAccountsPageWithRuntimeStatusFilter(access, options)
      if (!filtered) throw new Error('旧账户动态筛选未返回分页结果')
      return filtered
    }
    const page = await listAccountManagementItemsPageAsync(access, options)
    return await hydrateAccountListPage(access, page)
  } catch (error) {
    if (error instanceof AccountRuntimeStatusFilterScanLimitError) throw error
    throw error
  }
}

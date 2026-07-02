import { runtimeConfig } from '../config/runtime.js'
import { errorLogFields, logger } from '../shared/logger.js'
import type { DatabaseClient } from './database-client.js'
import {
  markAllGroupAccountStatsDirty,
  markAllGroupAccountStatsDirtyAsync,
  markGroupAccountStatsDirty,
  markGroupAccountStatsDirtyByAccountIds,
  markGroupAccountStatsDirtyByAccountIdsAsync,
  markGroupAccountStatsDirtyAsync
} from './usage-stats.repository.js'

export function refreshGroupAccountStatsAfterWrite(input: {
  groupIds?: Array<string | null | undefined>
  accountIds?: Array<string | null | undefined>
  all?: boolean
  reason?: string
} = {}): void {
  if (runtimeConfig.databaseDriver === 'postgres') {
    markPostgresGroupAccountStatsDirtyInBackground(input)
    return
  }
  const reason = input.reason ?? 'business_write'
  if (input.all) {
    markAllGroupAccountStatsDirty(reason)
    return
  }
  if (input.groupIds?.length) {
    markGroupAccountStatsDirty(input.groupIds, reason)
  }
  if (input.accountIds?.length) {
    markGroupAccountStatsDirtyByAccountIds(input.accountIds, reason)
  }
  if (!input.groupIds?.length && !input.accountIds?.length) {
    markAllGroupAccountStatsDirty(reason)
  }
}

export async function refreshGroupAccountStatsAfterWriteAsync(
  input: {
    groupIds?: Array<string | null | undefined>
    accountIds?: Array<string | null | undefined>
    all?: boolean
    reason?: string
  } = {},
  client?: DatabaseClient
): Promise<void> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    refreshGroupAccountStatsAfterWrite(input)
    return
  }
  const reason = input.reason ?? 'business_write'
  if (input.all) {
    await markAllGroupAccountStatsDirtyAsync(reason, client)
    return
  }
  if (input.groupIds?.length) {
    await markGroupAccountStatsDirtyAsync(input.groupIds, reason, client)
  }
  if (input.accountIds?.length) {
    await markGroupAccountStatsDirtyByAccountIdsAsync(input.accountIds, reason, client)
  }
  if (!input.groupIds?.length && !input.accountIds?.length) {
    await markAllGroupAccountStatsDirtyAsync(reason, client)
  }
}

function markPostgresGroupAccountStatsDirtyInBackground(input: {
  groupIds?: Array<string | null | undefined>
  accountIds?: Array<string | null | undefined>
  all?: boolean
  reason?: string
}): void {
  void refreshGroupAccountStatsAfterWriteAsync(input).catch((error) => {
    logger.error(errorLogFields(error, {
      event: 'postgres_group_account_stats_dirty_mark_failed',
      reason: input.reason ?? 'business_write'
    }), 'PostgreSQL 分组账户统计脏标记写入失败')
  })
}

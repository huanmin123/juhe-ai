import type { DbServiceOperation } from './db-service-types.js'

export type DbServiceOperationPriority = 'high' | 'normal' | 'low'

const lowPriorityOperationTypes = new Set<DbServiceOperation['type']>([
  'account_test_task_maintenance',
  'cleanup_expired_codex_context_states',
  'cleanup_expired_deleted_accounts',
  'cleanup_expired_system_sessions',
  'delete_group_account_stats_dirty_rows',
  'expire_due_resource_authorizations',
  'mark_all_group_account_stats_dirty',
  'sync_account_availability_schedule_statuses',
  'sync_api_key_availability_schedule_statuses',
  'update_group_account_stats_all_cursor'
])

export function dbServiceOperationPriority(operation: DbServiceOperation): DbServiceOperationPriority {
  return lowPriorityOperationTypes.has(operation.type) ? 'low' : 'high'
}

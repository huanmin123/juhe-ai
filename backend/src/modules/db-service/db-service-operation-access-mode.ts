import type { DatabaseDriver } from '../../config/runtime.js'
import type { DbServiceOperation } from './db-service-types.js'

export type DbServiceOperationAccessMode = 'read' | 'write' | 'maintenance' | 'runtime'

export const dbServiceOperationAccessModeByType = {
  list_public_global_settings: 'read',
  validate_gateway_api_key: 'read',
  read_gateway_settings: 'read',
  resolve_group_usage_access: 'read',
  list_openai_accounts_for_group: 'read',
  list_openai_accounts_for_group_result: 'read',
  find_openai_account_for_group: 'read',
  list_recoverable_unavailable_openai_accounts_for_group: 'read',
  read_gateway_runtime: 'read',
  create_openai_compatible_file: 'write',
  list_openai_compatible_files: 'read',
  get_openai_compatible_file: 'read',
  delete_openai_compatible_file: 'write',
  create_openai_compatible_vector_store: 'write',
  list_openai_compatible_vector_stores: 'read',
  get_openai_compatible_vector_store: 'read',
  delete_openai_compatible_vector_store: 'write',
  create_openai_compatible_vector_store_file: 'write',
  list_openai_compatible_vector_store_files: 'read',
  get_openai_compatible_vector_store_file: 'read',
  delete_openai_compatible_vector_store_file: 'write',
  search_openai_compatible_vector_store: 'read',
  list_openai_compatible_vector_store_file_chunks: 'read',
  list_provider_model_catalog: 'read',
  check_api_key_quota: 'read',
  read_api_key_quota_costs: 'read',
  check_authorization_quota: 'read',
  check_authorization_quota_batch: 'read',
  update_openai_oauth_credentials: 'write',
  find_openai_oauth_account_for_refresh: 'read',
  mark_openai_oauth_local_configuration_exception: 'write',
  persist_openai_codex_usage_headers: 'write',
  apply_account_error_handling: 'write',
  record_account_api_key_failure: 'write',
  record_account_api_key_success: 'write',
  defer_account_api_key_probe: 'write',
  record_account_stream_failure: 'write',
  clear_account_stream_failure_state: 'write',
  mark_account_precheck_temporary_unavailable: 'write',
  mark_account_temporary_unavailable: 'write',
  clear_account_failure_state: 'write',
  mark_account_test_temporary_unavailable: 'write',
  find_account_for_test: 'read',
  list_accounts_due_for_health_check: 'maintenance',
  find_account_for_health_check: 'maintenance',
  record_account_health_check_success: 'write',
  commit_account_balance_refresh: 'write',
  enable_detected_account_balance_query: 'write',
  record_account_health_check_failure: 'write',
  list_accounts_due_for_cooldown_retest: 'maintenance',
  list_account_api_key_runtime_states_due_for_probe: 'maintenance',
  account_api_key_pool_probe_cursor: 'maintenance',
  find_account_for_cooldown_retest: 'maintenance',
  record_cooldown_account_retest_success: 'write',
  record_cooldown_account_retest_failure: 'write',
  defer_cooldown_account_retest: 'write',
  mark_account_exception: 'write',
  update_proxy_test_state: 'write',
  mark_all_group_account_stats_dirty: 'maintenance',
  delete_group_account_stats_dirty_rows: 'maintenance',
  update_group_account_stats_all_cursor: 'maintenance',
  sync_api_key_availability_schedule_statuses: 'maintenance',
  sync_account_availability_schedule_statuses: 'maintenance',
  expire_due_resource_authorizations: 'maintenance',
  cleanup_expired_deleted_accounts: 'maintenance',
  cleanup_expired_system_sessions: 'maintenance',
  advance_account_circuit_dispatch_revision: 'write',
  compare_and_set_account_circuit_incident: 'write',
  get_account_circuit_incident_by_scope_key: 'read',
  claim_account_circuit_outbox: 'write',
  ack_account_circuit_outbox: 'write',
  release_account_circuit_outbox_for_replay: 'write',
  list_account_circuit_incidents_for_rebuild: 'read',
  list_account_circuit_incidents_by_runtime_keys: 'read',
  list_account_circuit_projection_gaps: 'read',
  cleanup_account_circuit_control_plane: 'maintenance',
  model_quality_command: 'maintenance',
  cleanup_chat_retention: 'maintenance',
  save_codex_context_response_state: 'write',
  save_codex_context_compact_state: 'write',
  read_codex_context_response_chain: 'read',
  read_codex_context_compact_state: 'read',
  cleanup_expired_codex_context_states: 'maintenance',
  settle_codex_context_storage_cleanup: 'maintenance',
  account_test_task_maintenance: 'maintenance',
  mark_account_test_task_running: 'write',
  mark_account_test_task_canceled: 'write',
  complete_account_test_task: 'write',
  fail_account_test_task: 'write',
  update_account_test_task_message: 'write',
  is_account_test_task_cancel_requested: 'read',
  read_account_test_task_cancel_message: 'read',
  clear_gateway_runtime_cache: 'runtime',
  list_active_client_ip_policies: 'read',
  list_active_response_inspection_policies: 'read',
  record_client_ip_policy_hits: 'write',
  list_runtime_logs: 'read',
  get_runtime_log_detail: 'read',
  get_runtime_log_detail_delta: 'read',
  get_runtime_log_facets: 'read',
  status: 'runtime'
} as const satisfies Record<DbServiceOperation['type'], DbServiceOperationAccessMode>

export function dbServiceOperationAccessMode(operation: DbServiceOperation): DbServiceOperationAccessMode {
  return dbServiceOperationAccessModeByType[operation.type]
}

export function isDbServiceReadOperation(operation: DbServiceOperation): boolean {
  return dbServiceOperationAccessMode(operation) === 'read'
}

export function isDbServiceWriteQueueOperation(operation: DbServiceOperation): boolean {
  const accessMode = dbServiceOperationAccessMode(operation)
  return accessMode === 'write' || accessMode === 'maintenance'
}

export function shouldQueueDbServiceOperationForDriver(
  operation: DbServiceOperation,
  databaseDriver: DatabaseDriver
): boolean {
  if (databaseDriver === 'postgres') {
    return false
  }
  return isDbServiceWriteQueueOperation(operation)
}

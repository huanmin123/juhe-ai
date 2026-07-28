import type { AccountListOptions, AccountOptionListOptions } from './account-list-options.js'
import type { ModelCheckAccountOptionListOptions } from './account-options.repository.js'
import type { ModelCheckAccountOption } from '../domain/types.js'
import type { ManagementSettingsSectionKey } from './settings.repository.js'
import type { AccountListResult } from './account-summary.repository.js'
import type { AccountManagementListPage } from './account-management-list.repository.js'
import type { AccountStatusProjection } from './account-status-snapshot.repository.js'
import type { AccountTagSummary } from './account-tags.repository.js'
import type { AccessScope } from './access-scope.js'
import type { AnnouncementListOptions, AnnouncementListResult } from './announcements.repository.js'
import type { ApiKeyListItem, ApiKeyListOptions, ApiKeyListResult, ApiKeySecretRecord } from './api-key.repository.js'
import type {
  AuditErrorGroupListOptions,
  AuditErrorGroupListResult,
  AuditLogDetail,
  AuditLogListOptions,
  AuditLogListResult,
  AuditLogPayloadDetail,
  AuditLogPayloadReadOptions,
  AuditLogListItem
} from './audit-logs.repository.js'
import type {
  AuthorizationGranteeGroupOptionListOptions,
  AuthorizationPrincipalOptionListOptions
} from './authorization-options.repository.js'
import type {
  AuthorizationUsageFilters,
  AuthorizationUsagePageOptions
} from './authorization-usage.repository.js'
import type { GroupListOptions, GroupOptionListOptions } from './group-read.repository.js'
import type {
  AccountSummary,
  AccountOptionSummary,
  AccountTestSession,
  AccountTestTask,
  AccountGroupOptionSummary,
  AccountUsageStatsListResult,
  AccountUsageStatsRange,
  AnnouncementSummary,
  PublicAnnouncementDetail,
  PublicAnnouncementListItem,
  AiPerformanceBase,
  AiHealthListResult,
  AiPerformanceAccountOption,
  AiPerformanceOverview,
  AiPerformanceSeries,
  ApiKeySummary,
  AuthorizationTeamUsageRowsResult,
  AuthorizationTeamUsageSummary,
  AuthorizationUserUsageRowsResult,
  AuthorizationUserUsageSummary,
  AuthorizationGranteeGroupOptionSummary,
  GatewayRequestEndpointFamily,
  GroupListResult,
  GroupEditDetail,
  GroupOptionSummary,
  GroupSummary,
  ModelCheckRunDetail,
  ModelCheckRunListResult,
  ProviderDefinition,
  ProviderCode,
  ProviderProtocolProfileDefinition,
  ResourceAuthorizationListResult,
  ResourceAuthorizationSummary,
  SystemTeamListResult,
  SystemTeamListItem,
  SystemTeamDetail,
  SystemTeamPrincipalSummary,
  SystemTeamSummary,
  RouteStrategyListItemResult,
  RouteStrategyListSnapshotResult,
  RouteStrategyListResult,
  RouteStrategyEditBasicDetail,
  RouteStrategyOptionSummary,
  RouteStrategySummary,
  SystemAccountOptionSummary,
  SystemAccountPrincipalSummary,
  SystemAccountSummary
} from '../domain/types.js'
import type {
  ClientIpStatsDetailOptions,
  ClientIpStatsDetailResult,
  ClientIpStatsListOptions,
  ClientIpStatsListResult,
  ActiveClientIpPolicy
} from './client-ip-stats.repository.js'
import type {
  ExternalIntegrationSourceListOptions,
  ExternalIntegrationSourceListResult,
  ExternalIntegrationSourceSummary,
  ExternalIntegrationSourceTokenRow,
  ExternalIntegrationSourceTokenSecret
} from './external-integration-source.repository.js'
import type { ModelCheckRunListOptions } from './model-checks.repository.js'
import type {
  OperationLogDetail,
  OperationLogListOptions,
  OperationLogListResult
} from './operation-logs.repository.js'
import type {
  PublicApiLogDetail,
  PublicApiLogListOptions,
  PublicApiLogListResult
} from './public-api-logs.repository.js'
import type {
  ProxyProfileListOptions,
  ProxyProfileListResult,
  ProxyProfileOptionListOptions,
  ProxyProfileOptionSummary,
  ProxyProfileSummary
} from './proxy.repository.js'
import type {
  ResourceAuthorizationListOptions
} from './resource-authorization-read.repository.js'
import type { ResourceAuthorizationUsageOptions } from './resource-authorization-usage.repository.js'
import type { RouteStrategyListOptions, RouteStrategyOptionListOptions } from './route-strategy.repository.js'
import type { RuntimeLogDetail, RuntimeLogFacets, RuntimeLogListOptions, RuntimeLogListResult } from './runtime-logs.repository.js'
import type { SystemTeamListOptions } from './system-team.repository.js'
import type { SystemAccountListOptions, SystemAccountListResult, SystemAccountOptionListOptions } from './system-accounts.repository.js'
import type { SessionWithAccount } from './system-accounts.repository.js'
import type {
  DatabaseStorageHistoryPoint,
  DatabaseStorageSnapshotSummary,
  MonitoredDatabaseRole,
  TableStorageOverview,
  TableStorageSnapshotSummary
} from './table-monitor.repository.js'
import type {
  GroupUsageAccessMetadata,
  OpenAIAccountSecret,
  OpenAIAccountsForGroupResult
} from './openai-account-selector.types.js'
import type {
  SystemMetricsOverview,
  UsageStatsOverview,
  UsageStatsOverviewDailyTrendResult,
  UsageStatsOverviewErrorsResult,
  UsageStatsOverviewHourlyTrendResult,
  UsageStatsOverviewModelDistributionResult,
  UsageStatsOverviewSummaryResult
} from './usage-stats.repository.js'
import type { UsageRecordListOptions, UsageRecordListResult, UsageRecordSummary } from './usage-records.repository.js'
import type {
  ResponseInspectionPolicyDetail,
  ResponseInspectionPolicyListResult,
  ResponseInspectionPolicyProviderOption,
  ResponseInspectionPolicySummary
} from './response-inspection-policy.repository.js'
import type { ModelCatalogListOptions, ProviderModelCatalogItem } from '../modules/model-pricing/model-catalog.service.js'
import type { AuthorizationQuotaDecision } from '../modules/gateway/quota/authorization-quota.service.js'
import type { ApiKeyQuotaDecision } from '../modules/gateway/quota/api-key-quota.service.js'
import type { RequestQuotaCosts } from '../modules/gateway/quota/request-quota-checker.js'
import type { DbServiceGatewayRuntime } from '../modules/db-service/db-service-types.js'
import type { GatewayApiKeyRow } from './gateway-api-key.repository.js'
import type { GatewaySettings } from '../modules/gateway/policy/account-error-policy.service.js'
import type {
  OpenAICompatibleFileListOptions,
  OpenAICompatibleFileListResult,
  OpenAICompatibleFileRecord
} from './openai-compatible-files.repository.js'
import type {
  OpenAICompatibleVectorStoreFileChunkRecord,
  OpenAICompatibleVectorStoreFileListOptions,
  OpenAICompatibleVectorStoreFileListResult,
  OpenAICompatibleVectorStoreFileRecord,
  OpenAICompatibleVectorStoreListOptions,
  OpenAICompatibleVectorStoreListResult,
  OpenAICompatibleVectorStoreRecord,
  OpenAICompatibleVectorStoreSearchOptions,
  OpenAICompatibleVectorStoreSearchResult
} from './openai-compatible-vector-stores.repository.js'

export type ProviderDefaultHealthCheckModelPreferenceEntries = Array<[string, string]>

export type SqliteReadWorkerOperation =
  | {
    type: 'list_accounts_page_read_only'
    access?: AccessScope
    options?: AccountListOptions
  }
  | {
    type: 'list_account_items_page_read_only'
    access?: AccessScope
    options?: AccountListOptions
  }
  | {
    type: 'list_account_management_items_page_read_only'
    access?: AccessScope
    options?: AccountListOptions
    candidateLimit?: number
  }
  | {
    type: 'list_account_status_snapshots_read_only'
    accountIds: string[]
    access?: AccessScope
  }
  | {
    type: 'find_account_summary_read_only'
    accountId: string
    access?: AccessScope
  }
  | {
    type: 'list_account_options_read_only'
    access?: AccessScope
    options?: AccountOptionListOptions
  }
  | {
    type: 'list_model_check_account_options_read_only'
    access?: AccessScope
    options: ModelCheckAccountOptionListOptions
  }
  | {
    type: 'list_account_tags_read_only'
    access?: AccessScope
  }
  | {
    type: 'get_account_test_session_read_only'
    id: string
    access?: AccessScope
  }
  | {
    type: 'get_account_test_task_read_only'
    id: string
    access?: AccessScope
  }
  | {
    type: 'list_account_test_tasks_read_only'
    ids: string[]
    access?: AccessScope
  }
  | {
    type: 'is_account_test_task_cancel_requested_read_only'
    id: string
  }
  | {
    type: 'read_account_test_task_cancel_message_read_only'
    id: string
  }
  | {
    type: 'list_resource_authorizations_page_read_only'
    filters?: Record<string, unknown>
    access?: AccessScope
    options?: ResourceAuthorizationListOptions
  }
  | {
    type: 'find_resource_authorization_read_only'
    id: string
    access?: AccessScope
    options?: ResourceAuthorizationListOptions
  }
  | {
    type: 'get_resource_authorization_usage_read_only'
    id: string
    access?: AccessScope
    options?: ResourceAuthorizationUsageOptions
  }
  | {
    type: 'list_authorization_grantee_accounts_read_only'
    access?: AccessScope
    options?: AuthorizationPrincipalOptionListOptions
  }
  | {
    type: 'list_authorization_grantee_teams_read_only'
    access?: AccessScope
    options?: AuthorizationPrincipalOptionListOptions
  }
  | {
    type: 'list_authorization_grantee_groups_read_only'
    access?: AccessScope
    options?: AuthorizationGranteeGroupOptionListOptions
  }
  | {
    type: 'get_authorization_team_usage_rows_read_only'
    filters: AuthorizationUsageFilters
    access?: AccessScope
    range: AccountUsageStatsRange
    options?: AuthorizationUsagePageOptions
  }
  | {
    type: 'get_authorization_team_usage_summary_read_only'
    filters: AuthorizationUsageFilters
    access?: AccessScope
    range: AccountUsageStatsRange
  }
  | {
    type: 'get_authorization_user_usage_rows_read_only'
    filters: AuthorizationUsageFilters
    access?: AccessScope
    range: AccountUsageStatsRange
    options?: AuthorizationUsagePageOptions
  }
  | {
    type: 'get_authorization_user_usage_summary_read_only'
    filters: AuthorizationUsageFilters
    access?: AccessScope
    range: AccountUsageStatsRange
  }
  | {
    type: 'list_public_announcements_read_only'
    systemAccountId: string
    limit?: number
  }
  | {
    type: 'find_public_announcement_read_only'
    id: string
  }
  | {
    type: 'list_announcements_page_read_only'
    options?: AnnouncementListOptions
  }
  | {
    type: 'find_announcement_read_only'
    id: string
  }
  | {
    type: 'list_system_teams_read_only'
    access?: AccessScope
  }
  | {
    type: 'list_system_teams_page_read_only'
    access?: AccessScope
    options?: SystemTeamListOptions
  }
  | {
    type: 'find_system_team_summary_read_only'
    id: string
    access?: AccessScope
  }
  | {
    type: 'find_system_team_detail_read_only'
    id: string
    access?: AccessScope
  }
  | {
    type: 'list_usage_records_read_only'
    access?: AccessScope
    options?: UsageRecordListOptions
  }
  | {
    type: 'get_usage_record_detail_read_only'
    id: string
    access?: AccessScope
  }
  | {
    type: 'list_operation_logs_read_only'
    options?: OperationLogListOptions
  }
  | {
    type: 'list_operation_logs_for_viewer_read_only'
    systemAccountId: string
    options?: OperationLogListOptions
  }
  | {
    type: 'get_operation_log_detail_read_only'
    id: string
  }
  | {
    type: 'get_operation_log_detail_for_viewer_read_only'
    id: string
    systemAccountId: string
  }
  | {
    type: 'list_public_api_logs_read_only'
    options?: PublicApiLogListOptions
  }
  | {
    type: 'get_public_api_log_detail_read_only'
    id: string
  }
  | {
    type: 'list_audit_logs_read_only'
    options?: AuditLogListOptions
  }
  | {
    type: 'list_audit_logs_by_ids_read_only'
    ids: string[]
  }
  | {
    type: 'list_audit_error_groups_read_only'
    options?: AuditErrorGroupListOptions
  }
  | {
    type: 'list_audit_error_group_events_read_only'
    errorGroupId: string
    options?: AuditLogListOptions
  }
  | {
    type: 'get_audit_log_detail_read_only'
    id: string
  }
  | {
    type: 'get_audit_log_payload_read_only'
    auditLogId: string
    payloadId: string
    options?: AuditLogPayloadReadOptions
  }
  | {
    type: 'list_client_ip_stats_read_only'
    options?: ClientIpStatsListOptions
  }
  | {
    type: 'get_client_ip_stats_detail_read_only'
    options: ClientIpStatsDetailOptions
  }
  | {
    type: 'get_table_storage_overview_read_only'
    input?: { limit?: number }
  }
  | {
    type: 'list_table_storage_history_read_only'
    input: { databaseRole: MonitoredDatabaseRole; tableName: string; startAt?: string; endAt?: string; limit?: number }
  }
  | {
    type: 'list_database_storage_history_read_only'
    input?: { startAt?: string; endAt?: string; limit?: number }
  }
  | {
    type: 'list_model_check_runs_read_only'
    access?: AccessScope
    options?: ModelCheckRunListOptions
  }
  | {
    type: 'get_model_check_run_detail_read_only'
    runId: string
    access?: AccessScope
  }
  | {
    type: 'list_response_inspection_policies_read_only'
  }
  | {
    type: 'get_response_inspection_policy_detail_read_only'
    id: string
  }
  | {
    type: 'list_response_inspection_policy_provider_options_read_only'
    input: {
      protocolCode: string
      scopeType: 'protocol' | 'provider'
      keyword?: string
    }
  }
  | {
    type: 'list_active_response_inspection_policies_read_only'
    input: {
      protocolCode: string
      providerCode?: string
    }
  }
  | {
    type: 'list_external_integration_sources_read_only'
    options?: ExternalIntegrationSourceListOptions
  }
  | {
    type: 'find_external_integration_source_read_only'
    id: string
  }
  | {
    type: 'find_external_integration_source_token_secret_read_only'
    sourceRefId: string
    tokenId: string
  }
  | {
    type: 'load_external_integration_source_token_for_auth_read_only'
    token: string
  }
  | {
    type: 'get_usage_stats_timezone_read_only'
  }
  | {
    type: 'get_usage_stats_overview_read_only'
    access?: AccessScope
    range: AccountUsageStatsRange
  }
  | {
    type: 'get_usage_stats_overview_summary_read_only'
    access?: AccessScope
    range: AccountUsageStatsRange
  }
  | {
    type: 'get_usage_stats_overview_daily_trend_read_only'
    access?: AccessScope
    range: AccountUsageStatsRange
  }
  | {
    type: 'get_usage_stats_overview_hourly_trend_read_only'
    access?: AccessScope
    range: AccountUsageStatsRange
  }
  | {
    type: 'get_usage_stats_overview_model_distribution_read_only'
    access?: AccessScope
    range: AccountUsageStatsRange
  }
  | {
    type: 'get_usage_stats_overview_errors_read_only'
    access?: AccessScope
    range: AccountUsageStatsRange
  }
  | {
    type: 'get_ai_performance_base_read_only'
    access?: AccessScope
    range?: AccountUsageStatsRange
  }
  | {
    type: 'get_ai_performance_series_read_only'
    access?: AccessScope
    range?: AccountUsageStatsRange
    accountIds: string[]
  }
  | {
    type: 'get_ai_performance_overview_read_only'
    access?: AccessScope
    range?: AccountUsageStatsRange
    accountIds?: string[]
  }
  | {
    type: 'list_ai_performance_account_options_read_only'
    access?: AccessScope
    options?: { keyword?: string; accountIds?: string[]; limit?: number }
  }
  | {
    type: 'get_ai_health_list_read_only'
    access?: AccessScope
    options?: { hours?: number; keyword?: string; page?: number; pageSize?: number }
  }
  | {
    type: 'get_account_usage_stats_overview_page_read_only'
    access?: AccessScope
    options?: AccountListOptions & { range?: AccountUsageStatsRange; accountIds?: string[] }
  }
  | {
    type: 'get_system_metrics_overview_read_only'
    range: AccountUsageStatsRange
  }
  | {
    type: 'resolve_group_usage_access_read_only'
    groupId: string
    systemAccountId: string
  }
  | {
    type: 'list_openai_accounts_for_group_read_only'
    groupId: string
    systemAccountId: string
    options?: {
      requestedModel?: string
      requestedEndpointFamily?: GatewayRequestEndpointFamily
      includeUnavailable?: boolean
    }
  }
  | {
    type: 'list_openai_accounts_for_group_result_read_only'
    groupId: string
    systemAccountId: string
    options?: {
      requestedModel?: string
      requestedEndpointFamily?: GatewayRequestEndpointFamily
      includeUnavailable?: boolean
    }
  }
  | {
    type: 'find_openai_account_for_group_read_only'
    groupId: string
    accountId: string
    systemAccountId: string
    options?: {
      includeUnavailable?: boolean
      ignoreAvailability?: boolean
    }
  }
  | {
    type: 'find_account_for_test_read_only'
    accountId: string
    access?: AccessScope
  }
  | {
    type: 'find_openai_oauth_account_for_refresh_read_only'
    accountId: string
  }
  | {
    type: 'load_gateway_api_key_for_validation_read_only'
    key: string
  }
  | {
    type: 'read_gateway_runtime_static_read_only'
    key: string
    groupId?: string
    systemAccountId?: string
    skipDynamicRouteSelection?: boolean
  }
  | {
    type: 'list_active_client_ip_policies_read_only'
  }
  | {
    type: 'find_active_client_ip_policy_by_hash_read_only'
    ipHash: string
  }
  | {
    type: 'check_authorization_quota_read_only'
    groupAuthorizationId?: string
    accountAuthorizationId?: string
  }
  | {
    type: 'check_api_key_quota_read_only'
    apiKey: GatewayApiKeyRow
  }
  | {
    type: 'read_api_key_quota_costs_read_only'
    apiKey: GatewayApiKeyRow
  }
  | {
    type: 'check_authorization_quota_batch_read_only'
    groupAuthorizationId?: string
    accounts: Array<{
      accountId: string
      accountAuthorizationId?: string
    }>
  }
  | {
    type: 'list_groups_read_only'
    access?: AccessScope
  }
  | {
    type: 'list_groups_page_read_only'
    access?: AccessScope
    options?: GroupListOptions
  }
  | {
    type: 'list_group_options_read_only'
    access?: AccessScope
    options?: GroupOptionListOptions
  }
  | {
    type: 'list_account_group_options_read_only'
    access?: AccessScope
    options?: GroupOptionListOptions
  }
  | {
    type: 'find_group_summary_read_only'
    id: string
    access?: AccessScope
  }
  | {
    type: 'find_group_edit_detail_read_only'
    id: string
    access?: AccessScope
  }
  | {
    type: 'list_api_keys_read_only'
    access?: AccessScope
    options?: ApiKeyListOptions
  }
  | {
    type: 'list_api_keys_page_read_only'
    access?: AccessScope
    options?: ApiKeyListOptions
  }
  | {
    type: 'find_api_key_summary_read_only'
    id: string
    access?: AccessScope
  }
  | {
    type: 'find_api_key_secret_read_only'
    id: string
    access?: AccessScope
  }
  | {
    type: 'list_route_strategies_page_read_only'
    access?: AccessScope
    options?: RouteStrategyListOptions
  }
  | {
    type: 'list_route_strategy_list_items_page_read_only'
    access?: AccessScope
    options?: RouteStrategyListOptions
  }
  | {
    type: 'list_route_strategy_list_snapshot_read_only'
    access?: AccessScope
    routeStrategyIds: string[]
  }
  | {
    type: 'list_route_strategy_options_read_only'
    access?: AccessScope
    options?: RouteStrategyOptionListOptions
  }
  | {
    type: 'find_route_strategy_summary_read_only'
    id: string
    access?: AccessScope
  }
  | {
    type: 'find_route_strategy_edit_basic_detail_read_only'
    id: string
    access?: AccessScope
  }
  | {
    type: 'list_proxies_read_only'
  }
  | {
    type: 'list_proxies_page_read_only'
    options?: ProxyProfileListOptions
  }
  | {
    type: 'list_proxy_options_read_only'
    options?: ProxyProfileOptionListOptions
  }
  | {
    type: 'find_proxy_read_only'
    id: string
  }
  | {
    type: 'list_system_accounts_page_read_only'
    options?: SystemAccountListOptions
  }
  | {
    type: 'list_system_account_options_read_only'
    options?: SystemAccountOptionListOptions
  }
  | {
    type: 'find_system_account_by_id_read_only'
    id: string
  }
  | {
    type: 'find_system_account_by_username_read_only'
    username: string
  }
  | {
    type: 'find_session_by_token_read_only'
    tokenHash: string
  }
  | {
    type: 'list_providers_read_only'
  }
  | {
    type: 'list_protocol_provider_codes_read_only'
    protocolCode: string
    protocolVersion: string
  }
  | {
    type: 'list_openai_protocol_profile_ids_read_only'
  }
  | {
    type: 'is_protocol_provider_code_read_only'
    providerCode: string
    protocolCode: string
    protocolVersion?: string
  }
  | {
    type: 'find_provider_default_health_check_model_read_only'
    providerCode: string
    systemAccountId?: string
  }
  | {
    type: 'find_provider_default_supported_models_read_only'
    providerCode: string
  }
  | {
    type: 'find_provider_protocol_profile_read_only'
    profileId: string
  }
  | {
    type: 'default_provider_protocol_profile_read_only'
    providerCode: string
  }
  | {
    type: 'list_provider_default_health_check_model_preferences_read_only'
    systemAccountId?: string
    providerCodes?: string[]
  }
  | {
    type: 'list_global_settings_read_only'
  }
  | {
    type: 'get_settings_read_only'
  }
  | {
    type: 'get_management_settings_section_read_only'
    sectionKey: ManagementSettingsSectionKey
  }
  | {
    type: 'read_gateway_settings_read_only'
  }
  | {
    type: 'list_runtime_logs_read_only'
    options?: RuntimeLogListOptions
  }
  | {
    type: 'get_runtime_log_detail_read_only'
    id: string
  }
  | {
    type: 'get_runtime_log_facets_read_only'
  }
  | {
    type: 'list_provider_model_catalog_read_only'
    options: ModelCatalogListOptions
  }
  | {
    type: 'list_openai_compatible_files_read_only'
    options: OpenAICompatibleFileListOptions
  }
  | {
    type: 'get_openai_compatible_file_read_only'
    fileId: string
    systemAccountId: string
    apiKeyId: string
  }
  | {
    type: 'list_openai_compatible_vector_stores_read_only'
    options: OpenAICompatibleVectorStoreListOptions
  }
  | {
    type: 'get_openai_compatible_vector_store_read_only'
    vectorStoreId: string
    systemAccountId: string
    apiKeyId: string
  }
  | {
    type: 'list_openai_compatible_vector_store_files_read_only'
    options: OpenAICompatibleVectorStoreFileListOptions
  }
  | {
    type: 'get_openai_compatible_vector_store_file_read_only'
    vectorStoreId: string
    fileId: string
    systemAccountId: string
    apiKeyId: string
  }
  | {
    type: 'search_openai_compatible_vector_store_read_only'
    options: OpenAICompatibleVectorStoreSearchOptions
  }
  | {
    type: 'list_openai_compatible_vector_store_file_chunks_read_only'
    vectorStoreId: string
    fileId: string
    systemAccountId: string
    apiKeyId: string
    limit?: number
  }

export type SqliteReadWorkerOperationResult<T extends SqliteReadWorkerOperation> =
  T extends { type: 'list_accounts_page_read_only' } ? AccountListResult :
  T extends { type: 'list_account_items_page_read_only' } ? AccountListResult :
  T extends { type: 'list_account_management_items_page_read_only' } ? AccountManagementListPage :
  T extends { type: 'list_account_status_snapshots_read_only' } ? AccountStatusProjection[] :
  T extends { type: 'find_account_summary_read_only' } ? AccountSummary | undefined :
  T extends { type: 'list_account_options_read_only' } ? AccountOptionSummary[] :
  T extends { type: 'list_model_check_account_options_read_only' } ? ModelCheckAccountOption[] :
  T extends { type: 'list_account_tags_read_only' } ? AccountTagSummary[] :
  T extends { type: 'get_account_test_session_read_only' } ? AccountTestSession | undefined :
  T extends { type: 'get_account_test_task_read_only' } ? AccountTestTask | undefined :
  T extends { type: 'list_account_test_tasks_read_only' } ? AccountTestTask[] :
  T extends { type: 'is_account_test_task_cancel_requested_read_only' } ? boolean :
  T extends { type: 'read_account_test_task_cancel_message_read_only' } ? string :
  T extends { type: 'list_resource_authorizations_page_read_only' } ? ResourceAuthorizationListResult :
  T extends { type: 'find_resource_authorization_read_only' } ? ResourceAuthorizationSummary | undefined :
  T extends { type: 'get_resource_authorization_usage_read_only' } ? ResourceAuthorizationSummary | undefined :
  T extends { type: 'list_authorization_grantee_accounts_read_only' } ? SystemAccountPrincipalSummary[] :
  T extends { type: 'list_authorization_grantee_teams_read_only' } ? SystemTeamPrincipalSummary[] :
  T extends { type: 'list_authorization_grantee_groups_read_only' } ? AuthorizationGranteeGroupOptionSummary[] :
  T extends { type: 'get_authorization_team_usage_rows_read_only' } ? AuthorizationTeamUsageRowsResult :
  T extends { type: 'get_authorization_team_usage_summary_read_only' } ? AuthorizationTeamUsageSummary :
  T extends { type: 'get_authorization_user_usage_rows_read_only' } ? AuthorizationUserUsageRowsResult :
  T extends { type: 'get_authorization_user_usage_summary_read_only' } ? AuthorizationUserUsageSummary :
  T extends { type: 'list_public_announcements_read_only' } ? PublicAnnouncementListItem[] :
  T extends { type: 'find_public_announcement_read_only' } ? PublicAnnouncementDetail | undefined :
  T extends { type: 'list_announcements_page_read_only' } ? AnnouncementListResult :
  T extends { type: 'find_announcement_read_only' } ? AnnouncementSummary | undefined :
  T extends { type: 'list_system_teams_read_only' } ? SystemTeamListItem[] :
  T extends { type: 'list_system_teams_page_read_only' } ? SystemTeamListResult :
  T extends { type: 'find_system_team_summary_read_only' } ? SystemTeamSummary | undefined :
  T extends { type: 'find_system_team_detail_read_only' } ? SystemTeamDetail | undefined :
  T extends { type: 'list_usage_records_read_only' } ? UsageRecordListResult :
  T extends { type: 'get_usage_record_detail_read_only' } ? UsageRecordSummary | undefined :
  T extends { type: 'list_operation_logs_read_only' } ? OperationLogListResult :
  T extends { type: 'list_operation_logs_for_viewer_read_only' } ? OperationLogListResult :
  T extends { type: 'get_operation_log_detail_read_only' } ? OperationLogDetail | undefined :
  T extends { type: 'get_operation_log_detail_for_viewer_read_only' } ? OperationLogDetail | undefined :
  T extends { type: 'list_public_api_logs_read_only' } ? PublicApiLogListResult :
  T extends { type: 'get_public_api_log_detail_read_only' } ? PublicApiLogDetail | undefined :
  T extends { type: 'list_audit_logs_read_only' } ? AuditLogListResult :
  T extends { type: 'list_audit_logs_by_ids_read_only' } ? AuditLogListItem[] :
  T extends { type: 'list_audit_error_groups_read_only' } ? AuditErrorGroupListResult :
  T extends { type: 'list_audit_error_group_events_read_only' } ? AuditLogListResult :
  T extends { type: 'get_audit_log_detail_read_only' } ? AuditLogDetail | undefined :
  T extends { type: 'get_audit_log_payload_read_only' } ? AuditLogPayloadDetail | undefined :
  T extends { type: 'list_client_ip_stats_read_only' } ? ClientIpStatsListResult :
  T extends { type: 'get_client_ip_stats_detail_read_only' } ? ClientIpStatsDetailResult | undefined :
  T extends { type: 'get_table_storage_overview_read_only' } ? TableStorageOverview :
  T extends { type: 'list_table_storage_history_read_only' } ? TableStorageSnapshotSummary[] :
  T extends { type: 'list_database_storage_history_read_only' } ? DatabaseStorageHistoryPoint[] :
  T extends { type: 'list_model_check_runs_read_only' } ? ModelCheckRunListResult :
  T extends { type: 'get_model_check_run_detail_read_only' } ? ModelCheckRunDetail | undefined :
  T extends { type: 'list_response_inspection_policies_read_only' } ? ResponseInspectionPolicyListResult :
  T extends { type: 'get_response_inspection_policy_detail_read_only' } ? ResponseInspectionPolicyDetail | undefined :
  T extends { type: 'list_response_inspection_policy_provider_options_read_only' } ? ResponseInspectionPolicyProviderOption[] :
  T extends { type: 'list_active_response_inspection_policies_read_only' } ? ResponseInspectionPolicySummary[] :
  T extends { type: 'list_external_integration_sources_read_only' } ? ExternalIntegrationSourceListResult :
  T extends { type: 'find_external_integration_source_read_only' } ? ExternalIntegrationSourceSummary | undefined :
  T extends { type: 'find_external_integration_source_token_secret_read_only' } ? ExternalIntegrationSourceTokenSecret | undefined :
  T extends { type: 'load_external_integration_source_token_for_auth_read_only' } ? ExternalIntegrationSourceTokenRow | undefined :
  T extends { type: 'get_usage_stats_timezone_read_only' } ? string :
  T extends { type: 'get_usage_stats_overview_read_only' } ? UsageStatsOverview :
  T extends { type: 'get_usage_stats_overview_summary_read_only' } ? UsageStatsOverviewSummaryResult :
  T extends { type: 'get_usage_stats_overview_daily_trend_read_only' } ? UsageStatsOverviewDailyTrendResult :
  T extends { type: 'get_usage_stats_overview_hourly_trend_read_only' } ? UsageStatsOverviewHourlyTrendResult :
  T extends { type: 'get_usage_stats_overview_model_distribution_read_only' } ? UsageStatsOverviewModelDistributionResult :
  T extends { type: 'get_usage_stats_overview_errors_read_only' } ? UsageStatsOverviewErrorsResult :
  T extends { type: 'get_ai_performance_base_read_only' } ? AiPerformanceBase :
  T extends { type: 'get_ai_performance_series_read_only' } ? AiPerformanceSeries :
  T extends { type: 'get_ai_performance_overview_read_only' } ? AiPerformanceOverview :
  T extends { type: 'list_ai_performance_account_options_read_only' } ? AiPerformanceAccountOption[] :
  T extends { type: 'get_ai_health_list_read_only' } ? AiHealthListResult :
  T extends { type: 'get_account_usage_stats_overview_page_read_only' } ? AccountUsageStatsListResult :
  T extends { type: 'get_system_metrics_overview_read_only' } ? SystemMetricsOverview :
  T extends { type: 'resolve_group_usage_access_read_only' } ? GroupUsageAccessMetadata | undefined :
  T extends { type: 'list_openai_accounts_for_group_read_only' } ? OpenAIAccountSecret[] :
  T extends { type: 'list_openai_accounts_for_group_result_read_only' } ? OpenAIAccountsForGroupResult :
  T extends { type: 'find_openai_account_for_group_read_only' } ? OpenAIAccountSecret | undefined :
  T extends { type: 'find_account_for_test_read_only' } ? AccountSummary | undefined :
  T extends { type: 'find_openai_oauth_account_for_refresh_read_only' } ? (AccountSummary & {
    proxyUrl?: string
    localConfigurationError?: {
      code: 'oauth_proxy_configuration_invalid'
      message: string
    }
  }) | undefined :
  T extends { type: 'load_gateway_api_key_for_validation_read_only' } ? GatewayApiKeyRow | undefined :
  T extends { type: 'read_gateway_runtime_static_read_only' } ? DbServiceGatewayRuntime :
  T extends { type: 'list_active_client_ip_policies_read_only' } ? ActiveClientIpPolicy[] :
  T extends { type: 'find_active_client_ip_policy_by_hash_read_only' } ? ActiveClientIpPolicy | undefined :
  T extends { type: 'check_authorization_quota_read_only' } ? AuthorizationQuotaDecision :
  T extends { type: 'check_api_key_quota_read_only' } ? ApiKeyQuotaDecision :
  T extends { type: 'read_api_key_quota_costs_read_only' } ? RequestQuotaCosts :
  T extends { type: 'check_authorization_quota_batch_read_only' } ? AuthorizationQuotaDecision[] :
  T extends { type: 'list_groups_read_only' } ? GroupSummary[] :
  T extends { type: 'list_groups_page_read_only' } ? GroupListResult :
  T extends { type: 'list_group_options_read_only' } ? GroupOptionSummary[] :
  T extends { type: 'list_account_group_options_read_only' } ? AccountGroupOptionSummary[] :
  T extends { type: 'find_group_summary_read_only' } ? GroupSummary | undefined :
  T extends { type: 'find_group_edit_detail_read_only' } ? GroupEditDetail | undefined :
  T extends { type: 'list_api_keys_read_only' } ? ApiKeyListItem[] :
  T extends { type: 'list_api_keys_page_read_only' } ? ApiKeyListResult :
  T extends { type: 'find_api_key_summary_read_only' } ? ApiKeySummary | undefined :
  T extends { type: 'find_api_key_secret_read_only' } ? ApiKeySecretRecord | undefined :
  T extends { type: 'list_route_strategies_page_read_only' } ? RouteStrategyListResult :
  T extends { type: 'list_route_strategy_list_items_page_read_only' } ? RouteStrategyListItemResult :
  T extends { type: 'list_route_strategy_list_snapshot_read_only' } ? RouteStrategyListSnapshotResult :
  T extends { type: 'list_route_strategy_options_read_only' } ? RouteStrategyOptionSummary[] :
  T extends { type: 'find_route_strategy_summary_read_only' } ? RouteStrategySummary | undefined :
  T extends { type: 'find_route_strategy_edit_basic_detail_read_only' } ? RouteStrategyEditBasicDetail | undefined :
  T extends { type: 'list_proxies_read_only' } ? ProxyProfileSummary[] :
  T extends { type: 'list_proxies_page_read_only' } ? ProxyProfileListResult :
  T extends { type: 'list_proxy_options_read_only' } ? ProxyProfileOptionSummary[] :
  T extends { type: 'find_proxy_read_only' } ? ProxyProfileSummary | undefined :
  T extends { type: 'list_system_accounts_page_read_only' } ? SystemAccountListResult :
  T extends { type: 'list_system_account_options_read_only' } ? SystemAccountOptionSummary[] :
  T extends { type: 'find_system_account_by_id_read_only' } ? SystemAccountSummary | undefined :
  T extends { type: 'find_system_account_by_username_read_only' } ? (SystemAccountSummary & { passwordHash: string }) | undefined :
  T extends { type: 'find_session_by_token_read_only' } ? (SessionWithAccount & { tokenHash: string }) | undefined :
  T extends { type: 'list_providers_read_only' } ? ProviderDefinition[] :
  T extends { type: 'list_protocol_provider_codes_read_only' } ? ProviderCode[] :
  T extends { type: 'list_openai_protocol_profile_ids_read_only' } ? string[] :
  T extends { type: 'is_protocol_provider_code_read_only' } ? boolean :
  T extends { type: 'find_provider_default_health_check_model_read_only' } ? string | undefined :
  T extends { type: 'find_provider_default_supported_models_read_only' } ? string[] :
  T extends { type: 'find_provider_protocol_profile_read_only' } ? ProviderProtocolProfileDefinition | undefined :
  T extends { type: 'default_provider_protocol_profile_read_only' } ? ProviderProtocolProfileDefinition | undefined :
  T extends { type: 'list_provider_default_health_check_model_preferences_read_only' } ? ProviderDefaultHealthCheckModelPreferenceEntries :
  T extends { type: 'list_global_settings_read_only' } ? Record<string, unknown> :
  T extends { type: 'get_settings_read_only' } ? Record<string, unknown> :
  T extends { type: 'get_management_settings_section_read_only' } ? Record<string, unknown> :
  T extends { type: 'read_gateway_settings_read_only' } ? GatewaySettings :
  T extends { type: 'list_runtime_logs_read_only' } ? RuntimeLogListResult :
  T extends { type: 'get_runtime_log_detail_read_only' } ? RuntimeLogDetail | undefined :
  T extends { type: 'get_runtime_log_facets_read_only' } ? RuntimeLogFacets :
  T extends { type: 'list_provider_model_catalog_read_only' } ? ProviderModelCatalogItem[] :
  T extends { type: 'list_openai_compatible_files_read_only' } ? OpenAICompatibleFileListResult :
  T extends { type: 'get_openai_compatible_file_read_only' } ? OpenAICompatibleFileRecord | undefined :
  T extends { type: 'list_openai_compatible_vector_stores_read_only' } ? OpenAICompatibleVectorStoreListResult :
  T extends { type: 'get_openai_compatible_vector_store_read_only' } ? OpenAICompatibleVectorStoreRecord | undefined :
  T extends { type: 'list_openai_compatible_vector_store_files_read_only' } ? OpenAICompatibleVectorStoreFileListResult :
  T extends { type: 'get_openai_compatible_vector_store_file_read_only' } ? OpenAICompatibleVectorStoreFileRecord | undefined :
  T extends { type: 'search_openai_compatible_vector_store_read_only' } ? OpenAICompatibleVectorStoreSearchResult[] :
  T extends { type: 'list_openai_compatible_vector_store_file_chunks_read_only' } ? OpenAICompatibleVectorStoreFileChunkRecord[] :
  never

export interface SqliteReadWorkerMessage {
  requestId: string
  operation: SqliteReadWorkerOperation
}

export interface SqliteReadWorkerResponse {
  requestId: string
  ok: boolean
  result?: unknown
  errorMessage?: string
}

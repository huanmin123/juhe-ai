import { runtimeConfig } from '../config/runtime.js'
import { isGptVendorCode, isOpenAIProtocolProfile } from '../domain/provider-protocol.js'
import { isDynamicRouteStrategyMode } from '../domain/route-strategy.js'
import {
  checkGatewayAuthorizationQuotaBatchByIdsReadOnly,
  checkGatewayAuthorizationQuotaByIdsReadOnly
} from '../modules/gateway/quota/authorization-quota.service.js'
import { checkGatewayApiKeyQuotaReadOnly, readGatewayApiKeyQuotaCostsExact } from '../modules/gateway/quota/api-key-quota.service.js'
import { readGatewaySettingsReadOnly } from '../modules/gateway/policy/account-error-policy.service.js'
import { orderGatewayApiKeyGroupBindingsForDispatch } from '../modules/gateway/routing/api-key-group-route-selector.service.js'
import { listProviderModelCatalogReadOnly } from '../modules/model-pricing/model-catalog.service.js'
import { logger } from '../shared/logger.js'
import { findAccountSummary, listAccountItemsPageReadOnly, listAccountsPageReadOnly } from './account-summary.repository.js'
import { listAccountManagementItemsPageReadOnly } from './account-management-list.repository.js'
import { getAiHealthList } from './account-health-monitor.repository.js'
import { listAccountStatusProjectionsReadOnly } from './account-status-snapshot.repository.js'
import { listAccountOptions, listModelCheckAccountOptions } from './account-options.repository.js'
import { listAccountTags } from './account-tags.repository.js'
import {
  accountTestTaskCancelMessage,
  getAccountTestSession,
  getAccountTestTask,
  isAccountTestTaskCancelRequested,
  listAccountTestTasks
} from './account-test-tasks.repository.js'
import { findAnnouncement, findPublicAnnouncement, listAnnouncementsPage, listPublicAnnouncements } from './announcements.repository.js'
import {
  findApiKeySecretReadOnly,
  findApiKeySummaryReadOnly,
  listApiKeysPageReadOnly,
  listApiKeysReadOnly
} from './api-key.repository.js'
import {
  getAuditLogDetail,
  getAuditLogPayloadReadOnly,
  listAuditErrorGroupEvents,
  listAuditErrorGroups,
  listAuditLogs,
  listAuditLogsByIds
} from './audit-log-read.repository.js'
import {
  listAuthorizationGranteeAccounts,
  listAuthorizationGranteeGroups,
  listAuthorizationGranteeTeams
} from './authorization-options.repository.js'
import { getAuthorizationTeamUsageRows, getAuthorizationTeamUsageSummary, getAuthorizationUserUsageRows, getAuthorizationUserUsageSummary } from './authorization-usage.repository.js'
import { closeStorageDatabases } from './database.js'
import { listClientIpStats, getClientIpStatsDetail, listActiveClientIpPolicies, findActiveClientIpPolicyByHash } from './client-ip-stats.repository.js'
import {
  loadExternalIntegrationSourceTokenForAuthReadOnly,
  findExternalIntegrationSource,
  findExternalIntegrationSourceTokenSecret,
  listExternalIntegrationSources
} from './external-integration-source.repository.js'
import {
  findGroupSummaryReadOnly,
  listAccountGroupOptionsReadOnly,
  listGroupOptionsReadOnly,
  listGroupsPageReadOnly,
  listGroupsReadOnly
} from './group-summary.repository.js'
import { getModelCheckRunDetail, listModelCheckRuns } from './model-checks.repository.js'
import {
  getOperationLogDetail,
  getOperationLogDetailForViewer,
  listOperationLogs,
  listOperationLogsForViewer
} from './operation-log-read.repository.js'
import { listProviderDefaultHealthCheckModelPreferenceEntriesReadOnly } from './provider-default-health-check-model.repository.js'
import {
  defaultProviderProtocolProfile,
  findProviderDefaultSupportedModels,
  findProviderDefaultHealthCheckModel,
  findProviderProtocolProfile,
  isProtocolProviderCode,
  listOpenAIProtocolProfileIds,
  listProtocolProviderCodes,
  listProvidersReadOnly
} from './provider.repository.js'
import { getPublicApiLogDetail, listPublicApiLogs } from './public-api-logs.repository.js'
import { loadGatewayApiKeyForValidationReadOnly } from './gateway-api-key.repository.js'
import { findOpenAICompatibleFile, listOpenAICompatibleFiles } from './openai-compatible-files.repository.js'
import {
  findOpenAICompatibleVectorStore,
  findOpenAICompatibleVectorStoreFile,
  listOpenAICompatibleVectorStoreFileChunks,
  listOpenAICompatibleVectorStoreFiles,
  listOpenAICompatibleVectorStores,
  searchOpenAICompatibleVectorStore
} from './openai-compatible-vector-stores.repository.js'
import {
  findProxyReadOnly,
  listProxiesPageReadOnly,
  listProxiesReadOnly,
  listProxyOptionsReadOnly
} from './proxy.repository.js'
import {
  getAccountUsageStatsOverviewPage,
  findAccountForTest,
  findOpenAIAccountForGroup,
  listOpenAIAccountsForGroup,
  listOpenAIAccountsForGroupResult,
  resolveGroupUsageAccessMetadata,
  resolveProxyUrlsForProfiles
} from './repositories.js'
import {
  findResourceAuthorizationSummary,
  listResourceAuthorizationSummariesPage
} from './resource-authorization-read.repository.js'
import { getResourceAuthorizationUsageReadOnly } from './resource-authorization-usage.repository.js'
import {
  findRouteStrategySummaryReadOnly,
  listRouteStrategyListItemsPageReadOnly,
  listRouteStrategyListSnapshotReadOnly,
  listRouteStrategiesPageReadOnly,
  listRouteStrategyOptionsReadOnly
} from './route-strategy.repository.js'
import {
  getRuntimeLogDetailReadOnly,
  getRuntimeLogFacetsReadOnly,
  listRuntimeLogsReadOnly
} from './runtime-logs.repository.js'
import { getManagementSettingsSectionReadOnly, getSettingsReadOnly, listGlobalSettingsReadOnly } from './settings.repository.js'
import type {
  SqliteReadWorkerMessage,
  SqliteReadWorkerOperation,
  SqliteReadWorkerResponse
} from './sqlite-read-worker-pool.types.js'
import {
  getTableStorageOverview,
  listDatabaseStorageHistory,
  listTableStorageHistory
} from './table-monitor.repository.js'
import {
  getResponseInspectionPolicyDetail,
  listActiveResponseInspectionPoliciesForGateway,
  listResponseInspectionPolicies,
  listResponseInspectionPolicyProviderOptions
} from './response-inspection-policy.repository.js'
import {
  findSessionByTokenReadOnly,
  findSystemAccountById,
  findSystemAccountByUsername,
  listSystemAccountOptionsReadOnly,
  listSystemAccountsPageReadOnly
} from './system-accounts.repository.js'
import { findSystemTeamDetail, findSystemTeamSummary, listSystemTeams, listSystemTeamsPage } from './system-team.repository.js'
import { getSystemMetricsOverview } from './system-metrics.repository.js'
import {
  getUsageStatsOverview,
  getUsageStatsOverviewDailyTrend,
  getUsageStatsOverviewErrors,
  getUsageStatsOverviewHourlyTrend,
  getUsageStatsOverviewModelDistribution,
  getUsageStatsOverviewSummary
} from './usage-stats.repository.js'
import {
  getAiPerformanceBase,
  getAiPerformanceOverview,
  getAiPerformanceSeries,
  listAiPerformanceAccountOptions
} from './usage-stats-ai-performance.repository.js'
import { usageStatsTimezone } from './usage-stats-helpers.js'
import { getUsageRecordDetail, listUsageRecords } from './usage-records.repository.js'

runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ops-worker'
runtimeConfig.databaseDriver = 'sqlite'
logger.level = runtimeConfig.log.consoleEnabled ? logger.level : 'silent'

process.on('message', async (message: SqliteReadWorkerMessage) => {
  try {
    const result = await handleSqliteReadWorkerOperation(message.operation)
    sendResponse({
      requestId: message.requestId,
      ok: true,
      result
    })
  } catch (error) {
    sendResponse({
      requestId: message.requestId,
      ok: false,
      errorMessage: error instanceof Error ? error.message : String(error)
    })
  }
})

process.once('exit', () => {
  closeStorageDatabases()
})

process.once('disconnect', () => {
  closeStorageDatabases()
  process.exit(0)
})

async function handleSqliteReadWorkerOperation(operation: SqliteReadWorkerOperation): Promise<unknown> {
  switch (operation.type) {
    case 'list_accounts_page_read_only':
      return listAccountsPageReadOnly(operation.access, operation.options)
    case 'list_account_items_page_read_only':
      return listAccountItemsPageReadOnly(operation.access, operation.options)
    case 'list_account_management_items_page_read_only':
      return listAccountManagementItemsPageReadOnly(operation.access, operation.options)
    case 'list_account_status_snapshots_read_only':
      return listAccountStatusProjectionsReadOnly(operation.access, operation.accountIds)
    case 'find_account_summary_read_only':
      return findAccountSummary(operation.accountId, operation.access)
    case 'list_account_options_read_only':
      return listAccountOptions(operation.access, operation.options)
    case 'list_model_check_account_options_read_only':
      return listModelCheckAccountOptions(operation.access, operation.options)
    case 'list_account_tags_read_only':
      return listAccountTags(operation.access)
    case 'get_account_test_session_read_only':
      return getAccountTestSession(operation.id, operation.access)
    case 'get_account_test_task_read_only':
      return getAccountTestTask(operation.id, operation.access)
    case 'list_account_test_tasks_read_only':
      return listAccountTestTasks(operation.ids, operation.access)
    case 'is_account_test_task_cancel_requested_read_only':
      return isAccountTestTaskCancelRequested(operation.id)
    case 'read_account_test_task_cancel_message_read_only':
      return accountTestTaskCancelMessage(operation.id)
    case 'list_resource_authorizations_page_read_only':
      return listResourceAuthorizationSummariesPage(operation.filters ?? {}, operation.access, operation.options)
    case 'find_resource_authorization_read_only':
      return findResourceAuthorizationSummary(operation.id, operation.access, operation.options)
    case 'get_resource_authorization_usage_read_only':
      return getResourceAuthorizationUsageReadOnly(operation.id, operation.access, operation.options)
    case 'list_authorization_grantee_accounts_read_only':
      return listAuthorizationGranteeAccounts(operation.access, operation.options)
    case 'list_authorization_grantee_teams_read_only':
      return listAuthorizationGranteeTeams(operation.access, operation.options)
    case 'list_authorization_grantee_groups_read_only':
      return listAuthorizationGranteeGroups(operation.access, operation.options)
    case 'get_authorization_team_usage_rows_read_only':
      return getAuthorizationTeamUsageRows(operation.filters, operation.access, operation.range, operation.options)
    case 'get_authorization_team_usage_summary_read_only':
      return getAuthorizationTeamUsageSummary(operation.filters, operation.access, operation.range)
    case 'get_authorization_user_usage_rows_read_only':
      return getAuthorizationUserUsageRows(operation.filters, operation.access, operation.range, operation.options)
    case 'get_authorization_user_usage_summary_read_only':
      return getAuthorizationUserUsageSummary(operation.filters, operation.access, operation.range)
    case 'list_public_announcements_read_only':
      return listPublicAnnouncements(operation.systemAccountId, operation.limit)
    case 'find_public_announcement_read_only':
      return findPublicAnnouncement(operation.id)
    case 'list_announcements_page_read_only':
      return listAnnouncementsPage(operation.options)
    case 'find_announcement_read_only':
      return findAnnouncement(operation.id)
    case 'list_system_teams_read_only':
      return listSystemTeams(operation.access)
    case 'list_system_teams_page_read_only':
      return listSystemTeamsPage(operation.access, operation.options)
    case 'find_system_team_summary_read_only':
      return findSystemTeamSummary(operation.id, operation.access)
    case 'find_system_team_detail_read_only':
      return findSystemTeamDetail(operation.id, operation.access)
    case 'list_audit_logs_read_only':
      return listAuditLogs(operation.options)
    case 'list_audit_logs_by_ids_read_only':
      return listAuditLogsByIds(operation.ids)
    case 'list_audit_error_groups_read_only':
      return listAuditErrorGroups(operation.options)
    case 'list_audit_error_group_events_read_only':
      return listAuditErrorGroupEvents(operation.errorGroupId, operation.options)
    case 'get_audit_log_detail_read_only':
      return getAuditLogDetail(operation.id)
    case 'get_audit_log_payload_read_only':
      return await getAuditLogPayloadReadOnly(operation.auditLogId, operation.payloadId, operation.options)
    case 'list_usage_records_read_only':
      return listUsageRecords(operation.access, operation.options)
    case 'get_usage_record_detail_read_only':
      return getUsageRecordDetail(operation.id, operation.access)
    case 'list_operation_logs_read_only':
      return listOperationLogs(operation.options)
    case 'list_operation_logs_for_viewer_read_only':
      return listOperationLogsForViewer(operation.systemAccountId, operation.options)
    case 'get_operation_log_detail_read_only':
      return getOperationLogDetail(operation.id)
    case 'get_operation_log_detail_for_viewer_read_only':
      return getOperationLogDetailForViewer(operation.id, operation.systemAccountId)
    case 'list_public_api_logs_read_only':
      return listPublicApiLogs(operation.options)
    case 'get_public_api_log_detail_read_only':
      return getPublicApiLogDetail(operation.id)
    case 'list_client_ip_stats_read_only':
      return listClientIpStats(operation.options)
    case 'get_client_ip_stats_detail_read_only':
      return getClientIpStatsDetail(operation.options)
    case 'get_table_storage_overview_read_only':
      return getTableStorageOverview(operation.input)
    case 'list_table_storage_history_read_only':
      return listTableStorageHistory(operation.input)
    case 'list_database_storage_history_read_only':
      return listDatabaseStorageHistory(operation.input)
    case 'list_model_check_runs_read_only':
      return listModelCheckRuns(operation.access, operation.options)
    case 'get_model_check_run_detail_read_only':
      return getModelCheckRunDetail(operation.runId, operation.access)
    case 'list_response_inspection_policies_read_only':
      return listResponseInspectionPolicies()
    case 'get_response_inspection_policy_detail_read_only':
      return getResponseInspectionPolicyDetail(operation.id)
    case 'list_response_inspection_policy_provider_options_read_only':
      return listResponseInspectionPolicyProviderOptions()
    case 'list_active_response_inspection_policies_read_only':
      return listActiveResponseInspectionPoliciesForGateway(operation.input)
    case 'list_external_integration_sources_read_only':
      return listExternalIntegrationSources(operation.options)
    case 'find_external_integration_source_read_only':
      return findExternalIntegrationSource(operation.id)
    case 'find_external_integration_source_token_secret_read_only':
      return findExternalIntegrationSourceTokenSecret(operation.sourceRefId, operation.tokenId)
    case 'load_external_integration_source_token_for_auth_read_only':
      return loadExternalIntegrationSourceTokenForAuthReadOnly(operation.token)
    case 'get_usage_stats_timezone_read_only':
      return usageStatsTimezone()
    case 'get_usage_stats_overview_read_only':
      return getUsageStatsOverview(operation.access, operation.range)
    case 'get_usage_stats_overview_summary_read_only':
      return getUsageStatsOverviewSummary(operation.access, operation.range)
    case 'get_usage_stats_overview_daily_trend_read_only':
      return getUsageStatsOverviewDailyTrend(operation.access, operation.range)
    case 'get_usage_stats_overview_hourly_trend_read_only':
      return getUsageStatsOverviewHourlyTrend(operation.access, operation.range)
    case 'get_usage_stats_overview_model_distribution_read_only':
      return getUsageStatsOverviewModelDistribution(operation.access, operation.range)
    case 'get_usage_stats_overview_errors_read_only':
      return getUsageStatsOverviewErrors(operation.access, operation.range)
    case 'get_ai_performance_base_read_only':
      return getAiPerformanceBase(operation.access, operation.range)
    case 'get_ai_performance_series_read_only':
      return getAiPerformanceSeries(operation.access, operation.range, operation.accountIds)
    case 'get_ai_performance_overview_read_only':
      return getAiPerformanceOverview(operation.access, operation.range, operation.accountIds)
    case 'list_ai_performance_account_options_read_only':
      return listAiPerformanceAccountOptions(operation.access, operation.options)
    case 'get_ai_health_list_read_only':
      return getAiHealthList(operation.access, operation.options)
    case 'get_account_usage_stats_overview_page_read_only':
      return getAccountUsageStatsOverviewPage(operation.access, operation.options)
    case 'get_system_metrics_overview_read_only':
      return getSystemMetricsOverview(operation.range)
    case 'resolve_group_usage_access_read_only':
      return resolveGroupUsageAccessMetadata(operation.groupId, operation.systemAccountId)
    case 'list_openai_accounts_for_group_read_only':
      return listOpenAIAccountsForGroup(operation.groupId, operation.systemAccountId, operation.options)
    case 'list_openai_accounts_for_group_result_read_only':
      return listOpenAIAccountsForGroupResult(operation.groupId, operation.systemAccountId, operation.options)
    case 'find_openai_account_for_group_read_only':
      return findOpenAIAccountForGroup(operation.groupId, operation.accountId, operation.systemAccountId, operation.options)
    case 'find_account_for_test_read_only':
      return findAccountForTest(operation.accountId, operation.access)
    case 'find_openai_oauth_account_for_refresh_read_only': {
      const account = findAccountForTest(operation.accountId)
      if (!account || !isGptVendorCode(account.providerCode) || !isOpenAIProtocolProfile(account) || account.type !== 'oauth') {
        return undefined
      }
      const proxyResolution = account.proxyProfileId
        ? resolveProxyUrlsForProfiles([account.proxyProfileId]).get(account.proxyProfileId)
        : undefined
      return {
        ...account,
        ...(proxyResolution?.proxyUrl
          ? { proxyUrl: proxyResolution.proxyUrl }
          : account.proxyProfileId
            ? {
                localConfigurationError: {
                  code: 'oauth_proxy_configuration_invalid' as const,
                  message: proxyResolution?.errorMessage ?? 'OpenAI OAuth 账户配置的代理不可用，请检查代理配置'
                }
              }
            : {})
      }
    }
    case 'load_gateway_api_key_for_validation_read_only':
      return loadGatewayApiKeyForValidationReadOnly(operation.key)
    case 'read_gateway_runtime_static_read_only':
      return readGatewayRuntimeStaticReadOnly(operation)
    case 'list_active_client_ip_policies_read_only':
      return listActiveClientIpPolicies()
    case 'find_active_client_ip_policy_by_hash_read_only':
      return findActiveClientIpPolicyByHash(operation.ipHash)
    case 'check_authorization_quota_read_only':
      return checkGatewayAuthorizationQuotaByIdsReadOnly({
        groupAuthorizationId: operation.groupAuthorizationId,
        accountAuthorizationId: operation.accountAuthorizationId
      })
    case 'check_api_key_quota_read_only':
      return checkGatewayApiKeyQuotaReadOnly(operation.apiKey)
    case 'read_api_key_quota_costs_read_only':
      return readGatewayApiKeyQuotaCostsExact(operation.apiKey)
    case 'check_authorization_quota_batch_read_only':
      return checkGatewayAuthorizationQuotaBatchByIdsReadOnly({
        groupAuthorizationId: operation.groupAuthorizationId,
        accounts: operation.accounts
      })
    case 'list_groups_read_only':
      return listGroupsReadOnly(operation.access)
    case 'list_groups_page_read_only':
      return listGroupsPageReadOnly(operation.access, operation.options)
    case 'list_group_options_read_only':
      return listGroupOptionsReadOnly(operation.access, operation.options)
    case 'list_account_group_options_read_only':
      return listAccountGroupOptionsReadOnly(operation.access, operation.options)
    case 'find_group_summary_read_only':
      return findGroupSummaryReadOnly(operation.id, operation.access)
    case 'list_api_keys_read_only':
      return listApiKeysReadOnly(operation.access, operation.options)
    case 'list_api_keys_page_read_only':
      return listApiKeysPageReadOnly(operation.access, operation.options)
    case 'find_api_key_summary_read_only':
      return findApiKeySummaryReadOnly(operation.id, operation.access)
    case 'find_api_key_secret_read_only':
      return findApiKeySecretReadOnly(operation.id, operation.access)
    case 'list_route_strategies_page_read_only':
      return listRouteStrategiesPageReadOnly(operation.access, operation.options)
    case 'list_route_strategy_list_items_page_read_only':
      return listRouteStrategyListItemsPageReadOnly(operation.access, operation.options)
    case 'list_route_strategy_list_snapshot_read_only':
      return listRouteStrategyListSnapshotReadOnly(operation.access, operation.routeStrategyIds)
    case 'list_route_strategy_options_read_only':
      return listRouteStrategyOptionsReadOnly(operation.access, operation.options)
    case 'find_route_strategy_summary_read_only':
      return findRouteStrategySummaryReadOnly(operation.id, operation.access)
    case 'list_proxies_read_only':
      return listProxiesReadOnly()
    case 'list_proxies_page_read_only':
      return listProxiesPageReadOnly(operation.options)
    case 'list_proxy_options_read_only':
      return listProxyOptionsReadOnly(operation.options)
    case 'find_proxy_read_only':
      return findProxyReadOnly(operation.id)
    case 'list_system_accounts_page_read_only':
      return listSystemAccountsPageReadOnly(operation.options)
    case 'list_system_account_options_read_only':
      return listSystemAccountOptionsReadOnly(operation.options)
    case 'find_system_account_by_id_read_only':
      return findSystemAccountById(operation.id)
    case 'find_system_account_by_username_read_only':
      return findSystemAccountByUsername(operation.username)
    case 'find_session_by_token_read_only':
      return findSessionByTokenReadOnly(operation.tokenHash)
    case 'list_providers_read_only':
      return listProvidersReadOnly()
    case 'list_protocol_provider_codes_read_only':
      return listProtocolProviderCodes(operation.protocolCode, operation.protocolVersion)
    case 'list_openai_protocol_profile_ids_read_only':
      return listOpenAIProtocolProfileIds()
    case 'is_protocol_provider_code_read_only':
      return isProtocolProviderCode(operation.providerCode, operation.protocolCode, operation.protocolVersion)
    case 'find_provider_default_health_check_model_read_only':
      return findProviderDefaultHealthCheckModel(operation.providerCode, operation.systemAccountId)
    case 'find_provider_default_supported_models_read_only':
      return findProviderDefaultSupportedModels(operation.providerCode)
    case 'find_provider_protocol_profile_read_only':
      return findProviderProtocolProfile(operation.profileId)
    case 'default_provider_protocol_profile_read_only':
      return defaultProviderProtocolProfile(operation.providerCode)
    case 'list_provider_default_health_check_model_preferences_read_only':
      return listProviderDefaultHealthCheckModelPreferenceEntriesReadOnly(operation.systemAccountId, operation.providerCodes)
    case 'list_global_settings_read_only':
      return listGlobalSettingsReadOnly()
    case 'get_settings_read_only':
      return getSettingsReadOnly()
    case 'get_management_settings_section_read_only':
      return getManagementSettingsSectionReadOnly(operation.sectionKey)
    case 'read_gateway_settings_read_only':
      return readGatewaySettingsReadOnly()
    case 'list_runtime_logs_read_only':
      return listRuntimeLogsReadOnly(operation.options)
    case 'get_runtime_log_detail_read_only':
      return getRuntimeLogDetailReadOnly(operation.id)
    case 'get_runtime_log_facets_read_only':
      return getRuntimeLogFacetsReadOnly()
    case 'list_provider_model_catalog_read_only':
      return listProviderModelCatalogReadOnly(operation.options)
    case 'list_openai_compatible_files_read_only':
      return listOpenAICompatibleFiles(operation.options)
    case 'get_openai_compatible_file_read_only':
      return findOpenAICompatibleFile({
        fileId: operation.fileId,
        systemAccountId: operation.systemAccountId,
        apiKeyId: operation.apiKeyId
      })
    case 'list_openai_compatible_vector_stores_read_only':
      return listOpenAICompatibleVectorStores(operation.options)
    case 'get_openai_compatible_vector_store_read_only':
      return findOpenAICompatibleVectorStore({
        vectorStoreId: operation.vectorStoreId,
        systemAccountId: operation.systemAccountId,
        apiKeyId: operation.apiKeyId
      })
    case 'list_openai_compatible_vector_store_files_read_only':
      return listOpenAICompatibleVectorStoreFiles(operation.options)
    case 'get_openai_compatible_vector_store_file_read_only':
      return findOpenAICompatibleVectorStoreFile({
        vectorStoreId: operation.vectorStoreId,
        fileId: operation.fileId,
        systemAccountId: operation.systemAccountId,
        apiKeyId: operation.apiKeyId
      })
    case 'search_openai_compatible_vector_store_read_only':
      return searchOpenAICompatibleVectorStore(operation.options)
    case 'list_openai_compatible_vector_store_file_chunks_read_only':
      return listOpenAICompatibleVectorStoreFileChunks({
        vectorStoreId: operation.vectorStoreId,
        fileId: operation.fileId,
        systemAccountId: operation.systemAccountId,
        apiKeyId: operation.apiKeyId,
        limit: operation.limit
      })
  }
  throw new Error(`未知 SQLite read worker 操作：${JSON.stringify(operation)}`)
}

function readGatewayRuntimeStaticReadOnly(
  operation: Extract<SqliteReadWorkerOperation, { type: 'read_gateway_runtime_static_read_only' }>
) {
  const settings = readGatewaySettingsReadOnly()
  const apiKey = loadGatewayApiKeyForValidationReadOnly(operation.key)
  if (!apiKey) {
    return {
      settings,
      accounts: []
    }
  }
  const systemAccountId = operation.systemAccountId ?? apiKey.system_account_id
  if (isDynamicRouteStrategyMode(apiKey.route_strategy_mode)) {
    if (operation.skipDynamicRouteSelection !== true) {
      throw new Error('动态路由 gateway runtime 禁止进入 SQLite 静态只读 worker')
    }
    return {
      apiKey: {
        ...apiKey,
        group_bindings: apiKey.group_bindings?.map((binding) => ({ ...binding }))
      },
      settings,
      accounts: [],
      responseInspectionPolicies: []
    }
  }
  const orderedBindings = orderGatewayApiKeyGroupBindingsForDispatch(apiKey)
  apiKey.selected_group_id = orderedBindings[0]?.group_id ?? apiKey.selected_group_id
  const candidateGroupIds = operation.groupId
    ? orderedBindings.some((binding) => binding.group_id === operation.groupId)
      ? [operation.groupId]
      : []
    : orderedBindings.map((binding) => binding.group_id)
  const uniqueCandidateGroupIds = [...new Set(candidateGroupIds.filter(Boolean))]

  for (const groupId of uniqueCandidateGroupIds) {
    const groupAccess = resolveGroupUsageAccessMetadata(groupId, systemAccountId)
    if (!groupAccess) {
      continue
    }
    const groupAccountsResult = listOpenAIAccountsForGroupResult(groupId, systemAccountId, { preResolvedGroupAccess: groupAccess })
    const accounts = groupAccountsResult.accounts
    if (!hasDispatchableGatewayAccount(accounts) && uniqueCandidateGroupIds.length > 1) {
      continue
    }
    const responseInspectionPolicies = listActiveResponseInspectionPoliciesForAccountsReadOnly(accounts)
    return {
      apiKey: {
        ...apiKey,
        selected_group_id: groupId,
        group_bindings: orderedBindings.length ? orderedBindings : apiKey.group_bindings
      },
      settings,
      groupAccess,
      accounts,
      accountDispatchDiagnostics: groupAccountsResult.diagnostics,
      responseInspectionPolicies
    }
  }

  return {
    apiKey,
    settings,
    accounts: [],
    responseInspectionPolicies: []
  }
}

function listActiveResponseInspectionPoliciesForAccountsReadOnly(
  accounts: ReadonlyArray<{ protocolCode?: string; providerCode?: string }>
) {
  const profileKeys = new Set<string>()
  const policiesById = new Map<string, ReturnType<typeof listActiveResponseInspectionPoliciesForGateway>[number]>()
  for (const account of accounts) {
    const protocolCode = account.protocolCode?.trim()
    if (!protocolCode) {
      continue
    }
    const providerCode = account.providerCode?.trim()
    const key = `${protocolCode}:${providerCode ?? ''}`
    if (profileKeys.has(key)) {
      continue
    }
    profileKeys.add(key)
    for (const policy of listActiveResponseInspectionPoliciesForGateway({ protocolCode, providerCode })) {
      policiesById.set(policy.id, policy)
    }
  }
  return [...policiesById.values()]
}

function hasDispatchableGatewayAccount(accounts: Array<{ status?: string; proxyProfileUnavailable?: boolean }>): boolean {
  return accounts.some((account) => account.status === 'active' && account.proxyProfileUnavailable !== true)
}

function sendResponse(message: SqliteReadWorkerResponse): void {
  if (typeof process.send === 'function') {
    process.send(message)
  }
}

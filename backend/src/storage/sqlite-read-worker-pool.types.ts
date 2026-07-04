import type { AccountListOptions } from './account-list-options.js'
import type { AccountListResult } from './account-summary.repository.js'
import type { AccessScope } from './access-scope.js'
import type { ApiKeyListOptions, ApiKeyListResult } from './api-key.repository.js'
import type { GroupListOptions, GroupOptionListOptions } from './group-read.repository.js'
import type {
  AccountGroupOptionSummary,
  ApiKeySummary,
  GroupListResult,
  GroupOptionSummary,
  GroupSummary,
  ProviderDefinition,
  RouteStrategyListItemResult,
  RouteStrategyListResult,
  RouteStrategyOptionSummary,
  RouteStrategySummary,
  SystemAccountPrincipalSummary
} from '../domain/types.js'
import type {
  ProxyProfileListOptions,
  ProxyProfileListResult,
  ProxyProfileOptionListOptions,
  ProxyProfileOptionSummary,
  ProxyProfileSummary
} from './proxy.repository.js'
import type { RouteStrategyListOptions, RouteStrategyOptionListOptions } from './route-strategy.repository.js'
import type { RuntimeLogDetail, RuntimeLogFacets, RuntimeLogListOptions, RuntimeLogListResult } from './runtime-logs.repository.js'
import type { SystemAccountListOptions, SystemAccountListResult, SystemAccountOptionListOptions } from './system-accounts.repository.js'
import type { ModelCatalogListOptions, ProviderModelCatalogItem } from '../modules/model-pricing/model-catalog.service.js'
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

export type ProviderDefaultTestModelPreferenceEntries = Array<[string, string]>

export type SqliteReadWorkerOperation =
  | {
    type: 'list_accounts_page_read_only'
    access?: AccessScope
    options?: AccountListOptions
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
    type: 'list_providers_read_only'
  }
  | {
    type: 'list_provider_default_test_model_preferences_read_only'
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
  T extends { type: 'list_groups_read_only' } ? GroupSummary[] :
  T extends { type: 'list_groups_page_read_only' } ? GroupListResult :
  T extends { type: 'list_group_options_read_only' } ? GroupOptionSummary[] :
  T extends { type: 'list_account_group_options_read_only' } ? AccountGroupOptionSummary[] :
  T extends { type: 'find_group_summary_read_only' } ? GroupSummary | undefined :
  T extends { type: 'list_api_keys_read_only' } ? ApiKeySummary[] :
  T extends { type: 'list_api_keys_page_read_only' } ? ApiKeyListResult :
  T extends { type: 'find_api_key_summary_read_only' } ? ApiKeySummary | undefined :
  T extends { type: 'find_api_key_secret_read_only' } ? ApiKeySummary | undefined :
  T extends { type: 'list_route_strategies_page_read_only' } ? RouteStrategyListResult :
  T extends { type: 'list_route_strategy_list_items_page_read_only' } ? RouteStrategyListItemResult :
  T extends { type: 'list_route_strategy_options_read_only' } ? RouteStrategyOptionSummary[] :
  T extends { type: 'find_route_strategy_summary_read_only' } ? RouteStrategySummary | undefined :
  T extends { type: 'list_proxies_read_only' } ? ProxyProfileSummary[] :
  T extends { type: 'list_proxies_page_read_only' } ? ProxyProfileListResult :
  T extends { type: 'list_proxy_options_read_only' } ? ProxyProfileOptionSummary[] :
  T extends { type: 'find_proxy_read_only' } ? ProxyProfileSummary | undefined :
  T extends { type: 'list_system_accounts_page_read_only' } ? SystemAccountListResult :
  T extends { type: 'list_system_account_options_read_only' } ? SystemAccountPrincipalSummary[] :
  T extends { type: 'list_providers_read_only' } ? ProviderDefinition[] :
  T extends { type: 'list_provider_default_test_model_preferences_read_only' } ? ProviderDefaultTestModelPreferenceEntries :
  T extends { type: 'list_global_settings_read_only' } ? Record<string, unknown> :
  T extends { type: 'get_settings_read_only' } ? Record<string, unknown> :
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

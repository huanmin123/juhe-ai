import { runtimeConfig } from '../config/runtime.js'
import { listProviderModelCatalog } from '../modules/model-pricing/model-catalog.service.js'
import { logger } from '../shared/logger.js'
import { listAccountsPageReadOnly } from './account-summary.repository.js'
import {
  findApiKeySecretReadOnly,
  findApiKeySummaryReadOnly,
  listApiKeysPageReadOnly,
  listApiKeysReadOnly
} from './api-key.repository.js'
import { closeStorageDatabases } from './database.js'
import {
  findGroupSummaryReadOnly,
  listAccountGroupOptionsReadOnly,
  listGroupOptionsReadOnly,
  listGroupsPageReadOnly,
  listGroupsReadOnly
} from './group-summary.repository.js'
import { listProviderDefaultTestModelPreferenceEntriesReadOnly } from './provider-default-test-model.repository.js'
import { listProvidersReadOnly } from './provider.repository.js'
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
  findRouteStrategySummaryReadOnly,
  listRouteStrategyListItemsPageReadOnly,
  listRouteStrategiesPageReadOnly,
  listRouteStrategyOptionsReadOnly
} from './route-strategy.repository.js'
import {
  getRuntimeLogDetailReadOnly,
  getRuntimeLogFacetsReadOnly,
  listRuntimeLogsReadOnly
} from './runtime-logs.repository.js'
import { getSettingsReadOnly, listGlobalSettingsReadOnly } from './settings.repository.js'
import type {
  SqliteReadWorkerMessage,
  SqliteReadWorkerOperation,
  SqliteReadWorkerResponse
} from './sqlite-read-worker-pool.types.js'
import { listSystemAccountOptionsReadOnly, listSystemAccountsPageReadOnly } from './system-accounts.repository.js'

runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ops-worker'
runtimeConfig.databaseDriver = 'sqlite'
logger.level = runtimeConfig.log.consoleEnabled ? logger.level : 'silent'

process.on('message', (message: SqliteReadWorkerMessage) => {
  try {
    const result = handleSqliteReadWorkerOperation(message.operation)
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

function handleSqliteReadWorkerOperation(operation: SqliteReadWorkerOperation): unknown {
  switch (operation.type) {
    case 'list_accounts_page_read_only':
      return listAccountsPageReadOnly(operation.access, operation.options)
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
    case 'list_providers_read_only':
      return listProvidersReadOnly()
    case 'list_provider_default_test_model_preferences_read_only':
      return listProviderDefaultTestModelPreferenceEntriesReadOnly(operation.systemAccountId, operation.providerCodes)
    case 'list_global_settings_read_only':
      return listGlobalSettingsReadOnly()
    case 'get_settings_read_only':
      return getSettingsReadOnly()
    case 'list_runtime_logs_read_only':
      return listRuntimeLogsReadOnly(operation.options)
    case 'get_runtime_log_detail_read_only':
      return getRuntimeLogDetailReadOnly(operation.id)
    case 'get_runtime_log_facets_read_only':
      return getRuntimeLogFacetsReadOnly()
    case 'list_provider_model_catalog_read_only':
      return listProviderModelCatalog(operation.options)
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

function sendResponse(message: SqliteReadWorkerResponse): void {
  if (typeof process.send === 'function') {
    process.send(message)
  }
}

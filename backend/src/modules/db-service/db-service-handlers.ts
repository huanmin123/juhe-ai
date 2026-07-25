import { performance } from 'node:perf_hooks'

import { runtimeConfig } from '../../config/runtime.js'
import { getChatDatabaseClient } from '../../storage/chat-client.js'
import { cleanupChatRetention } from '../../storage/chat.repository.js'
import { cleanupExpiredChatAssets } from '../chat/chat-asset-cleanup.js'
import { isActiveChatGeneration } from '../chat/chat-generation-runtime.js'
import {
  chatContextMaintenanceMaxBatchSize,
  cleanupExpiredChatContextCheckpoints,
  recoverStaleChatContextCompactions
} from '../../storage/chat-context.repository.js'
import {
  commitAccountBalanceRefreshAsync,
  enableDetectedAccountBalanceQueryAsync
} from '../../storage/account-balance.repository.js'
import {
  accountTestTaskCancelMessage,
  accountTestTaskCancelMessageAsync,
  cleanupExpiredAccountTestTasks,
  cleanupExpiredAccountTestTasksAsync,
  completeIdleAccountTestSessions,
  completeIdleAccountTestSessionsAsync,
  completeAccountTestTask,
  completeAccountTestTaskAsync,
  failAccountTestTask,
  failAccountTestTaskAsync,
  failExpiredQueuedAccountTestTasks,
  failExpiredQueuedAccountTestTasksAsync,
  isAccountTestTaskCancelRequested,
  isAccountTestTaskCancelRequestedAsync,
  listRunnableAccountTestTaskIds,
  listRunnableAccountTestTaskIdsAsync,
  markAccountTestTaskCanceled,
  markAccountTestTaskCanceledAsync,
  markAccountTestTaskRunning,
  markAccountTestTaskRunningAsync,
  requeueInterruptedAccountTestTasks,
  requeueInterruptedAccountTestTasksAsync,
  updateAccountTestTaskMessage,
  updateAccountTestTaskMessageAsync
} from '../../storage/account-test-tasks.repository.js'
import {
  clearGatewayApiKeyValidationCache,
  deferCooldownAccountRetest,
  deferCooldownAccountRetestAsync,
  clearAuthorizedAccountBindingFailureStateByContext,
  clearAuthorizedAccountBindingFailureStateByContextAsync,
  clearAccountFailureStateResult,
  clearAccountFailureStateResultAsync,
  cleanupExpiredLogicallyDeletedAccounts,
  cleanupExpiredLogicallyDeletedAccountsAsync,
  clearAccountStreamFailureState,
  clearAccountStreamFailureStateAsync,
  clearAuthorizedAccountBindingStreamFailureState,
  clearAuthorizedAccountBindingStreamFailureStateAsync,
  findAccountForTest,
  findAccountForTestAsync,
  findAccountForCooldownRetest,
  findAccountForCooldownRetestAsync,
  findAccountForHealthCheck,
  findAccountForHealthCheckAsync,
  findOpenAIAccountForGroup,
  findOpenAIAccountForGroupAsync,
  getAccountPrecheckMutationState,
  getAccountPrecheckMutationStateAsync,
  listOpenAIAccountsForGroup,
  listOpenAIAccountsForGroupResult,
  listOpenAIAccountsForGroupResultAsync,
  listAccountsDueForCooldownRetest,
  listAccountsDueForCooldownRetestAsync,
  listAccountsDueForCooldownRetestPage,
  listAccountsDueForCooldownRetestPageAsync,
  listAccountsDueForHealthCheck,
  listAccountsDueForHealthCheckAsync,
  listRecoverableUnavailableOpenAIAccountsForGroup,
  listPublicGlobalSettings,
  listPublicGlobalSettingsAsync,
  markAccountException,
  markOpenAIOAuthLocalConfigurationExceptionIfCurrent,
  markOpenAIOAuthLocalConfigurationExceptionIfCurrentAsync,
  markAccountCooldown,
  markAccountCooldownAsync,
  markAccountDisabledByFailure,
  markAccountDisabledByFailureAsync,
  markAccountTestTemporaryUnavailable,
  markAccountTestTemporaryUnavailableAsync,
  markAccountTemporaryUnavailable,
  markAccountTemporaryUnavailableAsync,
  markAccountExceptionAsync,
  markAuthorizedAccountBindingCooldownByContext,
  markAuthorizedAccountBindingCooldownByContextAsync,
  markAuthorizedAccountBindingDisabledByFailure,
  markAuthorizedAccountBindingDisabledByFailureAsync,
  markAuthorizedAccountBindingTemporaryUnavailableByContext,
  markAuthorizedAccountBindingTemporaryUnavailableByContextAsync,
  recordAccountStreamFailure,
  recordAccountStreamFailureAsync,
  recordAccountHealthCheckFailure,
  recordAccountHealthCheckFailureAsync,
  recordAccountHealthCheckSuccess,
  recordAccountHealthCheckSuccessAsync,
  recordCooldownAccountRetestFailure,
  recordCooldownAccountRetestFailureAsync,
  recordCooldownAccountRetestSuccess,
  recordCooldownAccountRetestSuccessAsync,
  recordAuthorizedAccountBindingStreamFailure,
  recordAuthorizedAccountBindingStreamFailureAsync,
  resolveGroupUsageAccessMetadata,
  resolveGroupUsageAccessMetadataAsync,
  resolveProxyUrlsForProfiles,
  resolveProxyUrlsForProfilesAsync,
  syncAccountAvailabilityScheduleStatuses,
  syncAccountAvailabilityScheduleStatusesAsync,
  syncApiKeyAvailabilityScheduleStatuses,
  syncApiKeyAvailabilityScheduleStatusesAsync,
  type OpenAIAccountSecret,
  updateAccount,
  updateAccountAsync,
  updateOpenAIOAuthCredentialsIfCurrent,
  updateOpenAIOAuthCredentialsIfCurrentAsync,
  updateProxyTestState,
  updateProxyTestStateAsync,
  validateGatewayApiKey,
  validateGatewayApiKeyAsync
} from '../../storage/repositories.js'
import type { AccessScope } from '../../storage/access-scope.js'
import {
  getRuntimeLogFacetsAsync,
  getRuntimeLogDetailAsync,
  listRuntimeLogsAsync
} from '../../storage/runtime-logs.repository.js'
import {
  listActiveClientIpPolicies,
  listActiveClientIpPoliciesAsync,
} from '../../storage/client-ip-stats.repository.js'
import { listActiveResponseInspectionPoliciesForGateway, listActiveResponseInspectionPoliciesForGatewayAsync } from '../../storage/response-inspection-policy.repository.js'
import { cleanupExpiredSystemSessions, cleanupExpiredSystemSessionsAsync } from '../../storage/data-retention.repository.js'
import {
  cleanupExpiredCodexContextStates,
  cleanupExpiredCodexContextStatesAsync,
  readCodexContextCompactState,
  readCodexContextCompactStateAsync,
  readCodexContextResponseStateChain,
  readCodexContextResponseStateChainAsync,
  saveCodexContextCompactStateIndex,
  saveCodexContextCompactStateIndexAsync,
  saveCodexContextResponseStateIndex,
  saveCodexContextResponseStateIndexAsync
} from '../../storage/codex-context-state.repository.js'
import {
  cleanupExpiredCodexContextStatesWithWriterPool,
  getCodexContextStateWriterPoolRuntime,
  readCodexContextCompactStateWithWriterPool,
  readCodexContextResponseStateChainWithWriterPool,
  saveCodexContextCompactStateIndexWithWriterPool,
  saveCodexContextResponseStateIndexWithWriterPool
} from '../../storage/codex-context-state-writer-pool.js'
import { getSqliteReadWorkerPoolRuntime, requestSqliteReadWorker } from '../../storage/sqlite-read-worker-pool.js'
import {
  deleteGroupAccountStatsDirtyRowsLocal,
  deleteGroupAccountStatsDirtyRowsAsync,
  markAllGroupAccountStatsDirty,
  markAllGroupAccountStatsDirtyAsync,
  updateGroupAccountStatsAllCursorAsync,
  updateGroupAccountStatsAllCursorLocal,
  type GroupAccountStatsDirtyRow
} from '../../storage/group-account-stats-cache.repository.js'
import {
  clearGatewayRuntimeCacheLocal,
  readCachedGatewaySettings,
} from '../gateway/runtime/runtime-cache.service.js'
import { authorizeAccountApiKeyPersistentMutationForTrafficSource } from '../gateway/runtime/account-api-key-mutation-authority.js'
import { isGptVendorCode, isOpenAIProtocolProfile } from '../../domain/provider-protocol.js'
import { isDynamicRouteStrategyMode } from '../../domain/route-strategy.js'
import {
  orderGatewayApiKeyGroupBindingsForDispatch,
  orderGatewayApiKeyGroupBindingsForDispatchAsync
} from '../gateway/routing/api-key-group-route-selector.service.js'
import { checkGatewayApiKeyQuota, checkGatewayApiKeyQuotaExactAsync, clearApiKeyQuotaCache, readGatewayApiKeyQuotaCostsExact, readGatewayApiKeyQuotaCostsExactAsync } from '../gateway/quota/api-key-quota.service.js'
import {
  checkGatewayAuthorizationQuotaBatchByIds,
  checkGatewayAuthorizationQuotaBatchByIdsExactAsync,
  checkGatewayAuthorizationQuotaByIds,
  checkGatewayAuthorizationQuotaByIdsExactAsync,
  clearAuthorizationQuotaCache
} from '../gateway/quota/authorization-quota.service.js'
import { applyAccountErrorHandling, applyAccountErrorHandlingAsync, readGatewaySettingsAsync } from '../gateway/policy/account-error-policy.service.js'
import {
  persistOpenAICodexUsageHeaders,
  persistOpenAICodexUsageHeadersAsync
} from '../gateway/adapters/gpt-codex/usage.service.js'
import { listProviderModelCatalog, listProviderModelCatalogAsync } from '../model-pricing/model-catalog.service.js'
import {
  deferAccountApiKeyRuntimeProbe,
  deferAccountApiKeyRuntimeProbeAsync,
  recordAccountApiKeyRuntimeFailure,
  recordAccountApiKeyRuntimeFailureAsync,
  recordAccountApiKeyRuntimeSuccess,
  recordAccountApiKeyRuntimeSuccessAsync
} from '../../storage/account-api-key-runtime-state.repository.js'
import {
  createOpenAICompatibleFile,
  createOpenAICompatibleFileAsync,
  deleteOpenAICompatibleFile,
  deleteOpenAICompatibleFileAsync,
  findOpenAICompatibleFile,
  findOpenAICompatibleFileAsync,
  listOpenAICompatibleFiles,
  listOpenAICompatibleFilesAsync
} from '../../storage/openai-compatible-files.repository.js'
import {
  createOpenAICompatibleVectorStore,
  createOpenAICompatibleVectorStoreAsync,
  createOpenAICompatibleVectorStoreFile,
  createOpenAICompatibleVectorStoreFileAsync,
  deleteOpenAICompatibleVectorStore,
  deleteOpenAICompatibleVectorStoreAsync,
  deleteOpenAICompatibleVectorStoreFile,
  deleteOpenAICompatibleVectorStoreFileAsync,
  findOpenAICompatibleVectorStore,
  findOpenAICompatibleVectorStoreAsync,
  findOpenAICompatibleVectorStoreFile,
  findOpenAICompatibleVectorStoreFileAsync,
  listOpenAICompatibleVectorStoreFileChunksAsync,
  listOpenAICompatibleVectorStoreFileChunks,
  listOpenAICompatibleVectorStoreFiles,
  listOpenAICompatibleVectorStoreFilesAsync,
  listOpenAICompatibleVectorStores,
  listOpenAICompatibleVectorStoresAsync,
  searchOpenAICompatibleVectorStore,
  searchOpenAICompatibleVectorStoreAsync
} from '../../storage/openai-compatible-vector-stores.repository.js'
import {
  acknowledgeAccountCircuitOutbox,
  advanceAccountCircuitDispatchRevision,
  claimAccountCircuitOutbox,
  cleanupAccountCircuitControlPlane,
  compareAndSetAccountCircuitIncident,
  getAccountCircuitIncidentByScopeKey,
  listAccountCircuitIncidentsForRebuild,
  listAccountCircuitIncidentsByRuntimeKeys,
  listAccountCircuitProjectionGaps,
  releaseAccountCircuitOutboxForReplay
} from '../../storage/account-circuit-control-plane.repository.js'
import type {
  DbServiceGatewayRuntime,
  DbServiceOperation,
  DbServiceOperationResult,
  DbServiceRuntimeSnapshot
} from './db-service-types.js'
import { currentProcessEventLoopLagMs } from '../../shared/process-event-loop-monitor.js'
import { expireDueResourceAuthorizations, expireDueResourceAuthorizationsAsync } from '../../storage/repositories.js'

let handledRequestCount = 0
let failedRequestCount = 0
let pendingRequestCount = 0
let lastRequestAt: string | undefined
let lastError: string | undefined
let dbServiceHttpEndpoint: { host: string; port: number } | undefined
let dbServiceQueueRuntimeProvider: (() => DbServiceQueueRuntimeMetrics) | undefined
let lastExecMs = 0
let maxExecMs = 0
let slowOpCount = 0
let lastSlowOpType: string | undefined
let lastSlowOpMs: number | undefined
let lastSlowOpAt: string | undefined
const internalDbServiceAccountAccess: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
const slowDbServiceOperationMs = 500

export interface DbServiceQueueRuntimeMetrics {
  queuedRequestCount: number
  queuedRequestBytes: number
  queuedHighRequestCount: number
  queuedNormalRequestCount: number
  queuedLowRequestCount: number
  oldestQueuedMs: number
  lastQueueWaitMs: number
  maxQueueWaitMs: number
  queueRejectedCount: number
  queueExpiredCount: number
  activeConcurrentRequestCount: number
  maxActiveConcurrentRequestCount: number
}

export async function handleDbServiceOperation<T extends DbServiceOperation>(operation: T): Promise<DbServiceOperationResult<T>> {
  pendingRequestCount += 1
  lastRequestAt = new Date().toISOString()
  const startedAt = performance.now()
  try {
    const result = await handleDbServiceOperationDispatch(operation) as DbServiceOperationResult<T>
    handledRequestCount += 1
    lastError = undefined
    return result
  } catch (error) {
    failedRequestCount += 1
    lastError = error instanceof Error ? error.message : String(error)
    throw error
  } finally {
    recordDbServiceOperationDuration(operation.type, performance.now() - startedAt)
    pendingRequestCount = Math.max(0, pendingRequestCount - 1)
  }
}

async function handleDbServiceOperationDispatch(operation: DbServiceOperation): Promise<unknown> {
  const operationType = operation.type
  switch (operation.type) {
    case 'list_public_global_settings':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await listPublicGlobalSettingsAsync()
      }
      return await requestSqliteReadWorker({ type: 'list_global_settings_read_only' })
    case 'validate_gateway_api_key':
      return await validateGatewayApiKeyAsync(operation.key)
    case 'read_gateway_settings':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await readGatewaySettingsAsync()
      }
      return await requestSqliteReadWorker({ type: 'read_gateway_settings_read_only' })
    case 'resolve_group_usage_access':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await resolveGroupUsageAccessMetadataAsync(operation.groupId, operation.systemAccountId)
      }
      return await requestSqliteReadWorker({
        type: 'resolve_group_usage_access_read_only',
        groupId: operation.groupId,
        systemAccountId: operation.systemAccountId
      })
    case 'list_openai_accounts_for_group':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return (await listOpenAIAccountsForGroupResultAsync(operation.groupId, operation.systemAccountId, {
          requestedModel: operation.requestedModel,
          requestedEndpointFamily: operation.requestedEndpointFamily
        })).accounts
      }
      return await requestSqliteReadWorker({
        type: 'list_openai_accounts_for_group_read_only',
        groupId: operation.groupId,
        systemAccountId: operation.systemAccountId,
        options: {
          requestedModel: operation.requestedModel,
          requestedEndpointFamily: operation.requestedEndpointFamily
        }
      })
    case 'list_openai_accounts_for_group_result':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await listOpenAIAccountsForGroupResultAsync(operation.groupId, operation.systemAccountId, {
          requestedModel: operation.requestedModel,
          requestedEndpointFamily: operation.requestedEndpointFamily
        })
      }
      return await requestSqliteReadWorker({
        type: 'list_openai_accounts_for_group_result_read_only',
        groupId: operation.groupId,
        systemAccountId: operation.systemAccountId,
        options: {
          requestedModel: operation.requestedModel,
          requestedEndpointFamily: operation.requestedEndpointFamily
        }
      })
    case 'find_openai_account_for_group':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await findOpenAIAccountForGroupAsync(operation.groupId, operation.accountId, operation.systemAccountId, {
          includeUnavailable: operation.includeUnavailable,
          ignoreAvailability: operation.ignoreAvailability
        })
      }
      return await requestSqliteReadWorker({
        type: 'find_openai_account_for_group_read_only',
        groupId: operation.groupId,
        accountId: operation.accountId,
        systemAccountId: operation.systemAccountId,
        options: {
          includeUnavailable: operation.includeUnavailable,
          ignoreAvailability: operation.ignoreAvailability
        }
      })
    case 'list_recoverable_unavailable_openai_accounts_for_group':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return recoverableUnavailableOpenAIAccounts(await listOpenAIAccountsForGroupResultAsync(operation.groupId, operation.systemAccountId, {
          requestedModel: operation.requestedModel,
          requestedEndpointFamily: operation.requestedEndpointFamily,
          includeUnavailable: true
        }), operation.windowMs)
      }
      return recoverableUnavailableOpenAIAccounts(await requestSqliteReadWorker({
        type: 'list_openai_accounts_for_group_result_read_only',
        groupId: operation.groupId,
        systemAccountId: operation.systemAccountId,
        options: {
          requestedModel: operation.requestedModel,
          requestedEndpointFamily: operation.requestedEndpointFamily,
          includeUnavailable: true
        }
      }), operation.windowMs)
    case 'read_gateway_runtime':
      return await readGatewayRuntimeAsync(operation)
    case 'create_openai_compatible_file':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await createOpenAICompatibleFileAsync(operation.input)
      }
      return handleDbServiceOperationSync(operation)
    case 'list_openai_compatible_files':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await listOpenAICompatibleFilesAsync(operation.options)
      }
      return await requestSqliteReadWorker({
        type: 'list_openai_compatible_files_read_only',
        options: operation.options
      })
    case 'get_openai_compatible_file':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await findOpenAICompatibleFileAsync({
          fileId: operation.fileId,
          systemAccountId: operation.systemAccountId,
          apiKeyId: operation.apiKeyId
        })
      }
      return await requestSqliteReadWorker({
        type: 'get_openai_compatible_file_read_only',
        fileId: operation.fileId,
        systemAccountId: operation.systemAccountId,
        apiKeyId: operation.apiKeyId
      })
    case 'delete_openai_compatible_file':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await deleteOpenAICompatibleFileAsync({
          fileId: operation.fileId,
          systemAccountId: operation.systemAccountId,
          apiKeyId: operation.apiKeyId
        })
      }
      return handleDbServiceOperationSync(operation)
    case 'create_openai_compatible_vector_store':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await createOpenAICompatibleVectorStoreAsync(operation.input)
      }
      return handleDbServiceOperationSync(operation)
    case 'list_openai_compatible_vector_stores':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await listOpenAICompatibleVectorStoresAsync(operation.options)
      }
      return await requestSqliteReadWorker({
        type: 'list_openai_compatible_vector_stores_read_only',
        options: operation.options
      })
    case 'get_openai_compatible_vector_store':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await findOpenAICompatibleVectorStoreAsync({
          vectorStoreId: operation.vectorStoreId,
          systemAccountId: operation.systemAccountId,
          apiKeyId: operation.apiKeyId
        })
      }
      return await requestSqliteReadWorker({
        type: 'get_openai_compatible_vector_store_read_only',
        vectorStoreId: operation.vectorStoreId,
        systemAccountId: operation.systemAccountId,
        apiKeyId: operation.apiKeyId
      })
    case 'delete_openai_compatible_vector_store':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await deleteOpenAICompatibleVectorStoreAsync({
          vectorStoreId: operation.vectorStoreId,
          systemAccountId: operation.systemAccountId,
          apiKeyId: operation.apiKeyId
        })
      }
      return handleDbServiceOperationSync(operation)
    case 'create_openai_compatible_vector_store_file':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await createOpenAICompatibleVectorStoreFileAsync(operation.input)
      }
      return handleDbServiceOperationSync(operation)
    case 'list_openai_compatible_vector_store_files':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await listOpenAICompatibleVectorStoreFilesAsync(operation.options)
      }
      return await requestSqliteReadWorker({
        type: 'list_openai_compatible_vector_store_files_read_only',
        options: operation.options
      })
    case 'get_openai_compatible_vector_store_file':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await findOpenAICompatibleVectorStoreFileAsync({
          vectorStoreId: operation.vectorStoreId,
          fileId: operation.fileId,
          systemAccountId: operation.systemAccountId,
          apiKeyId: operation.apiKeyId
        })
      }
      return await requestSqliteReadWorker({
        type: 'get_openai_compatible_vector_store_file_read_only',
        vectorStoreId: operation.vectorStoreId,
        fileId: operation.fileId,
        systemAccountId: operation.systemAccountId,
        apiKeyId: operation.apiKeyId
      })
    case 'delete_openai_compatible_vector_store_file':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await deleteOpenAICompatibleVectorStoreFileAsync({
          vectorStoreId: operation.vectorStoreId,
          fileId: operation.fileId,
          systemAccountId: operation.systemAccountId,
          apiKeyId: operation.apiKeyId
        })
      }
      return handleDbServiceOperationSync(operation)
    case 'search_openai_compatible_vector_store':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await searchOpenAICompatibleVectorStoreAsync(operation.options)
      }
      return await requestSqliteReadWorker({
        type: 'search_openai_compatible_vector_store_read_only',
        options: operation.options
      })
    case 'list_openai_compatible_vector_store_file_chunks':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await listOpenAICompatibleVectorStoreFileChunksAsync({
          vectorStoreId: operation.vectorStoreId,
          fileId: operation.fileId,
          systemAccountId: operation.systemAccountId,
          apiKeyId: operation.apiKeyId,
          limit: operation.limit
        })
      }
      return await requestSqliteReadWorker({
        type: 'list_openai_compatible_vector_store_file_chunks_read_only',
        vectorStoreId: operation.vectorStoreId,
        fileId: operation.fileId,
        systemAccountId: operation.systemAccountId,
        apiKeyId: operation.apiKeyId,
        limit: operation.limit
      })
    case 'list_provider_model_catalog':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await listProviderModelCatalogAsync({
          providerCode: operation.providerCode,
          systemAccountId: operation.systemAccountId,
          includeInactive: operation.includeInactive,
          includeUnpriced: operation.includeUnpriced
        })
      }
      return await requestSqliteReadWorker({
        type: 'list_provider_model_catalog_read_only',
        options: {
          providerCode: operation.providerCode,
          systemAccountId: operation.systemAccountId,
          includeInactive: operation.includeInactive,
          includeUnpriced: operation.includeUnpriced
        }
      })
    case 'find_account_for_test':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await findAccountForTestAsync(operation.accountId, operation.access)
      }
      return await requestSqliteReadWorker({
        type: 'find_account_for_test_read_only',
        accountId: operation.accountId,
        access: operation.access
      })
    case 'mark_account_test_temporary_unavailable': {
      if (runtimeConfig.databaseDriver === 'postgres') {
        const account = await findAccountForTestAsync(operation.accountId, operation.access ?? internalDbServiceAccountAccess)
        const updated = account
          ? await markAccountTestTemporaryUnavailableAsync(
              account,
              operation.reason,
              operation.access ?? internalDbServiceAccountAccess,
              operation.healthCheckGuard,
              operation.traceId
            )
          : undefined
        if (updated) {
          clearGatewayRuntimeCacheLocal()
        }
        return { updated: Boolean(updated), accountStatus: updated?.status }
      }
      return handleDbServiceOperationSync(operation)
    }
    case 'clear_account_failure_state': {
      if (runtimeConfig.databaseDriver === 'postgres') {
        const result = operation.authorizedBinding
          ? await clearAuthorizedAccountBindingFailureStateByContextAsync({
            accountId: operation.accountId,
            ...operation.authorizedBinding
          }, {
            allowPendingTestRestore: operation.allowPendingTestRestore,
            allowErrorRestore: operation.allowErrorRestore,
            expectedLastErrorCodes: operation.expectedLastErrorCodes
          })
          : await clearAccountFailureStateResultAsync(operation.accountId, internalDbServiceAccountAccess, {
            allowPendingTestRestore: operation.allowPendingTestRestore,
            allowErrorRestore: operation.allowErrorRestore,
            expectedLastErrorCodes: operation.expectedLastErrorCodes
          })
        if (result.changed) {
          clearGatewayRuntimeCacheLocal()
        }
        return { changed: result.changed, accountStatus: result.account?.status }
      }
      return handleDbServiceOperationSync(operation)
    }
    case 'account_test_task_maintenance':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await handleAccountTestTaskMaintenanceAsync(operation)
      }
      return handleDbServiceOperationSync(operation)
    case 'mark_account_test_task_running':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await markAccountTestTaskRunningAsync(operation.taskId)
      }
      return handleDbServiceOperationSync(operation)
    case 'mark_account_test_task_canceled':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await markAccountTestTaskCanceledAsync(operation.taskId, operation.message)
      }
      return handleDbServiceOperationSync(operation)
    case 'complete_account_test_task':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await completeAccountTestTaskAsync(operation.taskId, operation.result)
      }
      return handleDbServiceOperationSync(operation)
    case 'fail_account_test_task':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await failAccountTestTaskAsync(operation.taskId, operation.message, operation.result)
      }
      return handleDbServiceOperationSync(operation)
    case 'update_account_test_task_message':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await updateAccountTestTaskMessageAsync(operation.taskId, operation.message)
      }
      return handleDbServiceOperationSync(operation)
    case 'is_account_test_task_cancel_requested':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return { canceled: await isAccountTestTaskCancelRequestedAsync(operation.taskId) }
      }
      return {
        canceled: await requestSqliteReadWorker({
          type: 'is_account_test_task_cancel_requested_read_only',
          id: operation.taskId
        })
      }
    case 'read_account_test_task_cancel_message':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return { message: await accountTestTaskCancelMessageAsync(operation.taskId) }
      }
      return {
        message: await requestSqliteReadWorker({
          type: 'read_account_test_task_cancel_message_read_only',
          id: operation.taskId
        })
      }
    case 'record_account_api_key_failure': {
      const authorization = authorizeAccountApiKeyPersistentMutationForTrafficSource(
        'failure',
        operation.trafficSource,
        operation.mutationContext
      )
      if (!authorization.allowed) {
        return unauthorizedAccountApiKeyMutationResult(authorization.reason)
      }
      if (runtimeConfig.databaseDriver === 'postgres') {
        const result = await recordAccountApiKeyRuntimeFailureAsync({
          account: operation.account,
          ...operation.input
        })
        if (result.changed) {
          clearGatewayRuntimeCacheLocal()
        }
        return result
      }
      return handleDbServiceOperationSync(operation)
    }
    case 'record_account_api_key_success': {
      const authorization = authorizeAccountApiKeyPersistentMutationForTrafficSource(
        'success',
        operation.trafficSource,
        operation.mutationContext
      )
      if (!authorization.allowed) {
        return unauthorizedAccountApiKeyMutationResult(authorization.reason)
      }
      if (runtimeConfig.databaseDriver === 'postgres') {
        const result = await recordAccountApiKeyRuntimeSuccessAsync(operation.account, {
          observedAt: operation.observedAt,
          expectedStatus: operation.expectedStatus,
          expectedNextProbeAt: operation.expectedNextProbeAt,
          expectedStateUpdatedAt: operation.expectedStateUpdatedAt,
          expectedAccountConfigRevision: operation.expectedAccountConfigRevision,
          expectedProbeClaimToken: operation.expectedProbeClaimToken
        })
        if (result.changed) {
          clearGatewayRuntimeCacheLocal()
        }
        return result
      }
      return handleDbServiceOperationSync(operation)
    }
    case 'defer_account_api_key_probe': {
      const authorization = authorizeAccountApiKeyPersistentMutationForTrafficSource(
        'defer',
        operation.trafficSource,
        operation.mutationContext
      )
      if (!authorization.allowed) {
        return unauthorizedAccountApiKeyMutationResult(authorization.reason)
      }
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await deferAccountApiKeyRuntimeProbeAsync({
          account: operation.account,
          ...operation.input
        })
      }
      return handleDbServiceOperationSync(operation)
    }
    case 'update_proxy_test_state': {
      if (runtimeConfig.databaseDriver === 'postgres') {
        const updated = await updateProxyTestStateAsync(operation.proxyId, operation.input)
        return { updated: Boolean(updated), proxyStatus: updated?.testStatus }
      }
      return handleDbServiceOperationSync(operation)
    }
    case 'update_openai_oauth_credentials': {
      if (runtimeConfig.databaseDriver === 'postgres') {
        const updated = await updateOpenAIOAuthCredentialsIfCurrentAsync(
          operation.accountId,
          operation.credentials,
          operation.expectedConfigRevision,
          internalDbServiceAccountAccess
        )
        if (updated) {
          clearGatewayRuntimeCacheLocal()
        }
        return { updated: Boolean(updated) }
      }
      return handleDbServiceOperationSync(operation)
    }
    case 'mark_openai_oauth_local_configuration_exception': {
      if (runtimeConfig.databaseDriver === 'postgres') {
        const updated = await markOpenAIOAuthLocalConfigurationExceptionIfCurrentAsync(operation)
        if (updated) clearGatewayRuntimeCacheLocal()
        return { updated }
      }
      return handleDbServiceOperationSync(operation)
    }
    case 'sync_api_key_availability_schedule_statuses': {
      if (runtimeConfig.databaseDriver === 'postgres') {
        const result = await syncApiKeyAvailabilityScheduleStatusesAsync()
        if (result.changedIds.length > 0) {
          clearGatewayRuntimeCacheLocal()
        }
        return result
      }
      return handleDbServiceOperationSync(operation)
    }
    case 'sync_account_availability_schedule_statuses': {
      if (runtimeConfig.databaseDriver === 'postgres') {
        const result = await syncAccountAvailabilityScheduleStatusesAsync()
        if (result.changedIds.length > 0) {
          clearGatewayRuntimeCacheLocal()
        }
        return result
      }
      return handleDbServiceOperationSync(operation)
    }
    case 'expire_due_resource_authorizations': {
      if (runtimeConfig.databaseDriver === 'postgres') {
        const expired = await expireDueResourceAuthorizationsAsync()
        if (expired > 0) {
          clearGatewayRuntimeCacheLocal()
        }
        return { expired }
      }
      return handleDbServiceOperationSync(operation)
    }
    case 'find_openai_oauth_account_for_refresh':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await findOpenAIOAuthAccountForRefreshAsync(operation.accountId)
      }
      return await requestSqliteReadWorker({
        type: 'find_openai_oauth_account_for_refresh_read_only',
        accountId: operation.accountId
      })
    case 'persist_openai_codex_usage_headers':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return {
          persisted: await persistOpenAICodexUsageHeadersAsync(operation.accountId, operation.headers, operation.source)
        }
      }
      return handleDbServiceOperationSync(operation)
    case 'apply_account_error_handling': {
      if (runtimeConfig.databaseDriver === 'postgres') {
        const result = await applyAccountErrorHandlingAsync(operation.account, operation.input)
        if (result.changed) {
          clearGatewayRuntimeCacheLocal()
        }
        return result
      }
      return handleDbServiceOperationSync(operation)
    }
    case 'record_account_stream_failure': {
      if (runtimeConfig.databaseDriver === 'postgres') {
        const authorizedTarget = authorizedBindingRuntimeTarget(operation.input.account)
        const result = authorizedTarget
          ? await recordAuthorizedAccountBindingStreamFailureAsync({
              ...operation.input,
              ...authorizedTarget
            })
          : await recordAccountStreamFailureAsync(operation.input)
        if (result.triggered) {
          clearGatewayRuntimeCacheLocal()
        }
        return { count: result.count, triggered: result.triggered }
      }
      return handleDbServiceOperationSync(operation)
    }
    case 'mark_account_precheck_temporary_unavailable': {
      if (runtimeConfig.databaseDriver === 'postgres') {
        const authorizedTarget = authorizedBindingRuntimeTarget(operation.account)
        const staleReason = await precheckTemporaryUnavailableSkipReasonAsync(operation, authorizedTarget)
        if (staleReason) {
          return { updated: false, skippedReason: staleReason }
        }
        const updated = await applyPrecheckErrorPolicyTargetAsync(operation, authorizedTarget)
        if (updated) {
          clearGatewayRuntimeCacheLocal()
        }
        return {
          updated: Boolean(updated),
          ...(!updated
            ? { skippedReason: await precheckTemporaryUnavailableSkipReasonAsync(operation, authorizedTarget) ?? 'mutation_fence_conflict' }
            : {})
        }
      }
      return handleDbServiceOperationSync(operation)
    }
    case 'mark_account_temporary_unavailable': {
      if (runtimeConfig.databaseDriver === 'postgres') {
        const authorizedTarget = authorizedBindingRuntimeTarget(operation.account)
        const updated = authorizedTarget
          ? await markAuthorizedAccountBindingTemporaryUnavailableByContextAsync({
              ...authorizedTarget,
              reason: operation.reason,
              traceId: operation.traceId
            })
          : await markAccountTemporaryUnavailableAsync(operation.account.id, operation.reason, undefined, operation.traceId)
        if (updated) {
          clearGatewayRuntimeCacheLocal()
        }
        return { updated: Boolean(updated) }
      }
      return handleDbServiceOperationSync(operation)
    }
    case 'list_active_client_ip_policies':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await listActiveClientIpPoliciesAsync()
      }
      return await requestSqliteReadWorker({
        type: 'list_active_client_ip_policies_read_only'
      })
    case 'list_active_response_inspection_policies':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await listActiveResponseInspectionPoliciesForGatewayAsync({
          protocolCode: operation.protocolCode,
          providerCode: operation.providerCode
        })
      }
      return await requestSqliteReadWorker({
        type: 'list_active_response_inspection_policies_read_only',
        input: {
          protocolCode: operation.protocolCode,
          providerCode: operation.providerCode
        }
      })
    case 'check_api_key_quota':
      return runtimeConfig.databaseDriver === 'postgres'
        ? await checkGatewayApiKeyQuotaExactAsync(operation.apiKey)
        : await requestSqliteReadWorker({
            type: 'check_api_key_quota_read_only',
            apiKey: operation.apiKey
          })
    case 'read_api_key_quota_costs':
      return runtimeConfig.databaseDriver === 'postgres'
        ? await readGatewayApiKeyQuotaCostsExactAsync(operation.apiKey)
        : await requestSqliteReadWorker({
            type: 'read_api_key_quota_costs_read_only',
            apiKey: operation.apiKey
          })
    case 'check_authorization_quota':
      return runtimeConfig.databaseDriver === 'postgres'
        ? await checkGatewayAuthorizationQuotaByIdsExactAsync({
            groupAuthorizationId: operation.groupAuthorizationId,
            accountAuthorizationId: operation.accountAuthorizationId
          })
        : await requestSqliteReadWorker({
            type: 'check_authorization_quota_read_only',
            groupAuthorizationId: operation.groupAuthorizationId,
            accountAuthorizationId: operation.accountAuthorizationId
          })
    case 'check_authorization_quota_batch':
      return runtimeConfig.databaseDriver === 'postgres'
        ? await checkGatewayAuthorizationQuotaBatchByIdsExactAsync({
            groupAuthorizationId: operation.groupAuthorizationId,
            accounts: operation.accounts
          })
        : await requestSqliteReadWorker({
            type: 'check_authorization_quota_batch_read_only',
            groupAuthorizationId: operation.groupAuthorizationId,
            accounts: operation.accounts
          })
    case 'list_accounts_due_for_health_check':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await listAccountsDueForHealthCheckAsync(operation.input)
      }
      return handleDbServiceOperationSync(operation)
    case 'find_account_for_health_check':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await findAccountForHealthCheckAsync(operation.accountId)
      }
      return handleDbServiceOperationSync(operation)
    case 'record_account_health_check_success':
      if (runtimeConfig.databaseDriver === 'postgres') {
        const changed = await recordAccountHealthCheckSuccessAsync(operation.accountId, operation.input)
        if (changed) {
          clearGatewayRuntimeCacheLocal()
        }
        return { changed }
      }
      return handleDbServiceOperationSync(operation)
    case 'commit_account_balance_refresh':
      return { changed: await commitAccountBalanceRefreshAsync(operation.input) }
    case 'enable_detected_account_balance_query':
      return { changed: await enableDetectedAccountBalanceQueryAsync(operation.input) }
    case 'record_account_health_check_failure':
      if (runtimeConfig.databaseDriver === 'postgres') {
        const result = await recordAccountHealthCheckFailureAsync(operation.accountId, operation.input)
        if (result.changed) {
          clearGatewayRuntimeCacheLocal()
        }
        return result
      }
      return handleDbServiceOperationSync(operation)
    case 'list_accounts_due_for_cooldown_retest':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await listAccountsDueForCooldownRetestPageAsync(operation.limit, operation.cursor)
      }
      return handleDbServiceOperationSync(operation)
    case 'find_account_for_cooldown_retest':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await findAccountForCooldownRetestAsync(operation.accountId)
      }
      return handleDbServiceOperationSync(operation)
    case 'record_cooldown_account_retest_success':
      if (runtimeConfig.databaseDriver === 'postgres') {
        const result = await recordCooldownAccountRetestSuccessAsync(operation.accountId, {
          expectedConfigRevision: operation.expectedConfigRevision,
          expectedDispatchRevision: operation.expectedDispatchRevision,
          expectedObservationStartedAt: operation.expectedObservationStartedAt,
          expectedGeneration: operation.expectedGeneration,
          expectedSourceConfigRevision: operation.expectedSourceConfigRevision
        })
        if (result.changed) clearGatewayRuntimeCacheLocal()
        return result
      }
      return handleDbServiceOperationSync(operation)
    case 'record_cooldown_account_retest_failure':
      if (runtimeConfig.databaseDriver === 'postgres') {
        const result = await recordCooldownAccountRetestFailureAsync(operation.accountId, operation.input)
        if (result.changed) {
          clearGatewayRuntimeCacheLocal()
        }
        return {
          changed: result.changed,
          failureCount: result.failureCount,
          action: result.action,
          cooldownUntil: result.cooldownUntil,
          backoffSeconds: result.backoffSeconds,
          backoffMinutes: result.backoffMinutes,
          recoveryStage: result.recoveryStage,
          fastThresholdSeconds: result.fastThresholdSeconds,
          maxPauseSeconds: result.maxPauseSeconds,
          maxRecoverySeconds: result.maxRecoverySeconds,
          longTermIntervalSeconds: result.longTermIntervalSeconds,
          maxedFailureCount: result.maxedFailureCount,
          observationStartedAt: result.observationStartedAt,
          observationElapsedSeconds: result.observationElapsedSeconds,
          observationTimeoutSeconds: result.observationTimeoutSeconds,
          transitionedToError: result.transitionedToError,
          errorCode: result.errorCode,
          errorMessage: result.errorMessage
        }
      }
      return handleDbServiceOperationSync(operation)
    case 'defer_cooldown_account_retest':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await deferCooldownAccountRetestAsync(operation.accountId, {
          delaySeconds: operation.delaySeconds,
          expectedConfigRevision: operation.expectedConfigRevision,
          expectedDispatchRevision: operation.expectedDispatchRevision,
          expectedObservationStartedAt: operation.expectedObservationStartedAt,
          expectedGeneration: operation.expectedGeneration,
          expectedSourceConfigRevision: operation.expectedSourceConfigRevision
        })
      }
      return handleDbServiceOperationSync(operation)
    case 'mark_account_exception': {
      if (runtimeConfig.databaseDriver === 'postgres') {
        const updated = await markAccountExceptionAsync(operation.accountId, operation.errorCode, operation.reason, {
          preserveDisabled: operation.preserveDisabled,
          traceId: operation.traceId,
          expectedConfigRevision: operation.expectedConfigRevision,
          expectedStatus: operation.expectedStatus
        })
        if (updated) {
          clearGatewayRuntimeCacheLocal()
        }
        return { updated: Boolean(updated), accountStatus: updated?.status }
      }
      return handleDbServiceOperationSync(operation)
    }
    case 'clear_account_stream_failure_state': {
      if (runtimeConfig.databaseDriver === 'postgres') {
        const authorizedTarget = authorizedBindingRuntimeTarget(operation.account)
        const accountId = operation.account?.id ?? operation.accountId
        const changed = authorizedTarget
          ? await clearAuthorizedAccountBindingStreamFailureStateAsync(authorizedTarget)
          : accountId ? await clearAccountStreamFailureStateAsync(accountId) : false
        if (changed) {
          clearGatewayRuntimeCacheLocal()
        }
        return { changed }
      }
      return handleDbServiceOperationSync(operation)
    }
    case 'clear_gateway_runtime_cache':
      return handleDbServiceOperationSync(operation)
    case 'mark_all_group_account_stats_dirty':
      if (runtimeConfig.databaseDriver === 'postgres') {
        await markAllGroupAccountStatsDirtyAsync(operation.reason)
        return { marked: true }
      }
      return handleDbServiceOperationSync(operation)
    case 'delete_group_account_stats_dirty_rows':
      if (runtimeConfig.databaseDriver === 'postgres') {
        await deleteGroupAccountStatsDirtyRowsAsync(
          operation.rows.map((row): GroupAccountStatsDirtyRow => ({
            groupId: row.groupId,
            reason: null,
            updatedAt: row.updatedAt
          }))
        )
        return { deleted: true }
      }
      return handleDbServiceOperationSync(operation)
    case 'update_group_account_stats_all_cursor':
      if (runtimeConfig.databaseDriver === 'postgres') {
        await updateGroupAccountStatsAllCursorAsync(operation.cursorGroupId)
        return { updated: true }
      }
      return handleDbServiceOperationSync(operation)
    case 'list_runtime_logs':
      return await listRuntimeLogsAsync(operation.options)
    case 'get_runtime_log_detail':
      return await getRuntimeLogDetailAsync(operation.id)
    case 'get_runtime_log_facets':
      return await getRuntimeLogFacetsAsync()
    case 'record_client_ip_policy_hits':
      throw new Error('record_client_ip_policy_hits 必须投递 stats-writer，禁止在 DB service 写 stats DB')
    case 'status':
      return buildDbServiceRuntimeSnapshot()
    case 'save_codex_context_response_state':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await saveCodexContextResponseStateIndexAsync(operation.input)
      }
      return await saveCodexContextResponseStateIndexWithWriterPool(operation.input)
    case 'save_codex_context_compact_state':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await saveCodexContextCompactStateIndexAsync(operation.input)
      }
      return await saveCodexContextCompactStateIndexWithWriterPool(operation.input)
    case 'read_codex_context_response_chain':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await readCodexContextResponseStateChainAsync({
          responseId: operation.responseId,
          boundary: operation.boundary,
          maxDepth: operation.maxDepth,
          now: operation.now,
          refreshExpiresAt: operation.refreshExpiresAt
        })
      }
      return await readCodexContextResponseStateChainWithWriterPool({
        responseId: operation.responseId,
        boundary: operation.boundary,
        maxDepth: operation.maxDepth,
        now: operation.now,
        refreshExpiresAt: operation.refreshExpiresAt
      })
    case 'read_codex_context_compact_state':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await readCodexContextCompactStateAsync({
          compactId: operation.compactId,
          boundary: operation.boundary,
          now: operation.now,
          refreshExpiresAt: operation.refreshExpiresAt
        })
      }
      return await readCodexContextCompactStateWithWriterPool({
        compactId: operation.compactId,
        boundary: operation.boundary,
        now: operation.now,
        refreshExpiresAt: operation.refreshExpiresAt
      })
    case 'cleanup_expired_codex_context_states':
      if (runtimeConfig.databaseDriver === 'postgres') {
        return await cleanupExpiredCodexContextStatesAsync({
          expiredBefore: operation.expiredBefore,
          limit: operation.limit
        })
      }
      return await cleanupExpiredCodexContextStatesWithWriterPool({
        expiredBefore: operation.expiredBefore,
        limit: operation.limit
      })
    case 'cleanup_expired_deleted_accounts':
      if (runtimeConfig.databaseDriver === 'postgres') {
        const result = await cleanupExpiredLogicallyDeletedAccountsAsync()
        if (result.attempted > 0 || result.orphanedAuthorizationInstances > 0) {
          clearGatewayRuntimeCacheLocal()
        }
        return result
      }
      return handleDbServiceOperationSync(operation)
    case 'cleanup_expired_system_sessions':
      return {
        deleted: runtimeConfig.databaseDriver === 'postgres'
          ? await cleanupExpiredSystemSessionsAsync(operation.expiredBefore, operation.limit)
          : cleanupExpiredSystemSessions(operation.expiredBefore, operation.limit)
      }
    case 'advance_account_circuit_dispatch_revision':
      return await advanceAccountCircuitDispatchRevision(operation.input)
    case 'compare_and_set_account_circuit_incident':
      return await compareAndSetAccountCircuitIncident(operation.input)
    case 'get_account_circuit_incident_by_scope_key':
      return await getAccountCircuitIncidentByScopeKey(operation.circuitScopeKey)
    case 'claim_account_circuit_outbox':
      return await claimAccountCircuitOutbox(operation)
    case 'ack_account_circuit_outbox':
      return {
        acknowledged: await acknowledgeAccountCircuitOutbox(operation)
      }
    case 'release_account_circuit_outbox_for_replay':
      return {
        released: await releaseAccountCircuitOutboxForReplay(operation)
      }
    case 'list_account_circuit_incidents_for_rebuild':
      return await listAccountCircuitIncidentsForRebuild(operation)
    case 'list_account_circuit_incidents_by_runtime_keys':
      return await listAccountCircuitIncidentsByRuntimeKeys(operation.accountRuntimeKeys, {
        includeRetainedClosed: operation.includeRetainedClosed,
        nowMs: operation.nowMs
      })
    case 'list_account_circuit_projection_gaps':
      return await listAccountCircuitProjectionGaps(operation)
    case 'cleanup_account_circuit_control_plane':
      return await cleanupAccountCircuitControlPlane(operation)
    case 'cleanup_chat_retention': {
      const client = await getChatDatabaseClient()
      const retention = await cleanupChatRetention(client, { ...operation, isActiveTurn: isActiveChatGeneration })
      const contextMaintenanceLimit = Math.max(1, Math.min(Math.trunc(operation.limit), chatContextMaintenanceMaxBatchSize))
      const recoveredCompactions = await recoverStaleChatContextCompactions(client, {
        now: operation.now,
        staleClaimBefore: operation.interruptedBefore,
        limit: contextMaintenanceLimit
      })
      const context = await cleanupExpiredChatContextCheckpoints(client, { now: operation.now, limit: contextMaintenanceLimit })
      const assets = await cleanupExpiredChatAssets({ client, now: operation.now, limit: contextMaintenanceLimit })
      return { ...retention, recoveredCompactions, ...context, ...assets, hasMoreCheckpoints: context.hasMore, hasMore: retention.hasMore || context.hasMore || assets.hasMoreAssets }
    }
    default:
      if (runtimeConfig.databaseDriver === 'postgres') {
        throw new Error(`PostgreSQL DB service operation 未接入 async driver：${operationType}`)
      }
      return handleDbServiceOperationSync(operation)
  }
}

async function handleAccountTestTaskMaintenanceAsync(
  operation: Extract<DbServiceOperation, { type: 'account_test_task_maintenance' }>
): Promise<{ taskIds: string[]; canceledTaskIds: string[]; expiredQueuedTaskIds: string[] }> {
  await cleanupExpiredAccountTestTasksAsync()
  if (operation.action === 'start' || operation.action === 'sweep') {
    await completeIdleAccountTestSessionsAsync()
  }
  const canceledTaskIds: string[] = []
  const expiredQueuedTaskIds = operation.action === 'sweep'
    ? await failExpiredQueuedAccountTestTasksAsync(operation.maxQueuedMs ?? 10 * 60_000, operation.sweepLimit ?? 500)
    : []
  const taskIds = operation.action === 'start'
    ? await requeueInterruptedAccountTestTasksAsync()
    : await listRunnableAccountTestTaskIdsAsync(operation.refillLimit ?? 100)
  return { taskIds, canceledTaskIds, expiredQueuedTaskIds }
}

export function buildDbServiceRuntimeSnapshot(pid = process.pid): DbServiceRuntimeSnapshot {
  const queueRuntime = dbServiceQueueRuntimeProvider?.()
  return {
    pid,
    ready: true,
    processRole: 'db-service',
    httpHost: dbServiceHttpEndpoint?.host,
    httpPort: dbServiceHttpEndpoint?.port,
    eventLoopLagMs: currentProcessEventLoopLagMs(),
    pendingRequestCount,
    queuedRequestCount: queueRuntime?.queuedRequestCount,
    queuedRequestBytes: queueRuntime?.queuedRequestBytes,
    queuedHighRequestCount: queueRuntime?.queuedHighRequestCount,
    queuedNormalRequestCount: queueRuntime?.queuedNormalRequestCount,
    queuedLowRequestCount: queueRuntime?.queuedLowRequestCount,
    oldestQueuedMs: queueRuntime?.oldestQueuedMs,
    lastQueueWaitMs: queueRuntime?.lastQueueWaitMs,
    maxQueueWaitMs: queueRuntime?.maxQueueWaitMs,
    queueRejectedCount: queueRuntime?.queueRejectedCount,
    queueExpiredCount: queueRuntime?.queueExpiredCount,
    activeConcurrentRequestCount: queueRuntime?.activeConcurrentRequestCount,
    maxActiveConcurrentRequestCount: queueRuntime?.maxActiveConcurrentRequestCount,
    lastExecMs,
    maxExecMs,
    slowOpCount,
    lastSlowOpType,
    lastSlowOpMs,
    lastSlowOpAt,
    handledRequestCount,
    failedRequestCount,
    lastRequestAt,
    lastError,
    codexContextStateWriterPool: getCodexContextStateWriterPoolRuntime(),
    sqliteReadWorkerPool: getSqliteReadWorkerPoolRuntime()
  }
}

export function setDbServiceHttpEndpoint(endpoint: { host: string; port: number }): void {
  dbServiceHttpEndpoint = endpoint
}

export function setDbServiceQueueRuntimeProvider(provider: () => DbServiceQueueRuntimeMetrics): void {
  dbServiceQueueRuntimeProvider = provider
}

function recordDbServiceOperationDuration(operationType: string, durationMs: number): void {
  const rounded = Math.round(durationMs)
  lastExecMs = rounded
  maxExecMs = Math.max(maxExecMs, rounded)
  if (rounded < slowDbServiceOperationMs) {
    return
  }
  slowOpCount += 1
  lastSlowOpType = operationType
  lastSlowOpMs = rounded
  lastSlowOpAt = new Date().toISOString()
}

function handleDbServiceOperationSync(operation: DbServiceOperation): unknown {
  switch (operation.type) {
    case 'list_public_global_settings':
      return listPublicGlobalSettings()
    case 'validate_gateway_api_key':
      return validateGatewayApiKey(operation.key)
    case 'read_gateway_settings':
      return readCachedGatewaySettings()
    case 'resolve_group_usage_access':
      return resolveGroupUsageAccessMetadata(operation.groupId, operation.systemAccountId)
    case 'list_openai_accounts_for_group':
      return listOpenAIAccountsForGroup(operation.groupId, operation.systemAccountId, {
        requestedModel: operation.requestedModel,
        requestedEndpointFamily: operation.requestedEndpointFamily
      })
    case 'list_openai_accounts_for_group_result':
      return listOpenAIAccountsForGroupResult(operation.groupId, operation.systemAccountId, {
        requestedModel: operation.requestedModel,
        requestedEndpointFamily: operation.requestedEndpointFamily
      })
    case 'find_openai_account_for_group':
      return findOpenAIAccountForGroup(operation.groupId, operation.accountId, operation.systemAccountId, {
        includeUnavailable: operation.includeUnavailable,
        ignoreAvailability: operation.ignoreAvailability
      })
    case 'list_recoverable_unavailable_openai_accounts_for_group':
      return listRecoverableUnavailableOpenAIAccountsForGroup(operation.groupId, operation.systemAccountId, {
        requestedModel: operation.requestedModel,
        requestedEndpointFamily: operation.requestedEndpointFamily,
        windowMs: operation.windowMs
      })
    case 'read_gateway_runtime':
      return readGatewayRuntime(operation)
    case 'create_openai_compatible_file':
      return createOpenAICompatibleFile(operation.input)
    case 'list_openai_compatible_files':
      return listOpenAICompatibleFiles(operation.options)
    case 'get_openai_compatible_file':
      return findOpenAICompatibleFile({
        fileId: operation.fileId,
        systemAccountId: operation.systemAccountId,
        apiKeyId: operation.apiKeyId
      })
    case 'delete_openai_compatible_file':
      return deleteOpenAICompatibleFile({
        fileId: operation.fileId,
        systemAccountId: operation.systemAccountId,
        apiKeyId: operation.apiKeyId
      })
    case 'create_openai_compatible_vector_store':
      return createOpenAICompatibleVectorStore(operation.input)
    case 'list_openai_compatible_vector_stores':
      return listOpenAICompatibleVectorStores(operation.options)
    case 'get_openai_compatible_vector_store':
      return findOpenAICompatibleVectorStore({
        vectorStoreId: operation.vectorStoreId,
        systemAccountId: operation.systemAccountId,
        apiKeyId: operation.apiKeyId
      })
    case 'delete_openai_compatible_vector_store':
      return deleteOpenAICompatibleVectorStore({
        vectorStoreId: operation.vectorStoreId,
        systemAccountId: operation.systemAccountId,
        apiKeyId: operation.apiKeyId
      })
    case 'create_openai_compatible_vector_store_file':
      return createOpenAICompatibleVectorStoreFile(operation.input)
    case 'list_openai_compatible_vector_store_files':
      return listOpenAICompatibleVectorStoreFiles(operation.options)
    case 'get_openai_compatible_vector_store_file':
      return findOpenAICompatibleVectorStoreFile({
        vectorStoreId: operation.vectorStoreId,
        fileId: operation.fileId,
        systemAccountId: operation.systemAccountId,
        apiKeyId: operation.apiKeyId
      })
    case 'delete_openai_compatible_vector_store_file':
      return deleteOpenAICompatibleVectorStoreFile({
        vectorStoreId: operation.vectorStoreId,
        fileId: operation.fileId,
        systemAccountId: operation.systemAccountId,
        apiKeyId: operation.apiKeyId
      })
    case 'search_openai_compatible_vector_store':
      return searchOpenAICompatibleVectorStore(operation.options)
    case 'list_openai_compatible_vector_store_file_chunks':
      return listOpenAICompatibleVectorStoreFileChunks({
        vectorStoreId: operation.vectorStoreId,
        fileId: operation.fileId,
        systemAccountId: operation.systemAccountId,
        apiKeyId: operation.apiKeyId,
        limit: operation.limit
      })
    case 'list_provider_model_catalog':
      return listProviderModelCatalog({
        providerCode: operation.providerCode,
        systemAccountId: operation.systemAccountId,
        includeInactive: operation.includeInactive,
        includeUnpriced: operation.includeUnpriced
      })
    case 'check_api_key_quota':
      return runtimeConfig.databaseDriver === 'postgres'
        ? checkGatewayApiKeyQuotaExactAsync(operation.apiKey)
        : checkGatewayApiKeyQuota(operation.apiKey)
    case 'read_api_key_quota_costs':
      return runtimeConfig.databaseDriver === 'postgres'
        ? readGatewayApiKeyQuotaCostsExactAsync(operation.apiKey)
        : readGatewayApiKeyQuotaCostsExact(operation.apiKey)
    case 'check_authorization_quota':
      return runtimeConfig.databaseDriver === 'postgres'
        ? checkGatewayAuthorizationQuotaByIdsExactAsync({
            groupAuthorizationId: operation.groupAuthorizationId,
            accountAuthorizationId: operation.accountAuthorizationId
          })
        : checkGatewayAuthorizationQuotaByIds({
            groupAuthorizationId: operation.groupAuthorizationId,
            accountAuthorizationId: operation.accountAuthorizationId
          })
    case 'check_authorization_quota_batch':
      return runtimeConfig.databaseDriver === 'postgres'
        ? checkGatewayAuthorizationQuotaBatchByIdsExactAsync({
            groupAuthorizationId: operation.groupAuthorizationId,
            accounts: operation.accounts
          })
        : checkGatewayAuthorizationQuotaBatchByIds({
            groupAuthorizationId: operation.groupAuthorizationId,
            accounts: operation.accounts
          })
    case 'update_openai_oauth_credentials': {
      const updated = updateOpenAIOAuthCredentialsIfCurrent(
        operation.accountId,
        operation.credentials,
        operation.expectedConfigRevision,
        internalDbServiceAccountAccess
      )
      if (updated) {
        clearGatewayRuntimeCacheLocal()
      }
      return { updated: Boolean(updated) }
    }
    case 'mark_openai_oauth_local_configuration_exception': {
      const updated = markOpenAIOAuthLocalConfigurationExceptionIfCurrent(operation)
      if (updated) clearGatewayRuntimeCacheLocal()
      return { updated }
    }
    case 'find_openai_oauth_account_for_refresh':
      return findOpenAIOAuthAccountForRefresh(operation.accountId)
    case 'persist_openai_codex_usage_headers':
      return {
        persisted: persistOpenAICodexUsageHeaders(operation.accountId, operation.headers, operation.source)
      }
    case 'apply_account_error_handling': {
      const result = applyAccountErrorHandling(operation.account, operation.input)
      if (result.changed) {
        clearGatewayRuntimeCacheLocal()
      }
      return result
    }
    case 'record_account_api_key_failure': {
      const authorization = authorizeAccountApiKeyPersistentMutationForTrafficSource(
        'failure',
        operation.trafficSource,
        operation.mutationContext
      )
      if (!authorization.allowed) {
        return unauthorizedAccountApiKeyMutationResult(authorization.reason)
      }
      const result = recordAccountApiKeyRuntimeFailure({
        account: operation.account,
        ...operation.input
      })
      if (result.changed) {
        clearGatewayRuntimeCacheLocal()
      }
      return result
    }
    case 'record_account_api_key_success': {
      const authorization = authorizeAccountApiKeyPersistentMutationForTrafficSource(
        'success',
        operation.trafficSource,
        operation.mutationContext
      )
      if (!authorization.allowed) {
        return unauthorizedAccountApiKeyMutationResult(authorization.reason)
      }
      const result = recordAccountApiKeyRuntimeSuccess(operation.account, {
        observedAt: operation.observedAt,
        expectedStatus: operation.expectedStatus,
        expectedNextProbeAt: operation.expectedNextProbeAt,
        expectedStateUpdatedAt: operation.expectedStateUpdatedAt,
        expectedAccountConfigRevision: operation.expectedAccountConfigRevision,
        expectedProbeClaimToken: operation.expectedProbeClaimToken
      })
      if (result.changed) {
        clearGatewayRuntimeCacheLocal()
      }
      return result
    }
    case 'defer_account_api_key_probe': {
      const authorization = authorizeAccountApiKeyPersistentMutationForTrafficSource(
        'defer',
        operation.trafficSource,
        operation.mutationContext
      )
      if (!authorization.allowed) {
        return unauthorizedAccountApiKeyMutationResult(authorization.reason)
      }
      return deferAccountApiKeyRuntimeProbe({
        account: operation.account,
        ...operation.input
      })
    }
    case 'record_account_stream_failure': {
      const authorizedTarget = authorizedBindingRuntimeTarget(operation.input.account)
      const result = authorizedTarget
        ? recordAuthorizedAccountBindingStreamFailure({
            ...operation.input,
            ...authorizedTarget
          })
        : recordAccountStreamFailure(operation.input)
      if (result.triggered) {
        clearGatewayRuntimeCacheLocal()
      }
      return { count: result.count, triggered: result.triggered }
    }
    case 'mark_account_precheck_temporary_unavailable': {
      const authorizedTarget = authorizedBindingRuntimeTarget(operation.account)
      const staleReason = precheckTemporaryUnavailableSkipReason(operation, authorizedTarget)
      if (staleReason) {
        return { updated: false, skippedReason: staleReason }
      }
      const updated = applyPrecheckErrorPolicyTarget(operation, authorizedTarget)
      if (updated) {
        clearGatewayRuntimeCacheLocal()
      }
      return {
        updated: Boolean(updated),
        ...(!updated
          ? { skippedReason: precheckTemporaryUnavailableSkipReason(operation, authorizedTarget) ?? 'mutation_fence_conflict' }
          : {})
      }
    }
    case 'mark_account_temporary_unavailable': {
      const authorizedTarget = authorizedBindingRuntimeTarget(operation.account)
      const updated = authorizedTarget
        ? markAuthorizedAccountBindingTemporaryUnavailableByContext({
            ...authorizedTarget,
            reason: operation.reason,
            traceId: operation.traceId
          })
        : markAccountTemporaryUnavailable(operation.account.id, operation.reason, undefined, operation.traceId)
      if (updated) {
        clearGatewayRuntimeCacheLocal()
      }
      return { updated: Boolean(updated) }
    }
    case 'clear_account_failure_state': {
      const result = operation.authorizedBinding
        ? clearAuthorizedAccountBindingFailureStateByContext({
            accountId: operation.accountId,
            ...operation.authorizedBinding
          }, {
            allowPendingTestRestore: operation.allowPendingTestRestore,
            allowErrorRestore: operation.allowErrorRestore
          })
        : clearAccountFailureStateResult(operation.accountId, internalDbServiceAccountAccess, {
        allowPendingTestRestore: operation.allowPendingTestRestore,
        allowErrorRestore: operation.allowErrorRestore,
        expectedLastErrorCodes: operation.expectedLastErrorCodes
        })
      if (result.changed) {
        clearGatewayRuntimeCacheLocal()
      }
      return { changed: result.changed, accountStatus: result.account?.status }
    }
    case 'mark_account_test_temporary_unavailable': {
      const account = findAccountForTest(operation.accountId, operation.access ?? internalDbServiceAccountAccess)
      const updated = account
        ? markAccountTestTemporaryUnavailable(
            account,
            operation.reason,
            operation.access ?? internalDbServiceAccountAccess,
            operation.healthCheckGuard,
            operation.traceId
          )
        : undefined
      if (updated) {
        clearGatewayRuntimeCacheLocal()
      }
      return { updated: Boolean(updated), accountStatus: updated?.status }
    }
    case 'find_account_for_test': {
      return findAccountForTest(operation.accountId, operation.access)
    }
    case 'list_accounts_due_for_health_check': {
      return listAccountsDueForHealthCheck(operation.input)
    }
    case 'find_account_for_health_check': {
      return findAccountForHealthCheck(operation.accountId)
    }
    case 'record_account_health_check_success': {
      const changed = recordAccountHealthCheckSuccess(operation.accountId, operation.input)
      if (changed) {
        clearGatewayRuntimeCacheLocal()
      }
      return { changed }
    }
    case 'commit_account_balance_refresh':
    case 'enable_detected_account_balance_query':
      throw new Error(`DB service 操作 ${operation.type} 必须通过异步处理器执行`)
    case 'record_account_health_check_failure': {
      const result = recordAccountHealthCheckFailure(operation.accountId, operation.input)
      if (result.changed) {
        clearGatewayRuntimeCacheLocal()
      }
      return result
    }
    case 'list_accounts_due_for_cooldown_retest': {
      return listAccountsDueForCooldownRetestPage(operation.limit, operation.cursor)
    }
    case 'find_account_for_cooldown_retest': {
      return findAccountForCooldownRetest(operation.accountId)
    }
    case 'record_cooldown_account_retest_success': {
      const result = recordCooldownAccountRetestSuccess(operation.accountId, {
        expectedConfigRevision: operation.expectedConfigRevision,
        expectedDispatchRevision: operation.expectedDispatchRevision,
        expectedObservationStartedAt: operation.expectedObservationStartedAt,
        expectedGeneration: operation.expectedGeneration,
        expectedSourceConfigRevision: operation.expectedSourceConfigRevision
      })
      if (result.changed) clearGatewayRuntimeCacheLocal()
      return result
    }
    case 'record_cooldown_account_retest_failure': {
      const result = recordCooldownAccountRetestFailure(operation.accountId, operation.input)
      if (result.changed) {
        clearGatewayRuntimeCacheLocal()
      }
      return {
        changed: result.changed,
        failureCount: result.failureCount,
        action: result.action,
        cooldownUntil: result.cooldownUntil,
        backoffSeconds: result.backoffSeconds,
        backoffMinutes: result.backoffMinutes,
        recoveryStage: result.recoveryStage,
        fastThresholdSeconds: result.fastThresholdSeconds,
        maxPauseSeconds: result.maxPauseSeconds,
        maxRecoverySeconds: result.maxRecoverySeconds,
        longTermIntervalSeconds: result.longTermIntervalSeconds,
        maxedFailureCount: result.maxedFailureCount,
        observationStartedAt: result.observationStartedAt,
        observationElapsedSeconds: result.observationElapsedSeconds,
        observationTimeoutSeconds: result.observationTimeoutSeconds,
        transitionedToError: result.transitionedToError,
        errorCode: result.errorCode,
        errorMessage: result.errorMessage
      }
    }
    case 'defer_cooldown_account_retest':
      return deferCooldownAccountRetest(operation.accountId, {
        delaySeconds: operation.delaySeconds,
        expectedConfigRevision: operation.expectedConfigRevision,
        expectedDispatchRevision: operation.expectedDispatchRevision,
        expectedObservationStartedAt: operation.expectedObservationStartedAt,
        expectedGeneration: operation.expectedGeneration,
        expectedSourceConfigRevision: operation.expectedSourceConfigRevision
      })
    case 'mark_account_exception': {
      const updated = markAccountException(operation.accountId, operation.errorCode, operation.reason, {
        preserveDisabled: operation.preserveDisabled,
        traceId: operation.traceId,
        expectedConfigRevision: operation.expectedConfigRevision,
        expectedStatus: operation.expectedStatus
      })
      if (updated) {
        clearGatewayRuntimeCacheLocal()
      }
      return { updated: Boolean(updated), accountStatus: updated?.status }
    }
    case 'update_proxy_test_state': {
      const updated = updateProxyTestState(operation.proxyId, operation.input)
      return { updated: Boolean(updated), proxyStatus: updated?.testStatus }
    }
    case 'mark_all_group_account_stats_dirty':
      markAllGroupAccountStatsDirty(operation.reason)
      return { marked: true }
    case 'delete_group_account_stats_dirty_rows': {
      deleteGroupAccountStatsDirtyRowsLocal(
        operation.rows.map((row): GroupAccountStatsDirtyRow => ({
          groupId: row.groupId,
          reason: null,
          updatedAt: row.updatedAt
        }))
      )
      return { deleted: true }
    }
    case 'update_group_account_stats_all_cursor':
      updateGroupAccountStatsAllCursorLocal(operation.cursorGroupId)
      return { updated: true }
    case 'sync_api_key_availability_schedule_statuses': {
      const result = syncApiKeyAvailabilityScheduleStatuses()
      if (result.changedIds.length > 0) {
        clearGatewayRuntimeCacheLocal()
      }
      return result
    }
    case 'sync_account_availability_schedule_statuses': {
      const result = syncAccountAvailabilityScheduleStatuses()
      if (result.changedIds.length > 0) {
        clearGatewayRuntimeCacheLocal()
      }
      return result
    }
    case 'expire_due_resource_authorizations': {
      const expired = expireDueResourceAuthorizations()
      if (expired > 0) {
        clearGatewayRuntimeCacheLocal()
      }
      return { expired }
    }
    case 'cleanup_expired_deleted_accounts': {
      const result = cleanupExpiredLogicallyDeletedAccounts()
      if (result.attempted > 0 || result.orphanedAuthorizationInstances > 0) {
        clearGatewayRuntimeCacheLocal()
      }
      return result
    }
    case 'cleanup_expired_system_sessions':
      return { deleted: cleanupExpiredSystemSessions(operation.expiredBefore, operation.limit) }
    case 'advance_account_circuit_dispatch_revision':
    case 'compare_and_set_account_circuit_incident':
    case 'get_account_circuit_incident_by_scope_key':
    case 'claim_account_circuit_outbox':
    case 'ack_account_circuit_outbox':
    case 'release_account_circuit_outbox_for_replay':
    case 'list_account_circuit_incidents_for_rebuild':
    case 'list_account_circuit_incidents_by_runtime_keys':
    case 'list_account_circuit_projection_gaps':
    case 'cleanup_account_circuit_control_plane':
      throw new Error(`DB service 操作 ${operation.type} 必须通过异步 repository 执行`)
    case 'save_codex_context_response_state':
      return saveCodexContextResponseStateIndex(operation.input)
    case 'save_codex_context_compact_state':
      return saveCodexContextCompactStateIndex(operation.input)
    case 'read_codex_context_response_chain':
      return readCodexContextResponseStateChain({
        responseId: operation.responseId,
        boundary: operation.boundary,
        maxDepth: operation.maxDepth,
        now: operation.now,
        refreshExpiresAt: operation.refreshExpiresAt
      })
    case 'read_codex_context_compact_state':
      return readCodexContextCompactState({
        compactId: operation.compactId,
        boundary: operation.boundary,
        now: operation.now,
        refreshExpiresAt: operation.refreshExpiresAt
      })
    case 'cleanup_expired_codex_context_states':
      return cleanupExpiredCodexContextStates({
        expiredBefore: operation.expiredBefore,
        limit: operation.limit
      })
    case 'account_test_task_maintenance': {
      cleanupExpiredAccountTestTasks()
      if (operation.action === 'start' || operation.action === 'sweep') {
        completeIdleAccountTestSessions()
      }
      const canceledTaskIds: string[] = []
      const expiredQueuedTaskIds = operation.action === 'sweep'
        ? failExpiredQueuedAccountTestTasks(operation.maxQueuedMs ?? 10 * 60_000, operation.sweepLimit ?? 500)
        : []
      const taskIds = operation.action === 'start'
        ? requeueInterruptedAccountTestTasks()
        : listRunnableAccountTestTaskIds(operation.refillLimit ?? 100)
      return { taskIds, canceledTaskIds, expiredQueuedTaskIds }
    }
    case 'mark_account_test_task_running':
      return markAccountTestTaskRunning(operation.taskId)
    case 'mark_account_test_task_canceled':
      return markAccountTestTaskCanceled(operation.taskId, operation.message)
    case 'complete_account_test_task':
      return completeAccountTestTask(operation.taskId, operation.result)
    case 'fail_account_test_task':
      return failAccountTestTask(operation.taskId, operation.message, operation.result)
    case 'update_account_test_task_message':
      return updateAccountTestTaskMessage(operation.taskId, operation.message)
    case 'is_account_test_task_cancel_requested':
      return { canceled: isAccountTestTaskCancelRequested(operation.taskId) }
    case 'read_account_test_task_cancel_message':
      return { message: accountTestTaskCancelMessage(operation.taskId) }
    case 'clear_account_stream_failure_state': {
      const authorizedTarget = authorizedBindingRuntimeTarget(operation.account)
      const accountId = operation.account?.id ?? operation.accountId
      const changed = authorizedTarget
        ? clearAuthorizedAccountBindingStreamFailureState(authorizedTarget)
        : accountId ? clearAccountStreamFailureState(accountId) : false
      if (changed) {
        clearGatewayRuntimeCacheLocal()
      }
      return { changed }
    }
    case 'clear_gateway_runtime_cache':
      clearGatewayRuntimeCacheLocal()
      clearGatewayApiKeyValidationCache()
      clearApiKeyQuotaCache()
      clearAuthorizationQuotaCache()
      return { cleared: true }
    case 'list_active_client_ip_policies':
      return listActiveClientIpPolicies()
    case 'list_active_response_inspection_policies':
      return listActiveResponseInspectionPoliciesForGateway({
        protocolCode: operation.protocolCode,
        providerCode: operation.providerCode
      })
    case 'record_client_ip_policy_hits':
      throw new Error('record_client_ip_policy_hits 必须投递 stats-writer，禁止在 DB service 写 stats DB')
    case 'list_runtime_logs':
      return listRuntimeLogsAsync(operation.options)
    case 'get_runtime_log_detail':
      return getRuntimeLogDetailAsync(operation.id)
    case 'get_runtime_log_facets':
      return getRuntimeLogFacetsAsync()
    case 'cleanup_chat_retention':
      throw new Error('cleanup_chat_retention 必须走异步 DB service handler')
    case 'status':
      return buildDbServiceRuntimeSnapshot()
    default:
      return assertNever(operation)
  }
}

function unauthorizedAccountApiKeyMutationResult(reason: string): { changed: false; skippedReason: string } {
  return {
    changed: false,
    skippedReason: `unauthorized_account_api_key_mutation:${reason}`
  }
}

function precheckTemporaryUnavailableSkipReason(
  operation: Extract<DbServiceOperation, { type: 'mark_account_precheck_temporary_unavailable' }>,
  authorizedTarget: ReturnType<typeof authorizedBindingRuntimeTarget>
): string | undefined {
  const startedAtMs = operation.precheckStartedAt ? Date.parse(operation.precheckStartedAt) : NaN
  if (!Number.isFinite(startedAtMs)) {
    return 'invalid_precheck_fence'
  }
  if (!Number.isSafeInteger(operation.expectedDispatchRevision) || operation.expectedDispatchRevision < 1) {
    return 'invalid_precheck_fence'
  }
  const state = getAccountPrecheckMutationState({
    accountId: operation.account.id,
    authorizedBinding: authorizedTarget
  })
  if (!state) {
    return 'account_missing'
  }
  if (state.status === 'disabled' || state.status === 'error') {
    return 'hard_unavailable'
  }
  if (state.dispatchRevision !== operation.expectedDispatchRevision) {
    return 'stale_dispatch_revision'
  }
  if (state.status !== operation.expectedStatus) {
    return 'stale_account_status'
  }
  const lastHealthSuccessAtMs = state.lastHealthSuccessAt ? Date.parse(state.lastHealthSuccessAt) : NaN
  if (Number.isFinite(lastHealthSuccessAtMs) && lastHealthSuccessAtMs >= startedAtMs) {
    return 'newer_health_success'
  }
  if (state.updatedAt && Date.parse(state.updatedAt) > startedAtMs && state.updatedAt !== state.lastUsedAt) {
    return 'stale_account_updated'
  }
  return undefined
}

async function precheckTemporaryUnavailableSkipReasonAsync(
  operation: Extract<DbServiceOperation, { type: 'mark_account_precheck_temporary_unavailable' }>,
  authorizedTarget: ReturnType<typeof authorizedBindingRuntimeTarget>
): Promise<string | undefined> {
  const startedAtMs = operation.precheckStartedAt ? Date.parse(operation.precheckStartedAt) : NaN
  if (!Number.isFinite(startedAtMs)) {
    return 'invalid_precheck_fence'
  }
  if (!Number.isSafeInteger(operation.expectedDispatchRevision) || operation.expectedDispatchRevision < 1) {
    return 'invalid_precheck_fence'
  }
  const state = await getAccountPrecheckMutationStateAsync({
    accountId: operation.account.id,
    authorizedBinding: authorizedTarget
  })
  if (!state) {
    return 'account_missing'
  }
  if (state.status === 'disabled' || state.status === 'error') {
    return 'hard_unavailable'
  }
  if (state.dispatchRevision !== operation.expectedDispatchRevision) {
    return 'stale_dispatch_revision'
  }
  if (state.status !== operation.expectedStatus) {
    return 'stale_account_status'
  }
  const lastHealthSuccessAtMs = state.lastHealthSuccessAt ? Date.parse(state.lastHealthSuccessAt) : NaN
  if (Number.isFinite(lastHealthSuccessAtMs) && lastHealthSuccessAtMs >= startedAtMs) {
    return 'newer_health_success'
  }
  if (state.updatedAt && Date.parse(state.updatedAt) > startedAtMs && state.updatedAt !== state.lastUsedAt) {
    return 'stale_account_updated'
  }
  return undefined
}

function applyPrecheckErrorPolicyTarget(
  operation: Extract<DbServiceOperation, { type: 'mark_account_precheck_temporary_unavailable' }>,
  authorizedTarget: ReturnType<typeof authorizedBindingRuntimeTarget>
): unknown {
  return authorizedTarget
    ? markAuthorizedAccountBindingTemporaryUnavailableByContext({
        ...authorizedTarget,
        reason: operation.reason,
        precheckGuard: precheckMutationGuard(operation)
      })
    : markAccountTemporaryUnavailable(operation.account.id, operation.reason, undefined, undefined, precheckMutationGuard(operation))
}

async function applyPrecheckErrorPolicyTargetAsync(
  operation: Extract<DbServiceOperation, { type: 'mark_account_precheck_temporary_unavailable' }>,
  authorizedTarget: ReturnType<typeof authorizedBindingRuntimeTarget>
): Promise<unknown> {
  return authorizedTarget
    ? await markAuthorizedAccountBindingTemporaryUnavailableByContextAsync({
        ...authorizedTarget,
        reason: operation.reason,
        precheckGuard: precheckMutationGuard(operation)
      })
    : await markAccountTemporaryUnavailableAsync(operation.account.id, operation.reason, undefined, undefined, precheckMutationGuard(operation))
}

function precheckMutationGuard(
  operation: Extract<DbServiceOperation, { type: 'mark_account_precheck_temporary_unavailable' }>
) {
  return {
    expectedDispatchRevision: operation.expectedDispatchRevision,
    expectedStatus: operation.expectedStatus,
    precheckStartedAt: operation.precheckStartedAt
  }
}

function recoverableUnavailableOpenAIAccounts(
  result: { accounts: OpenAIAccountSecret[] },
  windowMsInput: number | undefined
): OpenAIAccountSecret[] {
  const nowMs = Date.now()
  const windowMs = Math.max(0, Math.min(Math.trunc(Number(windowMsInput ?? 0)), 60_000))
  const latestRecoverableAtMs = nowMs + windowMs
  return result.accounts.filter((account) => {
    const cooldownUntilMs = account.cooldownUntil ? Date.parse(account.cooldownUntil) : undefined
    if (cooldownUntilMs === undefined || !Number.isFinite(cooldownUntilMs) || cooldownUntilMs > latestRecoverableAtMs) {
      return false
    }
    if (account.status === 'active') {
      return cooldownUntilMs > nowMs
    }
    return account.status === 'temporary_unavailable' || account.status === 'rate_limited'
  })
}

function authorizedBindingRuntimeTarget(account: OpenAIAccountSecret | undefined): {
    accountId: string
    systemAccountId: string
    groupId: string
    accountAuthorizationId: string
  } | undefined {
  if (!account || typeof account !== 'object') return undefined
  const candidate = account as {
    id?: string
    accountAccessType?: string
    bindingSystemAccountId?: string
    groupOwnerSystemAccountId?: string
    boundGroupId?: string
    accountAuthorizationId?: string
  }
  if (candidate.accountAccessType !== 'account_authorized') return undefined
  const systemAccountId = candidate.bindingSystemAccountId
  if (!candidate.id || !systemAccountId || !candidate.boundGroupId || !candidate.accountAuthorizationId) {
    return undefined
  }
  return {
    accountId: candidate.id,
    systemAccountId,
    groupId: candidate.boundGroupId,
    accountAuthorizationId: candidate.accountAuthorizationId
  }
}

function findOpenAIOAuthAccountForRefresh(accountId: string): unknown {
  const account = findAccountForTest(accountId)
  if (!account || !isGptVendorCode(account.providerCode) || !isOpenAIProtocolProfile(account) || account.type !== 'oauth') {
    return undefined
  }
  const proxyResolution = account.proxyProfileId
    ? resolveProxyUrlsForProfiles([account.proxyProfileId]).get(account.proxyProfileId)
    : undefined
  return {
    ...account,
    ...openAIOAuthRefreshProxyResolutionFields(account.proxyProfileId, proxyResolution)
  }
}

async function findOpenAIOAuthAccountForRefreshAsync(accountId: string): Promise<unknown> {
  const account = await findAccountForTestAsync(accountId)
  if (!account || !isGptVendorCode(account.providerCode) || !isOpenAIProtocolProfile(account) || account.type !== 'oauth') {
    return undefined
  }
  const proxyResolution = account.proxyProfileId
    ? (await resolveProxyUrlsForProfilesAsync([account.proxyProfileId])).get(account.proxyProfileId)
    : undefined
  return {
    ...account,
    ...openAIOAuthRefreshProxyResolutionFields(account.proxyProfileId, proxyResolution)
  }
}

function openAIOAuthRefreshProxyResolutionFields(
  proxyProfileId: string | undefined,
  resolution: { proxyUrl?: string; unavailable?: boolean; errorMessage?: string } | undefined
): {
    proxyUrl?: string
    localConfigurationError?: {
      code: 'oauth_proxy_configuration_invalid'
      message: string
    }
  } {
  if (!proxyProfileId) return {}
  if (resolution?.proxyUrl) return { proxyUrl: resolution.proxyUrl }
  return {
    localConfigurationError: {
      code: 'oauth_proxy_configuration_invalid',
      message: resolution?.errorMessage ?? 'OpenAI OAuth 账户配置的代理不可用，请检查代理配置'
    }
  }
}

function readGatewayRuntime(operation: Extract<DbServiceOperation, { type: 'read_gateway_runtime' }>): DbServiceGatewayRuntime {
  const settings = readCachedGatewaySettings()
  const apiKey = validateGatewayApiKey(operation.key)
  if (!apiKey) {
    return {
      settings,
      accounts: []
    }
  }
  const systemAccountId = operation.systemAccountId ?? apiKey.system_account_id
  if (operation.skipDynamicRouteSelection === true && isDynamicRouteStrategyMode(apiKey.route_strategy_mode)) {
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
    const responseInspectionPolicies = listActiveResponseInspectionPoliciesForAccounts(accounts)
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

async function readGatewayRuntimeAsync(operation: Extract<DbServiceOperation, { type: 'read_gateway_runtime' }>): Promise<DbServiceGatewayRuntime> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return await requestSqliteReadWorker({
      type: 'read_gateway_runtime_static_read_only',
      key: operation.key,
      groupId: operation.groupId,
      systemAccountId: operation.systemAccountId,
      skipDynamicRouteSelection: operation.skipDynamicRouteSelection
    })
  }
  const settings = await readGatewaySettingsAsync()
  const apiKey = await validateGatewayApiKeyAsync(operation.key)
  if (!apiKey) {
    return {
      settings,
      accounts: []
    }
  }
  const systemAccountId = operation.systemAccountId ?? apiKey.system_account_id
  if (operation.skipDynamicRouteSelection === true && isDynamicRouteStrategyMode(apiKey.route_strategy_mode)) {
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
  const orderedBindings = await orderGatewayApiKeyGroupBindingsForDispatchAsync(apiKey)
  apiKey.selected_group_id = orderedBindings[0]?.group_id ?? apiKey.selected_group_id
  const candidateGroupIds = operation.groupId
    ? orderedBindings.some((binding) => binding.group_id === operation.groupId)
      ? [operation.groupId]
      : []
    : orderedBindings.map((binding) => binding.group_id)
  const uniqueCandidateGroupIds = [...new Set(candidateGroupIds.filter(Boolean))]

  for (const groupId of uniqueCandidateGroupIds) {
    const groupAccess = await resolveGroupUsageAccessMetadataAsync(groupId, systemAccountId)
    if (!groupAccess) {
      continue
    }
    const groupAccountsResult = await listOpenAIAccountsForGroupResultAsync(groupId, systemAccountId, { preResolvedGroupAccess: groupAccess })
    const accounts = groupAccountsResult.accounts
    if (!hasDispatchableGatewayAccount(accounts) && uniqueCandidateGroupIds.length > 1) {
      continue
    }
    const responseInspectionPolicies = await listActiveResponseInspectionPoliciesForAccountsAsync(accounts)
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

function withDbServiceLocalRole<T>(operation: () => T): T {
  if (runtimeConfig.processRole !== 'server') {
    return operation()
  }
  const previousProcessRole = runtimeConfig.processRole
  try {
    runtimeConfig.processRole = 'db-service'
    return operation()
  } finally {
    runtimeConfig.processRole = previousProcessRole
  }
}

function listActiveResponseInspectionPoliciesForAccounts(accounts: readonly Pick<OpenAIAccountSecret, 'protocolCode' | 'providerCode'>[]) {
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

async function listActiveResponseInspectionPoliciesForAccountsAsync(accounts: readonly Pick<OpenAIAccountSecret, 'protocolCode' | 'providerCode'>[]) {
  const profileKeys = new Set<string>()
  const profileScopes: Array<{ protocolCode: string; providerCode?: string }> = []
  const policiesById = new Map<string, Awaited<ReturnType<typeof listActiveResponseInspectionPoliciesForGatewayAsync>>[number]>()
  for (const account of accounts) {
    const protocolCode = account.protocolCode?.trim()
    if (!protocolCode) {
      continue
    }
    const providerCode = account.providerCode?.trim() || undefined
    const key = `${protocolCode}:${providerCode ?? ''}`
    if (profileKeys.has(key)) {
      continue
    }
    profileKeys.add(key)
    profileScopes.push({ protocolCode, providerCode })
  }
  const policyGroups = await Promise.all(profileScopes.map((scope) =>
    listActiveResponseInspectionPoliciesForGatewayAsync(scope)))
  for (const policies of policyGroups) {
    for (const policy of policies) {
      policiesById.set(policy.id, policy)
    }
  }
  return [...policiesById.values()]
}

function hasDispatchableGatewayAccount(accounts: OpenAIAccountSecret[]): boolean {
  return accounts.some((account) => account.status === 'active' && account.proxyProfileUnavailable !== true)
}

function assertNever(value: never): never {
  throw new Error(`未知 DB service 操作：${JSON.stringify(value)}`)
}

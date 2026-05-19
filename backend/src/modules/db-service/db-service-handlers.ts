import {
  clearGatewayApiKeyValidationCache,
  clearAccountStreamFailureState,
  findAccountForTest,
  listOpenAIAccountsForGroup,
  listPublicGlobalSettings,
  recordAccountStreamFailure,
  resolveGroupUsageAccessMetadata,
  resolveProxyUrlForProfile,
  updateAccount,
  validateGatewayApiKey
} from '../../storage/repositories.js'
import {
  getRuntimeLogFacets,
  getRuntimeLogDetail,
  listRuntimeLogs
} from '../../storage/runtime-logs.repository.js'
import {
  clearGatewayRuntimeCacheLocal,
  readCachedGatewaySettings,
} from '../gateway/gateway-runtime-cache.service.js'
import { checkGatewayApiKeyQuota, clearApiKeyQuotaCache } from '../gateway/api-key-quota.service.js'
import { checkGatewayAuthorizationQuotaBatchByIds, checkGatewayAuthorizationQuotaByIds, clearAuthorizationQuotaCache } from '../gateway/authorization-quota.service.js'
import { applyAccountErrorHandling } from '../gateway/account-error-policy.service.js'
import { persistOpenAICodexUsageHeaders } from '../gateway/openai-codex-usage.service.js'
import type {
  DbServiceGatewayRuntime,
  DbServiceOperation,
  DbServiceOperationResult,
  DbServiceRuntimeSnapshot
} from './db-service-types.js'
import { currentProcessEventLoopLagMs } from '../../shared/process-event-loop-monitor.js'

let handledRequestCount = 0
let failedRequestCount = 0
let pendingRequestCount = 0
let lastRequestAt: string | undefined
let lastError: string | undefined
let dbServiceHttpEndpoint: { host: string; port: number } | undefined

export async function handleDbServiceOperation<T extends DbServiceOperation>(operation: T): Promise<DbServiceOperationResult<T>> {
  pendingRequestCount += 1
  lastRequestAt = new Date().toISOString()
  try {
    const result = handleDbServiceOperationSync(operation) as DbServiceOperationResult<T>
    handledRequestCount += 1
    lastError = undefined
    return result
  } catch (error) {
    failedRequestCount += 1
    lastError = error instanceof Error ? error.message : String(error)
    throw error
  } finally {
    pendingRequestCount = Math.max(0, pendingRequestCount - 1)
  }
}

export function buildDbServiceRuntimeSnapshot(pid = process.pid): DbServiceRuntimeSnapshot {
  return {
    pid,
    ready: true,
    processRole: 'db-service',
    httpHost: dbServiceHttpEndpoint?.host,
    httpPort: dbServiceHttpEndpoint?.port,
    eventLoopLagMs: currentProcessEventLoopLagMs(),
    pendingRequestCount,
    handledRequestCount,
    failedRequestCount,
    lastRequestAt,
    lastError
  }
}

export function setDbServiceHttpEndpoint(endpoint: { host: string; port: number }): void {
  dbServiceHttpEndpoint = endpoint
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
      return listOpenAIAccountsForGroup(operation.groupId, operation.systemAccountId)
    case 'read_gateway_runtime':
      return readGatewayRuntime(operation)
    case 'check_api_key_quota':
      return checkGatewayApiKeyQuota(operation.apiKey)
    case 'check_authorization_quota':
      return checkGatewayAuthorizationQuotaByIds({
        groupAuthorizationId: operation.groupAuthorizationId,
        accountAuthorizationId: operation.accountAuthorizationId
      })
    case 'check_authorization_quota_batch':
      return checkGatewayAuthorizationQuotaBatchByIds({
        groupAuthorizationId: operation.groupAuthorizationId,
        accounts: operation.accounts
      })
    case 'update_openai_oauth_credentials': {
      const updated = updateAccount(operation.accountId, { credentials: operation.credentials })
      if (updated) {
        clearGatewayRuntimeCacheLocal()
      }
      return { updated: Boolean(updated) }
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
    case 'record_account_stream_failure': {
      const result = recordAccountStreamFailure(operation.input)
      if (result.triggered) {
        clearGatewayRuntimeCacheLocal()
      }
      return { count: result.count, triggered: result.triggered }
    }
    case 'clear_account_stream_failure_state': {
      const changed = clearAccountStreamFailureState(operation.accountId)
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
    case 'list_runtime_logs':
      return listRuntimeLogs(operation.options)
    case 'get_runtime_log_detail':
      return getRuntimeLogDetail(operation.id)
    case 'get_runtime_log_facets':
      return getRuntimeLogFacets()
    case 'status':
      return buildDbServiceRuntimeSnapshot()
    default:
      return assertNever(operation)
  }
}

function findOpenAIOAuthAccountForRefresh(accountId: string): unknown {
  const account = findAccountForTest(accountId)
  if (!account || account.providerCode !== 'openai' || account.type !== 'oauth') {
    return undefined
  }
  return {
    ...account,
    proxyUrl: account.proxyProfileId ? resolveProxyUrlForProfile(account.proxyProfileId) : undefined
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
  const groupId = operation.groupId ?? apiKey.group_id
  const groupAccess = resolveGroupUsageAccessMetadata(groupId, systemAccountId)
  if (!groupAccess) {
    return {
      apiKey,
      settings,
      accounts: []
    }
  }

  return {
    apiKey,
    settings,
    groupAccess,
    accounts: listOpenAIAccountsForGroup(groupId, systemAccountId, { preResolvedGroupAccess: groupAccess })
  }
}

function assertNever(value: never): never {
  throw new Error(`未知 DB service 操作：${JSON.stringify(value)}`)
}

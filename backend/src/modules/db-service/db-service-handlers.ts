import {
  clearGatewayApiKeyValidationCache,
  listOpenAIAccountsForGroup,
  resolveGroupUsageAccessMetadata,
  validateGatewayApiKey
} from '../../storage/repositories.js'
import {
  clearGatewayRuntimeCacheLocal,
  listCachedOpenAIAccountsForGroup,
  readCachedGatewaySettings,
  resolveCachedGroupUsageAccessMetadata
} from '../gateway/gateway-runtime-cache.service.js'
import type {
  DbServiceGatewayRuntime,
  DbServiceOperation,
  DbServiceOperationResult,
  DbServiceRuntimeSnapshot
} from './db-service-types.js'

let handledRequestCount = 0
let failedRequestCount = 0
let pendingRequestCount = 0
let lastRequestAt: string | undefined
let lastError: string | undefined

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
    pendingRequestCount,
    handledRequestCount,
    failedRequestCount,
    lastRequestAt,
    lastError
  }
}

function handleDbServiceOperationSync(operation: DbServiceOperation): unknown {
  switch (operation.type) {
    case 'validate_gateway_api_key':
      return validateGatewayApiKey(operation.key)
    case 'read_gateway_settings':
      return readCachedGatewaySettings()
    case 'resolve_group_usage_access':
      return resolveCachedGroupUsageAccessMetadata(operation.groupId, operation.systemAccountId)
    case 'list_openai_accounts_for_group':
      return listCachedOpenAIAccountsForGroup(operation.groupId, operation.systemAccountId)
    case 'read_gateway_runtime':
      return readGatewayRuntime(operation)
    case 'clear_gateway_runtime_cache':
      clearGatewayRuntimeCacheLocal()
      clearGatewayApiKeyValidationCache()
      return { cleared: true }
    case 'status':
      return buildDbServiceRuntimeSnapshot()
    default:
      return assertNever(operation)
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
    accounts: listOpenAIAccountsForGroup(groupId, systemAccountId)
  }
}

function assertNever(value: never): never {
  throw new Error(`未知 DB service 操作：${JSON.stringify(value)}`)
}

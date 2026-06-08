import {
  clearGatewayApiKeyValidationCache,
  clearAccountStreamFailureState,
  clearAuthorizedAccountBindingStreamFailureState,
  findAccountForTest,
  getAccountPrecheckMutationState,
  isGatewayApiKeyScheduleInactive,
  listOpenAIAccountsForGroup,
  listOpenAIAccountsForGroupResult,
  listPublicGlobalSettings,
  markAccountCooldown,
  markAccountDisabledByFailure,
  markAccountTemporaryUnavailable,
  markAuthorizedAccountBindingCooldownByContext,
  markAuthorizedAccountBindingDisabledByFailure,
  markAuthorizedAccountBindingTemporaryUnavailableByContext,
  recordAccountStreamFailure,
  recordAuthorizedAccountBindingStreamFailure,
  resolveGroupUsageAccessMetadata,
  resolveProxyUrlForProfile,
  type OpenAIAccountSecret,
  updateAccount,
  validateGatewayApiKey
} from '../../storage/repositories.js'
import type { AccessScope } from '../../storage/access-scope.js'
import {
  getRuntimeLogFacets,
  getRuntimeLogDetail,
  listRuntimeLogs
} from '../../storage/runtime-logs.repository.js'
import {
  findActiveClientIpPolicyByHash,
  recordClientIpPolicyHits
} from '../../storage/client-ip-stats.repository.js'
import { listActiveStreamInterceptPoliciesForGateway } from '../../storage/stream-intercept-policy.repository.js'
import {
  clearGatewayRuntimeCacheLocal,
  readCachedGatewaySettings,
} from '../gateway/gateway-runtime-cache.service.js'
import { isGptVendorCode, isOpenAIProtocolProfile } from '../../domain/provider-protocol.js'
import { orderGatewayApiKeyGroupBindingsForDispatch } from '../gateway/api-key-group-route-selector.service.js'
import { checkGatewayApiKeyQuota, clearApiKeyQuotaCache } from '../gateway/api-key-quota.service.js'
import { checkGatewayAuthorizationQuotaBatchByIds, checkGatewayAuthorizationQuotaByIds, clearAuthorizationQuotaCache } from '../gateway/authorization-quota.service.js'
import { applyAccountErrorHandling } from '../gateway/account-error-policy.service.js'
import { persistOpenAICodexUsageHeaders } from '../gateway/openai-codex-usage.service.js'
import { listProviderModelCatalog } from '../model-pricing/model-catalog.service.js'
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
const internalDbServiceAccountAccess: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }

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
    case 'list_openai_accounts_for_group_result':
      return listOpenAIAccountsForGroupResult(operation.groupId, operation.systemAccountId)
    case 'read_gateway_runtime':
      return readGatewayRuntime(operation)
    case 'list_provider_model_catalog':
      return listProviderModelCatalog({
        providerCode: operation.providerCode,
        systemAccountId: operation.systemAccountId,
        includeMappingTargets: operation.includeMappingTargets,
        includeInactive: operation.includeInactive,
        includeUnpriced: operation.includeUnpriced
      })
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
      const updated = updateAccount(operation.accountId, { credentials: operation.credentials }, internalDbServiceAccountAccess)
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
      return { updated: Boolean(updated) }
    }
    case 'mark_account_temporary_unavailable': {
      const authorizedTarget = authorizedBindingRuntimeTarget(operation.account)
      const updated = authorizedTarget
        ? markAuthorizedAccountBindingTemporaryUnavailableByContext({
            ...authorizedTarget,
            reason: operation.reason
          })
        : markAccountTemporaryUnavailable(operation.account.id, operation.reason)
      if (updated) {
        clearGatewayRuntimeCacheLocal()
      }
      return { updated: Boolean(updated) }
    }
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
    case 'find_active_client_ip_policy':
      return findActiveClientIpPolicyByHash(operation.ipHash)
    case 'list_active_stream_intercept_policies':
      return listActiveStreamInterceptPoliciesForGateway({
        protocolCode: operation.protocolCode,
        providerCode: operation.providerCode
      })
    case 'record_client_ip_policy_hits':
      return recordClientIpPolicyHits(operation.hits)
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

function precheckTemporaryUnavailableSkipReason(
  operation: Extract<DbServiceOperation, { type: 'mark_account_precheck_temporary_unavailable' }>,
  authorizedTarget: ReturnType<typeof authorizedBindingRuntimeTarget>
): string | undefined {
  const startedAtMs = operation.precheckStartedAt ? Date.parse(operation.precheckStartedAt) : NaN
  if (!Number.isFinite(startedAtMs)) {
    return undefined
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
  if (state.updatedAt && Date.parse(state.updatedAt) > startedAtMs && state.updatedAt !== state.lastUsedAt) {
    return 'stale_account_updated'
  }
  return undefined
}

function applyPrecheckErrorPolicyTarget(
  operation: Extract<DbServiceOperation, { type: 'mark_account_precheck_temporary_unavailable' }>,
  authorizedTarget: ReturnType<typeof authorizedBindingRuntimeTarget>
): unknown {
  const decision = operation.errorPolicyDecision
  if (decision?.action === 'disable') {
    return authorizedTarget
      ? markAuthorizedAccountBindingDisabledByFailure({ ...authorizedTarget, reason: operation.reason })
      : markAccountDisabledByFailure(operation.account.id, operation.reason)
  }
  if (decision?.action === 'cooldown' && decision.cooldownStatus === 'rate_limited') {
    const cooldownUntil = decision.cooldownUntil ?? new Date(Date.now() + 60_000).toISOString()
    return authorizedTarget
      ? markAuthorizedAccountBindingCooldownByContext({
          ...authorizedTarget,
          cooldownUntil,
          reason: operation.reason,
          status: 'rate_limited'
        })
      : markAccountCooldown(operation.account.id, cooldownUntil, operation.reason, 'rate_limited')
  }
  return authorizedTarget
    ? markAuthorizedAccountBindingTemporaryUnavailableByContext({
        ...authorizedTarget,
        reason: operation.reason
      })
    : markAccountTemporaryUnavailable(operation.account.id, operation.reason)
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
  if (isGatewayApiKeyScheduleInactive(apiKey)) {
    return {
      apiKey,
      settings,
      accounts: []
    }
  }

  const systemAccountId = operation.systemAccountId ?? apiKey.system_account_id
  const orderedBindings = orderGatewayApiKeyGroupBindingsForDispatch(apiKey)
  apiKey.selected_group_id = orderedBindings[0]?.group_id ?? apiKey.selected_group_id
  const candidateGroupIds = operation.groupId
    ? orderedBindings.some((binding) => binding.group_id === operation.groupId)
      ? [operation.groupId]
      : []
    : orderedBindings.map((binding) => binding.group_id)
  const uniqueCandidateGroupIds = [...new Set(candidateGroupIds.filter(Boolean))]
  let hasCandidateAccountAvailabilitySchedule = false

  for (const groupId of uniqueCandidateGroupIds) {
    const groupAccess = resolveGroupUsageAccessMetadata(groupId, systemAccountId)
    if (!groupAccess) {
      continue
    }
    const groupAccountsResult = listOpenAIAccountsForGroupResult(groupId, systemAccountId, { preResolvedGroupAccess: groupAccess })
    const accounts = groupAccountsResult.accounts
    hasCandidateAccountAvailabilitySchedule = hasCandidateAccountAvailabilitySchedule || groupAccountsResult.hasAccountAvailabilitySchedule
    if (!hasDispatchableGatewayAccount(accounts) && uniqueCandidateGroupIds.length > 1) {
      continue
    }
    const streamInterceptPolicies = listActiveStreamInterceptPoliciesForGateway({
      protocolCode: groupAccess.protocolCode,
      providerCode: groupAccess.providerCode
    })
    return {
      apiKey: {
        ...apiKey,
        selected_group_id: groupId,
        group_bindings: orderedBindings.length ? orderedBindings : apiKey.group_bindings
      },
      settings,
      groupAccess,
      accounts,
      hasAccountAvailabilitySchedule: hasCandidateAccountAvailabilitySchedule,
      accountDispatchDiagnostics: groupAccountsResult.diagnostics,
      streamInterceptPolicies
    }
  }

  return {
    apiKey,
    settings,
    accounts: [],
    hasAccountAvailabilitySchedule: hasCandidateAccountAvailabilitySchedule,
    streamInterceptPolicies: []
  }
}

function hasDispatchableGatewayAccount(accounts: OpenAIAccountSecret[]): boolean {
  return accounts.some((account) => account.status === 'active' && account.proxyProfileUnavailable !== true)
}

function assertNever(value: never): never {
  throw new Error(`未知 DB service 操作：${JSON.stringify(value)}`)
}

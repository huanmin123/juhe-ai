import type { GatewayApiKeyRow, GroupUsageAccessMetadata, OpenAIAccountSecret } from '../../storage/repositories.js'
import type { ApiKeyQuotaDecision } from '../gateway/api-key-quota.service.js'
import type { AccountErrorHandlingResult, GatewaySettings } from '../gateway/account-error-policy.service.js'
import type { AuthorizationQuotaDecision } from '../gateway/authorization-quota.service.js'

export interface DbServiceRuntimeSnapshot {
  pid: number
  ready: boolean
  processRole: 'db-service'
  pendingRequestCount: number
  handledRequestCount: number
  failedRequestCount: number
  lastRequestAt?: string
  lastError?: string
}

export interface DbServiceGatewayRuntime {
  apiKey?: GatewayApiKeyRow
  settings: GatewaySettings
  groupAccess?: GroupUsageAccessMetadata
  accounts: OpenAIAccountSecret[]
}

export type DbServiceOperation =
  | {
    type: 'validate_gateway_api_key'
    key: string
  }
  | {
    type: 'read_gateway_settings'
  }
  | {
    type: 'resolve_group_usage_access'
    groupId: string
    systemAccountId: string
  }
  | {
    type: 'list_openai_accounts_for_group'
    groupId: string
    systemAccountId: string
  }
  | {
    type: 'read_gateway_runtime'
    key: string
    groupId?: string
    systemAccountId?: string
  }
  | {
    type: 'check_api_key_quota'
    apiKey: GatewayApiKeyRow
  }
  | {
    type: 'check_authorization_quota'
    groupAuthorizationId?: string
    accountAuthorizationId?: string
  }
  | {
    type: 'check_authorization_quota_batch'
    groupAuthorizationId?: string
    accounts: Array<{
      accountId: string
      accountAuthorizationId?: string
    }>
  }
  | {
    type: 'update_openai_oauth_credentials'
    accountId: string
    credentials: Record<string, unknown>
  }
  | {
    type: 'persist_openai_codex_usage_headers'
    accountId: string
    headers: Record<string, string>
    source: string
  }
  | {
    type: 'apply_account_error_handling'
    account: OpenAIAccountSecret
    input: {
      success: boolean
      statusCode?: number
      headers?: Record<string, string | string[]>
      bodyText?: string
      errorMessage?: string
      settings?: GatewaySettings
    }
  }
  | {
    type: 'record_account_stream_failure'
    input: {
      accountId: string
      thresholdCount: number
      thresholdWindowMinutes: number
      action: 'cooldown' | 'disable' | 'none'
      cooldownMinutes: number
      reason: string
    }
  }
  | {
    type: 'clear_account_stream_failure_state'
    accountId: string
  }
  | {
    type: 'clear_gateway_runtime_cache'
  }
  | {
    type: 'status'
  }

export type DbServiceOperationResult<T extends DbServiceOperation = DbServiceOperation> =
  T extends { type: 'validate_gateway_api_key' } ? GatewayApiKeyRow | undefined :
  T extends { type: 'read_gateway_settings' } ? GatewaySettings :
  T extends { type: 'resolve_group_usage_access' } ? GroupUsageAccessMetadata | undefined :
  T extends { type: 'list_openai_accounts_for_group' } ? OpenAIAccountSecret[] :
  T extends { type: 'read_gateway_runtime' } ? DbServiceGatewayRuntime :
  T extends { type: 'check_api_key_quota' } ? ApiKeyQuotaDecision :
  T extends { type: 'check_authorization_quota' } ? AuthorizationQuotaDecision :
  T extends { type: 'check_authorization_quota_batch' } ? AuthorizationQuotaDecision[] :
  T extends { type: 'update_openai_oauth_credentials' } ? { updated: boolean } :
  T extends { type: 'persist_openai_codex_usage_headers' } ? { persisted: boolean } :
  T extends { type: 'apply_account_error_handling' } ? AccountErrorHandlingResult :
  T extends { type: 'record_account_stream_failure' } ? { count: number; triggered: boolean } :
  T extends { type: 'clear_account_stream_failure_state' } ? { changed: boolean } :
  T extends { type: 'clear_gateway_runtime_cache' } ? { cleared: true } :
  T extends { type: 'status' } ? DbServiceRuntimeSnapshot :
  unknown

export type DbServiceParentMessage =
  | {
    type: 'db_service_request'
    requestId: string
    operation: DbServiceOperation
  }

export type DbServiceChildMessage =
  | {
    type: 'db_service_ready'
    pid: number
  }
  | {
    type: 'db_service_response'
    requestId: string
    ok: true
    result: unknown
  }
  | {
    type: 'db_service_response'
    requestId: string
    ok: false
    errorMessage: string
  }

import type { GatewayApiKeyRow, GroupUsageAccessMetadata, OpenAIAccountSecret } from '../../storage/repositories.js'
import type { GatewaySettings } from '../gateway/account-error-policy.service.js'

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

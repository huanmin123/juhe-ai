import type {
  AccountClientCompatibility,
  AccountSupportedEndpointMode,
  AccountModelMapping,
  AccountStatus,
  AccountType,
  GroupSchedulingPolicy,
  GroupType,
  ProviderCode,
  ResourceAuthorizationSourceType
} from '../domain/types.js'
import type { ProxyProfileUrlResolution } from './proxy.repository.js'
import type { ResourceAuthorizationRow } from './repository-row-types.js'
import type { AccountApiKeyRuntimeSelectionState } from './account-api-key-rotation.js'

export interface OpenAIAccountSecret {
  id: string
  providerCode: ProviderCode
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  systemAccountId: string
  accountOwnerSystemAccountId: string
  groupOwnerSystemAccountId: string
  accountAccessType: 'owner' | 'account_authorized' | 'group_authorized'
  groupAccessType: 'owner' | 'authorized'
  accountAuthorizationId?: string
  accountAuthorizationExpiresAt?: string
  accountAuthorizationQuotaLimited?: boolean
  accountAuthorizationSourceType?: ResourceAuthorizationSourceType
  accountAuthorizationSourceTeamId?: string
  bindingSystemAccountId?: string
  boundGroupId?: string
  groupAuthorizationId?: string
  groupAuthorizationExpiresAt?: string
  groupAuthorizationQuotaLimited?: boolean
  groupAuthorizationSourceType?: ResourceAuthorizationSourceType
  groupAuthorizationSourceTeamId?: string
  name: string
  type: AccountType
  status: AccountStatus
  concurrencyLimit: number
  priority: number
  superPriorityEnabled: boolean
  fallbackEnabled: boolean
  clientCompatibility: AccountClientCompatibility
  supportedEndpointModes?: AccountSupportedEndpointMode[]
  supportedModels?: string[]
  modelMappings?: AccountModelMapping[]
  lastSuccessfulTestModel?: string
  qualityScore?: number
  qualityState?: string
  qualityEwmaFirstTokenMs?: number
  currentConcurrency?: number
  baseUrl: string
  apiKey: string
  apiKeys?: string[]
  apiKeyRuntimeStates?: AccountApiKeyRuntimeSelectionState[]
  selectedApiKeyFingerprint?: string
  selectedApiKeyIndex?: number
  refreshToken?: string
  clientId?: string
  credentialSourceAccountId?: string
  proxyProfileId?: string
  proxyUrl?: string
  proxyProfileUnavailable?: boolean
  proxyProfileErrorMessage?: string
  cooldownUntil?: string
  lastErrorMessage?: string
  streamFailureCount: number
  streamFailureWindowStartedAt?: string
  accountExpiresAt?: string
  expiresAt?: string
  credentials: Record<string, unknown>
}

export type DispatchAccountSecret = OpenAIAccountSecret

export interface GroupUsageAccessMetadata {
  groupOwnerSystemAccountId: string
  providerCode: ProviderCode
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  groupAccessType: 'owner' | 'authorized'
  groupType?: GroupType
  schedulingPolicy?: GroupSchedulingPolicy
  groupAuthorizationId?: string
  groupAuthorizationExpiresAt?: string
  groupAuthorizationQuotaLimited?: boolean
  groupAuthorizationSourceType?: ResourceAuthorizationSourceType
  groupAuthorizationSourceTeamId?: string
}

export interface OpenAIAccountsForGroupResult {
  accounts: OpenAIAccountSecret[]
  diagnostics?: OpenAIAccountsForGroupDiagnostics
}

export type DispatchAccountsForGroupResult = OpenAIAccountsForGroupResult

export interface OpenAIAccountsForGroupDiagnostics {
  scanLimit: number
  finalLimit: number
  candidateRowCount: number
  scannedRowCount: number
  eligibleRowCount: number
  hydrationBatchCount: number
  hydratedAccountCount: number
  hydrationDroppedCount: number
  finalAccountCount: number
  scanLimitReached: boolean
}

export interface GroupAccountRow {
  account_id: string
  binding_system_account_id?: string | null
  group_id?: string | null
  account_authorization_id?: string | null
  local_priority?: number | null
  local_super_priority_enabled?: number | null
  local_fallback_enabled?: number | null
}

export interface OpenAIAccountRow {
  id: string
  system_account_id: string
  provider_code: ProviderCode
  provider_protocol_profile_id: string
  protocol_code: string
  protocol_version: string
  name: string
  type: AccountType
  status: AccountStatus
  schedulable: number
  concurrency_limit: number
  priority: number
  super_priority_enabled: number
  fallback_enabled: number
  client_compatibility: AccountClientCompatibility
  credentials_encrypted: string
  proxy_profile_id: string | null
  cooldown_until: string | null
  last_error_message: string | null
  stream_failure_count: number
  stream_failure_window_started_at: string | null
  availability_schedule_active: number
  account_expires_at: string | null
  last_successful_test_model: string | null
  authorization_instance_source_account_id?: string | null
  authorization_instance_authorization_id?: string | null
  authorization_instance_owner_system_account_id?: string | null
  resource_account_id?: string | null
  resource_provider_code?: ProviderCode | null
  resource_provider_protocol_profile_id?: string | null
  resource_protocol_code?: string | null
  resource_protocol_version?: string | null
  resource_type?: AccountType | null
  resource_status?: AccountStatus | null
  resource_schedulable?: number | null
  resource_availability_schedule_active?: number | null
  resource_account_expires_at?: string | null
  resource_cooldown_until?: string | null
  resource_last_error_code?: string | null
  resource_credentials_encrypted?: string | null
  resource_proxy_profile_id?: string | null
  resource_concurrency_limit?: number | null
  resource_client_compatibility?: AccountClientCompatibility | null
  quality_score?: number | null
  quality_state?: string | null
  quality_ewma_first_token_ms?: number | null
}

export type OpenAIGroupAccountSelectionRow = GroupAccountRow & OpenAIAccountRow

export type OpenAIAccountAccess = {
  accountAccessType: 'owner' | 'account_authorized' | 'group_authorized'
  accountOwnerSystemAccountId?: string
  accountAuthorizationId?: string
  accountAuthorizationExpiresAt?: string
  accountAuthorizationQuotaLimited?: boolean
  accountAuthorizationSourceType?: ResourceAuthorizationSourceType
  accountAuthorizationSourceTeamId?: string
}

export type EligibleOpenAIGroupAccountSelection = {
  row: OpenAIGroupAccountSelectionRow
  accountAccess: OpenAIAccountAccess
}

export type OpenAIAccountSecretOptions = {
  enforceSchedulableAuthorization?: boolean
  accountAuthorizationsByIdOrResourceId?: Map<string, ResourceAuthorizationRow>
  proxyProfilesById?: Map<string, ProxyProfileUrlResolution>
  supportedModelsByAccountId?: Map<string, string[]>
  modelMappingsByAccountId?: Map<string, AccountModelMapping[]>
  apiKeyRuntimeStatesByAccountId?: Map<string, AccountApiKeyRuntimeSelectionState[]>
  accountAccess?: OpenAIAccountAccess
}

export const gatewayDispatchAccountCandidateLimit = 256
export const gatewayDispatchAccountCandidateScanLimit = gatewayDispatchAccountCandidateLimit * 2

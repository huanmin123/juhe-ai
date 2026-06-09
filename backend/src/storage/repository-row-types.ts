import type {
  AccountStatus,
  AccountType,
  AccountClientCompatibility,
  OpenAIResponsesUpstreamMode,
  AuthorizationStatus,
  GroupType,
  ProviderCode,
  ResourceAuthorizationResourceType,
  ResourceAuthorizationSourceStatus,
  ResourceAuthorizationSourceType,
  ResourceAuthorizationGranteeType,
  AnnouncementLevel,
  AnnouncementStatus,
  SystemTeamMemberStatus,
  SystemTeamStatus
} from '../domain/types.js'
import type { AccountModelMapping } from '../domain/types.js'

export interface AccountRow {
  id: string
  system_account_id: string
  provider_code: ProviderCode
  provider_protocol_profile_id: string
  protocol_code: string
  protocol_version: string
  name: string
  notes: string | null
  type: AccountType
  status: AccountStatus
  credential_fingerprint: string | null
  credential_mask: string
  credentials_encrypted: string
  oauth_access_token_expires_at?: string | null
  oauth_refresh_token_present?: number
  proxy_profile_id: string | null
  concurrency_limit: number
  priority: number
  super_priority_enabled: number
  fallback_enabled: number
  client_compatibility: AccountClientCompatibility
  openai_responses_upstream_mode: OpenAIResponsesUpstreamMode
  supported_models?: string[]
  model_mappings?: AccountModelMapping[]
  schedulable: number
  availability_schedule_json: string | null
  account_expires_at: string | null
  last_used_at: string | null
  cooldown_until: string | null
  last_error_code: string | null
  last_error_message: string | null
  cooldown_retest_failure_count: number
  cooldown_retest_observation_started_at: string | null
  cooldown_retest_last_at: string | null
  cooldown_retest_last_status_code: number | null
  last_successful_test_model: string | null
  stream_failure_count: number
  stream_failure_window_started_at: string | null
  authorization_instance_source_account_id: string | null
  authorization_instance_authorization_id: string | null
  authorization_instance_owner_system_account_id: string | null
  deleted_at: string | null
  deleted_by: string | null
  created_at: string
  updated_at: string
}

export interface AccountFailureRow {
  id: string
  status: AccountStatus
  stream_failure_count: number
  stream_failure_window_started_at: string | null
}

export interface SystemTeamRow {
  id: string
  name: string
  description: string | null
  status: SystemTeamStatus
  created_by: string
  created_at: string
  updated_at: string
}

export interface SystemTeamMemberRow {
  id: string
  team_id: string
  system_account_id: string
  member_role: 'member'
  status: SystemTeamMemberStatus
  joined_at: string
  removed_at: string | null
  created_by: string
  created_at: string
  updated_at: string
}

export interface ResourceAuthorizationRow {
  id: string
  resource_type: ResourceAuthorizationResourceType
  resource_id: string
  resource_owner_system_account_id: string
  grantee_system_account_id: string
  scope: 'use'
  status: AuthorizationStatus
  effective_source_type: ResourceAuthorizationSourceType | null
  effective_source_team_id: string | null
  activated_at: string | null
  last_source_changed_at: string | null
  remark: string | null
  expires_at: string | null
  limits_json: string | null
  created_by: string
  created_at: string
  revoked_by: string | null
  revoked_at: string | null
  revoked_reason: string | null
  updated_at: string
}

export interface ResourceAuthorizationSourceRow {
  id: string
  authorization_id: string
  source_type: ResourceAuthorizationSourceType
  source_team_id: string | null
  status: ResourceAuthorizationSourceStatus
  activated_at: string | null
  ended_at: string | null
  ended_reason: string | null
  created_by: string
  created_at: string
  revoked_by: string | null
  revoked_at: string | null
  updated_at: string
}

export interface ResourceAuthorizationGrantRow {
  id: string
  resource_type: ResourceAuthorizationResourceType
  resource_id: string
  resource_owner_system_account_id: string
  grantee_type: ResourceAuthorizationGranteeType
  grantee_system_account_id: string | null
  grantee_team_id: string | null
  scope: 'use'
  status: AuthorizationStatus
  remark: string | null
  expires_at: string | null
  limits_json: string | null
  created_by: string
  created_at: string
  revoked_by: string | null
  revoked_at: string | null
  updated_at: string
}

export interface AnnouncementRow {
  id: string
  title: string
  content: string
  level: AnnouncementLevel
  status: AnnouncementStatus
  created_by: string
  updated_by: string | null
  published_at: string | null
  created_at: string
  updated_at: string
}

export interface AnnouncementReadRow {
  announcement_id: string
  system_account_id: string
  read_at: string
}

export type AccountListRow = AccountRow & {
  access_type?: 'owner' | 'authorized'
  authorization_id?: string | null
  authorization_status?: AuthorizationStatus | null
  authorization_expires_at?: string | null
  authorization_limits_json?: string | null
  authorization_effective_source_type?: ResourceAuthorizationSourceType | null
  authorization_effective_source_team_id?: string | null
  authorization_resource_owner_system_account_id?: string | null
  authorization_resource_id?: string | null
  system_account_sort_name?: string | null
  binding_system_account_id?: string | null
  bound_group_id?: string | null
  bound_group_name?: string | null
  bound_group_account_authorization_id?: string | null
  bound_group_local_priority?: number | null
  bound_group_local_super_priority_enabled?: number | null
  bound_group_local_fallback_enabled?: number | null
  source_provider_code?: ProviderCode | null
  source_provider_protocol_profile_id?: string | null
  source_protocol_code?: string | null
  source_protocol_version?: string | null
  source_type?: AccountType | null
  source_status?: AccountStatus | null
  source_schedulable?: number | null
  source_availability_schedule_json?: string | null
  source_account_expires_at?: string | null
  source_cooldown_until?: string | null
  source_last_error_code?: string | null
  source_last_error_message?: string | null
  source_last_successful_test_model?: string | null
  source_credential_mask?: string | null
  source_credentials_encrypted?: string | null
  source_proxy_profile_id?: string | null
  source_concurrency_limit?: number | null
  source_client_compatibility?: AccountClientCompatibility | null
  source_openai_responses_upstream_mode?: OpenAIResponsesUpstreamMode | null
  quality_score?: number | null
  quality_state?: string | null
  quality_ewma_first_token_ms?: number | null
  quality_recent_avg_first_token_ms?: number | null
  quality_recent_request_count?: number | null
  quality_recent_success_rate?: number | null
  quality_updated_at?: string | null
}

export interface GroupRow {
  id: string
  system_account_id: string
  name: string
  provider_code: ProviderCode
  provider_protocol_profile_id: string
  protocol_code: string
  protocol_version: string
  description: string | null
  enabled: number
  is_default: number
  group_type: GroupType | null
  scheduling_policy_json: string | null
  created_at: string
  updated_at: string
}

export type GroupListRow = GroupRow & {
  access_type?: 'owner' | 'authorized'
  authorization_id?: string | null
  authorization_status?: AuthorizationStatus | null
  authorization_expires_at?: string | null
  authorization_limits_json?: string | null
  authorization_effective_source_type?: ResourceAuthorizationSourceType | null
  authorization_effective_source_team_id?: string | null
}

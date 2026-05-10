import type {
  AccountStatus,
  AccountType,
  AuthorizationStatus,
  ProviderCode,
  ResourceAuthorizationResourceType,
  ResourceAuthorizationSourceStatus,
  ResourceAuthorizationSourceType,
  AnnouncementLevel,
  AnnouncementStatus,
  SystemTeamMemberStatus,
  SystemTeamStatus
} from '../domain/types.js'

export interface AccountRow {
  id: string
  system_account_id: string
  provider_code: ProviderCode
  name: string
  notes: string | null
  type: AccountType
  status: AccountStatus
  credential_mask: string
  credentials_encrypted: string
  proxy_profile_id: string | null
  concurrency_limit: number
  passthrough_enabled: number
  error_policy_id: string | null
  priority: number
  super_priority_enabled: number
  fallback_enabled: number
  schedulable: number
  account_expires_at: string | null
  last_used_at: string | null
  cooldown_until: string | null
  last_error_message: string | null
  stream_failure_count: number
  stream_failure_window_started_at: string | null
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
  model_policy_json: string | null
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

export interface TeamResourceAuthorizationGrantRow {
  id: string
  resource_type: ResourceAuthorizationResourceType
  resource_id: string
  resource_owner_system_account_id: string
  team_id: string
  scope: 'use'
  status: AuthorizationStatus
  remark: string | null
  expires_at: string | null
  limits_json: string | null
  model_policy_json: string | null
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
  binding_system_account_id?: string | null
  bound_group_id?: string | null
  bound_group_name?: string | null
  bound_group_account_authorization_id?: string | null
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
  description: string | null
  enabled: number
  is_default: number
}

export type GroupListRow = GroupRow & {
  access_type?: 'owner' | 'authorized'
  authorization_id?: string | null
  authorization_status?: AuthorizationStatus | null
}

export type ProviderCode = string
export type AccountType = string
export type AccountStatus = 'active' | 'pending_test' | 'disabled' | 'error' | 'rate_limited' | 'temporary_unavailable' | 'quality_isolated'
export type AccountTrafficMigrationSourceStatus = 'temporary_unavailable' | 'disabled' | 'unchanged'
export type SystemAccountRole = 'super_admin' | 'admin' | 'user'
export type SystemAccountStatus = 'active' | 'disabled'
export type ResourceAccessType = 'owner' | 'authorized'
export type GroupType = 'personal' | 'high_concurrency'
export type AuthorizationStatus = 'active' | 'paused' | 'expired' | 'revoked' | 'returned'
export type AccountGroupBindStatus = 'bound' | 'authorization_unavailable'
export type AuthorizationResourceType = 'account' | 'group'
export type AuthorizationSourceType = 'manual' | 'team'
export type AuthorizationGranteeType = 'system_account' | 'team'
export type AuthorizationSourceStatus = 'active' | 'superseded' | 'revoked'
export type TeamStatus = 'active' | 'disabled'
export type TeamMemberStatus = 'active' | 'removed'
export type ProcessRole =
  | 'server'
  | 'ingest-worker'
  | 'stats-worker'
  | 'ops-worker'
  | 'db-service'
  | `gateway:${string}`
  | `control:${string}`
  | `db-service:${string}`
  | `usage-worker:${number}`
  | `log-worker:${number}`
  | `stats-worker:${number}`
  | `ops-worker:${number}`

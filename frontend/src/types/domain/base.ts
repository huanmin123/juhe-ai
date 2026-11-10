export type ProviderCode = string
export type AccountType = string
export type AccountStatus = 'active' | 'pending_test' | 'disabled' | 'error' | 'rate_limited' | 'temporary_unavailable' | 'quality_isolated'
export type AccountTrafficMigrationSourceStatus = 'temporary_unavailable' | 'disabled' | 'unchanged'
export type SystemAccountRole = 'super_admin' | 'admin' | 'user'
export type SystemAccountStatus = 'active' | 'disabled'
export type ResourceAccessType = 'owner' | 'authorized'
// 高并发分组类型已退役（普通分组可选调度策略设计 §2.1）：唯一类型 personal，
// API 响应继续回 groupType: "personal" 兼容字段，废弃类型写入面一律 400 拒绝。
export type GroupType = 'personal'
export type AuthorizationStatus = 'active' | 'paused' | 'expired' | 'revoked' | 'returned'
export type AccountGroupBindStatus = 'bound' | 'authorization_unavailable'
export type AuthorizationResourceType = 'account' | 'group'
export type AuthorizationSourceType = 'manual' | 'team'
export type AuthorizationGranteeType = 'system_account' | 'team'
export type AuthorizationSourceStatus = 'active' | 'superseded' | 'revoked'
export type TeamStatus = 'active' | 'disabled'
export type TeamMemberStatus = 'active' | 'removed'

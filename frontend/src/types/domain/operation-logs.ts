export type OperationLogActorRole = 'admin' | 'user'
export type OperationLogMode = 'self' | 'admin'
export type OperationLogDetailLevel = 'full' | 'summary'
export type OperationLogVisibilityScope = 'targeted' | 'all_users' | 'admin_only'
export type OperationLogVisibilityReason =
  | 'actor_self'
  | 'resource_owner'
  | 'admin_managed_my_resource'
  | 'authorization_owner'
  | 'authorization_grantee'
  | 'team_member'
  | 'team_authorization'
  | 'global_affected'
  | 'bound_resource_affected'

export interface OperationLogChange {
  field: string
  label: string
  before?: unknown
  after?: unknown
  sensitive?: boolean
}

export interface OperationLogSummary {
  id: string
  traceId?: string
  actorSystemAccountId: string
  actorUsername?: string
  actorDisplayName?: string
  actorSystemAccountName?: string
  actorRole: OperationLogActorRole
  operationScopeSystemAccountId?: string
  operationScopeSystemAccountName?: string
  mode: OperationLogMode
  module: string
  action: string
  operationKey: string
  resourceType: string
  resourceId?: string
  resourceName?: string
  summary: string
  detailLevel: OperationLogDetailLevel
  visibilityScope: OperationLogVisibilityScope
  changes: OperationLogChange[]
  metadata: Record<string, unknown>
  method?: string
  path?: string
  statusCode?: number
  clientIp?: string
  userAgent?: string
  createdAt: string
}

export interface OperationLogTargetSummary {
  id: string
  targetType: string
  targetId?: string
  targetName?: string
  targetOwnerSystemAccountId?: string
  targetOwnerSystemAccountName?: string
  relation: string
  createdAt: string
}

export interface OperationLogViewerSummary {
  systemAccountId: string
  systemAccountName?: string
  visibilityReason: OperationLogVisibilityReason
  detailLevel: OperationLogDetailLevel
  createdAt: string
}

export interface OperationLogDetail extends OperationLogSummary {
  targets: OperationLogTargetSummary[]
  viewers: OperationLogViewerSummary[]
}

export interface OperationLogListResult {
  items: OperationLogSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

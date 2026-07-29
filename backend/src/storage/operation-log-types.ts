import type { SystemAccountRole } from '../domain/types.js'

export type OperationLogActorRole = SystemAccountRole
export type OperationLogMode = 'self' | 'admin'
export type OperationLogDetailLevel = 'full' | 'summary'
export type OperationLogVisibilityScope = 'targeted' | 'all_users' | 'admin_only'
export type OperationLogTargetRelation = 'primary' | 'affected' | 'created' | 'deleted' | 'owner' | 'grantee' | 'team_member' | 'bound_resource'
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

export interface OperationLogTargetInput {
  targetType: string
  targetId?: string
  targetName?: string
  targetOwnerSystemAccountId?: string
  relation?: OperationLogTargetRelation
}

export interface OperationLogViewerInput {
  systemAccountId: string
  visibilityReason: OperationLogVisibilityReason
  detailLevel?: OperationLogDetailLevel
}

export interface OperationLogInput {
  id?: string
  traceId?: string
  actorSystemAccountId: string
  actorUsername?: string
  actorDisplayName?: string
  actorRole: OperationLogActorRole
  operationScopeSystemAccountId?: string
  mode?: OperationLogMode
  module: string
  action: string
  operationKey: string
  resourceType: string
  resourceId?: string
  resourceName?: string
  summary: string
  detailLevel?: OperationLogDetailLevel
  visibilityScope?: OperationLogVisibilityScope
  changes?: OperationLogChange[]
  metadata?: Record<string, unknown>
  method?: string
  path?: string
  statusCode?: number
  clientIp?: string
  userAgent?: string
  targets?: OperationLogTargetInput[]
  viewers?: OperationLogViewerInput[]
  createdAt?: string
}

export interface OperationLogListOptions {
  page?: number
  pageSize?: number
  summaryKeyword?: string
  module?: string
  action?: string
  resourceType?: string
  resourceId?: string
  actorSystemAccountId?: string
  affectedSystemAccountId?: string
  operationScopeSystemAccountId?: string
  traceId?: string
  startAt?: string
  endAt?: string
}

export interface OperationLogListResult {
  items: OperationLogListItem[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export interface OperationLogListItem {
  id: string
  traceId?: string
  actorSystemAccountId: string
  actorDisplayName?: string
  actorSystemAccountName?: string
  operationScopeSystemAccountId?: string
  operationScopeSystemAccountName?: string
  module: string
  action: string
  summary: string
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

export interface OperationLogDetail extends OperationLogSummary {
  targets: OperationLogTargetSummary[]
  viewers: OperationLogViewerSummary[]
}

export interface OperationLogDetailTarget {
  id: string
  targetType: string
  targetId?: string
  targetName?: string
  targetOwnerSystemAccountName?: string
  relation: string
}

export interface OperationLogDetailViewer {
  systemAccountId: string
  systemAccountName?: string
  visibilityReason: OperationLogVisibilityReason
  detailLevel: OperationLogDetailLevel
}

/**
 * Fields that are not already present on OperationLogListItem.
 * The management detail endpoints return this delta and let the UI merge it
 * with the immutable list row that opened the drawer.
 */
export interface OperationLogDetailSupplement {
  operationKey: string
  resourceType: string
  resourceId?: string
  resourceName?: string
  visibilityScope: OperationLogVisibilityScope
  changes: OperationLogChange[]
  method?: string
  path?: string
  clientIp?: string
  targets: OperationLogDetailTarget[]
  viewers: OperationLogDetailViewer[]
}

import { optionalString } from './value-utils.js'
import type {
  OperationLogActorRole,
  OperationLogChange,
  OperationLogDetailLevel,
  OperationLogDetailSupplement,
  OperationLogListItem,
  OperationLogMode,
  OperationLogSummary,
  OperationLogTargetSummary,
  OperationLogViewerSummary,
  OperationLogVisibilityReason,
  OperationLogVisibilityScope
} from './operation-log-types.js'

export type OperationLogRow = Record<string, unknown>

export function operationLogListItemFromRow(row: OperationLogRow, systemAccountNames: Map<string, string>): OperationLogListItem {
  const actorSystemAccountId = String(row.actor_system_account_id)
  const operationScopeSystemAccountId = optionalString(row.operation_scope_system_account_id)
  return {
    id: String(row.id),
    traceId: optionalString(row.trace_id),
    actorSystemAccountId,
    actorDisplayName: optionalString(row.actor_display_name),
    actorSystemAccountName: systemAccountNames.get(actorSystemAccountId),
    operationScopeSystemAccountId,
    operationScopeSystemAccountName: operationScopeSystemAccountId ? systemAccountNames.get(operationScopeSystemAccountId) : undefined,
    module: String(row.module),
    action: String(row.action),
    summary: String(row.summary),
    createdAt: String(row.created_at)
  }
}

export function operationLogSummaryFromRow(row: OperationLogRow, systemAccountNames: Map<string, string>, options: { includePayload?: boolean } = {}): OperationLogSummary {
  const actorSystemAccountId = String(row.actor_system_account_id)
  const operationScopeSystemAccountId = optionalString(row.operation_scope_system_account_id)
  const includePayload = options.includePayload !== false
  return {
    id: String(row.id),
    traceId: optionalString(row.trace_id),
    actorSystemAccountId,
    actorUsername: optionalString(row.actor_username),
    actorDisplayName: optionalString(row.actor_display_name),
    actorSystemAccountName: systemAccountNames.get(actorSystemAccountId),
    actorRole: String(row.actor_role) as OperationLogActorRole,
    operationScopeSystemAccountId,
    operationScopeSystemAccountName: operationScopeSystemAccountId ? systemAccountNames.get(operationScopeSystemAccountId) : undefined,
    mode: String(row.mode) as OperationLogMode,
    module: String(row.module),
    action: String(row.action),
    operationKey: String(row.operation_key),
    resourceType: String(row.resource_type),
    resourceId: optionalString(row.resource_id),
    resourceName: optionalString(row.resource_name),
    summary: String(row.summary),
    detailLevel: String(row.detail_level) as OperationLogDetailLevel,
    visibilityScope: String(row.visibility_scope) as OperationLogVisibilityScope,
    changes: includePayload ? parseJsonArray(row.changes_json) : [],
    metadata: includePayload ? parseJsonObject(row.metadata_json) : {},
    method: optionalString(row.method),
    path: optionalString(row.path),
    statusCode: numberValue(row.status_code),
    clientIp: optionalString(row.client_ip),
    userAgent: optionalString(row.user_agent),
    createdAt: String(row.created_at)
  }
}

export function operationLogDetailSupplementFromRow(row: OperationLogRow): OperationLogDetailSupplement {
  return {
    operationKey: String(row.operation_key),
    resourceType: String(row.resource_type),
    resourceId: optionalString(row.resource_id),
    resourceName: optionalString(row.resource_name),
    visibilityScope: String(row.visibility_scope) as OperationLogVisibilityScope,
    changes: parseJsonArray(row.changes_json),
    method: optionalString(row.method),
    path: optionalString(row.path),
    clientIp: optionalString(row.client_ip),
    targets: [],
    viewers: []
  }
}

export function operationLogTargetFromRow(row: OperationLogRow, systemAccountNames: Map<string, string>): OperationLogTargetSummary {
  const targetOwnerSystemAccountId = optionalString(row.target_owner_system_account_id)
  return {
    id: String(row.id),
    targetType: String(row.target_type),
    targetId: optionalString(row.target_id),
    targetName: optionalString(row.target_name),
    targetOwnerSystemAccountId,
    targetOwnerSystemAccountName: targetOwnerSystemAccountId ? systemAccountNames.get(targetOwnerSystemAccountId) : undefined,
    relation: String(row.relation),
    createdAt: String(row.created_at)
  }
}

export function operationLogViewerFromRow(row: OperationLogRow, systemAccountNames: Map<string, string>): OperationLogViewerSummary {
  const systemAccountId = String(row.system_account_id)
  return {
    systemAccountId,
    systemAccountName: systemAccountNames.get(systemAccountId),
    visibilityReason: String(row.visibility_reason) as OperationLogVisibilityReason,
    detailLevel: String(row.detail_level) as OperationLogDetailLevel,
    createdAt: String(row.created_at)
  }
}

function parseJsonArray(value: unknown): OperationLogChange[] {
  if (typeof value !== 'string' || !value.trim()) return []
  const parsed = JSON.parse(value) as unknown
  if (!Array.isArray(parsed)) {
    throw new Error('操作日志 changes_json 必须是数组')
  }
  return parsed as OperationLogChange[]
}

function parseJsonObject(value: unknown): Record<string, unknown> {
  if (typeof value !== 'string' || !value.trim()) return {}
  const parsed = JSON.parse(value) as unknown
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error('操作日志 metadata_json 必须是对象')
  }
  return parsed as Record<string, unknown>
}

function numberValue(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

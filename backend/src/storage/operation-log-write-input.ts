import { newId, nowIso } from './database.js'
import { operationLogSummaryFromRow } from './operation-log-mappers.js'
import type {
  OperationLogDetailLevel,
  OperationLogInput,
  OperationLogSummary,
  OperationLogTargetInput,
  OperationLogViewerInput,
  OperationLogVisibilityScope
} from './operation-log-types.js'

export interface PreparedOperationLogInput {
  id: string
  input: OperationLogInput
  createdAt: string
  visibilityScope: OperationLogVisibilityScope
  detailLevel: OperationLogDetailLevel
  targets: OperationLogTargetInput[]
  viewers: OperationLogViewerInput[]
  changesJson: string
  metadataJson: string
}

export function prepareOperationLogInput(input: OperationLogInput): PreparedOperationLogInput {
  const detailLevel = input.detailLevel ?? 'full'
  return {
    id: input.id ?? newId('oplog'),
    input,
    createdAt: input.createdAt ?? nowIso(),
    visibilityScope: input.visibilityScope ?? 'targeted',
    detailLevel,
    targets: normalizeTargets(input),
    viewers: normalizeViewers(input),
    changesJson: safeJsonStringify(input.changes ?? [], '[]'),
    metadataJson: safeJsonStringify(input.metadata ?? {}, '{}')
  }
}

export function operationLogSummaryFromPrepared(prepared: PreparedOperationLogInput): OperationLogSummary {
  const input = prepared.input
  return operationLogSummaryFromRow({
    id: prepared.id,
    trace_id: input.traceId,
    actor_system_account_id: input.actorSystemAccountId,
    actor_username: input.actorUsername,
    actor_display_name: input.actorDisplayName,
    actor_role: input.actorRole,
    operation_scope_system_account_id: input.operationScopeSystemAccountId,
    mode: input.mode ?? 'self',
    module: input.module,
    action: input.action,
    operation_key: input.operationKey,
    resource_type: input.resourceType,
    resource_id: input.resourceId,
    resource_name: input.resourceName,
    summary: input.summary,
    detail_level: prepared.detailLevel,
    visibility_scope: prepared.visibilityScope,
    changes_json: prepared.changesJson,
    metadata_json: prepared.metadataJson,
    method: input.method,
    path: input.path,
    status_code: input.statusCode,
    client_ip: input.clientIp,
    user_agent: input.userAgent,
    created_at: prepared.createdAt
  }, new Map())
}

function normalizeTargets(input: OperationLogInput): OperationLogTargetInput[] {
  const targets = [...(input.targets ?? [])]
  if (input.resourceId || input.resourceName) {
    const hasPrimary = targets.some((target) => target.relation === 'primary')
    if (!hasPrimary) {
      targets.unshift({
        targetType: input.resourceType,
        targetId: input.resourceId,
        targetName: input.resourceName,
        targetOwnerSystemAccountId: input.operationScopeSystemAccountId,
        relation: 'primary'
      })
    }
  }
  return targets
}

function normalizeViewers(input: OperationLogInput): OperationLogViewerInput[] {
  const viewers = [...(input.viewers ?? [])]
  if (input.visibilityScope === 'admin_only' || input.visibilityScope === 'all_users') {
    return []
  }
  viewers.push({ systemAccountId: input.actorSystemAccountId, visibilityReason: 'actor_self', detailLevel: input.detailLevel })
  if (input.operationScopeSystemAccountId && input.operationScopeSystemAccountId !== input.actorSystemAccountId) {
    viewers.push({
      systemAccountId: input.operationScopeSystemAccountId,
      visibilityReason: input.actorRole === 'admin' ? 'admin_managed_my_resource' : 'resource_owner',
      detailLevel: input.detailLevel
    })
  }
  return dedupeViewers(viewers)
}

function dedupeViewers(viewers: OperationLogViewerInput[]): OperationLogViewerInput[] {
  const seen = new Set<string>()
  const output: OperationLogViewerInput[] = []
  for (const viewer of viewers) {
    if (!viewer.systemAccountId.trim()) continue
    const key = `${viewer.systemAccountId}:${viewer.visibilityReason}:${viewer.detailLevel ?? ''}`
    if (seen.has(key)) continue
    seen.add(key)
    output.push(viewer)
  }
  return output
}

function safeJsonStringify(value: unknown, fallback: string): string {
  try {
    return JSON.stringify(value) ?? fallback
  } catch {
    return fallback
  }
}

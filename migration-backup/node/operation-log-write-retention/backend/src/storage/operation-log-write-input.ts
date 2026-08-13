import { newId, nowIso } from './database.js'
import type {
  OperationLogChange,
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
  changes: OperationLogChange[]
  metadata: Record<string, unknown>
}

export function prepareOperationLogInput(input: OperationLogInput): PreparedOperationLogInput {
  const detailLevel = input.detailLevel ?? 'full'
  const changes = prepareJsonValue(input.changes ?? [], [])
  const metadata = prepareJsonValue(input.metadata ?? {}, {})
  return {
    id: input.id ?? newId('oplog'),
    input,
    createdAt: input.createdAt ?? nowIso(),
    visibilityScope: input.visibilityScope ?? 'targeted',
    detailLevel,
    targets: normalizeTargets(input),
    viewers: normalizeViewers(input),
    changesJson: changes.json,
    metadataJson: metadata.json,
    changes: changes.value,
    metadata: metadata.value
  }
}

export function operationLogSummaryFromPrepared(prepared: PreparedOperationLogInput): OperationLogSummary {
  const input = prepared.input
  return {
    id: prepared.id,
    traceId: input.traceId,
    actorSystemAccountId: input.actorSystemAccountId,
    actorUsername: input.actorUsername,
    actorDisplayName: input.actorDisplayName,
    actorRole: input.actorRole,
    operationScopeSystemAccountId: input.operationScopeSystemAccountId,
    mode: input.mode ?? 'self',
    module: input.module,
    action: input.action,
    operationKey: input.operationKey,
    resourceType: input.resourceType,
    resourceId: input.resourceId,
    resourceName: input.resourceName,
    summary: input.summary,
    detailLevel: prepared.detailLevel,
    visibilityScope: prepared.visibilityScope,
    changes: prepared.changes,
    metadata: prepared.metadata,
    method: input.method,
    path: input.path,
    statusCode: input.statusCode,
    clientIp: input.clientIp,
    userAgent: input.userAgent,
    createdAt: prepared.createdAt
  }
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

function prepareJsonValue<T>(value: T, fallback: T): { json: string; value: T } {
  try {
    const json = JSON.stringify(value)
    return json === undefined
      ? { json: JSON.stringify(fallback), value: fallback }
      : { json, value }
  } catch {
    return { json: JSON.stringify(fallback), value: fallback }
  }
}

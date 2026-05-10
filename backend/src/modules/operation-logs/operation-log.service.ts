import type { Request } from 'express'

import { getRequestContext } from '../../shared/request-context.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { runInDatabaseTransaction } from '../../storage/database.js'
import {
  createOperationLog,
  getSettings,
  type OperationLogChange,
  type OperationLogInput,
  type OperationLogTargetInput,
  type OperationLogViewerInput,
  type OperationLogVisibilityReason,
  type SystemAccountRole
} from '../../storage/repositories.js'
import { getRequestAuthContext } from '../auth/request-context.js'

export type OperationLogRecordInput = Omit<OperationLogInput, 'actorSystemAccountId' | 'actorUsername' | 'actorDisplayName' | 'actorRole'> & {
  actorSystemAccountId?: string
  actorUsername?: string
  actorDisplayName?: string
  actorRole?: SystemAccountRole
  force?: boolean
}

type LoggedOperationResult<T> = {
  result: T
  log?: OperationLogRecordInput | OperationLogRecordInput[]
  afterCommit?: () => void
}

const sensitiveFieldNames = new Set([
  'apiKey',
  'api_key',
  'key',
  'password',
  'proxyPassword',
  'refreshToken',
  'refresh_token',
  'accessToken',
  'access_token',
  'credentials',
  'token',
  'secret'
])

export function recordOperationLog(input: OperationLogRecordInput, req?: Request): void {
  if (!input.force && !operationLogEnabled()) {
    return
  }
  const actor = getRequestAuthContext()
  const requestContext = getRequestContext()
  const actorSystemAccountId = input.actorSystemAccountId ?? actor?.systemAccountId
  if (!actorSystemAccountId) {
    return
  }

  const requestPath = input.path ?? (req ? `${req.baseUrl}${req.path}` : requestContext?.path)
  createOperationLog({
    ...input,
    actorSystemAccountId,
    actorUsername: input.actorUsername ?? actor?.username,
    actorDisplayName: input.actorDisplayName ?? actor?.displayName,
    actorRole: input.actorRole ?? actor?.role ?? 'user',
    traceId: input.traceId ?? requestContext?.traceId,
    method: input.method ?? req?.method ?? requestContext?.method,
    path: requestPath,
    clientIp: input.clientIp ?? requestContext?.clientIp,
    userAgent: input.userAgent ?? req?.header('user-agent'),
    changes: sanitizeOperationChanges(input.changes ?? []),
    targets: input.targets,
    viewers: input.viewers
  })
}

export function runLoggedOperation<T>(operation: () => LoggedOperationResult<T>, req?: Request): T {
  let afterCommit: (() => void) | undefined
  const result = runInDatabaseTransaction(() => {
    const outcome = operation()
    const logs = Array.isArray(outcome.log) ? outcome.log : outcome.log ? [outcome.log] : []
    for (const log of logs) {
      recordOperationLog(log, req)
    }
    afterCommit = outcome.afterCommit
    return outcome.result
  })
  afterCommit?.()
  return result
}

export function operationMode(access?: Pick<AccessScope, 'role'>): 'admin' | 'self' {
  return access?.role === 'admin' ? 'admin' : 'self'
}

export function resolveOperationOwner(resource?: Record<string, unknown>, access?: AccessScope): string | undefined {
  const ownerFromResource = firstString(
    resource?.systemAccountId,
    resource?.ownerSystemAccountId,
    resource?.resourceOwnerSystemAccountId
  )
  if (ownerFromResource) return ownerFromResource
  if (access?.role === 'admin' && access.systemAccountFilterId?.trim()) {
    return access.systemAccountFilterId.trim()
  }
  return access?.systemAccountId
}

export function viewer(systemAccountId: string | undefined, visibilityReason: OperationLogVisibilityReason, detailLevel: 'full' | 'summary' = 'full'): OperationLogViewerInput[] {
  return systemAccountId ? [{ systemAccountId, visibilityReason, detailLevel }] : []
}

export function viewers(...items: Array<OperationLogViewerInput | OperationLogViewerInput[] | undefined>): OperationLogViewerInput[] {
  const flattened = items.flatMap((item) => Array.isArray(item) ? item : item ? [item] : [])
  const seen = new Set<string>()
  const output: OperationLogViewerInput[] = []
  for (const item of flattened) {
    if (!item.systemAccountId.trim()) continue
    const key = `${item.systemAccountId}:${item.visibilityReason}:${item.detailLevel ?? ''}`
    if (seen.has(key)) continue
    seen.add(key)
    output.push(item)
  }
  return output
}

export function ownerTarget(input: { targetType: string; targetId?: string; targetName?: string; ownerSystemAccountId?: string; relation?: OperationLogTargetInput['relation'] }): OperationLogTargetInput {
  return {
    targetType: input.targetType,
    targetId: input.targetId,
    targetName: input.targetName,
    targetOwnerSystemAccountId: input.ownerSystemAccountId,
    relation: input.relation ?? 'primary'
  }
}

export function diffSafeFields(before: Record<string, unknown> | undefined, after: Record<string, unknown> | undefined, labels: Record<string, string>): OperationLogChange[] {
  const changes: OperationLogChange[] = []
  for (const [field, label] of Object.entries(labels)) {
    const beforeValue = before?.[field]
    const afterValue = after?.[field]
    if (JSON.stringify(beforeValue ?? null) === JSON.stringify(afterValue ?? null)) {
      continue
    }
    changes.push(safeChange(field, label, beforeValue, afterValue))
  }
  return changes
}

export function safeChange(field: string, label: string, before: unknown, after: unknown): OperationLogChange {
  if (isSensitiveField(field)) {
    return {
      field,
      label,
      before: before === undefined || before === null || before === '' ? '未设置' : '已设置',
      after: after === undefined || after === null || after === '' ? '未设置' : '已变更',
      sensitive: true
    }
  }
  return { field, label, before: normalizeSafeValue(before), after: normalizeSafeValue(after) }
}

function sanitizeOperationChanges(changes: OperationLogChange[]): OperationLogChange[] {
  const maxChanges = operationLogMaxChangesPerRecord()
  const normalized = changes.map((change) => isSensitiveField(change.field) || change.sensitive
    ? {
        field: change.field,
        label: change.label,
        before: change.before === undefined || change.before === null || change.before === '未设置' ? '未设置' : '已设置',
        after: change.after === undefined || change.after === null || change.after === '未设置' ? '未设置' : '已变更',
        sensitive: true
      }
    : {
        ...change,
        before: normalizeSafeValue(change.before),
        after: normalizeSafeValue(change.after)
      })
  if (normalized.length <= maxChanges) {
    return normalized
  }
  return [
    ...normalized.slice(0, maxChanges),
    {
      field: '__truncated__',
      label: '其余变更',
      before: undefined,
      after: `还有 ${normalized.length - maxChanges} 项变更未展开`
    }
  ]
}

function normalizeSafeValue(value: unknown): unknown {
  if (value === undefined) return undefined
  if (typeof value === 'string') {
    return value.length > 200 ? `${value.slice(0, 200)}...` : value
  }
  if (value === null || typeof value === 'number' || typeof value === 'boolean') {
    return value
  }
  return JSON.stringify(value).slice(0, 500)
}

function isSensitiveField(field: string): boolean {
  const normalized = field.trim()
  return sensitiveFieldNames.has(normalized) || [...sensitiveFieldNames].some((name) => normalized.toLowerCase().includes(name.toLowerCase()))
}

function operationLogEnabled(): boolean {
  const value = getSettings().operationLogEnabled
  return value !== false
}

function operationLogMaxChangesPerRecord(): number {
  const value = getSettings().operationLogMaxChangesPerRecord
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? Math.max(1, Math.min(500, Math.trunc(number))) : 100
}

function firstString(...values: unknown[]): string | undefined {
  return values.find((value): value is string => typeof value === 'string' && value.trim().length > 0)?.trim()
}

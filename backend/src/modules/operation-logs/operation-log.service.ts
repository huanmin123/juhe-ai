import type { Request } from 'express'

import { isAdminRole } from '../../domain/types.js'
import { errorLogFields } from '../../shared/logger.js'
import { getRequestContext, getRequestLogger } from '../../shared/request-context.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { newId, nowIso, runInDatabaseTransaction } from '../../storage/database.js'
import {
  getSettings,
  getSettingsAsync,
  type OperationLogChange,
  type OperationLogInput,
  type OperationLogTargetInput,
  type OperationLogViewerInput,
  type OperationLogVisibilityReason,
  type SystemAccountRole
} from '../../storage/repositories.js'
import { getRequestAuthContext } from '../auth/request-context.js'
import { dispatchOperationLogToGo } from './operation-log-go-input.service.js'

export type OperationLogRecordInput = Omit<OperationLogInput, 'actorSystemAccountId' | 'actorUsername' | 'actorDisplayName' | 'actorRole'> & {
  actorSystemAccountId?: string
  actorUsername?: string
  actorDisplayName?: string
  actorRole?: SystemAccountRole
}

type LoggedOperationResult<T> = {
  result: T
  log?: OperationLogRecordInput | OperationLogRecordInput[]
  afterCommit?: () => void | Promise<void>
}

export function recordOperationLog(input: OperationLogRecordInput, req?: Request): void {
	const operationLogId = input.id ?? newId('oplog')
	const inputWithId = { ...input, id: operationLogId }
  try {
    recordOperationLogUnsafe(inputWithId, req)
  } catch (error) {
    logOperationLogFailure(error, inputWithId, operationLogId)
  }
}

function recordOperationLogUnsafe(input: OperationLogRecordInput, req?: Request): void {
  const actor = getRequestAuthContext()
  const requestContext = getRequestContext()
  const actorSystemAccountId = input.actorSystemAccountId ?? actor?.systemAccountId
  if (!actorSystemAccountId) {
    return
  }

  const settings = getSettings()

  const requestPath = input.path ?? (req ? `${req.baseUrl}${req.path}` : requestContext?.path)
  const operationLogId = input.id ?? newId('oplog')
  void dispatchOperationLogToGo({
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
    changes: sanitizeOperationChanges(input.changes ?? [], operationLogMaxChangesPerRecord(settings)),
    targets: input.targets,
    viewers: input.viewers,
    id: operationLogId,
    createdAt: input.createdAt ?? nowIso()
  }).catch((error) => {
    logOperationLogFailure(error, input, operationLogId)
  })
}

export function runLoggedOperation<T>(operation: () => LoggedOperationResult<T>, req?: Request): T {
  let afterCommit: (() => void) | undefined
  let logs: OperationLogRecordInput[] = []
  const result = runInDatabaseTransaction(() => {
    const outcome = operation()
    logs = Array.isArray(outcome.log) ? outcome.log : outcome.log ? [outcome.log] : []
    afterCommit = outcome.afterCommit
    return outcome.result
  })
  for (const log of logs) {
    recordOperationLog(log, req)
  }
  runAfterCommitEffect(afterCommit)
  return result
}

export async function runLoggedOperationAsync<T>(operation: () => Promise<LoggedOperationResult<T>>, req?: Request): Promise<T> {
  const outcome = await operation()
  const logs = Array.isArray(outcome.log) ? outcome.log : outcome.log ? [outcome.log] : []
  for (const log of logs) {
    await recordOperationLogAsync(log, req)
  }
  await runAfterCommitEffectAsync(outcome.afterCommit)
  return outcome.result
}

export async function recordOperationLogAsync(input: OperationLogRecordInput, req?: Request): Promise<void> {
	const operationLogId = input.id ?? newId('oplog')
	const inputWithId = { ...input, id: operationLogId }
  try {
    await recordOperationLogUnsafeAsync(inputWithId, req)
  } catch (error) {
    logOperationLogFailure(error, inputWithId, operationLogId)
  }
}

async function recordOperationLogUnsafeAsync(input: OperationLogRecordInput, req?: Request): Promise<void> {
  const actor = getRequestAuthContext()
  const requestContext = getRequestContext()
  const actorSystemAccountId = input.actorSystemAccountId ?? actor?.systemAccountId
  if (!actorSystemAccountId) {
    return
  }

  const settings = await getSettingsAsync()

  const requestPath = input.path ?? (req ? `${req.baseUrl}${req.path}` : requestContext?.path)
  const operationLogId = input.id ?? newId('oplog')
  await dispatchOperationLogToGo({
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
    changes: sanitizeOperationChanges(input.changes ?? [], operationLogMaxChangesPerRecord(settings)),
    targets: input.targets,
    viewers: input.viewers,
    id: operationLogId,
    createdAt: input.createdAt ?? nowIso()
  })
}

function logOperationLogFailure(error: unknown, input: Pick<OperationLogRecordInput, 'id' | 'module' | 'action'>, operationLogId = input.id): void {
  getRequestLogger().warn(errorLogFields(error, {
    event: 'operation_log_go_input_failed',
    operationLogId,
    producer: 'node_operation_log_service',
    module: input.module,
    action: input.action,
    errorClass: error instanceof Error ? error.name : typeof error
  }), 'F4 Go 操作日志提交失败')
}

export function operationMode(access?: Pick<AccessScope, 'role'>): 'admin' | 'self' {
  return isAdminRole(access?.role) ? 'admin' : 'self'
}

export function resolveOperationOwner(resource?: Record<string, unknown>, access?: AccessScope): string | undefined {
  if (resource?.accessType === 'authorized') {
    return firstString(
      resource.bindingSystemAccountId,
      resource.systemAccountId,
      access?.systemAccountFilterId,
      access?.systemAccountId
    )
  }
  const ownerFromResource = firstString(
    resource?.systemAccountId,
    resource?.ownerSystemAccountId,
    resource?.resourceOwnerSystemAccountId
  )
  if (ownerFromResource) return ownerFromResource
  if (isAdminRole(access?.role) && access.systemAccountFilterId?.trim()) {
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
    if (operationLogComparableValue(beforeValue ?? null) === operationLogComparableValue(afterValue ?? null)) {
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

export function sanitizeOperationChanges(changes: OperationLogChange[], maxChanges: number): OperationLogChange[] {
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
  if (typeof value === 'bigint') {
    return value.toString()
  }
  try {
    const serialized = JSON.stringify(value)
    return (serialized ?? String(value)).slice(0, 500)
  } catch {
    return String(value).slice(0, 500)
  }
}

const sensitiveOperationChangeContainers = new Set([
  'credentials',
  'credential',
  'token',
  'key',
  'secret',
  'password',
  'apikey',
  'api_key',
  'apikeys',
  'api_keys',
  'accesstoken',
  'access_token',
  'refreshtoken',
  'refresh_token',
  'idtoken',
  'id_token',
  'identitytoken',
  'identity_token',
  'clientsecret',
  'client_secret',
  'sessiontoken',
  'session_token',
  'proxypassword',
  'proxy_password'
])

function isSensitiveField(field: string): boolean {
  return sensitiveOperationChangeContainers.has(field.trim().toLowerCase())
}

function operationLogMaxChangesPerRecord(settings: Record<string, unknown>): number {
  const value = settings.operationLogMaxChangesPerRecord
  if (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value)) {
    throw new Error('系统设置 operationLogMaxChangesPerRecord 必须是整数')
  }
  if (value < 1 || value > 500) {
    throw new Error('系统设置 operationLogMaxChangesPerRecord 必须在 1 到 500 之间')
  }
  return value
}

function operationLogComparableValue(value: unknown): string {
  if (typeof value === 'bigint') {
    return `bigint:${value.toString()}`
  }
  try {
    const serialized = JSON.stringify(value)
    return serialized ?? String(value)
  } catch {
    return String(value)
  }
}

function runAfterCommitEffect(afterCommit?: () => void | Promise<void>): void {
  if (!afterCommit) return
  try {
    const result = afterCommit()
    if (result && typeof result === 'object' && 'catch' in result && typeof result.catch === 'function') {
      void result.catch((error: unknown) => {
        getRequestLogger().warn(errorLogFields(error, {
          event: 'operation_log_after_commit_effect_failed'
        }), '操作日志提交后副作用执行失败')
      })
    }
  } catch (error) {
    getRequestLogger().warn(errorLogFields(error, {
      event: 'operation_log_after_commit_effect_failed'
    }), '操作日志提交后副作用执行失败')
  }
}

async function runAfterCommitEffectAsync(afterCommit?: () => void | Promise<void>): Promise<void> {
  if (!afterCommit) return
  try {
    await afterCommit()
  } catch (error) {
    getRequestLogger().warn(errorLogFields(error, {
      event: 'operation_log_after_commit_effect_failed'
    }), '操作日志提交后副作用执行失败')
  }
}

function firstString(...values: unknown[]): string | undefined {
  return values.find((value): value is string => typeof value === 'string' && value.trim().length > 0)?.trim()
}

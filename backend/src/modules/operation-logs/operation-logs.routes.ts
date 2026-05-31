import { Router } from 'express'

import { ok, sendNotFound } from '../../shared/http.js'
import { finiteNumberQueryValue, optionalQueryText } from '../../shared/query-values.js'
import {
  getOperationLogDetail,
  getOperationLogDetailForViewer,
  listOperationLogs,
  listOperationLogsForViewer,
  type OperationLogListOptions
} from '../../storage/repositories.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { getRequestAuthContext } from '../auth/request-context.js'

export const operationLogsRouter = Router()
export const myOperationLogsRouter = Router()

myOperationLogsRouter.get('/', (req, res) => {
  const context = getRequestAuthContext()
  if (!context) {
    res.status(401).json({ message: '请先登录' })
    return
  }
  res.json(ok(listOperationLogsForViewer(context.systemAccountId, parseOperationLogListOptions(req.query, false))))
})

myOperationLogsRouter.get('/:id', (req, res) => {
  const context = getRequestAuthContext()
  if (!context) {
    res.status(401).json({ message: '请先登录' })
    return
  }
  const detail = getOperationLogDetailForViewer(req.params.id, context.systemAccountId)
  if (!detail) {
    sendNotFound(res, '操作日志不存在')
    return
  }
  res.json(ok(detail))
})

operationLogsRouter.get('/', requireAdmin, (req, res) => {
  res.json(ok(listOperationLogs(parseOperationLogListOptions(req.query, true))))
})

operationLogsRouter.get('/:id', requireAdmin, (req, res) => {
  const detail = getOperationLogDetail(req.params.id)
  if (!detail) {
    sendNotFound(res, '操作日志不存在')
    return
  }
  res.json(ok(detail))
})

function parseOperationLogListOptions(query: Record<string, unknown>, includeAdminFilters: boolean): OperationLogListOptions {
  const createdAtRange = dateTimeRangeQueryValue(query.startAt, query.endAt)
  return {
    page: finiteNumberQueryValue(query.page),
    pageSize: finiteNumberQueryValue(query.pageSize),
    keyword: optionalQueryText(query.keyword),
    module: optionalQueryText(query.module),
    action: optionalQueryText(query.action),
    resourceType: optionalQueryText(query.resourceType),
    resourceId: optionalQueryText(query.resourceId),
    traceId: optionalQueryText(query.traceId),
    startAt: createdAtRange.startAt,
    endAt: createdAtRange.endAt,
    actorSystemAccountId: includeAdminFilters ? optionalQueryText(query.actorSystemAccountId) : undefined,
    affectedSystemAccountId: includeAdminFilters ? optionalQueryText(query.affectedSystemAccountId) : undefined,
    operationScopeSystemAccountId: includeAdminFilters ? optionalQueryText(query.operationScopeSystemAccountId) : undefined
  }
}

function dateTimeRangeQueryValue(startValue: unknown, endValue: unknown): { startAt?: string; endAt?: string } {
  const startAt = dateTimeQueryValue(startValue)
  const endAt = dateTimeQueryValue(endValue)
  if (startAt && endAt && startAt > endAt) {
    return { startAt: endAt, endAt: startAt }
  }
  return { startAt, endAt }
}

function dateTimeQueryValue(value: unknown): string | undefined {
  const text = optionalQueryText(value)
  if (!text) return undefined
  const time = Date.parse(text)
  return Number.isNaN(time) ? undefined : new Date(time).toISOString()
}

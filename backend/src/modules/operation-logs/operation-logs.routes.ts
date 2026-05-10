import { Router } from 'express'

import { ok, sendNotFound } from '../../shared/http.js'
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
  return {
    page: numberQueryValue(query.page),
    pageSize: numberQueryValue(query.pageSize),
    limit: numberQueryValue(query.limit),
    keyword: optionalQueryText(query.keyword),
    module: optionalQueryText(query.module),
    action: optionalQueryText(query.action),
    resourceType: optionalQueryText(query.resourceType),
    resourceId: optionalQueryText(query.resourceId),
    traceId: optionalQueryText(query.traceId),
    startAt: optionalQueryText(query.startAt),
    endAt: optionalQueryText(query.endAt),
    actorSystemAccountId: includeAdminFilters ? optionalQueryText(query.actorSystemAccountId) : undefined,
    affectedSystemAccountId: includeAdminFilters ? optionalQueryText(query.affectedSystemAccountId) : undefined,
    operationScopeSystemAccountId: includeAdminFilters ? optionalQueryText(query.operationScopeSystemAccountId) : undefined
  }
}

function numberQueryValue(value: unknown): number | undefined {
  const text = Array.isArray(value) ? value[0] : value
  const number = typeof text === 'string' ? Number(text) : typeof text === 'number' ? text : undefined
  return typeof number === 'number' && Number.isFinite(number) ? number : undefined
}

function optionalQueryText(value: unknown): string | undefined {
  const text = Array.isArray(value) ? value[0] : value
  return typeof text === 'string' && text.trim() ? text.trim() : undefined
}

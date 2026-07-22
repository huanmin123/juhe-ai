import { Router } from 'express'

import { ok, sendNotFound } from '../../shared/http.js'
import { finiteNumberQueryValue, optionalQueryText } from '../../shared/query-values.js'
import {
  getOperationLogDetailAsync,
  getOperationLogDetailForViewerAsync,
  listOperationLogsAsync,
  listOperationLogsForViewerAsync,
  type OperationLogListOptions
} from '../../storage/repositories.js'
import type { OperationLogListResult, OperationLogSummary } from '../../storage/operation-log-types.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { getRequestAuthContext } from '../auth/request-context.js'

export const operationLogsRouter = Router()
export const myOperationLogsRouter = Router()

myOperationLogsRouter.get('/', async (req, res, next) => {
  try {
    const context = getRequestAuthContext()
    if (!context) {
      res.status(401).json({ message: '请先登录' })
      return
    }
    const result = await listOperationLogsForViewerAsync(context.systemAccountId, parseOperationLogListOptions(req.query, false))
    res.json(ok(toOperationLogListResponse(result)))
  } catch (error) {
    next(error)
  }
})

myOperationLogsRouter.get('/:id', async (req, res, next) => {
  try {
    const context = getRequestAuthContext()
    if (!context) {
      res.status(401).json({ message: '请先登录' })
      return
    }
    const detail = await getOperationLogDetailForViewerAsync(req.params.id, context.systemAccountId)
    if (!detail) {
      sendNotFound(res, '操作日志不存在')
      return
    }
    res.json(ok(detail))
  } catch (error) {
    next(error)
  }
})

operationLogsRouter.get('/', requireAdmin, async (req, res, next) => {
  try {
    const result = await listOperationLogsAsync(parseOperationLogListOptions(req.query, true))
    res.json(ok(toOperationLogListResponse(result)))
  } catch (error) {
    next(error)
  }
})

operationLogsRouter.get('/:id', requireAdmin, async (req, res, next) => {
  try {
    const detail = await getOperationLogDetailAsync(req.params.id)
    if (!detail) {
      sendNotFound(res, '操作日志不存在')
      return
    }
    res.json(ok(detail))
  } catch (error) {
    next(error)
  }
})

function parseOperationLogListOptions(query: Record<string, unknown>, includeAdminFilters: boolean): OperationLogListOptions {
  const createdAtRange = dateTimeRangeQueryValue(query.startAt, query.endAt)
  return {
    page: finiteNumberQueryValue(query.page),
    pageSize: finiteNumberQueryValue(query.pageSize),
    summaryKeyword: optionalQueryText(query.summaryKeyword),
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

type OperationLogListItem = Omit<OperationLogSummary, 'changes' | 'metadata' | 'userAgent'>

function toOperationLogListResponse(result: OperationLogListResult): Omit<OperationLogListResult, 'items'> & { items: OperationLogListItem[] } {
  return {
    ...result,
    items: result.items.map(({ changes, metadata, userAgent, ...item }) => item)
  }
}

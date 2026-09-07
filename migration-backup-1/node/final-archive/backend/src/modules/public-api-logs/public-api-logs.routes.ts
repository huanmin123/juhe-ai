import { Router } from 'express'

import { ok, sendNotFound } from '../../shared/http.js'
import { finiteNumberQueryValue, optionalQueryText, strictDateTimeRangeQueryValue } from '../../shared/query-values.js'
import {
  getPublicApiLogDetailSupplementAsync,
  listPublicApiLogsAsync,
  type PublicApiLogListOptions,
  type PublicApiLogResultFilter
} from '../../storage/repositories.js'
import { requireAdmin } from '../auth/auth.middleware.js'

export const publicApiLogsRouter = Router()
publicApiLogsRouter.use(requireAdmin)

publicApiLogsRouter.get('/', async (req, res, next) => {
  try {
    const result = await listPublicApiLogsAsync(parsePublicApiLogListOptions(req.query))
    res.json(ok(result))
  } catch (error) {
    next(error)
  }
})

publicApiLogsRouter.get('/:id', async (req, res, next) => {
  try {
    const supplement = await getPublicApiLogDetailSupplementAsync(req.params.id)
    if (!supplement) {
      sendNotFound(res, '公开接口日志不存在')
      return
    }
    res.json(ok(supplement))
  } catch (error) {
    next(error)
  }
})

const resultFilters = new Set<PublicApiLogResultFilter>(['success', 'failed', 'all'])

function parsePublicApiLogListOptions(query: Record<string, unknown>): PublicApiLogListOptions {
  const rawPage = finiteNumberQueryValue(query.page)
  const rawPageSize = finiteNumberQueryValue(query.pageSize)
  const rawStatusCode = finiteNumberQueryValue(query.statusCode)
  const result = optionalQueryText(query.result)
  return {
    page: Number.isInteger(rawPage) ? rawPage : undefined,
    pageSize: Number.isInteger(rawPageSize) ? rawPageSize : undefined,
    traceId: optionalQueryText(query.traceId),
    sourceRefId: optionalQueryText(query.sourceRefId),
    path: optionalQueryText(query.path),
    result: resultFilters.has(result as PublicApiLogResultFilter) ? result as PublicApiLogResultFilter : undefined,
    statusCode: isHttpStatusCode(rawStatusCode) ? rawStatusCode : undefined,
    clientIp: optionalQueryText(query.clientIp),
    ...strictDateTimeRangeQueryValue(query.startAt, query.endAt)
  }
}

function isHttpStatusCode(value: unknown): value is number {
  return Number.isInteger(value) && Number(value) >= 100 && Number(value) <= 599
}

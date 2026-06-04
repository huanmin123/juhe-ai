import { Router } from 'express'

import { ok, sendNotFound } from '../../shared/http.js'
import { finiteNumberQueryValue, optionalQueryText } from '../../shared/query-values.js'
import {
  getPublicApiLogDetail,
  listPublicApiLogs,
  type PublicApiLogListOptions,
  type PublicApiLogResultFilter
} from '../../storage/repositories.js'

export const publicApiLogsRouter = Router()

publicApiLogsRouter.get('/', (req, res) => {
  res.json(ok(listPublicApiLogs(parsePublicApiLogListOptions(req.query))))
})

publicApiLogsRouter.get('/:id', (req, res) => {
  const detail = getPublicApiLogDetail(req.params.id)
  if (!detail) {
    sendNotFound(res, '公开接口日志不存在')
    return
  }
  res.json(ok(detail))
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
    startAt: optionalQueryText(query.startAt),
    endAt: optionalQueryText(query.endAt)
  }
}

function isHttpStatusCode(value: unknown): value is number {
  return Number.isInteger(value) && Number(value) >= 100 && Number(value) <= 599
}

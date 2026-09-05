import { Router } from 'express'

import { ok } from '../../shared/http.js'
import { finiteNumberQueryValue, optionalQueryText, strictDateTimeRangeQueryValue } from '../../shared/query-values.js'
import type { RuntimeLogLevel, RuntimeLogListOptions } from '../../storage/runtime-logs.repository.js'
import { requestDbService } from '../db-service/db-service-ipc.js'
import { getRuntimeLogGrepDetail, getRuntimeLogGrepRuntime, grepRuntimeLogFiles } from './runtime-log-grep.service.js'
import { requireAdmin } from '../auth/auth.middleware.js'

export const runtimeLogsRouter = Router()
const runtimeLogRouteTimeoutMs = 120_000

runtimeLogsRouter.use((req, res, next) => {
  req.setTimeout(runtimeLogRouteTimeoutMs)
  res.setTimeout(runtimeLogRouteTimeoutMs)
  next()
})
runtimeLogsRouter.use(requireAdmin)

runtimeLogsRouter.get('/', async (req, res, next) => {
  try {
    const result = await requestDbService({
      type: 'list_runtime_logs',
      options: parseRuntimeLogListOptions(req.query)
    })
    res.json(ok(result))
  } catch (error) {
    next(error)
  }
})

runtimeLogsRouter.get('/facets', async (_req, res, next) => {
  try {
    res.json(ok(await requestDbService({ type: 'get_runtime_log_facets' })))
  } catch (error) {
    next(error)
  }
})

runtimeLogsRouter.get('/grep-options', async (_req, res, next) => {
  try {
    res.json(ok(await getRuntimeLogGrepRuntime()))
  } catch (error) {
    next(error)
  }
})

runtimeLogsRouter.get('/grep', async (req, res, next) => {
  try {
    const result = await grepRuntimeLogFiles(parseRuntimeLogGrepOptions(req.query))
    res.json(ok(result))
  } catch (error) {
    next(error)
  }
})

runtimeLogsRouter.get('/grep-detail', async (req, res, next) => {
  try {
    const id = optionalQueryText(req.query.id)
    const fileName = optionalQueryText(req.query.fileName)
    const lineNumber = finiteNumberQueryValue(req.query.lineNumber)
    if (!id || !fileName || !Number.isInteger(lineNumber) || Number(lineNumber) < 1) {
      res.status(400).json({ message: 'grep 详情定位参数无效' })
      return
    }
    const result = await getRuntimeLogGrepDetail({ id, fileName, lineNumber: Number(lineNumber) })
    if (result.status === 'not_found') {
      res.status(404).json({ message: 'grep 匹配行不存在' })
      return
    }
    if (result.status === 'stale') {
      res.status(409).json({ message: '日志文件已经轮转或内容发生变化，请重新搜索' })
      return
    }
    res.json(ok(result.detail))
  } catch (error) {
    next(error)
  }
})

runtimeLogsRouter.get('/:id', async (req, res, next) => {
  try {
    const detail = await requestDbService({
      type: 'get_runtime_log_detail_delta',
      id: req.params.id
    })
    if (!detail) {
      res.status(404).json({ message: '运行日志不存在' })
      return
    }
    res.json(ok(detail))
  } catch (error) {
    next(error)
  }
})

const runtimeLogLevels = new Set<RuntimeLogLevel | 'all'>([
  'all',
  'trace',
  'debug',
  'info',
  'warn',
  'error',
  'fatal'
])

function parseRuntimeLogListOptions(query: Record<string, unknown>): RuntimeLogListOptions {
  const rawPage = finiteNumberQueryValue(query.page)
  const rawPageSize = finiteNumberQueryValue(query.pageSize)
  const rawLevel = optionalQueryText(query.level)?.toLowerCase()
  const timeRange = strictDateTimeRangeQueryValue(query.startAt, query.endAt)
  return {
    page: Number.isInteger(rawPage) ? rawPage : undefined,
    pageSize: Number.isInteger(rawPageSize) ? rawPageSize : undefined,
    traceId: optionalQueryText(query.traceId),
    level: rawLevel && runtimeLogLevels.has(rawLevel as RuntimeLogLevel | 'all')
      ? rawLevel as RuntimeLogLevel | 'all'
      : undefined,
    event: optionalQueryText(query.event),
    keyword: optionalQueryText(query.keyword),
    startAt: timeRange.startAt,
    endAt: timeRange.endAt
  }
}

function parseRuntimeLogGrepOptions(query: Record<string, unknown>): { keywords: string[]; limit?: number; startAt?: string; endAt?: string } {
  const timeRange = strictDateTimeRangeQueryValue(query.startAt, query.endAt)
  return {
    keywords: stringArrayQueryValues(query.keywords),
    limit: finiteNumberQueryValue(query.limit),
    startAt: timeRange.startAt,
    endAt: timeRange.endAt
  }
}

function stringArrayQueryValues(value: unknown): string[] {
  if (Array.isArray(value)) {
    return value.filter((item): item is string => typeof item === 'string')
  }
  return typeof value === 'string' ? [value] : []
}

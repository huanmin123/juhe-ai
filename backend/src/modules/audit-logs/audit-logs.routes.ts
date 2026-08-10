import { Router } from 'express'

import { ok, sendNotFound } from '../../shared/http.js'
import { finiteNumberQueryValue, optionalQueryText } from '../../shared/query-values.js'
import type {
  AuditErrorGroupListOptions,
  AuditLogListOptions,
  AuditOutcome,
  PersistedAuditTrafficSource
} from '../../storage/audit-log-types.js'
import { runtimeConfig } from '../../config/runtime.js'
import {
  createAuditLogF3QueryRepository,
  type AuditLogF3QueryRepository
} from '../../storage/audit-log-f3-query.repository.js'
import { requireAdmin } from '../auth/auth.middleware.js'

export const auditLogsRouter = Router()
const auditLogRouteTimeoutMs = 120_000
let f3RepositoryPromise: Promise<AuditLogF3QueryRepository> | undefined

async function getF3Repository(): Promise<AuditLogF3QueryRepository> {
  if (!f3RepositoryPromise) {
    const isPostgres = runtimeConfig.databaseDriver === 'postgres'
    f3RepositoryPromise = createAuditLogF3QueryRepository({
      ...(isPostgres ? { postgresUrl: runtimeConfig.postgres.url } : { sqlitePath: runtimeConfig.auditLogF3.sqlitePath }),
      postgresSchema: runtimeConfig.auditLogF3.postgresSchema,
      postgresPoolMax: runtimeConfig.auditLogF3.postgresPoolMax,
      payloadBlobDirectory: runtimeConfig.auditLogF3.payloadBlobDirectory,
      hotSearchDirectory: runtimeConfig.auditLogF3.hotSearchDirectory
    })
  }
  return f3RepositoryPromise
}

export async function closeAuditLogF3QueryRepository(): Promise<void> {
  const repositoryPromise = f3RepositoryPromise
  f3RepositoryPromise = undefined
  if (repositoryPromise) await (await repositoryPromise).close()
}

auditLogsRouter.use((req, res, next) => {
  req.setTimeout(auditLogRouteTimeoutMs)
  res.setTimeout(auditLogRouteTimeoutMs)
  next()
})
auditLogsRouter.use(requireAdmin)

auditLogsRouter.get('/', async (req, res, next) => {
  try {
    res.json(ok(await (await getF3Repository()).listAuditLogs(parseAuditLogListOptions(req.query))))
  } catch (error) {
    if (isInvalidAuditTrafficSourceQueryError(error)) {
      res.status(400).json({ message: error.message })
      return
    }
    next(error)
  }
})

auditLogsRouter.get('/search-hot', async (req, res, next) => {
  try {
    const grepResult = await (await getF3Repository()).searchHot(parseAuditHotSearchOptions(req.query))
    const items = await (await getF3Repository()).listAuditLogsByIds(grepResult.auditLogIds)
    res.json(ok({
      items,
      total: grepResult.truncated ? Math.max(items.length + 1, grepResult.auditLogIds.length) : items.length,
      hasMore: grepResult.truncated,
      page: 1,
      pageSize: grepResult.limit,
      available: grepResult.available,
      elapsedMs: grepResult.elapsedMs,
      keywords: grepResult.keywords,
      startAt: grepResult.startAt,
      endAt: grepResult.endAt,
      limit: grepResult.limit,
      truncated: grepResult.truncated,
      scannedFileCount: grepResult.scannedFileCount,
      message: grepResult.message
    }))
  } catch (error) {
    next(error)
  }
})

auditLogsRouter.get('/runtime', async (_req, res, next) => {
  try {
    const runtime = (await getF3Repository()).getRuntime()
    res.json(ok({
      ...runtime,
      available: true
    }))
  } catch (error) {
    next(error)
  }
})

auditLogsRouter.get('/error-groups', async (req, res, next) => {
  try {
    res.json(ok(await (await getF3Repository()).listAuditErrorGroups(parseAuditErrorGroupListOptions(req.query))))
  } catch (error) {
    next(error)
  }
})

auditLogsRouter.get('/error-groups/:id/events', async (req, res, next) => {
  try {
    res.json(ok(await (await getF3Repository()).listAuditErrorGroupEvents(req.params.id, parseAuditLogListOptions(req.query))))
  } catch (error) {
    if (isInvalidAuditTrafficSourceQueryError(error)) {
      res.status(400).json({ message: error.message })
      return
    }
    next(error)
  }
})

auditLogsRouter.get('/:id', async (req, res, next) => {
  try {
    const detail = await (await getF3Repository()).getAuditLogDetail(req.params.id)
    if (!detail) {
      sendNotFound(res, '审计日志不存在')
      return
    }
    res.json(ok(detail))
  } catch (error) {
    next(error)
  }
})

auditLogsRouter.get('/:id/payloads/:payloadId', async (req, res, next) => {
  try {
    const payload = await (await getF3Repository()).getAuditLogPayload(req.params.id, req.params.payloadId, {
      full: true
    })
    if (!payload) {
      sendNotFound(res, '审计原文不存在')
      return
    }
    res.json(ok(payload))
  } catch (error) {
    next(error)
  }
})

const auditOutcomes = new Set<AuditOutcome | 'all'>([
  'all',
  'success',
  'success_after_retry',
  'gateway_succeeded',
  'gateway_failed',
  'upstream_failed',
  'stream_failed',
  'downstream_closed'
])
const auditTrafficSources = new Set<PersistedAuditTrafficSource>([
  'gateway',
  'manual_account_test',
  'hybrid_scoring',
  'hybrid_quality_scoring'
])

function parseAuditLogListOptions(query: Record<string, unknown>): AuditLogListOptions {
  const rawPage = finiteNumberQueryValue(query.page)
  const rawPageSize = finiteNumberQueryValue(query.pageSize)
  const rawStatusCode = finiteNumberQueryValue(query.statusCode)
  return {
    page: Number.isInteger(rawPage) ? rawPage : undefined,
    pageSize: Number.isInteger(rawPageSize) ? rawPageSize : undefined,
    traceId: optionalQueryText(query.traceId),
    sessionId: optionalQueryText(query.sessionId),
    sessionClientType: optionalQueryText(query.sessionClientType),
    errorGroupId: optionalQueryText(query.errorGroupId),
    outcome: typeof query.outcome === 'string' && auditOutcomes.has(query.outcome as AuditOutcome | 'all')
      ? query.outcome as AuditOutcome | 'all'
      : undefined,
    statusCode: isHttpStatusCode(rawStatusCode) ? rawStatusCode : undefined,
    path: optionalQueryText(query.path),
    model: optionalQueryText(query.model),
    systemAccountId: optionalQueryText(query.systemAccountId),
    apiKeyId: optionalQueryText(query.apiKeyId),
    groupId: optionalQueryText(query.groupId),
    accountId: optionalQueryText(query.accountId),
    clientIp: optionalQueryText(query.clientIp),
    startAt: optionalQueryText(query.startAt),
    endAt: optionalQueryText(query.endAt),
    trafficSource: auditTrafficSourceQueryValue(query.trafficSource)
  }
}

function parseAuditErrorGroupListOptions(query: Record<string, unknown>): AuditErrorGroupListOptions {
  const rawPage = finiteNumberQueryValue(query.page)
  const rawPageSize = finiteNumberQueryValue(query.pageSize)
  const rawStatusCode = finiteNumberQueryValue(query.statusCode)
  return {
    page: Number.isInteger(rawPage) ? rawPage : undefined,
    pageSize: Number.isInteger(rawPageSize) ? rawPageSize : undefined,
    path: optionalQueryText(query.path),
    model: optionalQueryText(query.model),
    statusCode: isHttpStatusCode(rawStatusCode) ? rawStatusCode : undefined,
    systemAccountId: optionalQueryText(query.systemAccountId),
    apiKeyId: optionalQueryText(query.apiKeyId),
    groupId: optionalQueryText(query.groupId),
    accountId: optionalQueryText(query.accountId)
  }
}

function parseAuditHotSearchOptions(query: Record<string, unknown>): { keywords: string[]; limit?: number; startAt?: string; endAt?: string } {
  return {
    keywords: stringArrayQueryValues(query.keywords),
    limit: finiteNumberQueryValue(query.limit),
    startAt: optionalQueryText(query.startAt),
    endAt: optionalQueryText(query.endAt)
  }
}

function stringArrayQueryValues(value: unknown): string[] {
  if (Array.isArray(value)) {
    return value.filter((item): item is string => typeof item === 'string')
  }
  return typeof value === 'string' ? [value] : []
}

function isHttpStatusCode(value: unknown): value is number {
  return Number.isInteger(value) && Number(value) >= 100 && Number(value) <= 599
}

function auditTrafficSourceQueryValue(value: unknown): PersistedAuditTrafficSource | undefined {
  if (value === undefined) return undefined
  if (typeof value === 'string' && auditTrafficSources.has(value as PersistedAuditTrafficSource)) {
    return value as PersistedAuditTrafficSource
  }
  const error = new Error('审计日志来源筛选无效，仅支持网关请求、AI 账户测试、混合路由选型或回答质量复核') as Error & { statusCode: number }
  error.statusCode = 400
  throw error
}

function isInvalidAuditTrafficSourceQueryError(error: unknown): error is Error & { statusCode: 400 } {
  return error instanceof Error
    && (error as Error & { statusCode?: unknown }).statusCode === 400
    && error.message === '审计日志来源筛选无效，仅支持网关请求、AI 账户测试、混合路由选型或回答质量复核'
}

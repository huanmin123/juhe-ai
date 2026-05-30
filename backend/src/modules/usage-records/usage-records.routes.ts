import { Router } from 'express'

import { ok, sendNotFound } from '../../shared/http.js'
import { finiteNumberQueryValue, optionalQueryText } from '../../shared/query-values.js'
import { getUsageRecordDetail, listUsageRecords, type UsageRecordListOptions, type UsageRecordSortField, type UsageRecordSummary, type UsageRecordTrafficSource } from '../../storage/repositories.js'
import { dateKey, startOfZonedDateKeyIso, usageStatsTimezone } from '../../storage/usage-stats-helpers.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { buildProviderCostBreakdown } from '../model-pricing/model-pricing.service.js'

export const usageRecordsRouter = Router()

usageRecordsRouter.get('/', (req, res) => {
  const result = listUsageRecords(getRequestAccessScope(req.query.systemAccountId), parseListOptions(req.query))
  res.json(ok({
    ...result,
    items: result.items.map(withCostBreakdown)
  }))
})

usageRecordsRouter.get('/:id', (req, res) => {
  const record = getUsageRecordDetail(req.params.id, getRequestAccessScope(req.query.systemAccountId))
  if (!record) {
    sendNotFound(res, '使用记录不存在')
    return
  }
  res.json(ok(withCostBreakdown(record)))
})

const usageRecordSortFields = new Set<UsageRecordSortField>(['createdAt', 'firstTokenMs', 'durationMs', 'costUsd'])
const usageRecordTrafficSources = new Set<UsageRecordTrafficSource>(['gateway', 'manual_account_test', 'cooldown_retest'])
const usageRecordDefaultLookbackDays = 31
const dayMs = 24 * 60 * 60 * 1000

function withCostBreakdown(record: UsageRecordSummary) {
  return {
    ...record,
    costBreakdown: buildProviderCostBreakdown({
      providerCode: record.providerCode ?? 'openai',
      model: record.model,
      inputTokens: record.inputTokens,
      outputTokens: record.outputTokens,
      cacheReadTokens: record.cacheReadTokens,
      inputImageTokens: record.inputImageTokens,
      outputImageTokens: record.outputImageTokens,
      costUsd: record.costUsd
    })
  }
}

function parseListOptions(query: Record<string, unknown>): UsageRecordListOptions {
  const rawPage = finiteNumberQueryValue(query.page)
  const rawPageSize = finiteNumberQueryValue(query.pageSize)
  const rawLimit = finiteNumberQueryValue(query.limit)
  const rawStatusCode = finiteNumberQueryValue(query.statusCode)
  const createdAtRange = dateRangeQueryValue(query.startDate, query.endDate)
  const sortBy = typeof query.sortBy === 'string' && usageRecordSortFields.has(query.sortBy as UsageRecordSortField)
    ? query.sortBy as UsageRecordSortField
    : undefined
  const sortOrder = query.sortOrder === 'asc' ? 'asc' : query.sortOrder === 'desc' ? 'desc' : undefined
  const result = query.result === 'success' || query.result === 'failed' || query.result === 'all'
    ? query.result
    : undefined
  return {
    page: Number.isInteger(rawPage) ? rawPage : undefined,
    pageSize: Number.isInteger(rawPageSize) ? rawPageSize : undefined,
    limit: Number.isInteger(rawLimit) ? rawLimit : undefined,
    sortBy,
    sortOrder,
    accountKeyword: optionalQueryText(query.accountKeyword ?? query.keyword ?? query.accountName),
    clientIp: optionalQueryText(query.clientIp),
    result,
    statusCode: isHttpStatusCode(rawStatusCode) ? rawStatusCode : undefined,
    groupId: optionalQueryText(query.groupId),
    model: optionalQueryText(query.model),
    trafficSource: usageRecordTrafficSourceQueryValue(query.trafficSource),
    startAt: createdAtRange.startAt,
    endAt: createdAtRange.endAt
  }
}

function isHttpStatusCode(value: unknown): value is number {
  return Number.isInteger(value) && Number(value) >= 100 && Number(value) <= 599
}

function usageRecordTrafficSourceQueryValue(value: unknown): UsageRecordTrafficSource | undefined {
  return typeof value === 'string' && usageRecordTrafficSources.has(value as UsageRecordTrafficSource)
    ? value as UsageRecordTrafficSource
    : undefined
}

function dateRangeQueryValue(startValue: unknown, endValue: unknown): { startAt?: string; endAt?: string } {
  const startDate = dateQueryValue(startValue)
  const endDate = dateQueryValue(endValue)
  if (!startDate && !endDate) {
    return optionalQueryText(startValue) || optionalQueryText(endValue)
      ? {}
      : defaultUsageRecordDateRange()
  }
  const start = startDate ?? endDate
  const end = endDate ?? startDate
  if (!start || !end) {
    return {}
  }
  const rangeStart = start <= end ? start : end
  const rangeEnd = start <= end ? end : start
  return {
    startAt: startOfDateKeyIso(rangeStart),
    endAt: startOfDateKeyIso(nextDateKey(rangeEnd))
  }
}

function defaultUsageRecordDateRange(): { startAt?: string; endAt?: string } {
  const timezone = usageStatsTimezone()
  const today = new Date()
  const startDate = dateKey(new Date(today.getTime() - (usageRecordDefaultLookbackDays - 1) * dayMs), timezone)
  const endDate = dateKey(today, timezone)
  return {
    startAt: startOfDateKeyIso(startDate),
    endAt: startOfDateKeyIso(nextDateKey(endDate))
  }
}

function dateQueryValue(value: unknown): string | undefined {
  const text = optionalQueryText(value)
  if (!text || !/^\d{4}-\d{2}-\d{2}$/.test(text)) {
    return undefined
  }
  const [year, month, day] = text.split('-').map((part) => Number(part))
  const date = new Date(year, month - 1, day)
  return date.getFullYear() === year && date.getMonth() === month - 1 && date.getDate() === day ? text : undefined
}

function startOfDateKeyIso(dateKey: string): string | undefined {
  return startOfZonedDateKeyIso(dateKey, usageStatsTimezone())
}

function nextDateKey(dateKey: string): string {
  const [year, month, day] = dateKey.split('-').map((part) => Number(part))
  const date = new Date(year, month - 1, day)
  date.setDate(date.getDate() + 1)
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}

import { Router } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { ok, sendNotFound } from '../../shared/http.js'
import { finiteNumberQueryValue, optionalQueryText } from '../../shared/query-values.js'
import { getUsageRecordDetailAsync, listUsageRecordsAsync, type UsageRecordListOptions, type UsageRecordSortField, type UsageRecordSummary, type UsageRecordTrafficSource } from '../../storage/repositories.js'
import { dateKey, startOfZonedDateKeyIso, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { buildCatalogCostBreakdown, buildCatalogCostBreakdownAsync } from '../model-pricing/model-catalog.service.js'
import type { ProviderCostBreakdown } from '../model-pricing/model-pricing.service.js'

export const usageRecordsRouter = Router()

usageRecordsRouter.get('/', async (req, res, next) => {
  try {
    const result = await listUsageRecordsAsync(getRequestAccessScope(req.query.systemAccountId), await parseListOptionsAsync(req.query))
    res.json(ok({
      ...result,
      items: await Promise.all(result.items.map(withCostBreakdownAsync))
    }))
  } catch (error) {
    next(error)
  }
})

usageRecordsRouter.get('/:id', async (req, res, next) => {
  try {
    const record = await getUsageRecordDetailAsync(req.params.id, getRequestAccessScope(req.query.systemAccountId))
    if (!record) {
      sendNotFound(res, '使用记录不存在')
      return
    }
    res.json(ok(await withCostBreakdownAsync(record)))
  } catch (error) {
    next(error)
  }
})

const usageRecordSortFields = new Set<UsageRecordSortField>(['createdAt'])
const usageRecordTrafficSources = new Set<UsageRecordTrafficSource>(['gateway', 'manual_account_test', 'runtime_recovery_probe', 'cooldown_retest', 'hybrid_scoring', 'hybrid_quality_scoring'])

export type UsageRecordResponse = UsageRecordSummary & {
  costBreakdown?: ProviderCostBreakdown
}

export function withCostBreakdown(record: UsageRecordSummary): UsageRecordResponse {
  const costBreakdown = usageRecordCostBreakdown(record)
  return {
    ...record,
    costBreakdown
  }
}

export async function withCostBreakdownAsync(record: UsageRecordSummary): Promise<UsageRecordResponse> {
  const costBreakdown = await usageRecordCostBreakdownAsync(record)
  return {
    ...record,
    costBreakdown
  }
}

function usageRecordCostBreakdown(record: UsageRecordSummary): ProviderCostBreakdown | undefined {
  if (!record.success || runtimeConfig.databaseDriver === 'postgres' || runtimeConfig.cacheDriver === 'redis') return undefined
  return usageRecordCatalogCostBreakdown(record) ?? fallbackUsageRecordCostBreakdown(record)
}

async function usageRecordCostBreakdownAsync(record: UsageRecordSummary): Promise<ProviderCostBreakdown | undefined> {
  if (!record.success) return undefined
  return await usageRecordCatalogCostBreakdownAsync(record) ?? fallbackUsageRecordCostBreakdown(record)
}

function usageRecordCatalogCostBreakdown(record: UsageRecordSummary): ProviderCostBreakdown | undefined {
  if (!record.providerCode) return undefined
  for (const model of usageRecordPricingCandidateModels(record)) {
    const breakdown = buildCatalogCostBreakdown({
      providerCode: record.providerCode,
      systemAccountId: record.systemAccountId,
      model,
      inputTokens: record.inputTokens,
      outputTokens: record.outputTokens,
      cacheReadTokens: record.cacheReadTokens,
      cacheWriteTokens: record.cacheWriteTokens,
      cacheWrite1hTokens: record.cacheWrite1hTokens,
      thinkingTokens: record.thinkingTokens,
      inputImageTokens: record.inputImageTokens,
      outputImageTokens: record.outputImageTokens,
      inputAudioTokens: record.inputAudioTokens,
      outputAudioTokens: record.outputAudioTokens,
      outputImageCount: record.outputImageCount,
      costUsd: record.costUsd
    })
    if (breakdown) return breakdown
  }
  return undefined
}

async function usageRecordCatalogCostBreakdownAsync(record: UsageRecordSummary): Promise<ProviderCostBreakdown | undefined> {
  if (!record.providerCode) return undefined
  for (const model of usageRecordPricingCandidateModels(record)) {
    const breakdown = await buildCatalogCostBreakdownAsync({
      providerCode: record.providerCode,
      systemAccountId: record.systemAccountId,
      model,
      inputTokens: record.inputTokens,
      outputTokens: record.outputTokens,
      cacheReadTokens: record.cacheReadTokens,
      cacheWriteTokens: record.cacheWriteTokens,
      cacheWrite1hTokens: record.cacheWrite1hTokens,
      thinkingTokens: record.thinkingTokens,
      inputImageTokens: record.inputImageTokens,
      outputImageTokens: record.outputImageTokens,
      inputAudioTokens: record.inputAudioTokens,
      outputAudioTokens: record.outputAudioTokens,
      outputImageCount: record.outputImageCount,
      costUsd: record.costUsd
    })
    if (breakdown) return breakdown
  }
  return undefined
}

function usageRecordPricingCandidateModels(record: UsageRecordSummary): string[] {
  const models = [
    record.pricingModel,
    record.upstreamModel,
    record.model
  ]
  const seen = new Set<string>()
  return models.flatMap((model) => {
    const normalized = model?.trim()
    if (!normalized || seen.has(normalized)) return []
    seen.add(normalized)
    return [normalized]
  })
}

function fallbackUsageRecordCostBreakdown(record: UsageRecordSummary): ProviderCostBreakdown {
  return {
    cacheReadCostUsd: record.cacheReadCostUsd,
    cacheWriteCostUsd: record.cacheWriteCostUsd,
    thinkingTokens: record.thinkingTokens,
    accountChargeUsd: record.costUsd,
    multiplier: 1
  }
}

async function parseListOptionsAsync(query: Record<string, unknown>): Promise<UsageRecordListOptions> {
  const timezone = await usageStatsTimezoneAsync()
  const rawPage = finiteNumberQueryValue(query.page)
  const rawPageSize = finiteNumberQueryValue(query.pageSize)
  const rawStatusCode = finiteNumberQueryValue(query.statusCode)
  const traceId = optionalQueryText(query.traceId)
  const createdAtRange = dateRangeQueryValue(query.startDate, query.endDate, timezone)
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
    sortBy,
    sortOrder,
    traceId,
    accountKeyword: optionalQueryText(query.accountKeyword),
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

function dateRangeQueryValue(startValue: unknown, endValue: unknown, timezone: string, skipDefaultRange = false): { startAt?: string; endAt?: string } {
  const startDate = dateQueryValue(startValue)
  const endDate = dateQueryValue(endValue)
  if (!startDate && !endDate) {
    return skipDefaultRange || optionalQueryText(startValue) || optionalQueryText(endValue)
      ? {}
      : defaultUsageRecordDateRange(timezone)
  }
  const start = startDate ?? endDate
  const end = endDate ?? startDate
  if (!start || !end) {
    return {}
  }
  const rangeStart = start <= end ? start : end
  const rangeEnd = start <= end ? end : start
  return {
    startAt: startOfDateKeyIso(rangeStart, timezone),
    endAt: startOfDateKeyIso(nextDateKey(rangeEnd), timezone)
  }
}

function defaultUsageRecordDateRange(timezone: string): { startAt?: string; endAt?: string } {
  const today = new Date()
  const todayKey = dateKey(today, timezone)
  return {
    startAt: startOfDateKeyIso(todayKey, timezone),
    endAt: startOfDateKeyIso(nextDateKey(todayKey), timezone)
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

function startOfDateKeyIso(dateKey: string, timezone: string): string | undefined {
  return startOfZonedDateKeyIso(dateKey, timezone)
}

function nextDateKey(dateKey: string): string {
  const [year, month, day] = dateKey.split('-').map((part) => Number(part))
  const date = new Date(year, month - 1, day)
  date.setDate(date.getDate() + 1)
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}

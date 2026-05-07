import { Router } from 'express'

import { ok } from '../../shared/http.js'
import { listUsageRecords, type UsageRecordListOptions, type UsageRecordSortField } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { buildProviderCostBreakdown } from '../model-pricing/model-pricing.service.js'

export const usageRecordsRouter = Router()

usageRecordsRouter.get('/', (req, res) => {
  const result = listUsageRecords(getRequestAccessScope(req.query.systemAccountId), parseListOptions(req.query))
  res.json(ok({
    ...result,
    items: result.items.map((record) => ({
      ...record,
      costBreakdown: buildProviderCostBreakdown({
        providerCode: record.providerCode ?? 'openai',
        model: record.model,
        inputTokens: record.inputTokens,
        outputTokens: record.outputTokens,
        cacheReadTokens: record.cacheReadTokens,
        costUsd: record.costUsd
      })
    }))
  }))
})

const usageRecordSortFields = new Set<UsageRecordSortField>(['createdAt', 'firstTokenMs', 'durationMs', 'costUsd'])

function parseListOptions(query: Record<string, unknown>): UsageRecordListOptions {
  const rawPage = numberQueryValue(query.page)
  const rawPageSize = numberQueryValue(query.pageSize)
  const rawLimit = numberQueryValue(query.limit)
  const rawStatusCode = numberQueryValue(query.statusCode)
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
    result,
    statusCode: isHttpStatusCode(rawStatusCode) ? rawStatusCode : undefined,
    model: optionalQueryText(query.model)
  }
}

function numberQueryValue(value: unknown): number | undefined {
  const text = Array.isArray(value) ? value[0] : value
  const number = typeof text === 'string' ? Number(text) : undefined
  return typeof number === 'number' && Number.isFinite(number) ? number : undefined
}

function isHttpStatusCode(value: unknown): value is number {
  return Number.isInteger(value) && Number(value) >= 100 && Number(value) <= 599
}

function optionalQueryText(value: unknown): string | undefined {
  const text = Array.isArray(value) ? value[0] : value
  return typeof text === 'string' && text.trim() ? text.trim() : undefined
}

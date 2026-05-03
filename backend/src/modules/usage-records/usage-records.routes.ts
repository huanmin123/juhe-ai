import { Router } from 'express'

import { ok } from '../../shared/http.js'
import { listUsageRecords, type UsageRecordListOptions, type UsageRecordSortField } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { buildProviderCostBreakdown } from '../model-pricing/model-pricing.service.js'

export const usageRecordsRouter = Router()

usageRecordsRouter.get('/', (req, res) => {
  res.json(ok(listUsageRecords(getRequestAccessScope(req.query.systemAccountId), parseListOptions(req.query)).map((record) => ({
    ...record,
    costBreakdown: buildProviderCostBreakdown({
      providerCode: record.providerCode ?? 'openai',
      model: record.model,
      inputTokens: record.inputTokens,
      outputTokens: record.outputTokens,
      cacheReadTokens: record.cacheReadTokens,
      costUsd: record.costUsd
    })
  }))))
})

const usageRecordSortFields = new Set<UsageRecordSortField>(['createdAt', 'firstTokenMs', 'durationMs', 'costUsd'])

function parseListOptions(query: Record<string, unknown>): UsageRecordListOptions {
  const sortBy = typeof query.sortBy === 'string' && usageRecordSortFields.has(query.sortBy as UsageRecordSortField)
    ? query.sortBy as UsageRecordSortField
    : undefined
  const sortOrder = query.sortOrder === 'asc' ? 'asc' : query.sortOrder === 'desc' ? 'desc' : undefined
  const rawLimit = typeof query.limit === 'string' ? Number(query.limit) : undefined
  const limit = Number.isInteger(rawLimit) ? rawLimit : undefined
  return { sortBy, sortOrder, limit }
}

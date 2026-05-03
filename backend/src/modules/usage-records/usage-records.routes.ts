import { Router } from 'express'

import { ok } from '../../shared/http.js'
import { listUsageRecords } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { buildProviderCostBreakdown } from '../model-pricing/model-pricing.service.js'

export const usageRecordsRouter = Router()

usageRecordsRouter.get('/', (req, res) => {
  res.json(ok(listUsageRecords(getRequestAccessScope(req.query.systemAccountId)).map((record) => ({
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

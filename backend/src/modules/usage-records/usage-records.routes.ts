import { Router } from 'express'

import { ok } from '../../shared/http.js'
import { listUsageRecords } from '../../storage/repositories.js'

export const usageRecordsRouter = Router()

usageRecordsRouter.get('/', (_req, res) => {
  res.json(ok(listUsageRecords()))
})

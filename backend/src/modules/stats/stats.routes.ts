import { Router } from 'express'

import { ok } from '../../shared/http.js'
import { getSystemMetricsOverview, getUsageStatsOverview } from '../../storage/repositories.js'
import { requireAdmin } from '../auth/auth.middleware.js'

export const statsRouter = Router()

statsRouter.get('/usage-overview', (_req, res) => {
  res.json(ok(getUsageStatsOverview()))
})

statsRouter.get('/system-metrics', requireAdmin, (_req, res) => {
  res.json(ok(getSystemMetricsOverview()))
})
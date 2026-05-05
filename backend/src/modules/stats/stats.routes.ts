import { Router } from 'express'
import { z } from 'zod'

import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import {
  getAccountAuthorizationUsageOverview,
  getAccountUsageStatsOverview
} from '../../storage/repositories.js'
import {
  getSystemMetricsOverview,
  getUsageStatsOverview
} from '../../storage/usage-stats.repository.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { getRequestAccessScope } from '../auth/request-context.js'

export const statsRouter = Router()

const accountIdParamsSchema = z.object({
  id: z.string().trim().min(1, '账户 ID 不能为空')
})

statsRouter.get('/usage-overview', (req, res) => {
  res.json(ok(getUsageStatsOverview(getRequestAccessScope(req.query.systemAccountId))))
})

statsRouter.get('/account-usage', (req, res) => {
  res.json(ok(getAccountUsageStatsOverview(getRequestAccessScope(req.query.systemAccountId))))
})

statsRouter.get('/accounts/:id/authorization-usage', (req, res) => {
  const parsed = accountIdParamsSchema.safeParse(req.params)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '账户 ID 不合法')))
    return
  }
  const overview = getAccountAuthorizationUsageOverview(parsed.data.id, getRequestAccessScope(req.query.systemAccountId))
  if (!overview) {
    res.status(404).json({ message: '账户不存在或没有权限查看授权用量' })
    return
  }
  res.json(ok(overview))
})

statsRouter.get('/system-metrics', requireAdmin, (_req, res) => {
  res.json(ok(getSystemMetricsOverview()))
})

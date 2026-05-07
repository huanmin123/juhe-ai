import { Router } from 'express'
import { z } from 'zod'

import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import {
  getAccountAuthorizationUsageOverview,
  getAccountUsageStatsOverviewPage,
  type AccountListOptions,
  type AccountListSchedulableFilter
} from '../../storage/repositories.js'
import {
  getSystemMetricsOverview,
  getUsageStatsOverview,
  type UsageOverviewWindowKey
} from '../../storage/usage-stats.repository.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { getRequestAccessScope } from '../auth/request-context.js'

export const statsRouter = Router()

const accountIdParamsSchema = z.object({
  id: z.string().trim().min(1, '账户 ID 不能为空')
})

const usageOverviewQuerySchema = z.object({
  window: z.enum(['last1d', 'last3d', 'last7d', 'last30d']).default('last1d')
})

statsRouter.get('/usage-overview', (req, res) => {
  const parsed = usageOverviewQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '统计窗口不合法')))
    return
  }
  res.json(ok(getUsageStatsOverview(getRequestAccessScope(req.query.systemAccountId), parsed.data.window as UsageOverviewWindowKey)))
})

statsRouter.get('/account-usage', (req, res) => {
  res.json(ok(getAccountUsageStatsOverviewPage(getRequestAccessScope(req.query.systemAccountId), parseAccountUsageOptions(req.query))))
})

function parseAccountUsageOptions(query: Record<string, unknown>): AccountListOptions {
  return {
    page: integerQueryValue(query.page),
    pageSize: integerQueryValue(query.pageSize),
    limit: integerQueryValue(query.limit),
    keyword: optionalQueryText(query.keyword),
    type: optionalQueryText(query.type),
    schedulable: schedulableQueryValue(query.schedulable)
  }
}

function integerQueryValue(value: unknown): number | undefined {
  const text = Array.isArray(value) ? value[0] : value
  const number = typeof text === 'string' ? Number(text) : typeof text === 'number' ? text : undefined
  return Number.isInteger(number) ? number : undefined
}

function optionalQueryText(value: unknown): string | undefined {
  const text = Array.isArray(value) ? value[0] : value
  return typeof text === 'string' && text.trim() ? text.trim() : undefined
}

function schedulableQueryValue(value: unknown): AccountListSchedulableFilter | undefined {
  const text = optionalQueryText(value)
  return text === 'all' || text === 'enabled' || text === 'disabled' || text === 'cooling' ? text : undefined
}

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

statsRouter.get('/system-metrics', requireAdmin, (req, res) => {
  const parsed = usageOverviewQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '监控窗口不合法')))
    return
  }
  res.json(ok(getSystemMetricsOverview(parsed.data.window as UsageOverviewWindowKey)))
})

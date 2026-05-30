import { Router } from 'express'
import { z } from 'zod'

import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import {
  externalIntegrationIpUsageReadScope,
  externalIntegrationSourceAuthDemoScope
} from '../../storage/external-integration-source.repository.js'
import { getExternalIntegrationSourceContext, requireExternalIntegrationSource } from './external-source-auth.middleware.js'
import {
  getPublicAccessInfo,
  getPublicClientIpUsage,
  getPublicConsumptionRanking
} from './external-public-welfare.service.js'

export const externalIntegrationsRouter = Router()

const rangePresetSchema = z.enum(['today', 'last7d', 'last31d'])
const dateKeySchema = z.string().trim().regex(/^\d{4}-\d{2}-\d{2}$/, '日期格式应为 YYYY-MM-DD')
const ipUsageQuerySchema = z.object({
  range: rangePresetSchema.optional(),
  startDate: dateKeySchema.optional(),
  endDate: dateKeySchema.optional(),
  page: z.coerce.number().int().min(1).optional(),
  pageSize: z.coerce.number().int().min(1).max(100).optional(),
  limit: z.coerce.number().int().min(1).max(100).optional(),
  keyword: z.string().trim().optional(),
  sortField: z.enum(['requestCount', 'successCount', 'errorCount', 'errorRate', 'totalTokens', 'totalCost', 'activeDays', 'lastUsedAt']).optional(),
  sortOrder: z.enum(['asc', 'desc']).optional()
})
const consumptionRankingQuerySchema = z.object({
  range: rangePresetSchema.optional(),
  startDate: dateKeySchema.optional(),
  endDate: dateKeySchema.optional(),
  limit: z.coerce.number().int().min(1).max(100).optional(),
  metric: z.enum(['totalTokens', 'totalCost', 'requestCount']).optional()
})

externalIntegrationsRouter.get(
  '/demo/source-auth',
  requireExternalIntegrationSource(externalIntegrationSourceAuthDemoScope),
  (_req, res) => {
    const context = getExternalIntegrationSourceContext(res)
    res.json(ok({
      ok: true,
      sourceName: context.sourceName,
      tokenName: context.tokenName,
      tokenPrefix: context.tokenPrefix,
      scopes: context.scopes,
      authenticatedAt: context.authenticatedAt,
      mock: context.isTestToken
    }))
  }
)

externalIntegrationsRouter.get(
  '/demo/mock-ranking',
  requireExternalIntegrationSource(externalIntegrationSourceAuthDemoScope),
  (req, res) => {
    const context = getExternalIntegrationSourceContext(res)
    const limit = normalizeLimit(req.query.limit)
    const range = normalizeRange(req.query.range)
    res.json(ok({
      mock: true,
      testToken: context.isTestToken,
      sourceName: context.sourceName,
      range,
      generatedAt: new Date().toISOString(),
      items: mockRankingItems.slice(0, limit)
    }))
  }
)

externalIntegrationsRouter.get(
  '/juhe-ai/ip-usage',
  requireExternalIntegrationSource(externalIntegrationIpUsageReadScope),
  (req, res) => {
    const parsed = ipUsageQuerySchema.safeParse(req.query)
    if (!parsed.success) {
      res.status(400).json(badRequest(firstIssueMessage(parsed.error, 'IP 聚合公开接口参数无效')))
      return
    }
    const context = getExternalIntegrationSourceContext(res)
    res.json(ok(getPublicClientIpUsage(parsed.data, { mock: context.isTestToken })))
  }
)

externalIntegrationsRouter.get(
  '/juhe-ai/consumption-ranking',
  requireExternalIntegrationSource(externalIntegrationIpUsageReadScope),
  (req, res) => {
    const parsed = consumptionRankingQuerySchema.safeParse(req.query)
    if (!parsed.success) {
      res.status(400).json(badRequest(firstIssueMessage(parsed.error, 'IP 消耗排行公开接口参数无效')))
      return
    }
    const context = getExternalIntegrationSourceContext(res)
    res.json(ok(getPublicConsumptionRanking(parsed.data, { mock: context.isTestToken })))
  }
)

externalIntegrationsRouter.get(
  '/juhe-ai/access-info',
  requireExternalIntegrationSource(externalIntegrationIpUsageReadScope),
  (_req, res) => {
    const context = getExternalIntegrationSourceContext(res)
    res.json(ok(getPublicAccessInfo({ mock: context.isTestToken })))
  }
)

const mockRankingItems = [
  {
    rank: 1,
    name: '公益体验入口',
    provider: 'OpenAI',
    requestCount: 1280,
    totalTokens: 842000,
    cachedTokens: 126000,
    totalCostUsd: 12.36,
    averageFirstTokenMs: 820,
    averageDurationMs: 3160
  },
  {
    rank: 2,
    name: '校园社群入口',
    provider: 'Azure OpenAI',
    requestCount: 936,
    totalTokens: 531400,
    cachedTokens: 68420,
    totalCostUsd: 8.42,
    averageFirstTokenMs: 940,
    averageDurationMs: 3440
  },
  {
    rank: 3,
    name: '志愿者测试入口',
    provider: 'OpenAI',
    requestCount: 648,
    totalTokens: 304800,
    cachedTokens: 38200,
    totalCostUsd: 5.18,
    averageFirstTokenMs: 760,
    averageDurationMs: 2810
  },
  {
    rank: 4,
    name: '夜间低峰入口',
    provider: 'OpenAI',
    requestCount: 415,
    totalTokens: 199600,
    cachedTokens: 22150,
    totalCostUsd: 3.64,
    averageFirstTokenMs: 1010,
    averageDurationMs: 3890
  },
  {
    rank: 5,
    name: '备用公益入口',
    provider: 'OpenAI',
    requestCount: 288,
    totalTokens: 126900,
    cachedTokens: 18300,
    totalCostUsd: 2.16,
    averageFirstTokenMs: 870,
    averageDurationMs: 3020
  }
]

function normalizeLimit(value: unknown): number {
  const raw = Array.isArray(value) ? value[0] : value
  const limit = Math.trunc(Number(raw ?? 5))
  if (!Number.isFinite(limit)) {
    return 5
  }
  return Math.max(1, Math.min(limit, 20))
}

function normalizeRange(value: unknown): string {
  const raw = Array.isArray(value) ? value[0] : value
  const range = typeof raw === 'string' ? raw.trim() : ''
  return range || 'last7d'
}

import { Router, type Request } from 'express'
import { z } from 'zod'

import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import {
  type ExternalIntegrationSourceAuthContext,
  externalIntegrationAccountPushScope,
  externalIntegrationIpUsageReadScope,
  externalIntegrationSourceAuthDemoScope
} from '../../storage/external-integration-source.repository.js'
import { createOperationLog } from '../../storage/repositories.js'
import { getExternalIntegrationSourceContext, requireExternalIntegrationSource } from './external-source-auth.middleware.js'
import {
  mockPublicWelfareAccountPush,
  pushPublicWelfareAccount,
  type PublicAccountPushResponse
} from './external-public-account-push.service.js'
import {
  getPublicAccessInfo,
  getPublicClientIpUsage,
  getPublicConsumptionRanking
} from './external-public-welfare.service.js'

export const externalIntegrationsRouter = Router()

const rangePresetSchema = z.enum(['today', 'last7d', 'last31d'])
const unsupportedDateRangeSchema = z.undefined({
  invalid_type_error: '公开 IP 聚合接口暂不支持自定义日期范围'
}).optional()
const ipUsageQuerySchema = z.object({
  range: rangePresetSchema.optional(),
  startDate: unsupportedDateRangeSchema,
  endDate: unsupportedDateRangeSchema,
  page: z.coerce.number().int().min(1).optional(),
  pageSize: z.coerce.number().int().min(1).max(100).optional(),
  limit: z.coerce.number().int().min(1).max(100).optional(),
  keyword: z.string().trim().optional(),
  sortField: z.enum(['requestCount', 'successCount', 'errorCount', 'errorRate', 'totalTokens', 'totalCost', 'activeDays', 'lastUsedAt']).optional(),
  sortOrder: z.enum(['asc', 'desc']).optional()
})
const consumptionRankingQuerySchema = z.object({
  range: rangePresetSchema.optional(),
  startDate: unsupportedDateRangeSchema,
  endDate: unsupportedDateRangeSchema,
  limit: z.coerce.number().int().min(1).max(100).optional(),
  metric: z.enum(['totalTokens', 'totalCost', 'requestCount']).optional()
})
const accountPushSchema = z.object({
  targetUsername: z.string().trim().min(2).max(80),
  targetDisplayName: z.string().trim().min(1).max(80).optional(),
  targetGroupName: z.string().trim().min(1).max(80),
  providerCode: z.string().trim().min(1).max(60).optional(),
  name: z.string().trim().min(1).max(120),
  type: z.string().trim().optional().refine((value) => value === undefined || value === 'api_key', '公益账号推送仅支持 API Key 账户'),
  baseUrl: z.string().trim().min(1).max(500),
  apiKey: z.string().trim().min(1).max(1000),
  supportedModels: z.array(z.string().trim().min(1).max(120)).max(500).optional(),
  status: z.enum(['active', 'disabled']).optional(),
  concurrencyLimit: z.coerce.number().int().min(1).max(100000).optional(),
  priority: z.coerce.number().int().min(0).max(100000).optional(),
  notes: z.string().trim().max(1000).optional(),
  externalId: z.string().trim().max(200).optional()
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

externalIntegrationsRouter.post(
  '/juhe-ai/accounts',
  requireExternalIntegrationSource(externalIntegrationAccountPushScope),
  (req, res) => {
    const parsed = accountPushSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest(firstIssueMessage(parsed.error, '公益账号推送参数无效')))
      return
    }
    const context = getExternalIntegrationSourceContext(res)
    if (context.isTestToken) {
      res.json(ok(mockPublicWelfareAccountPush(parsed.data)))
      return
    }

    try {
      const result = pushPublicWelfareAccount(parsed.data)
      const statusCode = result.action === 'created' ? 201 : 200
      recordPublicWelfareAccountPushOperation(context, result, req, statusCode)
      res.status(statusCode).json(ok(result))
    } catch (error) {
      const message = error instanceof Error ? error.message : '公益账号推送失败'
      res.status(message.includes('已存在') || message.includes('重复') ? 409 : 400).json(badRequest(message))
    }
  }
)

function recordPublicWelfareAccountPushOperation(
  context: ExternalIntegrationSourceAuthContext,
  result: PublicAccountPushResponse,
  req: Request,
  statusCode: number
): void {
  try {
    createOperationLog({
      actorSystemAccountId: `external:${context.sourceRefId}`,
      actorUsername: context.sourceName,
      actorDisplayName: `外部来源：${context.sourceName}`,
      actorRole: 'user',
      operationScopeSystemAccountId: result.target.systemAccountId,
      mode: 'self',
      module: 'external_integrations',
      action: 'account_push',
      operationKey: 'external_integrations.public_account_push',
      resourceType: 'account',
      resourceId: result.account.id,
      resourceName: result.account.name,
      summary: `${context.sourceName} 推送公益账号：${result.account.name}`,
      detailLevel: 'full',
      visibilityScope: 'admin_only',
      changes: [
        { field: 'action', label: '写入动作', after: result.action },
        { field: 'status', label: '账户状态', after: result.account.status },
        { field: 'schedulable', label: '可调度', after: result.account.schedulable },
        { field: 'targetCreated', label: '新建目标用户', after: result.target.created },
        { field: 'groupCreated', label: '新建目标分组', after: result.target.groupCreated },
        { field: 'externalId', label: '外部登记 ID', after: result.externalId }
      ],
      metadata: {
        sourceRefId: context.sourceRefId,
        sourceName: context.sourceName,
        tokenId: context.tokenId,
        tokenName: context.tokenName,
        tokenPrefix: context.tokenPrefix,
        targetUsername: result.target.username,
        targetSystemAccountId: result.target.systemAccountId,
        groupId: result.target.groupId,
        groupName: result.target.groupName,
        accountId: result.account.id,
        accountName: result.account.name,
        providerCode: result.account.providerCode,
        type: result.account.type,
        supportedModels: result.account.supportedModels,
        externalId: result.externalId
      },
      method: req.method,
      path: `${req.baseUrl}${req.path}`,
      statusCode,
      targets: [
        { targetType: 'external_integration_source', targetId: context.sourceRefId, targetName: context.sourceName, relation: 'affected' },
        { targetType: 'system_account', targetId: result.target.systemAccountId, targetName: result.target.username, relation: result.target.created ? 'created' : 'affected' },
        { targetType: 'group', targetId: result.target.groupId, targetName: result.target.groupName, targetOwnerSystemAccountId: result.target.systemAccountId, relation: result.target.groupCreated ? 'created' : 'affected' },
        { targetType: 'account', targetId: result.account.id, targetName: result.account.name, targetOwnerSystemAccountId: result.target.systemAccountId, relation: result.action === 'created' ? 'created' : 'affected' }
      ]
    })
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'external_account_push_operation_log_failed',
      sourceRefId: context.sourceRefId,
      accountId: result.account.id
    }), '公益账号推送操作日志写入失败')
  }
}

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

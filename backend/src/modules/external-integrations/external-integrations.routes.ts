import { Router, type Request } from 'express'
import { z } from 'zod'

import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import {
  type ExternalIntegrationSourceAuthContext,
  externalIntegrationAccessInfoReadScope,
  externalIntegrationAccountAddWriteScope,
  externalIntegrationAccountDeleteWriteScope,
  externalIntegrationAccountListReadScope,
  externalIntegrationAccountUpdateWriteScope,
  externalIntegrationAccountUsageReadScope,
  externalIntegrationApiKeyAddWriteScope,
  externalIntegrationApiKeyDeleteWriteScope,
  externalIntegrationApiKeyListReadScope,
  externalIntegrationApiKeyUpdateWriteScope,
  externalIntegrationConsumptionRankingReadScope,
  externalIntegrationGroupAddWriteScope,
  externalIntegrationGroupDeleteWriteScope,
  externalIntegrationGroupListReadScope,
  externalIntegrationGroupUpdateWriteScope,
  externalIntegrationIpUsageReadScope,
  externalIntegrationSourceAuthDemoScope
} from '../../storage/external-integration-source.repository.js'
import { createOperationLog } from '../../storage/repositories.js'
import { requestQuotaLimitsSchema } from '../request-quota-limit.schema.js'
import { apiKeyAvailabilityScheduleSchema } from '../api-keys/api-key-availability-schedule.schema.js'
import { getExternalIntegrationSourceContext, requireExternalIntegrationSource } from './external-source-auth.middleware.js'
import {
  addPublicWelfareAccount,
  deletePublicWelfareAccount,
  addPublicApiKey,
  addPublicGroup,
  deletePublicApiKey,
  deletePublicGroup,
  mockPublicApiKeyAdd,
  mockPublicApiKeyDelete,
  mockPublicApiKeyList,
  mockPublicApiKeyUpdate,
  mockPublicGroupAdd,
  mockPublicGroupDelete,
  mockPublicGroupList,
  mockPublicGroupUpdate,
  mockPublicWelfareAccountDelete,
  mockPublicWelfareAccountList,
  mockPublicWelfareAccountPush,
  listPublicApiKeys,
  listPublicGroups,
  listPublicWelfareAccounts,
  updatePublicWelfareAccount,
  updatePublicApiKey,
  updatePublicGroup,
  type PublicAccountDeleteResponse,
  type PublicAccountPushResponse
} from './external-public-account-push.service.js'
import {
  getPublicAccessInfo,
  getPublicAccountUsage,
  getPublicClientIpUsage,
  getPublicConsumptionRanking
} from './external-public-welfare.service.js'

export const externalIntegrationsRouter = Router()

const rangePresetSchema = z.enum(['today', 'last7d', 'last31d'])
const unsupportedDateRangeSchema = z.undefined({
  invalid_type_error: '公开 IP 聚合接口暂不支持自定义日期范围'
}).optional()
const publicUsageKeywordSchema = z.string().trim().max(120, '关键词不能超过 120 个字符').optional()
const ipUsageQuerySchema = z.object({
  range: rangePresetSchema.optional(),
  startDate: unsupportedDateRangeSchema,
  endDate: unsupportedDateRangeSchema,
  page: z.coerce.number().int().min(1).optional(),
  pageSize: z.coerce.number().int().min(1).max(100).optional(),
  keyword: publicUsageKeywordSchema,
  sortField: z.enum(['requestCount', 'successCount', 'errorCount', 'errorRate', 'totalTokens', 'totalCost', 'activeDays', 'lastUsedAt']).optional(),
  sortOrder: z.enum(['asc', 'desc']).optional()
})
const accountUsageQuerySchema = z.object({
  range: rangePresetSchema.optional(),
  startDate: z.undefined({
    invalid_type_error: '公开账号聚合接口暂不支持自定义日期范围'
  }).optional(),
  endDate: z.undefined({
    invalid_type_error: '公开账号聚合接口暂不支持自定义日期范围'
  }).optional(),
  page: z.coerce.number().int().min(1).optional(),
  pageSize: z.coerce.number().int().min(1).max(100).optional(),
  keyword: publicUsageKeywordSchema,
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
const providerCodeSchema = z.string({ required_error: '供应商编码不能为空' }).trim().min(1, '供应商编码不能为空').max(60)
const providerProtocolProfileIdSchema = z.string().trim().min(1).max(120)
const publicAccountTypeSchema = z.custom<'api_key'>((value) => value === 'api_key', {
  message: '公开账号接口仅支持 API Key 账户'
})
const accountPushSchema = z.object({
  targetUsername: z.string().trim().min(2).max(80),
  targetDisplayName: z.string().trim().min(1).max(80).optional(),
  targetGroupName: z.string().trim().min(1).max(80),
  providerCode: providerCodeSchema,
  providerProtocolProfileId: providerProtocolProfileIdSchema.optional(),
  name: z.string().trim().min(1).max(120),
  type: publicAccountTypeSchema,
  baseUrl: z.string().trim().min(1).max(500),
  apiKey: z.string().trim().min(1).max(1000),
  supportedModels: z.array(z.string().trim().min(1).max(120)).max(500).optional(),
  status: z.enum(['active', 'disabled']).optional(),
  concurrencyLimit: z.number().int().min(1).max(100000).optional(),
  priority: z.number().int().min(0).max(100000).optional(),
  availabilitySchedule: z.record(z.string(), z.unknown()).nullable().optional(),
  notes: z.string().trim().max(1000).optional()
}).strict()
const accountUpdateSchema = accountPushSchema.extend({
  accountId: z.string().trim().min(1).max(120)
}).strict()
const accountDeleteSchema = z.object({
  targetUsername: z.string().trim().min(2).max(80),
  targetGroupName: z.string().trim().min(1).max(80),
  providerCode: providerCodeSchema,
  providerProtocolProfileId: providerProtocolProfileIdSchema.optional(),
  accountId: z.string().trim().min(1).max(120)
}).strict()
const accountListQuerySchema = z.object({
  targetUsername: z.string().trim().min(2).max(80),
  targetGroupName: z.string().trim().min(1).max(80).optional(),
  providerCode: z.string().trim().min(1).max(60).optional(),
  providerProtocolProfileId: providerProtocolProfileIdSchema.optional(),
  groupId: z.string().trim().min(1).max(120).optional(),
  keyword: z.string().trim().max(120).optional(),
  type: z.string().trim().max(60).optional(),
  status: z.string().trim().max(200).optional(),
  schedulable: z.enum(['all', 'enabled', 'disabled', 'cooling']).optional(),
  page: z.coerce.number().int().min(1).optional(),
  pageSize: z.coerce.number().int().min(1).max(100).optional()
}).strict()
const groupAddSchema = z.object({
  targetUsername: z.string().trim().min(2).max(80),
  targetDisplayName: z.string().trim().min(1).max(80).optional(),
  name: z.string().trim().min(1).max(80),
  providerCode: providerCodeSchema,
  providerProtocolProfileId: providerProtocolProfileIdSchema.optional(),
  description: z.string().trim().max(500).optional(),
  enabled: z.boolean().optional(),
  groupType: z.enum(['personal', 'high_concurrency']).optional()
}).strict()
const groupUpdateSchema = z.object({
  targetUsername: z.string().trim().min(2).max(80),
  groupId: z.string().trim().min(1).max(120),
  name: z.string().trim().min(1).max(80).optional(),
  providerCode: z.string().trim().min(1).max(60).optional(),
  providerProtocolProfileId: providerProtocolProfileIdSchema.optional(),
  description: z.string().trim().max(500).nullable().optional(),
  enabled: z.boolean().optional(),
  groupType: z.enum(['personal', 'high_concurrency']).optional()
}).strict()
const groupDeleteSchema = z.object({
  targetUsername: z.string().trim().min(2).max(80),
  groupId: z.string().trim().min(1).max(120)
}).strict()
const groupListQuerySchema = z.object({
  targetUsername: z.string().trim().min(2).max(80),
  providerCode: z.string().trim().min(1).max(60).optional(),
  providerProtocolProfileId: providerProtocolProfileIdSchema.optional(),
  keyword: z.string().trim().max(80).optional(),
  page: z.coerce.number().int().min(1).optional(),
  pageSize: z.coerce.number().int().min(1).max(100).optional()
}).strict()
const apiKeyGroupBindingSchema = z.object({
  groupId: z.string().trim().min(1).max(120),
  priority: z.number().int().positive().optional(),
  weight: z.number().int().min(1).max(100).optional(),
  status: z.enum(['active', 'disabled']).optional()
}).strict()
const apiKeyAddSchema = z.object({
  targetUsername: z.string().trim().min(2).max(80),
  name: z.string().trim().min(1).max(120),
  description: z.string().trim().max(200).nullable().optional(),
  groupBindings: z.array(apiKeyGroupBindingSchema).min(1).max(20),
  groupRouteStrategy: z.enum(['priority_failover', 'round_robin', 'weighted_round_robin']).optional(),
  status: z.enum(['active', 'disabled']).optional(),
  expiresAt: z.string().trim().optional(),
  quotaLimits: requestQuotaLimitsSchema.nullable().optional(),
  availabilitySchedule: apiKeyAvailabilityScheduleSchema.nullable().optional()
}).strict()
const apiKeyUpdateSchema = z.object({
  targetUsername: z.string().trim().min(2).max(80),
  apiKeyId: z.string().trim().min(1).max(120),
  name: z.string().trim().min(1).max(120).optional(),
  description: z.string().trim().max(200).nullable().optional(),
  groupBindings: z.array(apiKeyGroupBindingSchema).min(1).max(20).optional(),
  groupRouteStrategy: z.enum(['priority_failover', 'round_robin', 'weighted_round_robin']).optional(),
  status: z.enum(['active', 'disabled']).optional(),
  expiresAt: z.string().trim().nullable().optional(),
  quotaLimits: requestQuotaLimitsSchema.nullable().optional(),
  availabilitySchedule: apiKeyAvailabilityScheduleSchema.nullable().optional()
}).strict()
const apiKeyDeleteSchema = z.object({
  targetUsername: z.string().trim().min(2).max(80),
  apiKeyId: z.string().trim().min(1).max(120)
}).strict()
const apiKeyListQuerySchema = z.object({
  targetUsername: z.string().trim().min(2).max(80),
  keyword: z.string().trim().max(120).optional(),
  status: z.enum(['active', 'disabled', 'all']).optional(),
  groupId: z.string().trim().min(1).max(120).optional(),
  page: z.coerce.number().int().min(1).optional(),
  pageSize: z.coerce.number().int().min(1).max(100).optional()
}).strict()

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
      authenticatedAt: context.authenticatedAt,
      mock: context.isTestToken
    }))
  }
)

externalIntegrationsRouter.get(
  '/ip/usage',
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
  '/account/usage',
  requireExternalIntegrationSource(externalIntegrationAccountUsageReadScope),
  (req, res) => {
    const parsed = accountUsageQuerySchema.safeParse(req.query)
    if (!parsed.success) {
      res.status(400).json(badRequest(firstIssueMessage(parsed.error, '账号聚合公开接口参数无效')))
      return
    }
    const context = getExternalIntegrationSourceContext(res)
    res.json(ok(getPublicAccountUsage(parsed.data, { mock: context.isTestToken })))
  }
)

externalIntegrationsRouter.get(
  '/consumption/ranking',
  requireExternalIntegrationSource(externalIntegrationConsumptionRankingReadScope),
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
  '/access/info',
  requireExternalIntegrationSource(externalIntegrationAccessInfoReadScope),
  (_req, res) => {
    const context = getExternalIntegrationSourceContext(res)
    res.json(ok(getPublicAccessInfo({ mock: context.isTestToken })))
  }
)

externalIntegrationsRouter.get(
  '/group/list',
  requireExternalIntegrationSource(externalIntegrationGroupListReadScope),
  (req, res) => {
    const parsed = groupListQuerySchema.safeParse(req.query)
    if (!parsed.success) {
      res.status(400).json(badRequest(firstIssueMessage(parsed.error, '分组列表参数无效')))
      return
    }
    const context = getExternalIntegrationSourceContext(res)
    if (context.isTestToken) {
      res.json(ok(mockPublicGroupList(parsed.data)))
      return
    }
    try {
      res.json(ok(listPublicGroups(parsed.data)))
    } catch (error) {
      const message = error instanceof Error ? error.message : '分组列表读取失败'
      res.status(message.includes('不存在') ? 404 : 400).json(badRequest(message))
    }
  }
)

externalIntegrationsRouter.get(
  '/api-key/list',
  requireExternalIntegrationSource(externalIntegrationApiKeyListReadScope),
  (req, res) => {
    const parsed = apiKeyListQuerySchema.safeParse(req.query)
    if (!parsed.success) {
      res.status(400).json(badRequest(firstIssueMessage(parsed.error, 'API Key 列表参数无效')))
      return
    }
    const context = getExternalIntegrationSourceContext(res)
    if (context.isTestToken) {
      res.json(ok(mockPublicApiKeyList(parsed.data)))
      return
    }
    try {
      res.json(ok(listPublicApiKeys(parsed.data)))
    } catch (error) {
      const message = error instanceof Error ? error.message : 'API Key 列表读取失败'
      res.status(message.includes('不存在') ? 404 : 400).json(badRequest(message))
    }
  }
)

externalIntegrationsRouter.get(
  '/account/list',
  requireExternalIntegrationSource(externalIntegrationAccountListReadScope),
  (req, res) => {
    const parsed = accountListQuerySchema.safeParse(req.query)
    if (!parsed.success) {
      res.status(400).json(badRequest(firstIssueMessage(parsed.error, '账号列表参数无效')))
      return
    }
    const context = getExternalIntegrationSourceContext(res)
    if (context.isTestToken) {
      res.json(ok(mockPublicWelfareAccountList(parsed.data)))
      return
    }
    try {
      res.json(ok(listPublicWelfareAccounts(parsed.data)))
    } catch (error) {
      const message = error instanceof Error ? error.message : '账号列表读取失败'
      res.status(message.includes('不存在') ? 404 : 400).json(badRequest(message))
    }
  }
)

externalIntegrationsRouter.post(
  '/group/add',
  requireExternalIntegrationSource(externalIntegrationGroupAddWriteScope),
  async (req, res) => {
    const parsed = groupAddSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest(firstIssueMessage(parsed.error, '分组新增参数无效')))
      return
    }
    const context = getExternalIntegrationSourceContext(res)
    if (context.isTestToken) {
      res.status(201).json(ok(mockPublicGroupAdd(parsed.data)))
      return
    }
    try {
      res.status(201).json(ok(await addPublicGroup(parsed.data)))
    } catch (error) {
      const message = error instanceof Error ? error.message : '分组新增失败'
      res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
    }
  }
)

externalIntegrationsRouter.post(
  '/group/update',
  requireExternalIntegrationSource(externalIntegrationGroupUpdateWriteScope),
  (req, res) => {
    const parsed = groupUpdateSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest(firstIssueMessage(parsed.error, '分组修改参数无效')))
      return
    }
    const context = getExternalIntegrationSourceContext(res)
    if (context.isTestToken) {
      res.json(ok(mockPublicGroupUpdate(parsed.data)))
      return
    }
    try {
      const result = updatePublicGroup(parsed.data)
      res.status(result.action === 'not_found' ? 404 : 200).json(result.action === 'not_found' ? { message: '分组不存在' } : ok(result))
    } catch (error) {
      const message = error instanceof Error ? error.message : '分组修改失败'
      res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
    }
  }
)

externalIntegrationsRouter.post(
  '/group/del',
  requireExternalIntegrationSource(externalIntegrationGroupDeleteWriteScope),
  (req, res) => {
    const parsed = groupDeleteSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest(firstIssueMessage(parsed.error, '分组删除参数无效')))
      return
    }
    const context = getExternalIntegrationSourceContext(res)
    if (context.isTestToken) {
      res.json(ok(mockPublicGroupDelete(parsed.data)))
      return
    }
    try {
      const result = deletePublicGroup(parsed.data)
      res.status(result.action === 'not_found' ? 404 : 200).json(result.action === 'not_found' ? { message: '分组不存在' } : ok(result))
    } catch (error) {
      res.status(400).json(badRequest(error instanceof Error ? error.message : '分组删除失败'))
    }
  }
)

externalIntegrationsRouter.post(
  '/api-key/add',
  requireExternalIntegrationSource(externalIntegrationApiKeyAddWriteScope),
  (req, res) => {
    const parsed = apiKeyAddSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest(firstIssueMessage(parsed.error, 'API Key 新增参数无效')))
      return
    }
    const context = getExternalIntegrationSourceContext(res)
    if (context.isTestToken) {
      res.status(201).json(ok(mockPublicApiKeyAdd(parsed.data)))
      return
    }
    try {
      res.status(201).json(ok(addPublicApiKey(parsed.data)))
    } catch (error) {
      const message = error instanceof Error ? error.message : 'API Key 新增失败'
      res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
    }
  }
)

externalIntegrationsRouter.post(
  '/api-key/update',
  requireExternalIntegrationSource(externalIntegrationApiKeyUpdateWriteScope),
  (req, res) => {
    const parsed = apiKeyUpdateSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest(firstIssueMessage(parsed.error, 'API Key 修改参数无效')))
      return
    }
    const context = getExternalIntegrationSourceContext(res)
    if (context.isTestToken) {
      res.json(ok(mockPublicApiKeyUpdate(parsed.data)))
      return
    }
    try {
      const result = updatePublicApiKey(parsed.data)
      res.status(result.action === 'not_found' ? 404 : 200).json(result.action === 'not_found' ? { message: 'API Key 不存在' } : ok(result))
    } catch (error) {
      const message = error instanceof Error ? error.message : 'API Key 修改失败'
      res.status(message.includes('已存在') ? 409 : 400).json(badRequest(message))
    }
  }
)

externalIntegrationsRouter.post(
  '/api-key/del',
  requireExternalIntegrationSource(externalIntegrationApiKeyDeleteWriteScope),
  (req, res) => {
    const parsed = apiKeyDeleteSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest(firstIssueMessage(parsed.error, 'API Key 删除参数无效')))
      return
    }
    const context = getExternalIntegrationSourceContext(res)
    if (context.isTestToken) {
      res.json(ok(mockPublicApiKeyDelete(parsed.data)))
      return
    }
    try {
      const result = deletePublicApiKey(parsed.data)
      res.status(result.action === 'not_found' ? 404 : 200).json(result.action === 'not_found' ? { message: 'API Key 不存在' } : ok(result))
    } catch (error) {
      res.status(400).json(badRequest(error instanceof Error ? error.message : 'API Key 删除失败'))
    }
  }
)

externalIntegrationsRouter.post(
  '/account/add',
  requireExternalIntegrationSource(externalIntegrationAccountAddWriteScope),
  async (req, res) => {
    const parsed = accountPushSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest(firstIssueMessage(parsed.error, '账号新增参数无效')))
      return
    }
    const context = getExternalIntegrationSourceContext(res)
    if (context.isTestToken) {
      res.json(ok(mockPublicWelfareAccountPush(parsed.data)))
      return
    }

    try {
      const result = await addPublicWelfareAccount(parsed.data)
      recordPublicWelfareAccountWriteOperation(context, result, req, 201)
      res.status(201).json(ok(result))
    } catch (error) {
      const message = error instanceof Error ? error.message : '账号新增失败'
      res.status(message.includes('已存在') || message.includes('重复') ? 409 : 400).json(badRequest(message))
    }
  }
)

externalIntegrationsRouter.post(
  '/account/update',
  requireExternalIntegrationSource(externalIntegrationAccountUpdateWriteScope),
  (req, res) => {
    const parsed = accountUpdateSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest(firstIssueMessage(parsed.error, '账号修改参数无效')))
      return
    }
    const context = getExternalIntegrationSourceContext(res)
    if (context.isTestToken) {
      res.json(ok(mockPublicWelfareAccountPush(parsed.data)))
      return
    }

    try {
      const result = updatePublicWelfareAccount(parsed.data)
      recordPublicWelfareAccountWriteOperation(context, result, req, 200)
      res.json(ok(result))
    } catch (error) {
      const message = error instanceof Error ? error.message : '账号修改失败'
      res.status(message.includes('不存在') ? 404 : message.includes('已存在') || message.includes('重复') ? 409 : 400).json(badRequest(message))
    }
  }
)

externalIntegrationsRouter.post(
  '/account/del',
  requireExternalIntegrationSource(externalIntegrationAccountDeleteWriteScope),
  (req, res) => {
    const parsed = accountDeleteSchema.safeParse(req.body)
    if (!parsed.success) {
      res.status(400).json(badRequest(firstIssueMessage(parsed.error, '账号删除参数无效')))
      return
    }
    const context = getExternalIntegrationSourceContext(res)
    if (context.isTestToken) {
      res.json(ok(mockPublicWelfareAccountDelete(parsed.data)))
      return
    }

    try {
      const result = deletePublicWelfareAccount(parsed.data)
      recordPublicWelfareAccountDeleteOperation(context, result, req, 200)
      res.json(ok(result))
    } catch (error) {
      const message = error instanceof Error ? error.message : '账号删除失败'
      res.status(message.includes('不存在') ? 404 : 400).json(badRequest(message))
    }
  }
)

function recordPublicWelfareAccountWriteOperation(
  context: ExternalIntegrationSourceAuthContext,
  result: PublicAccountPushResponse,
  req: Request,
  statusCode: number
): void {
  const created = result.action === 'created'
  try {
    createOperationLog({
      actorSystemAccountId: `external:${context.sourceRefId}`,
      actorUsername: context.sourceName,
      actorDisplayName: `外部来源：${context.sourceName}`,
      actorRole: 'user',
      operationScopeSystemAccountId: result.target.systemAccountId,
      mode: 'self',
      module: 'external_integrations',
      action: created ? 'account_add' : 'account_update',
      operationKey: created ? 'external_integrations.public_account_add' : 'external_integrations.public_account_update',
      resourceType: 'account',
      resourceId: result.account.id,
      resourceName: result.account.name,
      summary: `${context.sourceName} ${created ? '新增' : '修改'}账号：${result.account.name}`,
      detailLevel: 'full',
      visibilityScope: 'admin_only',
      changes: [
        { field: 'action', label: '写入动作', after: result.action },
        { field: 'status', label: '账户状态', after: result.account.status },
        { field: 'schedulable', label: '可调度', after: result.account.schedulable },
        { field: 'targetCreated', label: '新建目标用户', after: result.target.created },
        { field: 'groupCreated', label: '新建目标分组', after: result.target.groupCreated }
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
        supportedModels: result.account.supportedModels
      },
      method: req.method,
      path: `${req.baseUrl}${req.path}`,
      statusCode,
      targets: [
        { targetType: 'external_integration_source', targetId: context.sourceRefId, targetName: context.sourceName, relation: 'affected' },
        { targetType: 'system_account', targetId: result.target.systemAccountId, targetName: result.target.username, relation: result.target.created ? 'created' : 'affected' },
        { targetType: 'group', targetId: result.target.groupId, targetName: result.target.groupName, targetOwnerSystemAccountId: result.target.systemAccountId, relation: result.target.groupCreated ? 'created' : 'affected' },
        { targetType: 'account', targetId: result.account.id, targetName: result.account.name, targetOwnerSystemAccountId: result.target.systemAccountId, relation: created ? 'created' : 'affected' }
      ]
    })
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'external_account_write_operation_log_failed',
      sourceRefId: context.sourceRefId,
      accountId: result.account.id
    }), '账号写入操作日志写入失败')
  }
}

function recordPublicWelfareAccountDeleteOperation(
  context: ExternalIntegrationSourceAuthContext,
  result: PublicAccountDeleteResponse,
  req: Request,
  statusCode: number
): void {
  if (result.action !== 'deleted' || !result.account) {
    return
  }

  try {
    createOperationLog({
      actorSystemAccountId: `external:${context.sourceRefId}`,
      actorUsername: context.sourceName,
      actorDisplayName: `外部来源：${context.sourceName}`,
      actorRole: 'user',
      operationScopeSystemAccountId: result.target.systemAccountId,
      mode: 'self',
      module: 'external_integrations',
      action: 'account_delete',
      operationKey: 'external_integrations.public_account_delete',
      resourceType: 'account',
      resourceId: result.account.id,
      resourceName: result.account.name,
      summary: `${context.sourceName} 删除账号：${result.account.name}`,
      detailLevel: 'full',
      visibilityScope: 'admin_only',
      changes: [
        { field: 'deleted', label: '删除状态', before: false, after: true }
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
        providerCode: result.account.providerCode
      },
      method: req.method,
      path: `${req.baseUrl}${req.path}`,
      statusCode,
      targets: [
        { targetType: 'external_integration_source', targetId: context.sourceRefId, targetName: context.sourceName, relation: 'affected' },
        { targetType: 'system_account', targetId: result.target.systemAccountId, targetName: result.target.username, relation: 'affected' },
        { targetType: 'group', targetId: result.target.groupId, targetName: result.target.groupName, targetOwnerSystemAccountId: result.target.systemAccountId, relation: 'affected' },
        { targetType: 'account', targetId: result.account.id, targetName: result.account.name, targetOwnerSystemAccountId: result.target.systemAccountId, relation: 'deleted' }
      ]
    })
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'external_account_delete_operation_log_failed',
      sourceRefId: context.sourceRefId,
      accountId: result.account.id
    }), '账号删除操作日志写入失败')
  }
}

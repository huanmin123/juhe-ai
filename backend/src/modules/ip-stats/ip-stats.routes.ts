import { Router, type Request, type Response } from 'express'
import { z } from 'zod'

import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import {
  getClientIpStatsDetailAsync,
  listClientIpStatsAsync,
  type ClientIpPolicyType,
  type ClientIpStatsSortField
} from '../../storage/client-ip-stats.repository.js'
import { requestStatsWriter } from '../background/background-stats-writer.js'
import { bodyField, mutationGuard } from '../deduplication/mutation-guard.middleware.js'
import { notifyClientIpPolicyCacheInvalidated } from '../db-service/db-service-ipc.js'
import { getRequestAuthContext } from '../auth/request-context.js'
import { recordOperationLogAsync, safeChange } from '../operation-logs/operation-log.service.js'

export const ipStatsRouter = Router()

const listQuerySchema = z.object({
  page: z.coerce.number().int().min(1).optional(),
  pageSize: z.coerce.number().int().min(1).max(100).optional(),
  keyword: z.string().trim().optional(),
  status: z.enum(['all', 'normal', 'blacklisted', 'allowlisted']).optional(),
  startDate: z.string().trim().optional(),
  endDate: z.string().trim().optional(),
  lastUsedStartDate: z.string().trim().optional(),
  lastUsedEndDate: z.string().trim().optional(),
  sortField: z.enum(['requestCount', 'successCount', 'errorCount', 'errorRate', 'totalTokens', 'totalCost', 'activeDays', 'lastUsedAt']).optional(),
  sortOrder: z.enum(['asc', 'desc']).optional()
})

const detailQuerySchema = z.object({
  page: z.coerce.number().int().min(1).optional(),
  pageSize: z.coerce.number().int().min(1).max(100).optional(),
  startDate: z.string().trim().optional(),
  endDate: z.string().trim().optional(),
  sortField: z.enum(['requestCount', 'successCount', 'errorCount', 'errorRate', 'totalTokens', 'totalCost', 'activeDays', 'lastUsedAt']).optional(),
  sortOrder: z.enum(['asc', 'desc']).optional()
})

const ipHashParamSchema = z.object({
  ipHash: z.string().trim().regex(/^[0-9a-fA-F]{64}$/, 'IP 标识无效')
})

const policyBodySchema = z.object({
  reason: z.string().trim().max(500, '原因不能超过 500 个字符').optional(),
  durationMinutes: z.number().int().min(1, '封禁分钟数不能小于 1').max(525600, '封禁分钟数不能超过 525600').optional(),
  durationDays: z.number().int().min(1, '封禁天数不能小于 1').max(3650, '封禁天数不能超过 3650').optional()
}).strict('IP 策略参数包含未知字段')

const simplePolicyBodySchema = z.object({
  reason: z.string().trim().max(500, '原因不能超过 500 个字符').optional()
}).strict('IP 策略参数包含未知字段')

type PolicyDurationResult = { expiresAt?: string; label: string; error?: string }

ipStatsRouter.get('/', async (req, res, next) => {
  const parsed = listQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, 'IP 统计参数无效')))
    return
  }
  try {
    res.json(ok(await listClientIpStatsAsync({
      ...parsed.data,
      lastUsedSortScope: 'global',
      sortField: parsed.data.sortField as ClientIpStatsSortField | undefined
    })))
  } catch (error) {
    next(error)
  }
})

ipStatsRouter.get('/:ipHash/detail', async (req, res, next) => {
  const params = ipHashParamSchema.safeParse(req.params)
  if (!params.success) {
    res.status(400).json(badRequest(firstIssueMessage(params.error, 'IP 标识无效')))
    return
  }
  const parsed = detailQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, 'IP 详情参数无效')))
    return
  }
  try {
    const detail = await getClientIpStatsDetailAsync({
      ipHash: params.data.ipHash,
      ...parsed.data,
      sortField: parsed.data.sortField as ClientIpStatsSortField | undefined
    })
    if (!detail) {
      res.status(404).json({ message: 'IP 不存在' })
      return
    }
    res.json(ok(detail))
  } catch (error) {
    next(error)
  }
})

ipStatsRouter.post('/:ipHash/blacklist', mutationGuard({
  operationKey: 'client_ip_stats.blacklist',
  fingerprint: (req) => ({
    ipHash: req.params.ipHash,
    reason: bodyField(req, 'reason'),
    durationMinutes: bodyField(req, 'durationMinutes'),
    durationDays: bodyField(req, 'durationDays')
  })
}), async (req, res, next) => {
  try {
    await handleCreatePolicy(req, res, 'blacklist')
  } catch (error) {
    next(error)
  }
})

ipStatsRouter.post('/:ipHash/allowlist', mutationGuard({
  operationKey: 'client_ip_stats.allowlist',
  fingerprint: (req) => ({
    ipHash: req.params.ipHash,
    reason: bodyField(req, 'reason')
  })
}), async (req, res, next) => {
  try {
    await handleCreatePolicy(req, res, 'allowlist')
  } catch (error) {
    next(error)
  }
})

ipStatsRouter.post('/:ipHash/unblock', mutationGuard({
  operationKey: 'client_ip_stats.unblock',
  fingerprint: (req) => ({
    ipHash: req.params.ipHash,
    reason: bodyField(req, 'reason')
  })
}), async (req, res, next) => {
  try {
    await handleDisablePolicy(req, res, 'blacklist')
  } catch (error) {
    next(error)
  }
})

ipStatsRouter.post('/:ipHash/unallowlist', mutationGuard({
  operationKey: 'client_ip_stats.unallowlist',
  fingerprint: (req) => ({
    ipHash: req.params.ipHash,
    reason: bodyField(req, 'reason')
  })
}), async (req, res, next) => {
  try {
    await handleDisablePolicy(req, res, 'allowlist')
  } catch (error) {
    next(error)
  }
})

async function handleCreatePolicy(req: Request, res: Response, policyType: ClientIpPolicyType): Promise<void> {
  const params = ipHashParamSchema.safeParse(req.params)
  if (!params.success) {
    res.status(400).json(badRequest(firstIssueMessage(params.error, 'IP 标识无效')))
    return
  }
  const body = (policyType === 'blacklist' ? policyBodySchema : simplePolicyBodySchema).safeParse(req.body ?? {})
  if (!body.success) {
    res.status(400).json(badRequest(firstIssueMessage(body.error, 'IP 策略参数无效')))
    return
  }
  const duration: PolicyDurationResult = policyType === 'blacklist'
    ? resolvePolicyDuration(body.data)
    : { label: '永久' }
  if (duration.error) {
    res.status(400).json(badRequest(duration.error))
    return
  }
  const actor = getRequestAuthContext()?.systemAccountId
  if (!actor) {
    res.status(401).json({ message: '请先登录' })
    return
  }
  try {
    const policy = await requestStatsWriter({
      type: 'create_client_ip_policy',
      input: {
        ipHash: params.data.ipHash,
        policyType,
        reason: body.data.reason,
        expiresAt: duration.expiresAt,
        actorSystemAccountId: actor
      }
    })
    notifyClientIpPolicyCacheInvalidated()
    await recordOperationLogAsync({
      module: 'client_ip_stats',
      action: policyType === 'blacklist' ? 'blacklist' : 'allowlist',
      operationKey: policyType === 'blacklist' ? 'client_ip_stats.blacklist' : 'client_ip_stats.allowlist',
      resourceType: 'client_ip',
      resourceId: params.data.ipHash,
      resourceName: params.data.ipHash.slice(0, 12),
      summary: `${policyType === 'blacklist' ? '封禁 IP' : '加入 IP 白名单'}：${params.data.ipHash.slice(0, 12)}`,
      detailLevel: 'full',
      visibilityScope: 'admin_only',
      changes: [
        safeChange('reason', '原因', undefined, body.data.reason),
        safeChange('policyType', '策略类型', undefined, policyType),
        safeChange('duration', policyType === 'blacklist' ? '封禁时长' : '白名单时长', undefined, duration.label),
        safeChange('expiresAt', '过期时间', undefined, duration.expiresAt)
      ],
      metadata: {
        ipHash: params.data.ipHash,
        policyId: policy.id,
        policyType,
        reason: body.data.reason,
        durationLabel: duration.label,
        durationMinutes: 'durationMinutes' in body.data ? body.data.durationMinutes : undefined,
        durationDays: 'durationDays' in body.data ? body.data.durationDays : undefined,
        expiresAt: duration.expiresAt
      }
    }, req)
    res.json(ok(policy))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : 'IP 策略保存失败'))
  }
}

async function handleDisablePolicy(req: Request, res: Response, policyType: ClientIpPolicyType): Promise<void> {
  const params = ipHashParamSchema.safeParse(req.params)
  if (!params.success) {
    res.status(400).json(badRequest(firstIssueMessage(params.error, 'IP 标识无效')))
    return
  }
  const body = simplePolicyBodySchema.safeParse(req.body ?? {})
  if (!body.success) {
    res.status(400).json(badRequest(firstIssueMessage(body.error, 'IP 策略参数无效')))
    return
  }
  const actor = getRequestAuthContext()?.systemAccountId
  if (!actor) {
    res.status(401).json({ message: '请先登录' })
    return
  }
  try {
    const result = await requestStatsWriter({
      type: 'disable_client_ip_policies',
      input: {
        ipHash: params.data.ipHash,
        policyType,
        reason: body.data.reason,
        actorSystemAccountId: actor
      }
    })
    notifyClientIpPolicyCacheInvalidated()
    const action = policyType === 'blacklist' ? 'unblock' : 'unallowlist'
    await recordOperationLogAsync({
      module: 'client_ip_stats',
      action,
      operationKey: `client_ip_stats.${action}`,
      resourceType: 'client_ip',
      resourceId: params.data.ipHash,
      resourceName: params.data.ipHash.slice(0, 12),
      summary: `${policyType === 'blacklist' ? '解除 IP 封禁' : '移出 IP 白名单'}：${params.data.ipHash.slice(0, 12)}`,
      detailLevel: 'full',
      visibilityScope: 'admin_only',
      changes: [
        safeChange('disabledCount', '停用策略数', undefined, result.disabledCount),
        safeChange('policyType', '策略类型', policyType, undefined),
        safeChange('reason', '原因', undefined, body.data.reason)
      ],
      metadata: {
        ipHash: params.data.ipHash,
        policyType,
        disabledCount: result.disabledCount,
        reason: body.data.reason
      }
    }, req)
    res.json(ok(result))
  } catch (error) {
    nextBadRequest(res, error, 'IP 策略停用失败')
  }
}

function nextBadRequest(res: Response, error: unknown, fallbackMessage: string): void {
  res.status(400).json(badRequest(error instanceof Error ? error.message : fallbackMessage))
}

function resolvePolicyDuration(input: z.infer<typeof policyBodySchema>): PolicyDurationResult {
  const hasDurationMinutes = input.durationMinutes !== undefined
  const hasDurationDays = input.durationDays !== undefined
  const selectedCount = [hasDurationMinutes, hasDurationDays].filter(Boolean).length
  if (selectedCount > 1) {
    return { label: '无效', error: '封禁时长只能选择一种' }
  }
  if (hasDurationMinutes) {
    return {
      expiresAt: new Date(Date.now() + Number(input.durationMinutes) * 60 * 1000).toISOString(),
      label: `${input.durationMinutes} 分钟`
    }
  }
  if (hasDurationDays) {
    return {
      expiresAt: new Date(Date.now() + Number(input.durationDays) * 24 * 60 * 60 * 1000).toISOString(),
      label: `${input.durationDays} 天`
    }
  }
  return { label: '永久' }
}

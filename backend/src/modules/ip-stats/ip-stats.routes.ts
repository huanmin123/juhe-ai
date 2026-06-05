import { Router, type Request, type Response } from 'express'
import { z } from 'zod'

import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import {
  createClientIpPolicy,
  disableClientIpPolicies,
  listClientIpStats,
  type ClientIpStatsSortField
} from '../../storage/client-ip-stats.repository.js'
import { bodyField, mutationGuard } from '../deduplication/mutation-guard.middleware.js'
import { notifyClientIpPolicyCacheInvalidated } from '../db-service/db-service-ipc.js'
import { getRequestAuthContext } from '../auth/request-context.js'
import { recordOperationLog, safeChange } from '../operation-logs/operation-log.service.js'

export const ipStatsRouter = Router()

const listQuerySchema = z.object({
  page: z.coerce.number().int().min(1).optional(),
  pageSize: z.coerce.number().int().min(1).max(100).optional(),
  keyword: z.string().trim().optional(),
  status: z.enum(['all', 'normal', 'blacklisted']).optional(),
  startDate: z.string().trim().optional(),
  endDate: z.string().trim().optional(),
  lastUsedStartDate: z.string().trim().optional(),
  lastUsedEndDate: z.string().trim().optional(),
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

ipStatsRouter.get('/', (req, res) => {
  const parsed = listQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, 'IP 统计参数无效')))
    return
  }
  res.json(ok(listClientIpStats({
    ...parsed.data,
    lastUsedSortScope: 'global',
    sortField: parsed.data.sortField as ClientIpStatsSortField | undefined
  })))
})

ipStatsRouter.post('/:ipHash/blacklist', mutationGuard({
  operationKey: 'client_ip_stats.blacklist',
  fingerprint: (req) => ({
    ipHash: req.params.ipHash,
    reason: bodyField(req, 'reason'),
    durationMinutes: bodyField(req, 'durationMinutes'),
    durationDays: bodyField(req, 'durationDays')
  })
}), (req, res) => {
  handleCreatePolicy(req, res)
})

ipStatsRouter.post('/:ipHash/unblock', mutationGuard({
  operationKey: 'client_ip_stats.unblock',
  fingerprint: (req) => ({
    ipHash: req.params.ipHash,
    reason: bodyField(req, 'reason')
  })
}), (req, res) => {
  const params = ipHashParamSchema.safeParse(req.params)
  if (!params.success) {
    res.status(400).json(badRequest(firstIssueMessage(params.error, 'IP 标识无效')))
    return
  }
  const body = policyBodySchema.safeParse(req.body ?? {})
  if (!body.success) {
    res.status(400).json(badRequest(firstIssueMessage(body.error, '解封参数无效')))
    return
  }
  const actor = getRequestAuthContext()?.systemAccountId
  if (!actor) {
    res.status(401).json({ message: '请先登录' })
    return
  }
  const result = disableClientIpPolicies({
    ipHash: params.data.ipHash,
    reason: body.data.reason,
    actorSystemAccountId: actor
  })
  notifyClientIpPolicyCacheInvalidated()
  recordOperationLog({
    module: 'client_ip_stats',
    action: 'unblock',
    operationKey: 'client_ip_stats.unblock',
    resourceType: 'client_ip',
    resourceId: params.data.ipHash,
    resourceName: params.data.ipHash.slice(0, 12),
    summary: `解除 IP 封禁：${params.data.ipHash.slice(0, 12)}`,
    detailLevel: 'full',
    visibilityScope: 'admin_only',
    changes: [
      safeChange('disabledCount', '解除策略数', undefined, result.disabledCount),
      safeChange('reason', '原因', undefined, body.data.reason)
    ],
    metadata: {
      ipHash: params.data.ipHash,
      disabledCount: result.disabledCount,
      reason: body.data.reason
    }
  }, req)
  res.json(ok(result))
})

function handleCreatePolicy(req: Request, res: Response): void {
  const params = ipHashParamSchema.safeParse(req.params)
  if (!params.success) {
    res.status(400).json(badRequest(firstIssueMessage(params.error, 'IP 标识无效')))
    return
  }
  const body = policyBodySchema.safeParse(req.body ?? {})
  if (!body.success) {
    res.status(400).json(badRequest(firstIssueMessage(body.error, 'IP 策略参数无效')))
    return
  }
  const duration = resolvePolicyDuration(body.data)
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
    const policy = createClientIpPolicy({
      ipHash: params.data.ipHash,
      reason: body.data.reason,
      expiresAt: duration.expiresAt,
      actorSystemAccountId: actor
    })
    notifyClientIpPolicyCacheInvalidated()
    recordOperationLog({
      module: 'client_ip_stats',
      action: 'blacklist',
      operationKey: 'client_ip_stats.blacklist',
      resourceType: 'client_ip',
      resourceId: params.data.ipHash,
      resourceName: params.data.ipHash.slice(0, 12),
      summary: `封禁 IP：${params.data.ipHash.slice(0, 12)}`,
      detailLevel: 'full',
      visibilityScope: 'admin_only',
      changes: [
        safeChange('reason', '原因', undefined, body.data.reason),
        safeChange('duration', '封禁时长', undefined, duration.label),
        safeChange('expiresAt', '过期时间', undefined, duration.expiresAt)
      ],
      metadata: {
        ipHash: params.data.ipHash,
        policyId: policy.id,
        reason: body.data.reason,
        durationLabel: duration.label,
        durationMinutes: body.data.durationMinutes,
        durationDays: body.data.durationDays,
        expiresAt: duration.expiresAt
      }
    }, req)
    res.json(ok(policy))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : 'IP 策略保存失败'))
  }
}

function resolvePolicyDuration(input: z.infer<typeof policyBodySchema>): { expiresAt?: string; label: string; error?: string } {
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

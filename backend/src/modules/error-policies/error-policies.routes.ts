import { Router } from 'express'
import { z } from 'zod'

import { OPENAI_PROTOCOL_CODE } from '../../domain/provider-protocol.js'
import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import {
  createErrorPolicy,
  deleteErrorPolicy,
  listErrorPolicies,
  updateErrorPolicy,
  type ErrorPolicySummary
} from '../../storage/error-policy.repository.js'
import { bodyField, mutationGuard, normalizedText } from '../deduplication/mutation-guard.middleware.js'
import { getRequestAuthContext } from '../auth/request-context.js'
import { recordOperationLog, safeChange } from '../operation-logs/operation-log.service.js'

export const errorPoliciesRouter = Router()

const textListSchema = z.array(z.string().trim().min(1).max(200)).max(50).optional()

const matchSchema = z.object({
  statusCodes: z.array(z.number().int().min(100).max(599)).max(50).optional(),
  errorCodes: textListSchema,
  errorTypes: textListSchema,
  keywords: textListSchema
}).strict().partial().optional()

const policyBodySchema = z.object({
  name: z.string().trim().min(1, '策略名称不能为空').max(100, '策略名称不能超过 100 个字符'),
  enabled: z.boolean().optional(),
  priority: z.number().int().min(1).max(9999).optional(),
  scopeType: z.enum(['global', 'protocol', 'provider', 'client', 'model'], {
    required_error: '请选择请求错误策略作用层级',
    invalid_type_error: '请求错误策略作用层级无效'
  }),
  providerCode: z.string().trim().min(1, '请选择供应商').max(80, '供应商编码不能超过 80 个字符').nullable().optional(),
  clientProfile: z.string().trim().min(1, '请选择客户端').max(80, '客户端标识不能超过 80 个字符').nullable().optional(),
  modelPattern: z.string().trim().min(1, '请填写模型匹配值').max(120, '模型匹配值不能超过 120 个字符').nullable().optional(),
  modelMatchType: z.enum(['exact', 'prefix', 'contains']).nullable().optional(),
  match: matchSchema,
  action: z.enum(['retry_next', 'temp_unschedulable', 'rate_limited', 'error_disabled']),
  resetStrategy: z.enum(['duration', 'daily', 'weekly']).nullable().optional(),
  durationHours: z.number().int().positive().nullable().optional(),
  dailyResetHour: z.number().int().min(0).max(23).nullable().optional(),
  weeklyResetDay: z.number().int().min(0).max(6).nullable().optional(),
  weeklyResetHour: z.number().int().min(0).max(23).nullable().optional(),
  notes: z.string().trim().max(1000, '备注不能超过 1000 个字符').nullable().optional()
}).strict().superRefine((value, context) => {
  if (value.scopeType === 'provider' && !value.providerCode?.trim()) {
    context.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['providerCode'],
      message: '供应商层请求错误策略必须选择供应商'
    })
  }
  if (value.scopeType === 'client' && !value.clientProfile?.trim()) {
    context.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['clientProfile'],
      message: '客户端层请求错误策略必须选择客户端'
    })
  }
  if (value.scopeType === 'model' && !value.modelPattern?.trim()) {
    context.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['modelPattern'],
      message: '模型层请求错误策略必须填写模型匹配值'
    })
  }
  if (value.scopeType !== 'provider' && value.scopeType !== 'model' && value.providerCode?.trim()) {
    context.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['providerCode'],
      message: '当前层级不能绑定供应商'
    })
  }
  if (value.scopeType !== 'client' && value.clientProfile?.trim()) {
    context.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['clientProfile'],
      message: '当前层级不能绑定客户端'
    })
  }
  if (value.scopeType !== 'model' && value.modelPattern?.trim()) {
    context.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['modelPattern'],
      message: '当前层级不能绑定模型'
    })
  }
  if (value.action === 'rate_limited') {
    if (!value.resetStrategy) {
      context.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['resetStrategy'],
        message: '限流策略必须选择恢复策略'
      })
    } else if (value.resetStrategy === 'duration' && !value.durationHours) {
      context.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['durationHours'],
        message: '固定时长恢复需要填写恢复小时数'
      })
    } else if (value.resetStrategy === 'daily' && value.dailyResetHour === undefined) {
      context.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['dailyResetHour'],
        message: '每天固定时间恢复需要填写恢复小时'
      })
    } else if (value.resetStrategy === 'weekly' && (value.weeklyResetDay === undefined || value.weeklyResetHour === undefined)) {
      context.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['weeklyResetDay'],
        message: '每周固定时间恢复需要填写星期和恢复小时'
      })
    }
  }
  const matcher = value.match ?? {}
  const hasMatcher = [
    matcher.statusCodes,
    matcher.errorCodes,
    matcher.errorTypes,
    matcher.keywords
  ].some((items) => Array.isArray(items) && items.length > 0)
  if (!hasMatcher) {
    context.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['match'],
      message: '至少需要填写一个匹配条件'
    })
  }
  if (matcher.statusCodes?.some((code) => code >= 200 && code <= 299)) {
    context.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['match', 'statusCodes'],
      message: '请求错误策略不能匹配 2xx 成功状态码'
    })
  }
})

errorPoliciesRouter.get('/', (_req, res) => {
  const result = listErrorPolicies()
  res.json(ok({
    policies: result.policies.map(publicPolicySummary)
  }))
})

errorPoliciesRouter.post('/', mutationGuard({
  operationKey: 'error_policies.create',
  fingerprint: (req) => ({
    name: normalizedText(bodyField(req, 'name')),
    scopeType: bodyField(req, 'scopeType'),
    providerCode: normalizedText(bodyField(req, 'providerCode')),
    clientProfile: normalizedText(bodyField(req, 'clientProfile')),
    modelPattern: normalizedText(bodyField(req, 'modelPattern')),
    priority: bodyField(req, 'priority')
  })
}), (req, res) => {
  const parsed = policyBodySchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '请求错误策略参数无效')))
    return
  }
  let policy: ErrorPolicySummary
  try {
    policy = createErrorPolicy(withProtocol(parsed.data))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '请求错误策略创建失败'))
    return
  }
  recordPolicyOperation(req, 'create', policy.id, policy.name, operationChanges(policy))
  res.status(201).json(ok(publicPolicySummary(policy)))
})

errorPoliciesRouter.patch('/:id', mutationGuard({
  operationKey: 'error_policies.update',
  fingerprint: (req) => ({
    id: req.params.id,
    name: normalizedText(bodyField(req, 'name')),
    scopeType: bodyField(req, 'scopeType'),
    providerCode: normalizedText(bodyField(req, 'providerCode')),
    clientProfile: normalizedText(bodyField(req, 'clientProfile')),
    modelPattern: normalizedText(bodyField(req, 'modelPattern')),
    enabled: bodyField(req, 'enabled'),
    priority: bodyField(req, 'priority'),
    updatedAt: Date.now()
  })
}), (req, res) => {
  const parsed = policyBodySchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '请求错误策略参数无效')))
    return
  }
  let policy: ErrorPolicySummary | undefined
  try {
    policy = updateErrorPolicy(req.params.id, withProtocol(parsed.data))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '请求错误策略更新失败'))
    return
  }
  if (!policy) {
    res.status(404).json({ message: '请求错误策略不存在' })
    return
  }
  recordPolicyOperation(req, 'update', policy.id, policy.name, operationChanges(policy))
  res.json(ok(publicPolicySummary(policy)))
})

errorPoliciesRouter.delete('/:id', mutationGuard({
  operationKey: 'error_policies.delete',
  fingerprint: (req) => ({ id: req.params.id })
}), (req, res) => {
  const deleted = deleteErrorPolicy(req.params.id)
  if (!deleted) {
    res.status(404).json({ message: '请求错误策略不存在' })
    return
  }
  recordPolicyOperation(req, 'delete', req.params.id, req.params.id, [
    safeChange('deleted', '删除', undefined, true)
  ])
  res.json(ok({ deleted }))
})

function withProtocol(input: z.infer<typeof policyBodySchema>): z.infer<typeof policyBodySchema> & { protocolCode?: string } {
  return input.scopeType === 'global'
    ? input
    : { ...input, protocolCode: OPENAI_PROTOCOL_CODE }
}

function operationChanges(policy: ErrorPolicySummary): NonNullable<Parameters<typeof recordOperationLog>[0]['changes']> {
  return [
    safeChange('name', '策略名称', undefined, policy.name),
    safeChange('scopeType', '作用层级', undefined, policy.scopeType),
    safeChange('providerCode', '供应商', undefined, policy.providerCode ?? ''),
    safeChange('clientProfile', '客户端', undefined, policy.clientProfile ?? ''),
    safeChange('modelPattern', '模型', undefined, policy.modelPattern ?? ''),
    safeChange('enabled', '启用状态', undefined, policy.enabled),
    safeChange('priority', '优先级', undefined, policy.priority)
  ]
}

function recordPolicyOperation(
  req: Parameters<typeof recordOperationLog>[1],
  action: 'create' | 'update' | 'delete',
  policyId: string,
  policyName: string,
  changes: NonNullable<Parameters<typeof recordOperationLog>[0]['changes']>
): void {
  const actor = getRequestAuthContext()?.systemAccountId
  recordOperationLog({
    module: 'error_policies',
    action,
    operationKey: `error_policies.${action}`,
    resourceType: 'error_policy',
    resourceId: policyId,
    resourceName: policyName,
    summary: `${operationActionText(action)}请求错误策略：${policyName}`,
    detailLevel: 'full',
    visibilityScope: 'admin_only',
    changes,
    metadata: {
      policyId,
      actorSystemAccountId: actor
    }
  }, req)
}

function operationActionText(action: 'create' | 'update' | 'delete'): string {
  if (action === 'create') return '创建'
  if (action === 'update') return '更新'
  return '删除'
}

type PublicErrorPolicySummary = ErrorPolicySummary

function publicPolicySummary(policy: ErrorPolicySummary): PublicErrorPolicySummary {
  return policy
}

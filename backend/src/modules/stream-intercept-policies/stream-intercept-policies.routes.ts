import { Router } from 'express'
import { z } from 'zod'

import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import {
  createStreamInterceptPolicy,
  deleteStreamInterceptPolicy,
  listStreamInterceptPolicies,
  updateStreamInterceptPolicy,
  type StreamInterceptPolicySummary
} from '../../storage/stream-intercept-policy.repository.js'
import { bodyField, mutationGuard, normalizedText } from '../deduplication/mutation-guard.middleware.js'
import { getRequestAuthContext } from '../auth/request-context.js'
import { recordOperationLog, safeChange } from '../operation-logs/operation-log.service.js'
import { OPENAI_PROTOCOL_CODE } from '../../domain/provider-protocol.js'

export const streamInterceptPoliciesRouter = Router()

const textListSchema = z.array(z.string().trim().min(1).max(200)).max(50).optional()

const matchSchema = z.object({
  eventTypes: textListSchema,
  dataTypes: textListSchema,
  errorCodes: textListSchema,
  errorTypes: textListSchema,
  textIncludes: textListSchema,
  textExcludes: textListSchema,
  jsonPathsExists: textListSchema
}).strict().partial().optional()

const policyBodySchema = z.object({
  name: z.string().trim().min(1, '规则名称不能为空').max(100, '规则名称不能超过 100 个字符'),
  enabled: z.boolean().optional(),
  priority: z.number().int().min(1).max(9999).optional(),
  scopeType: z.enum(['protocol', 'provider'], {
    required_error: '请选择流式拦截策略作用层级',
    invalid_type_error: '流式拦截策略作用层级无效'
  }),
  providerCode: z.string().trim().min(1, '请选择供应商').max(80, '供应商编码不能超过 80 个字符').nullable().optional(),
  match: matchSchema,
  action: z.enum([
    'observe',
    'drop_event',
    'retry_no_avoidance',
    'retry_next_account',
    'avoid_account_ttl',
    'avoid_upstream_bucket_ttl'
  ]),
  notes: z.string().trim().max(1000, '备注不能超过 1000 个字符').nullable().optional()
}).strict().superRefine((value, context) => {
  if (value.scopeType === 'protocol' && value.providerCode?.trim()) {
    context.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['providerCode'],
      message: '协议层流式拦截策略不能绑定供应商'
    })
  }
  if (value.scopeType === 'provider' && !value.providerCode?.trim()) {
    context.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['providerCode'],
      message: '供应商层流式拦截策略必须选择供应商'
    })
  }
  const matcher = value.match ?? {}
  const hasMatcher = [
    matcher.eventTypes,
    matcher.dataTypes,
    matcher.errorCodes,
    matcher.errorTypes,
    matcher.textIncludes,
    matcher.jsonPathsExists
  ].some((items) => Array.isArray(items) && items.length > 0)
  if (!hasMatcher) {
    context.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['match'],
      message: '至少需要填写一个匹配条件'
    })
  }
})

streamInterceptPoliciesRouter.get('/', (_req, res) => {
  const result = listStreamInterceptPolicies()
  res.json(ok({
    defaultRules: result.defaultRules.map(publicPolicySummary),
    policies: result.policies.map(publicPolicySummary)
  }))
})

streamInterceptPoliciesRouter.post('/', mutationGuard({
  operationKey: 'stream_intercept_policies.create',
  fingerprint: (req) => ({
    name: normalizedText(bodyField(req, 'name')),
    scopeType: bodyField(req, 'scopeType'),
    providerCode: normalizedText(bodyField(req, 'providerCode')),
    priority: bodyField(req, 'priority')
  })
}), (req, res) => {
  const parsed = policyBodySchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '流式拦截策略参数无效')))
    return
  }
  let policy: StreamInterceptPolicySummary
  try {
    policy = createStreamInterceptPolicy({ ...parsed.data, protocolCode: OPENAI_PROTOCOL_CODE })
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '流式拦截策略创建失败'))
    return
  }
  recordPolicyOperation(req, 'create', policy.id, policy.name, [
    safeChange('name', '规则名称', undefined, policy.name),
    safeChange('scopeType', '作用层级', undefined, policy.scopeType),
    safeChange('providerCode', '供应商', undefined, policy.providerCode ?? ''),
    safeChange('enabled', '启用状态', undefined, policy.enabled),
    safeChange('priority', '优先级', undefined, policy.priority)
  ])
  res.status(201).json(ok(publicPolicySummary(policy)))
})

streamInterceptPoliciesRouter.patch('/:id', mutationGuard({
  operationKey: 'stream_intercept_policies.update',
  fingerprint: (req) => ({
    id: req.params.id,
    name: normalizedText(bodyField(req, 'name')),
    scopeType: bodyField(req, 'scopeType'),
    providerCode: normalizedText(bodyField(req, 'providerCode')),
    enabled: bodyField(req, 'enabled'),
    priority: bodyField(req, 'priority'),
    updatedAt: Date.now()
  })
}), (req, res) => {
  const parsed = policyBodySchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '流式拦截策略参数无效')))
    return
  }
  let policy: StreamInterceptPolicySummary | undefined
  try {
    policy = updateStreamInterceptPolicy(req.params.id, { ...parsed.data, protocolCode: OPENAI_PROTOCOL_CODE })
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '流式拦截策略更新失败'))
    return
  }
  if (!policy) {
    res.status(404).json({ message: '流式拦截策略不存在' })
    return
  }
  recordPolicyOperation(req, 'update', policy.id, policy.name, [
    safeChange('name', '规则名称', undefined, policy.name),
    safeChange('scopeType', '作用层级', undefined, policy.scopeType),
    safeChange('providerCode', '供应商', undefined, policy.providerCode ?? ''),
    safeChange('enabled', '启用状态', undefined, policy.enabled),
    safeChange('priority', '优先级', undefined, policy.priority)
  ])
  res.json(ok(publicPolicySummary(policy)))
})

streamInterceptPoliciesRouter.delete('/:id', mutationGuard({
  operationKey: 'stream_intercept_policies.delete',
  fingerprint: (req) => ({ id: req.params.id })
}), (req, res) => {
  const deleted = deleteStreamInterceptPolicy(req.params.id)
  if (!deleted) {
    res.status(404).json({ message: '流式拦截策略不存在' })
    return
  }
  recordPolicyOperation(req, 'delete', req.params.id, req.params.id, [
    safeChange('deleted', '删除', undefined, true)
  ])
  res.json(ok({ deleted }))
})

function recordPolicyOperation(
  req: Parameters<typeof recordOperationLog>[1],
  action: 'create' | 'update' | 'delete',
  policyId: string,
  policyName: string,
  changes: NonNullable<Parameters<typeof recordOperationLog>[0]['changes']>
): void {
  const actor = getRequestAuthContext()?.systemAccountId
  recordOperationLog({
    module: 'stream_intercept_policies',
    action,
    operationKey: `stream_intercept_policies.${action}`,
    resourceType: 'stream_intercept_policy',
    resourceId: policyId,
    resourceName: policyName,
    summary: `${operationActionText(action)}流式拦截策略：${policyName}`,
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

type PublicStreamInterceptPolicySummary = StreamInterceptPolicySummary

function publicPolicySummary(policy: StreamInterceptPolicySummary): PublicStreamInterceptPolicySummary {
  return policy
}

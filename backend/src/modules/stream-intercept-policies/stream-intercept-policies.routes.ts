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
}).partial().optional()

const policyBodySchema = z.object({
  name: z.string().trim().min(1, '规则名称不能为空').max(100, '规则名称不能超过 100 个字符'),
  enabled: z.boolean().optional(),
  executionMode: z.enum(['intercept', 'dry_run']).optional(),
  priority: z.coerce.number().int().min(1).max(9999).optional(),
  match: matchSchema,
  dataHandling: z.enum(['discard_event', 'discard_stream', 'replace_with_failure']).optional(),
  retryEnabled: z.boolean().optional(),
  accountSwitch: z.enum(['none', 'request_next_account', 'avoid_account_ttl', 'avoid_upstream_bucket_ttl']).optional(),
  accountState: z.enum(['none', 'runtime_avoidance']).optional(),
  avoidanceTtlSeconds: z.coerce.number().int().min(1).max(86400).nullable().optional(),
  notes: z.string().trim().max(1000, '备注不能超过 1000 个字符').nullable().optional()
}).superRefine((value, context) => {
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
  if (value.retryEnabled === true && value.dataHandling === 'discard_event') {
    context.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['dataHandling'],
      message: '需要重试时不能只丢弃命中事件'
    })
  }
})

streamInterceptPoliciesRouter.get('/', (_req, res) => {
  const result = listStreamInterceptPolicies()
  res.json(ok({
    presets: result.presets.map(publicPolicySummary),
    policies: result.policies.map(publicPolicySummary)
  }))
})

streamInterceptPoliciesRouter.post('/', mutationGuard({
  operationKey: 'stream_intercept_policies.create',
  fingerprint: (req) => ({
    name: normalizedText(bodyField(req, 'name')),
    priority: bodyField(req, 'priority')
  })
}), (req, res) => {
  const parsed = policyBodySchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '流式拦截策略参数无效')))
    return
  }
  const policy = createStreamInterceptPolicy(parsed.data)
  recordPolicyOperation(req, 'create', policy.id, policy.name, [
    safeChange('name', '规则名称', undefined, policy.name),
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
  const policy = updateStreamInterceptPolicy(req.params.id, parsed.data)
  if (!policy) {
    res.status(404).json({ message: '流式拦截策略不存在' })
    return
  }
  recordPolicyOperation(req, 'update', policy.id, policy.name, [
    safeChange('name', '规则名称', undefined, policy.name),
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

type PublicStreamInterceptPolicySummary = Omit<StreamInterceptPolicySummary, 'providerCode'>

function publicPolicySummary({ providerCode: _providerCode, ...policy }: StreamInterceptPolicySummary): PublicStreamInterceptPolicySummary {
  return policy
}

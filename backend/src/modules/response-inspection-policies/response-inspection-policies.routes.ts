import { Router } from 'express'
import { z } from 'zod'

import { ANTHROPIC_PROTOCOL_CODE, GEMINI_PROTOCOL_CODE, OPENAI_PROTOCOL_CODE } from '../../domain/provider-protocol.js'
import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import {
  createResponseInspectionPolicyAsync,
  deleteResponseInspectionPolicyAsync,
  listResponseInspectionPoliciesAsync,
  updateResponseInspectionPolicyAsync,
  type ResponseInspectionPolicySummary
} from '../../storage/response-inspection-policy.repository.js'
import { getRequestAuthContext } from '../auth/request-context.js'
import { bodyField, mutationGuard, normalizedText } from '../deduplication/mutation-guard.middleware.js'
import { recordOperationLogAsync, safeChange } from '../operation-logs/operation-log.service.js'

export const responseInspectionPoliciesRouter = Router()

const textListSchema = z.array(z.string().trim().min(1).max(200)).max(50).optional()
const clientProfileListSchema = z.array(z.enum(['codex', 'generic_openai', 'claude_code', 'generic_anthropic', 'generic_gemini', 'gemini_cli'])).max(6).optional()

const matchSchema = z.object({
  clientProfiles: clientProfileListSchema,
  outputTextIncludes: textListSchema,
  outputTextExcludes: textListSchema,
  errorCodes: textListSchema,
  errorTypes: textListSchema,
  errorMessageIncludes: textListSchema,
  finishReasons: textListSchema,
  jsonPathsExists: textListSchema,
  rawTextIncludes: textListSchema
}).strict().partial().optional()

const policyBodySchema = z.object({
  name: z.string().trim().min(1, '规则名称不能为空').max(100, '规则名称不能超过 100 个字符'),
  enabled: z.boolean().optional(),
  priority: z.number().int().min(1).max(9999).optional(),
  scopeType: z.enum(['protocol', 'provider'], {
    required_error: '请选择响应检查策略作用层级',
    invalid_type_error: '响应检查策略作用层级无效'
  }),
  protocolCode: z.enum([OPENAI_PROTOCOL_CODE, ANTHROPIC_PROTOCOL_CODE, GEMINI_PROTOCOL_CODE], {
    required_error: '请选择响应检查策略协议',
    invalid_type_error: '响应检查策略协议无效'
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
      message: '协议层响应检查策略不能绑定供应商'
    })
  }
  if (value.scopeType === 'provider' && !value.providerCode?.trim()) {
    context.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['providerCode'],
      message: '供应商层响应检查策略必须选择供应商'
    })
  }
  const matcher = value.match ?? {}
  const hasMatcher = [
    matcher.outputTextIncludes,
    matcher.errorCodes,
    matcher.errorTypes,
    matcher.errorMessageIncludes,
    matcher.finishReasons,
    matcher.jsonPathsExists,
    matcher.rawTextIncludes
  ].some((items) => Array.isArray(items) && items.length > 0)
  if (!hasMatcher) {
    context.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['match'],
      message: '至少需要填写一个匹配条件'
    })
  }
})

responseInspectionPoliciesRouter.get('/', async (_req, res, next) => {
  try {
    const result = await listResponseInspectionPoliciesAsync()
    res.json(ok({
      defaultRules: result.defaultRules.map(publicPolicySummary),
      policies: result.policies.map(publicPolicySummary)
    }))
  } catch (error) {
    next(error)
  }
})

responseInspectionPoliciesRouter.post('/', mutationGuard({
  operationKey: 'response_inspection_policies.create',
  fingerprint: (req) => ({
    name: normalizedText(bodyField(req, 'name')),
    scopeType: bodyField(req, 'scopeType'),
    protocolCode: bodyField(req, 'protocolCode'),
    providerCode: normalizedText(bodyField(req, 'providerCode')),
    priority: bodyField(req, 'priority')
  })
}), async (req, res) => {
  const parsed = policyBodySchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '响应检查策略参数无效')))
    return
  }
  let policy: ResponseInspectionPolicySummary
  try {
    policy = await createResponseInspectionPolicyAsync(parsed.data)
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '响应检查策略创建失败'))
    return
  }
  await recordPolicyOperation(req, 'create', policy.id, policy.name, [
    safeChange('name', '规则名称', undefined, policy.name),
    safeChange('protocolCode', '协议', undefined, policy.protocolCode),
    safeChange('scopeType', '作用层级', undefined, policy.scopeType),
    safeChange('providerCode', '供应商', undefined, policy.providerCode ?? ''),
    safeChange('enabled', '启用状态', undefined, policy.enabled),
    safeChange('priority', '优先级', undefined, policy.priority)
  ])
  res.status(201).json(ok(publicPolicySummary(policy)))
})

responseInspectionPoliciesRouter.put('/:id', mutationGuard({
  operationKey: 'response_inspection_policies.update',
  fingerprint: (req) => ({
    id: req.params.id,
    name: normalizedText(bodyField(req, 'name')),
    scopeType: bodyField(req, 'scopeType'),
    protocolCode: bodyField(req, 'protocolCode'),
    providerCode: normalizedText(bodyField(req, 'providerCode')),
    enabled: bodyField(req, 'enabled'),
    priority: bodyField(req, 'priority'),
    updatedAt: Date.now()
  })
}), async (req, res) => {
  const parsed = policyBodySchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '响应检查策略参数无效')))
    return
  }
  let policy: ResponseInspectionPolicySummary | undefined
  try {
    policy = await updateResponseInspectionPolicyAsync(req.params.id, parsed.data)
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '响应检查策略更新失败'))
    return
  }
  if (!policy) {
    res.status(404).json({ message: '响应检查策略不存在' })
    return
  }
  await recordPolicyOperation(req, 'update', policy.id, policy.name, [
    safeChange('name', '规则名称', undefined, policy.name),
    safeChange('protocolCode', '协议', undefined, policy.protocolCode),
    safeChange('scopeType', '作用层级', undefined, policy.scopeType),
    safeChange('providerCode', '供应商', undefined, policy.providerCode ?? ''),
    safeChange('enabled', '启用状态', undefined, policy.enabled),
    safeChange('priority', '优先级', undefined, policy.priority)
  ])
  res.json(ok(publicPolicySummary(policy)))
})

responseInspectionPoliciesRouter.delete('/:id', mutationGuard({
  operationKey: 'response_inspection_policies.delete',
  fingerprint: (req) => ({ id: req.params.id })
}), async (req, res) => {
  const deleted = await deleteResponseInspectionPolicyAsync(req.params.id)
  if (!deleted) {
    res.status(404).json({ message: '响应检查策略不存在' })
    return
  }
  await recordPolicyOperation(req, 'delete', req.params.id, req.params.id, [
    safeChange('deleted', '删除', undefined, true)
  ])
  res.json(ok({ deleted }))
})

function recordPolicyOperation(
  req: Parameters<typeof recordOperationLogAsync>[1],
  action: 'create' | 'update' | 'delete',
  policyId: string,
  policyName: string,
  changes: NonNullable<Parameters<typeof recordOperationLogAsync>[0]['changes']>
): Promise<void> {
  const actor = getRequestAuthContext()?.systemAccountId
  return recordOperationLogAsync({
    module: 'response_inspection_policies',
    action,
    operationKey: `response_inspection_policies.${action}`,
    resourceType: 'response_inspection_policy',
    resourceId: policyId,
    resourceName: policyName,
    summary: `${operationActionText(action)}响应检查策略：${policyName}`,
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

type PublicResponseInspectionPolicySummary = ResponseInspectionPolicySummary

function publicPolicySummary(policy: ResponseInspectionPolicySummary): PublicResponseInspectionPolicySummary {
  return policy
}

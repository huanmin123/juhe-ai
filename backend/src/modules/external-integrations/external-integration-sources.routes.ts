import { Router, type Request, type Response } from 'express'
import { z } from 'zod'

import { badRequest, firstIssueMessage, ok } from '../../shared/http.js'
import {
  builtInExternalIntegrationTestSourceId,
  externalIntegrationScopeOptions
} from '../../storage/external-integration-source-constants.js'
import { optionalServerDateTimeIso } from '../../storage/value-utils.js'
import {
  createExternalIntegrationSourceAuthorizationAsync,
  createExternalIntegrationSourceTokenAsync,
  deleteExternalIntegrationSourceAsync,
  findExternalIntegrationSourceAsync,
  findExternalIntegrationSourceTokenSecretAsync,
  listExternalIntegrationSourcesAsync,
  resetBuiltInExternalIntegrationTestTokenAsync,
  updateExternalIntegrationSourceAsync,
  updateExternalIntegrationSourceTokenAsync
} from '../../storage/external-integration-source.repository.js'
import { getRequestAuthContext } from '../auth/request-context.js'
import { bodyField, mutationGuard } from '../deduplication/mutation-guard.middleware.js'
import { recordOperationLogAsync, safeChange } from '../operation-logs/operation-log.service.js'
import { getExternalPublicApiCatalog } from './external-public-api-catalog.js'

export const externalIntegrationSourcesRouter = Router()

const rateLimitRuleSchema = z.object({
  windowSeconds: z.number().int().min(1, '限频窗口不能小于 1 秒').max(86400, '限频窗口不能超过 86400 秒'),
  maxRequests: z.number().int().min(1, '限频次数不能小于 1').max(100000, '限频次数不能超过 100000')
}).strict()

const expiresAtSchema = z.union([
  z.string().trim().min(1, '过期时间无效').refine((value) => Boolean(optionalServerDateTimeIso(value)), '过期时间无效'),
  z.null()
]).optional()

const sourceBodySchema = z.object({
  name: z.string().trim().min(1, '来源系统名称不能为空').max(80, '来源系统名称不能超过 80 个字符'),
  status: z.enum(['active', 'disabled']).optional(),
  scopes: z.array(z.string().trim().min(1)).optional(),
  rateLimits: z.array(rateLimitRuleSchema).max(8, '限频规则最多 8 条').optional(),
  expiresAt: expiresAtSchema,
  notes: z.string().trim().max(500, '备注不能超过 500 个字符').nullable().optional()
}).strict()

const sourceUpdateBodySchema = sourceBodySchema.partial()

const tokenBodySchema = z.object({
  name: z.string().trim().min(1, 'Token 名称不能为空').max(80, 'Token 名称不能超过 80 个字符'),
  status: z.enum(['active', 'disabled', 'revoked']).optional(),
  scopes: z.array(z.string().trim().min(1)).optional(),
  expiresAt: expiresAtSchema
}).strict()

const tokenUpdateBodySchema = tokenBodySchema.partial()

const listQuerySchema = z.object({
  page: z.coerce.number().int().min(1).optional(),
  pageSize: z.coerce.number().int().min(1).max(100).optional(),
  keyword: z.string().trim().optional(),
  status: z.enum(['all', 'active', 'disabled']).optional()
})

const idParamSchema = z.object({
  id: z.string().trim().min(1, '来源系统不存在')
})

const tokenParamSchema = z.object({
  id: z.string().trim().min(1, '来源系统不存在'),
  tokenId: z.string().trim().min(1, 'Token 不存在')
})

externalIntegrationSourcesRouter.get('/scopes', (_req, res) => {
  res.json(ok(externalIntegrationScopeOptions))
})

externalIntegrationSourcesRouter.get('/api-docs', (_req, res) => {
  res.json(ok(getExternalPublicApiCatalog()))
})

externalIntegrationSourcesRouter.get('/', async (req, res, next) => {
  const parsed = listQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '来源系统列表参数无效')))
    return
  }
  try {
    res.json(ok(await listExternalIntegrationSourcesAsync(parsed.data)))
  } catch (error) {
    next(error)
  }
})

externalIntegrationSourcesRouter.post('/built-in-test-token/reset', mutationGuard({
  operationKey: 'external_integration_sources.reset_builtin_test_token',
  fingerprint: () => ({
    target: 'built_in_test_token'
  })
}), async (req, res) => {
  try {
    const token = await resetBuiltInExternalIntegrationTestTokenAsync()
    const source = await findExternalIntegrationSourceAsync(builtInExternalIntegrationTestSourceId)
    await recordSourceOperation(req, {
      action: 'reset_builtin_test_token',
      operationKey: 'external_integration_sources.reset_builtin_test_token',
      sourceRefId: source?.id ?? 'built_in_test_token',
      sourceName: source?.name ?? '内置测试 Token',
      summary: '重置内置测试 Token',
      changes: [
        safeChange('tokenPreview', 'Token 标识', undefined, `${token.tokenPrefix}...${token.tokenSuffix}`)
      ]
    })
    setSecretResponseHeaders(res)
    res.json(ok({
      token,
      source
    }))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '重置内置测试 Token 失败'))
  }
})

externalIntegrationSourcesRouter.post('/', mutationGuard({
  operationKey: 'external_integration_sources.create',
  fingerprint: (req) => ({
    name: bodyField(req, 'name')
  })
}), async (req, res) => {
  const parsed = sourceBodySchema.safeParse(req.body ?? {})
  if (!parsed.success) {
    res.status(400).json(badRequest(firstIssueMessage(parsed.error, '来源系统参数无效')))
    return
  }
  try {
    const created = await createExternalIntegrationSourceAuthorizationAsync(parsed.data)
    const source = created.source
    await recordSourceOperation(req, {
      action: 'create',
      operationKey: 'external_integration_sources.create',
      sourceRefId: source.id,
      sourceName: source.name,
      summary: `创建外部来源系统：${source.name}`,
      changes: [
        safeChange('name', '名称', undefined, source.name),
        safeChange('status', '状态', undefined, source.status),
        safeChange('expiresAt', '到期时间', undefined, source.expiresAt),
        safeChange('rateLimits', '限频规则', undefined, formatRateLimits(source.rateLimits))
      ]
    })
    setSecretResponseHeaders(res)
    res.status(201).json(ok(created))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '来源系统创建失败'))
  }
})

externalIntegrationSourcesRouter.get('/:id', async (req, res, next) => {
  const params = idParamSchema.safeParse(req.params)
  if (!params.success) {
    res.status(400).json(badRequest(firstIssueMessage(params.error, '来源系统不存在')))
    return
  }
  try {
    const source = await findExternalIntegrationSourceAsync(params.data.id)
    if (!source) {
      res.status(404).json({ message: '来源系统不存在' })
      return
    }
    res.json(ok(source))
  } catch (error) {
    next(error)
  }
})

externalIntegrationSourcesRouter.patch('/:id', mutationGuard({
  operationKey: 'external_integration_sources.update',
  fingerprint: (req) => ({
    id: req.params.id,
    name: bodyField(req, 'name'),
    status: bodyField(req, 'status'),
    expiresAt: bodyField(req, 'expiresAt'),
    rateLimits: bodyField(req, 'rateLimits')
  })
}), async (req, res) => {
  const params = idParamSchema.safeParse(req.params)
  const body = sourceUpdateBodySchema.safeParse(req.body ?? {})
  if (!params.success) {
    res.status(400).json(badRequest(firstIssueMessage(params.error, '来源系统不存在')))
    return
  }
  if (!body.success) {
    res.status(400).json(badRequest(firstIssueMessage(body.error, '来源系统参数无效')))
    return
  }
  const before = await findExternalIntegrationSourceAsync(params.data.id)
  let source: Awaited<ReturnType<typeof updateExternalIntegrationSourceAsync>>
  try {
    source = await updateExternalIntegrationSourceAsync(params.data.id, body.data)
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '来源系统更新失败'))
    return
  }
  if (!source) {
    res.status(404).json({ message: '来源系统不存在' })
    return
  }
  await recordSourceOperation(req, {
    action: 'update',
    operationKey: 'external_integration_sources.update',
    sourceRefId: source.id,
    sourceName: source.name,
    summary: `更新外部来源系统：${source.name}`,
    changes: [
      safeChange('name', '名称', before?.name, source.name),
      safeChange('status', '状态', before?.status, source.status),
      safeChange('expiresAt', '到期时间', before?.expiresAt, source.expiresAt),
      safeChange('rateLimits', '限频规则', formatRateLimits(before?.rateLimits ?? []), formatRateLimits(source.rateLimits))
    ]
  })
  res.json(ok(source))
})

externalIntegrationSourcesRouter.delete('/:id', mutationGuard({
  operationKey: 'external_integration_sources.delete',
  fingerprint: (req) => ({
    id: req.params.id
  })
}), async (req, res) => {
  const params = idParamSchema.safeParse(req.params)
  if (!params.success) {
    res.status(400).json(badRequest(firstIssueMessage(params.error, '来源系统不存在')))
    return
  }
  const before = await findExternalIntegrationSourceAsync(params.data.id)
  try {
    if (!await deleteExternalIntegrationSourceAsync(params.data.id)) {
      res.status(404).json({ message: '来源系统不存在' })
      return
    }
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '删除来源授权失败'))
    return
  }
  await recordSourceOperation(req, {
    action: 'delete',
    operationKey: 'external_integration_sources.delete',
    sourceRefId: params.data.id,
    sourceName: before?.name ?? params.data.id,
    summary: `删除外部来源系统：${before?.name ?? params.data.id}`,
    changes: [
      safeChange('deleted', '删除状态', false, true),
      safeChange('tokenCount', '关联 Token 数量', before?.tokenCount ?? 0, 0)
    ]
  })
  res.status(204).send()
})

externalIntegrationSourcesRouter.post('/:id/tokens', mutationGuard({
  operationKey: 'external_integration_sources.create_token',
  fingerprint: (req) => ({
    id: req.params.id,
    name: bodyField(req, 'name'),
    expiresAt: bodyField(req, 'expiresAt')
  })
}), async (req, res) => {
  const params = idParamSchema.safeParse(req.params)
  const body = tokenBodySchema.safeParse(req.body ?? {})
  if (!params.success) {
    res.status(400).json(badRequest(firstIssueMessage(params.error, '来源系统不存在')))
    return
  }
  if (!body.success) {
    res.status(400).json(badRequest(firstIssueMessage(body.error, 'Token 参数无效')))
    return
  }
  try {
    const token = await createExternalIntegrationSourceTokenAsync({
      sourceRefId: params.data.id,
      ...body.data
    })
    const source = await findExternalIntegrationSourceAsync(params.data.id)
    await recordSourceOperation(req, {
      action: 'create_token',
      operationKey: 'external_integration_sources.create_token',
      sourceRefId: params.data.id,
      sourceName: source?.name ?? params.data.id,
      summary: `生成外部来源系统 Token：${source?.name ?? params.data.id}`,
      changes: [
        safeChange('tokenName', 'Token 名称', undefined, token.name),
        safeChange('tokenPreview', 'Token 标识', undefined, `${token.tokenPrefix}...${token.tokenSuffix}`),
        safeChange('expiresAt', '到期时间', undefined, token.expiresAt)
      ]
    })
    setSecretResponseHeaders(res)
    res.status(201).json(ok({
      token,
      source
    }))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : 'Token 创建失败'))
  }
})

externalIntegrationSourcesRouter.get('/:id/tokens/:tokenId/secret', async (req, res) => {
  const params = tokenParamSchema.safeParse(req.params)
  if (!params.success) {
    res.status(400).json(badRequest(firstIssueMessage(params.error, 'Token 不存在')))
    return
  }
  try {
    const token = await findExternalIntegrationSourceTokenSecretAsync(params.data.id, params.data.tokenId)
    if (!token) {
      res.status(404).json({ message: 'Token 不存在' })
      return
    }
    setSecretResponseHeaders(res)
    res.json(ok(token))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : '读取 Token 失败'))
  }
})

externalIntegrationSourcesRouter.patch('/:id/tokens/:tokenId', mutationGuard({
  operationKey: 'external_integration_sources.update_token',
  fingerprint: (req) => ({
    id: req.params.id,
    tokenId: req.params.tokenId,
    name: bodyField(req, 'name'),
    status: bodyField(req, 'status'),
    expiresAt: bodyField(req, 'expiresAt')
  })
}), async (req, res) => {
  const params = tokenParamSchema.safeParse(req.params)
  const body = tokenUpdateBodySchema.safeParse(req.body ?? {})
  if (!params.success) {
    res.status(400).json(badRequest(firstIssueMessage(params.error, 'Token 不存在')))
    return
  }
  if (!body.success) {
    res.status(400).json(badRequest(firstIssueMessage(body.error, 'Token 参数无效')))
    return
  }
  let token: Awaited<ReturnType<typeof updateExternalIntegrationSourceTokenAsync>>
  try {
    token = await updateExternalIntegrationSourceTokenAsync(params.data.id, params.data.tokenId, body.data)
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : 'Token 更新失败'))
    return
  }
  if (!token) {
    res.status(404).json({ message: 'Token 不存在' })
    return
  }
  const source = await findExternalIntegrationSourceAsync(params.data.id)
  await recordSourceOperation(req, {
    action: 'update_token',
    operationKey: 'external_integration_sources.update_token',
    sourceRefId: params.data.id,
    sourceName: source?.name ?? params.data.id,
    summary: `更新外部来源系统 Token：${token.name}`,
    changes: [
      safeChange('tokenName', 'Token 名称', undefined, token.name),
      safeChange('tokenStatus', 'Token 状态', undefined, token.status),
      safeChange('expiresAt', '到期时间', undefined, token.expiresAt)
    ]
  })
  res.json(ok(token))
})

async function recordSourceOperation(req: Request, input: {
  action: string
  operationKey: string
  sourceRefId: string
  sourceName: string
  summary: string
  changes: ReturnType<typeof safeChange>[]
}): Promise<void> {
  const actor = getRequestAuthContext()?.systemAccountId
  if (!actor) {
    return
  }
  await recordOperationLogAsync({
    module: 'external_integration_sources',
    action: input.action,
    operationKey: input.operationKey,
    resourceType: 'external_integration_source',
    resourceId: input.sourceRefId,
    resourceName: input.sourceName,
    summary: input.summary,
    detailLevel: 'full',
    visibilityScope: 'admin_only',
    changes: input.changes
  }, req)
}

function formatRateLimits(rules: Array<{ windowSeconds: number; maxRequests: number }>): string {
  return rules.length
    ? rules.map((rule) => `${rule.windowSeconds}s/${rule.maxRequests}次`).join(', ')
    : '不限制'
}

function setSecretResponseHeaders(res: Response): void {
  res.setHeader('Cache-Control', 'no-store')
  res.setHeader('Pragma', 'no-cache')
}

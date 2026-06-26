import { Router } from 'express'
import { z } from 'zod'

import { runtimeConfig } from '../../config/runtime.js'
import { badRequest, ok, sendNotFound } from '../../shared/http.js'
import { integerQueryValue, optionalQueryText } from '../../shared/query-values.js'
import {
  createOpenAICompatibleMcpServer,
  deleteOpenAICompatibleMcpServer,
  findLatestOpenAICompatibleMcpServerDiagnostic,
  findOpenAICompatibleMcpServerForAccess,
  listOpenAICompatibleMcpToolCacheForServer,
  listOpenAICompatibleMcpServersPage,
  updateOpenAICompatibleMcpServer
} from '../../storage/openai-compatible-mcp-server.repository.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { diagnoseOpenAICompatibleMcpServer } from './mcp-proxy-executor.js'
import { bodyField, mutationGuard, normalizedText, queryField, sensitiveFingerprint, sortedTextValues, textValue } from '../deduplication/mutation-guard.middleware.js'

export const mcpServersRouter = Router()

const mcpServerSchema = z.object({
  label: z.string().trim().min(1, 'MCP server label 不能为空').max(100, 'MCP server label 不能超过 100 个字符').regex(/^\S+$/, 'MCP server label 不能包含空格'),
  serverUrl: z.string().trim().url('MCP server URL 无效').refine(isAllowedMcpServerUrl, 'MCP server URL 必须是 HTTPS'),
  description: z.string().trim().max(500, '说明不能超过 500 个字符').nullable().optional(),
  enabled: z.boolean().optional(),
  allowedTools: z.array(z.string().trim().min(1)).max(200, '允许工具不能超过 200 个').optional(),
  defaultApprovalPolicy: z.enum(['always', 'never']).optional(),
  timeoutMs: z.number().int().min(1000).max(120000).nullable().optional(),
  maxRetries: z.number().int().min(0).max(3).nullable().optional(),
  retryDelayMs: z.number().int().min(0).max(5000).nullable().optional(),
  maxBodyBytes: z.number().int().min(16 * 1024).max(4 * 1024 * 1024).nullable().optional(),
  maxOutputBytes: z.number().int().min(4 * 1024).max(1024 * 1024).nullable().optional(),
  allowRequestAuthorization: z.boolean().optional(),
  authorizationRef: z.string().trim().max(500, '授权引用不能超过 500 个字符').nullable().optional()
}).strict()

const mcpServerUpdateSchema = mcpServerSchema.partial().strict()

const mcpServerDiagnoseSchema = z.object({
  authorization: z.string().trim().min(1).max(4000).optional()
}).strict()

mcpServersRouter.get('/', (req, res) => {
  res.json(ok(listOpenAICompatibleMcpServersPage(
    getRequestAccessScope(req.query.systemAccountId),
    parseMcpServerListOptions(req.query)
  )))
})

mcpServersRouter.get('/:id', (req, res) => {
  const record = findOpenAICompatibleMcpServerForAccess(req.params.id, getRequestAccessScope(req.query.systemAccountId))
  if (!record) {
    sendNotFound(res, 'MCP server 不存在')
    return
  }
  res.json(ok(record))
})

mcpServersRouter.get('/:id/tools', (req, res) => {
  const access = getRequestAccessScope(req.query.systemAccountId)
  const record = findOpenAICompatibleMcpServerForAccess(req.params.id, access)
  if (!record) {
    sendNotFound(res, 'MCP server 不存在')
    return
  }
  res.json(ok({
    server: record,
    latestDiagnostic: findLatestOpenAICompatibleMcpServerDiagnostic(record.id, access) ?? null,
    tools: listOpenAICompatibleMcpToolCacheForServer(record.id, access)
  }))
})

mcpServersRouter.post('/', mutationGuard({
  operationKey: 'mcp_servers.create',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    systemAccountId: normalizedText(queryField(req, 'systemAccountId')),
    label: normalizedText(bodyField(req, 'label')),
    serverUrl: normalizedText(bodyField(req, 'serverUrl')),
    allowedTools: sortedTextValues(bodyField(req, 'allowedTools'))
  })
}), (req, res) => {
  const parsed = mcpServerSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? 'MCP server 参数无效'))
    return
  }
  const access = getRequestAccessScope(req.query.systemAccountId)
  if (!access) {
    res.status(401).json(badRequest('缺少系统账户上下文'))
    return
  }
  try {
    const record = createOpenAICompatibleMcpServer(parsed.data, access)
    res.status(201).json(ok(record))
  } catch (error) {
    res.status(error instanceof Error && error.message.includes('UNIQUE') ? 409 : 400)
      .json(badRequest(error instanceof Error ? error.message : '创建 MCP server 失败'))
  }
})

mcpServersRouter.patch('/:id', mutationGuard({
  operationKey: 'mcp_servers.update',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    id: req.params.id,
    systemAccountId: normalizedText(queryField(req, 'systemAccountId')),
    label: textValue(bodyField(req, 'label')),
    serverUrl: textValue(bodyField(req, 'serverUrl')),
    enabled: bodyField(req, 'enabled'),
    allowedTools: sortedTextValues(bodyField(req, 'allowedTools'))
  })
}), (req, res) => {
  const parsed = mcpServerUpdateSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? 'MCP server 参数无效'))
    return
  }
  const access = getRequestAccessScope(req.query.systemAccountId)
  if (!access) {
    res.status(401).json(badRequest('缺少系统账户上下文'))
    return
  }
  try {
    const record = updateOpenAICompatibleMcpServer(req.params.id, parsed.data, access)
    if (!record) {
      sendNotFound(res, 'MCP server 不存在')
      return
    }
    res.json(ok(record))
  } catch (error) {
    res.status(error instanceof Error && error.message.includes('UNIQUE') ? 409 : 400)
      .json(badRequest(error instanceof Error ? error.message : '更新 MCP server 失败'))
  }
})

mcpServersRouter.post('/:id/diagnose', mutationGuard({
  operationKey: 'mcp_servers.diagnose',
  processingTtlMs: 120_000,
  succeededTtlMs: 1_000,
  failedTtlMs: 1_000,
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    id: req.params.id,
    systemAccountId: normalizedText(queryField(req, 'systemAccountId')),
    authorization: sensitiveFingerprint(bodyField(req, 'authorization'))
  })
}), async (req, res) => {
  const parsed = mcpServerDiagnoseSchema.safeParse(req.body ?? {})
  if (!parsed.success) {
    res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? 'MCP server 诊断参数无效'))
    return
  }
  const access = getRequestAccessScope(req.query.systemAccountId)
  if (!access) {
    res.status(401).json(badRequest('缺少系统账户上下文'))
    return
  }
  const record = findOpenAICompatibleMcpServerForAccess(req.params.id, access)
  if (!record) {
    sendNotFound(res, 'MCP server 不存在')
    return
  }
  if (!record.enabled) {
    res.status(400).json(badRequest('MCP server 已禁用，不能执行远程诊断'))
    return
  }
  if (parsed.data.authorization && !record.allowRequestAuthorization) {
    res.status(403).json(badRequest('该 MCP server 未允许请求级 authorization，已拒绝远程诊断'))
    return
  }
  const result = await diagnoseOpenAICompatibleMcpServer({
    server: record,
    authorization: parsed.data.authorization
  })
  res.json(ok(result))
})

mcpServersRouter.delete('/:id', mutationGuard({
  operationKey: 'mcp_servers.delete',
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    id: req.params.id,
    systemAccountId: normalizedText(queryField(req, 'systemAccountId'))
  })
}), (req, res) => {
  const access = getRequestAccessScope(req.query.systemAccountId)
  if (!access) {
    res.status(401).json(badRequest('缺少系统账户上下文'))
    return
  }
  if (!deleteOpenAICompatibleMcpServer(req.params.id, access)) {
    sendNotFound(res, 'MCP server 不存在')
    return
  }
  res.json(ok(true))
})

function parseMcpServerListOptions(query: Record<string, unknown>) {
  const enabledText = optionalQueryText(query.enabled)
  return {
    page: integerQueryValue(query.page),
    pageSize: integerQueryValue(query.pageSize),
    keyword: optionalQueryText(query.keyword),
    enabled: enabledText === 'true' ? true : enabledText === 'false' ? false : undefined
  }
}

function isAllowedMcpServerUrl(value: string): boolean {
  let url: URL
  try {
    url = new URL(value)
  } catch {
    return false
  }
  if (url.protocol === 'https:') return true
  if (url.protocol !== 'http:' || !runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls) return false
  const hostname = url.hostname.toLowerCase()
  return hostname === 'localhost' || hostname === '127.0.0.1' || hostname === '::1' || hostname === '[::1]'
}

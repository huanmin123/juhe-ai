import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok, sendNotFound } from '../../shared/http.js'
import { finiteNumberQueryValue, optionalQueryText } from '../../shared/query-values.js'
import {
  approveOpenAICompatibleMcpApprovalRequestForAccess,
  findOpenAICompatibleMcpApprovalRequestForAccess,
  listOpenAICompatibleMcpApprovalRequestsPage,
  rejectOpenAICompatibleMcpApprovalRequestForAccess,
  type OpenAICompatibleMcpApprovalPageOptions,
  type OpenAICompatibleMcpApprovalStatus
} from '../../storage/openai-compatible-mcp-approval.repository.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { bodyField, mutationGuard, normalizedText } from '../deduplication/mutation-guard.middleware.js'

export const mcpApprovalRequestsRouter = Router()

const mcpApprovalStatuses = new Set<OpenAICompatibleMcpApprovalStatus>([
  'pending',
  'approved',
  'rejected',
  'expired',
  'consumed'
])

const rejectApprovalSchema = z.object({
  rejectReason: z.string().trim().max(500, '拒绝原因不能超过 500 个字符').optional()
}).strict()

mcpApprovalRequestsRouter.get('/', (req, res) => {
  res.json(ok(listOpenAICompatibleMcpApprovalRequestsPage(
    getRequestAccessScope(req.query.systemAccountId),
    parseMcpApprovalRequestListOptions(req.query)
  )))
})

mcpApprovalRequestsRouter.get('/:id', (req, res) => {
  const record = findOpenAICompatibleMcpApprovalRequestForAccess(
    req.params.id,
    getRequestAccessScope(req.query.systemAccountId)
  )
  if (!record) {
    sendNotFound(res, 'MCP 审批记录不存在')
    return
  }
  res.json(ok(record))
})

mcpApprovalRequestsRouter.post('/:id/approve', mutationGuard({
  operationKey: 'mcp_approval_requests.approve',
  scope: (req) => normalizedText(req.query.systemAccountId),
  fingerprint: (req) => ({
    id: req.params.id,
    systemAccountId: normalizedText(req.query.systemAccountId)
  })
}), (req, res) => {
  const record = approveOpenAICompatibleMcpApprovalRequestForAccess(
    req.params.id,
    getRequestAccessScope(req.query.systemAccountId)
  )
  if (!record) {
    sendNotFound(res, 'MCP 审批记录不存在')
    return
  }
  if (record.status !== 'approved') {
    res.status(409).json(badRequest('MCP 审批记录不是待审批状态，无法批准'))
    return
  }
  res.json(ok(record))
})

mcpApprovalRequestsRouter.post('/:id/reject', mutationGuard({
  operationKey: 'mcp_approval_requests.reject',
  scope: (req) => normalizedText(req.query.systemAccountId),
  fingerprint: (req) => ({
    id: req.params.id,
    systemAccountId: normalizedText(req.query.systemAccountId),
    rejectReason: normalizedText(bodyField(req, 'rejectReason'))
  })
}), (req, res) => {
  const parsed = rejectApprovalSchema.safeParse(req.body ?? {})
  if (!parsed.success) {
    res.status(400).json(badRequest(parsed.error.issues[0]?.message ?? 'MCP 审批拒绝参数无效'))
    return
  }
  const record = rejectOpenAICompatibleMcpApprovalRequestForAccess(
    req.params.id,
    getRequestAccessScope(req.query.systemAccountId),
    parsed.data.rejectReason
  )
  if (!record) {
    sendNotFound(res, 'MCP 审批记录不存在')
    return
  }
  if (record.status !== 'rejected') {
    res.status(409).json(badRequest('MCP 审批记录不是待审批状态，无法拒绝'))
    return
  }
  res.json(ok(record))
})

function parseMcpApprovalRequestListOptions(query: Record<string, unknown>): OpenAICompatibleMcpApprovalPageOptions {
  const rawPage = finiteNumberQueryValue(query.page)
  const rawPageSize = finiteNumberQueryValue(query.pageSize)
  const status = typeof query.status === 'string' && mcpApprovalStatuses.has(query.status as OpenAICompatibleMcpApprovalStatus)
    ? query.status as OpenAICompatibleMcpApprovalStatus
    : undefined
  return {
    page: Number.isInteger(rawPage) ? rawPage : undefined,
    pageSize: Number.isInteger(rawPageSize) ? rawPageSize : undefined,
    apiKeyId: optionalQueryText(query.apiKeyId),
    groupId: optionalQueryText(query.groupId),
    traceId: optionalQueryText(query.traceId),
    serverLabel: optionalQueryText(query.serverLabel),
    toolName: optionalQueryText(query.toolName),
    status,
    startAt: optionalQueryText(query.startAt),
    endAt: optionalQueryText(query.endAt)
  }
}

import { Router } from 'express'

import { ok, sendNotFound } from '../../shared/http.js'
import { finiteNumberQueryValue, optionalQueryText } from '../../shared/query-values.js'
import {
  findOpenAICompatibleMcpExecutionRecordForAccess,
  listOpenAICompatibleMcpExecutionRecordsPage,
  type OpenAICompatibleMcpExecutionPageOptions,
  type OpenAICompatibleMcpExecutionStatus
} from '../../storage/openai-compatible-mcp-execution.repository.js'
import { getRequestAccessScope } from '../auth/request-context.js'

export const mcpExecutionRecordsRouter = Router()

const mcpExecutionStatuses = new Set<OpenAICompatibleMcpExecutionStatus>(['succeeded', 'failed'])

mcpExecutionRecordsRouter.get('/', (req, res) => {
  res.json(ok(listOpenAICompatibleMcpExecutionRecordsPage(
    getRequestAccessScope(req.query.systemAccountId),
    parseMcpExecutionRecordListOptions(req.query)
  )))
})

mcpExecutionRecordsRouter.get('/:id', (req, res) => {
  const record = findOpenAICompatibleMcpExecutionRecordForAccess(
    req.params.id,
    getRequestAccessScope(req.query.systemAccountId)
  )
  if (!record) {
    sendNotFound(res, 'MCP 执行记录不存在')
    return
  }
  res.json(ok(record))
})

function parseMcpExecutionRecordListOptions(query: Record<string, unknown>): OpenAICompatibleMcpExecutionPageOptions {
  const rawPage = finiteNumberQueryValue(query.page)
  const rawPageSize = finiteNumberQueryValue(query.pageSize)
  const status = typeof query.status === 'string' && mcpExecutionStatuses.has(query.status as OpenAICompatibleMcpExecutionStatus)
    ? query.status as OpenAICompatibleMcpExecutionStatus
    : undefined
  return {
    page: Number.isInteger(rawPage) ? rawPage : undefined,
    pageSize: Number.isInteger(rawPageSize) ? rawPageSize : undefined,
    apiKeyId: optionalQueryText(query.apiKeyId),
    groupId: optionalQueryText(query.groupId),
    traceId: optionalQueryText(query.traceId),
    approvalRequestId: optionalQueryText(query.approvalRequestId),
    serverLabel: optionalQueryText(query.serverLabel),
    toolName: optionalQueryText(query.toolName),
    status,
    startAt: optionalQueryText(query.startAt),
    endAt: optionalQueryText(query.endAt)
  }
}

import { z } from 'zod'

import { parseOrBadRequest, type ParseResult } from '../../shared/http.js'

export interface RequestScopeQuery {
  systemAccountId?: string
}

const requestScopeQuerySchema = z.object({
  systemAccountId: z.string().trim().min(1, '系统账号 ID 不能为空').optional()
})

export function parseRequestScopeQuery(query: unknown): ParseResult<RequestScopeQuery> {
  return parseOrBadRequest(requestScopeQuerySchema, query, '查询参数不合法')
}

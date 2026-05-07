import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { createApiKeyRecord, deleteApiKey, listApiKeysPage, updateApiKey, type ApiKeyListOptions } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { clearApiKeyQuotaCache, invalidateApiKeyQuotaCacheById } from '../gateway/api-key-quota.service.js'
import { clearGatewayRuntimeCache } from '../gateway/gateway-runtime-cache.service.js'

export const apiKeysRouter = Router()

const apiKeyCreateSchema = z.object({
  name: z.string().min(1),
  description: z.string().trim().max(200).nullable().optional(),
  groupId: z.string().min(1),
  status: z.enum(['active', 'disabled']).optional(),
  expiresAt: z.string().optional(),
  quotaLimits: z.record(z.string(), z.unknown()).nullable().optional()
})

apiKeysRouter.get('/', (req, res) => {
  res.json(ok(listApiKeysPage(getRequestAccessScope(req.query.systemAccountId), parseApiKeyListOptions(req.query))))
})

function parseApiKeyListOptions(query: Record<string, unknown>): ApiKeyListOptions {
  return {
    page: integerQueryValue(query.page),
    pageSize: integerQueryValue(query.pageSize),
    limit: integerQueryValue(query.limit),
    keyword: optionalQueryText(query.keyword),
    status: apiKeyStatusQueryValue(query.status),
    groupId: optionalQueryText(query.groupId)
  }
}

function integerQueryValue(value: unknown): number | undefined {
  const text = Array.isArray(value) ? value[0] : value
  const number = typeof text === 'string' ? Number(text) : typeof text === 'number' ? text : undefined
  return Number.isInteger(number) ? number : undefined
}

function optionalQueryText(value: unknown): string | undefined {
  const text = Array.isArray(value) ? value[0] : value
  return typeof text === 'string' && text.trim() ? text.trim() : undefined
}

function apiKeyStatusQueryValue(value: unknown): ApiKeyListOptions['status'] {
  const text = optionalQueryText(value)
  return text === 'active' || text === 'disabled' || text === 'all' ? text : undefined
}

apiKeysRouter.post('/', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = apiKeyCreateSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('Invalid API key payload'))
    return
  }
  try {
    const apiKey = createApiKeyRecord(parsed.data, requestAccess)
    clearGatewayRuntimeCache()
    clearApiKeyQuotaCache()
    res.status(201).json(ok(apiKey, '明文密钥已保存，列表中可直接查看'))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : 'Invalid API key payload'))
  }
})

apiKeysRouter.patch('/:id', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const apiKey = updateApiKey(req.params.id, req.body as Record<string, unknown>, getRequestAccessScope(scopeQuery.data.systemAccountId))
  if (!apiKey) {
    res.status(404).json({ message: 'API key not found' })
    return
  }
  clearGatewayRuntimeCache()
  invalidateApiKeyQuotaCacheById(req.params.id)
  res.json(ok(apiKey))
})

apiKeysRouter.delete('/:id', (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  if (!deleteApiKey(req.params.id, getRequestAccessScope(scopeQuery.data.systemAccountId))) {
    res.status(404).json({ message: 'API key not found' })
    return
  }
  clearGatewayRuntimeCache()
  invalidateApiKeyQuotaCacheById(req.params.id)
  res.status(204).send()
})

import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { createApiKeyRecord, deleteApiKey, listApiKeys, updateApiKey } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { clearGatewayRuntimeCache } from '../gateway/gateway-runtime-cache.service.js'

export const apiKeysRouter = Router()

const apiKeyCreateSchema = z.object({
  name: z.string().min(1),
  groupId: z.string().min(1),
  status: z.enum(['active', 'disabled']).optional(),
  expiresAt: z.string().optional()
})

apiKeysRouter.get('/', (req, res) => {
  res.json(ok(listApiKeys(getRequestAccessScope(req.query.systemAccountId))))
})

apiKeysRouter.post('/', (req, res) => {
  const parsed = apiKeyCreateSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('Invalid API key payload'))
    return
  }
  try {
    const apiKey = createApiKeyRecord(parsed.data)
    clearGatewayRuntimeCache()
    res.status(201).json(ok(apiKey, '明文密钥已保存，列表中可直接查看'))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : 'Invalid API key payload'))
  }
})

apiKeysRouter.patch('/:id', (req, res) => {
  const apiKey = updateApiKey(req.params.id, req.body as Record<string, unknown>)
  if (!apiKey) {
    res.status(404).json({ message: 'API key not found' })
    return
  }
  clearGatewayRuntimeCache()
  res.json(ok(apiKey))
})

apiKeysRouter.delete('/:id', (req, res) => {
  if (!deleteApiKey(req.params.id)) {
    res.status(404).json({ message: 'API key not found' })
    return
  }
  clearGatewayRuntimeCache()
  res.status(204).send()
})

import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { createProxy, deleteProxy, listProxies, updateProxy } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'

export const proxiesRouter = Router()

const proxySchema = z.object({
  name: z.string().min(1),
  type: z.enum(['http', 'https', 'socks5']),
  host: z.string().min(1),
  port: z.number().int().min(1).max(65535),
  username: z.string().optional(),
  password: z.string().optional(),
  enabled: z.boolean().optional()
})

proxiesRouter.get('/', (req, res) => {
  res.json(ok(listProxies(getRequestAccessScope(req.query.systemAccountId))))
})

proxiesRouter.post('/', (req, res) => {
  const parsed = proxySchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('Invalid proxy payload'))
    return
  }
  res.status(201).json(ok(createProxy(parsed.data)))
})

proxiesRouter.patch('/:id', (req, res) => {
  const proxy = updateProxy(req.params.id, req.body as Record<string, unknown>)
  if (!proxy) {
    res.status(404).json({ message: 'Proxy not found' })
    return
  }
  res.json(ok(proxy))
})

proxiesRouter.delete('/:id', (req, res) => {
  if (!deleteProxy(req.params.id)) {
    res.status(404).json({ message: 'Proxy not found' })
    return
  }
  res.status(204).send()
})

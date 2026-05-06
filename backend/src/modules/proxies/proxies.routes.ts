import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { createProxy, deleteProxy, listProxies, listProxyOptions, ProxyInUseError, updateProxy } from '../../storage/repositories.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { clearGatewayRuntimeCache } from '../gateway/gateway-runtime-cache.service.js'
import { testProxyById } from './proxy-test.service.js'

export const proxiesRouter = Router()

const proxySchema = z.object({
  name: z.string().min(1),
  description: z.string().trim().max(200).nullable().optional(),
  type: z.enum(['http', 'https', 'socks5', 'socks5h']),
  host: z.string().min(1),
  port: z.number().int().min(1).max(65535),
  username: z.string().optional(),
  password: z.string().optional(),
  enabled: z.boolean().optional()
})

proxiesRouter.get('/options', (_req, res) => {
  res.json(ok(listProxyOptions()))
})

proxiesRouter.get('/', requireAdmin, (_req, res) => {
  res.json(ok(listProxies()))
})

proxiesRouter.post('/', requireAdmin, (req, res) => {
  const parsed = proxySchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('Invalid proxy payload'))
    return
  }
  const proxy = createProxy(parsed.data)
  clearGatewayRuntimeCache()
  res.status(201).json(ok(proxy))
})

proxiesRouter.patch('/:id', requireAdmin, (req, res) => {
  const proxy = updateProxy(req.params.id, req.body as Record<string, unknown>)
  if (!proxy) {
    res.status(404).json({ message: 'Proxy not found' })
    return
  }
  clearGatewayRuntimeCache()
  res.json(ok(proxy))
})

proxiesRouter.post('/:id/test', requireAdmin, async (req, res) => {
  try {
    const report = await testProxyById(req.params.id)
    if (!report) {
      res.status(404).json({ message: 'Proxy not found' })
      return
    }
    res.json(ok(report))
  } catch (error) {
    res.status(502).json({ message: error instanceof Error ? error.message : '代理检测失败' })
  }
})

proxiesRouter.delete('/:id', requireAdmin, (req, res) => {
  try {
    if (!deleteProxy(req.params.id)) {
      res.status(404).json({ message: 'Proxy not found' })
      return
    }
    clearGatewayRuntimeCache()
    res.status(204).send()
  } catch (error) {
    if (error instanceof ProxyInUseError) {
      res.status(409).json({ message: error.message })
      return
    }
    res.status(400).json(badRequest(error instanceof Error ? error.message : 'Delete proxy failed'))
  }
})

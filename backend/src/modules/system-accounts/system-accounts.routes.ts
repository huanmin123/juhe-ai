import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { clearGatewayApiKeyValidationCache, createSystemAccount, listSystemAccounts, revokeAllSessionsForAccount, updateSystemAccount } from '../../storage/repositories.js'
import { clearGatewayRuntimeCache } from '../gateway/gateway-runtime-cache.service.js'

export const systemAccountsRouter = Router()

const createSchema = z.object({
  username: z.string().trim().min(2),
  displayName: z.string().trim().min(1),
  description: z.string().trim().max(200).nullable().optional(),
  password: z.string().min(4),
  role: z.enum(['admin', 'user']).optional(),
  status: z.enum(['active', 'disabled']).optional(),
  mustChangePassword: z.boolean().optional()
})

const updateSchema = z.object({
  displayName: z.string().trim().min(1).optional(),
  description: z.string().trim().max(200).nullable().optional(),
  password: z.string().min(4).optional(),
  role: z.enum(['admin', 'user']).optional(),
  status: z.enum(['active', 'disabled']).optional(),
  mustChangePassword: z.boolean().optional()
})

systemAccountsRouter.get('/', requireAdmin, (_req, res) => {
  res.json(ok(listSystemAccounts()))
})

systemAccountsRouter.post('/', requireAdmin, (req, res) => {
  const parsed = createSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('系统账户参数无效'))
    return
  }
  try {
    res.status(201).json(ok(createSystemAccount(parsed.data)))
  } catch (error) {
    res.status(409).json({ message: error instanceof Error ? error.message : '创建系统账户失败' })
  }
})

systemAccountsRouter.patch('/:id', requireAdmin, (req, res) => {
  if (req.body && Object.prototype.hasOwnProperty.call(req.body, 'username')) {
    res.status(400).json(badRequest('用户账户创建后不能修改'))
    return
  }
  const parsed = updateSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('系统账户参数无效'))
    return
  }
  try {
    const account = updateSystemAccount(req.params.id, parsed.data)
    if (!account) {
      res.status(404).json({ message: '系统账户不存在' })
      return
    }
    if (parsed.data.status === 'disabled' || parsed.data.password) {
      revokeAllSessionsForAccount(req.params.id)
    }
    if (parsed.data.status) {
      clearGatewayApiKeyValidationCache()
      clearGatewayRuntimeCache()
    }
    res.json(ok(account))
  } catch (error) {
    res.status(409).json({ message: error instanceof Error ? error.message : '更新系统账户失败' })
  }
})

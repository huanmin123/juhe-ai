import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { clearAccountFailureState, createAccount, deleteAccount, listAccounts, updateAccount } from '../../storage/repositories.js'
import { testOpenAIAccount } from './account-test.service.js'

export const accountsRouter = Router()

const accountCreateSchema = z.object({
  name: z.string().min(1),
  type: z.enum(['oauth', 'api_key']),
  credentials: z.record(z.unknown()).optional(),
  status: z.enum(['active', 'disabled', 'error']).optional(),
  concurrencyLimit: z.number().int().min(1).optional(),
  priority: z.number().int().optional(),
  proxyProfileId: z.string().optional(),
  passthroughEnabled: z.boolean().optional(),
  schedulable: z.boolean().optional(),
  notes: z.string().optional()
})

accountsRouter.get('/', (_req, res) => {
  res.json(ok(listAccounts()))
})

accountsRouter.post('/', (req, res) => {
  const parsed = accountCreateSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('Invalid account payload'))
    return
  }
  res.status(201).json(ok(createAccount(parsed.data)))
})

accountsRouter.patch('/:id', (req, res) => {
  if ((req.body as Record<string, unknown>).clearFailureState === true) {
    clearAccountFailureState(req.params.id)
  }
  const account = updateAccount(req.params.id, req.body as Record<string, unknown>)
  if (!account) {
    res.status(404).json({ message: 'Account not found' })
    return
  }
  res.json(ok(account))
})

accountsRouter.post('/:id/test', async (req, res) => {
  const account = listAccounts().find((item) => item.id === req.params.id)
  if (!account) {
    res.status(404).json({ message: 'Account not found' })
    return
  }
  if (account.providerCode !== 'openai') {
    res.status(400).json({ message: 'Only OpenAI accounts can be tested in phase 1' })
    return
  }

  const result = await testOpenAIAccount(account)
  res.json(ok(result))
})

accountsRouter.delete('/:id', (req, res) => {
  if (!deleteAccount(req.params.id)) {
    res.status(404).json({ message: 'Account not found' })
    return
  }
  res.status(204).send()
})

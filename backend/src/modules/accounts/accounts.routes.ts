import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { addAccountToGroup, clearAccountFailureState, createAccount, deleteAccount, listAccounts, listProviders, updateAccount } from '../../storage/repositories.js'
import { testOpenAIAccount } from './account-test.service.js'

export const accountsRouter = Router()

const accountCreateSchema = z.object({
  providerCode: z.string().min(1).optional(),
  name: z.string().min(1),
  type: z.string().min(1),
  credentials: z.record(z.unknown()).optional(),
  status: z.enum(['active', 'disabled', 'error', 'rate_limited', 'temporary_unavailable']).optional(),
  concurrencyLimit: z.number().int().min(1).optional(),
  priority: z.number().int().optional(),
  proxyProfileId: z.string().optional(),
  passthroughEnabled: z.boolean().optional(),
  errorPolicyId: z.string().nullable().optional(),
  schedulable: z.boolean().optional(),
  groupId: z.string().optional(),
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

  const providerCode = parsed.data.providerCode?.trim() || 'openai'
  const provider = listProviders().find((item) => item.code === providerCode)
  if (!provider) {
    res.status(400).json(badRequest(`Unsupported provider: ${providerCode}`))
    return
  }
  if (!provider.enabled) {
    res.status(400).json(badRequest(`Provider is disabled: ${providerCode}`))
    return
  }
  if (!provider.accountTypes.includes(parsed.data.type)) {
    res.status(400).json(badRequest(`Provider ${providerCode} does not support account type ${parsed.data.type}`))
    return
  }

  const account = createAccount({
    ...parsed.data,
    providerCode
  })
  if (parsed.data.groupId) {
    addAccountToGroup(parsed.data.groupId, account.id)
  }

  res.status(201).json(ok(account))
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

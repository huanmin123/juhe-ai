import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { DuplicateAccountCredentialError, clearAccountFailureState, createAccount, createAccountAuthorization, deleteAccount, findAccountForTest, listAccountAuthorizations, listAccounts, listGroups, listProviders, returnGrantedAccountAuthorization, revokeAccountAuthorization, setAccountGroup, updateAccount, updateGrantedAccountAuthorizationSchedulable } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
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
  errorPolicyId: z.string().nullable().optional(),
  schedulable: z.boolean().optional(),
  groupId: z.string().nullable().optional(),
  accountExpiresAt: z.string().nullable().optional(),
  account_expires_at: z.string().nullable().optional(),
  notes: z.string().optional()
})

const accountTestSchema = z.object({
  model: z.string().trim().optional(),
  prompt: z.string().trim().optional()
}).optional()

const accountAuthorizationSchema = z.object({
  granteeSystemAccountId: z.string().trim().min(1),
  remark: z.string().trim().max(200).optional()
})

const grantedAuthorizationScheduleSchema = z.object({
  schedulable: z.boolean()
})

accountsRouter.get('/', (req, res) => {
  res.json(ok(listAccounts(getRequestAccessScope(req.query.systemAccountId))))
})

accountsRouter.get('/:id/authorizations', (req, res) => {
  res.json(ok(listAccountAuthorizations(req.params.id, getRequestAccessScope(req.query.systemAccountId))))
})

accountsRouter.post('/:id/authorizations', (req, res) => {
  const parsed = accountAuthorizationSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('Invalid account authorization payload'))
    return
  }
  try {
    res.status(201).json(ok(createAccountAuthorization(req.params.id, parsed.data, getRequestAccessScope(req.query.systemAccountId))))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : 'Create account authorization failed'))
  }
})

accountsRouter.delete('/:id/authorizations/:authorizationId', (req, res) => {
  const authorization = revokeAccountAuthorization(req.params.id, req.params.authorizationId, getRequestAccessScope(req.query.systemAccountId))
  if (!authorization) {
    res.status(404).json({ message: 'Account authorization not found' })
    return
  }
  res.json(ok(authorization))
})

accountsRouter.patch('/:id/granted-authorization/:authorizationId/schedulable', (req, res) => {
  const parsed = grantedAuthorizationScheduleSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('Invalid account authorization schedule payload'))
    return
  }
  let account: ReturnType<typeof updateGrantedAccountAuthorizationSchedulable>
  try {
    account = updateGrantedAccountAuthorizationSchedulable(req.params.id, req.params.authorizationId, parsed.data.schedulable, getRequestAccessScope(req.query.systemAccountId))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : 'Update account authorization schedule failed'))
    return
  }
  if (!account) {
    res.status(404).json({ message: 'Account authorization not found' })
    return
  }
  res.json(ok(account))
})

accountsRouter.post('/:id/granted-authorization/:authorizationId/return', (req, res) => {
  const returned = returnGrantedAccountAuthorization(req.params.id, req.params.authorizationId, getRequestAccessScope(req.query.systemAccountId))
  if (!returned) {
    res.status(404).json({ message: 'Account authorization not found' })
    return
  }
  res.json(ok({ returned: true }))
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
  const groupId = typeof parsed.data.groupId === 'string' && parsed.data.groupId ? parsed.data.groupId : undefined
  if (groupId) {
    const group = listGroups().find((item) => item.id === groupId)
    if (!group || group.providerCode !== providerCode) {
      res.status(400).json(badRequest('Invalid account group'))
      return
    }
  }

  try {
    const account = createAccount({
      ...parsed.data,
      providerCode
    })
    res.status(201).json(ok(account))
  } catch (error) {
    if (error instanceof DuplicateAccountCredentialError) {
      res.status(409).json({ message: error.message })
      return
    }
    res.status(400).json(badRequest(error instanceof Error ? error.message : 'Invalid account payload'))
  }
})

accountsRouter.patch('/:id', (req, res) => {
  const body = req.body as Record<string, unknown>
  const existingAccount = listAccounts().find((item) => item.id === req.params.id)
  if (!existingAccount) {
    res.status(404).json({ message: 'Account not found' })
    return
  }
  const hasGroupId = Object.prototype.hasOwnProperty.call(body, 'groupId')
  if (hasGroupId && (typeof body.groupId !== 'string' || !body.groupId)) {
    res.status(400).json(badRequest('Account group is required'))
    return
  }
  if (hasGroupId) {
    const group = listGroups().find((item) => item.id === body.groupId)
    if (!group || group.providerCode !== existingAccount.providerCode) {
      res.status(400).json(badRequest('Invalid account group'))
      return
    }
  }
  if (body.clearFailureState === true) {
    clearAccountFailureState(req.params.id)
  }
  let account: ReturnType<typeof updateAccount>
  try {
    account = updateAccount(req.params.id, body)
  } catch (error) {
    if (error instanceof DuplicateAccountCredentialError) {
      res.status(409).json({ message: error.message })
      return
    }
    res.status(400).json(badRequest(error instanceof Error ? error.message : 'Update account failed'))
    return
  }
  if (!account) {
    res.status(404).json({ message: 'Account not found' })
    return
  }
  if (hasGroupId) {
    const nextAccount = setAccountGroup(account.id, body.groupId as string)
    if (!nextAccount) {
      res.status(400).json(badRequest('Invalid account group'))
      return
    }
  }
  res.json(ok(account))
})

accountsRouter.post('/:id/test', async (req, res) => {
  const parsed = accountTestSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('Invalid account test payload'))
    return
  }
  const account = findAccountForTest(req.params.id, getRequestAccessScope(req.query.systemAccountId))
  if (!account) {
    res.status(404).json({ message: 'Account not found' })
    return
  }
  if (account.providerCode !== 'openai') {
    res.status(400).json({ message: 'Only OpenAI accounts can be tested in phase 1' })
    return
  }

  const result = await testOpenAIAccount(account, parsed.data ?? {})
  res.json(ok(result))
})

accountsRouter.delete('/:id', (req, res) => {
  if (!deleteAccount(req.params.id)) {
    res.status(404).json({ message: 'Account not found' })
    return
  }
  res.status(204).send()
})

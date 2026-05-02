import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { createGroup, deleteGroup, listGroups, listProviders, setGroupAccounts, updateGroup } from '../../storage/repositories.js'

export const groupsRouter = Router()

const groupSchema = z.object({
  name: z.string().min(1),
  providerCode: z.string().min(1).optional(),
  description: z.string().optional(),
  enabled: z.boolean().optional()
})

groupsRouter.get('/', (_req, res) => {
  res.json(ok(listGroups()))
})

groupsRouter.post('/', (req, res) => {
  const parsed = groupSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('Invalid group payload'))
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
  res.status(201).json(ok(createGroup({ ...parsed.data, providerCode })))
})

groupsRouter.patch('/:id', (req, res) => {
  const providerCode = typeof (req.body as Record<string, unknown>).providerCode === 'string'
    ? String((req.body as Record<string, unknown>).providerCode).trim()
    : undefined
  if (providerCode) {
    const provider = listProviders().find((item) => item.code === providerCode)
    if (!provider) {
      res.status(400).json(badRequest(`Unsupported provider: ${providerCode}`))
      return
    }
    if (!provider.enabled) {
      res.status(400).json(badRequest(`Provider is disabled: ${providerCode}`))
      return
    }
  }
  const group = updateGroup(req.params.id, req.body as Record<string, unknown>)
  if (!group) {
    res.status(404).json({ message: 'Group not found' })
    return
  }
  res.json(ok(group))
})

groupsRouter.patch('/:id/accounts', (req, res) => {
  const accountIds = Array.isArray(req.body.accountIds) ? req.body.accountIds.map(String) : []
  const group = setGroupAccounts(req.params.id, accountIds)
  if (!group) {
    res.status(404).json({ message: 'Group not found' })
    return
  }
  res.json(ok(group))
})

groupsRouter.delete('/:id', (req, res) => {
  if (!deleteGroup(req.params.id)) {
    res.status(404).json({ message: 'Group not found' })
    return
  }
  res.status(204).send()
})

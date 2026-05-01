import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { createGroup, deleteGroup, listGroups, setGroupAccounts, updateGroup } from '../../storage/repositories.js'

export const groupsRouter = Router()

const groupSchema = z.object({
  name: z.string().min(1),
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
  res.status(201).json(ok(createGroup(parsed.data)))
})

groupsRouter.patch('/:id', (req, res) => {
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

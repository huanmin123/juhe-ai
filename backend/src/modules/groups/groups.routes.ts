import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { createGroup, createGroupAuthorization, deleteGroup, listGroupAuthorizations, listGroups, listProviders, revokeGroupAuthorization, updateGroup } from '../../storage/repositories.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { clearGatewayRuntimeCache } from '../gateway/gateway-runtime-cache.service.js'

export const groupsRouter = Router()

const groupSchema = z.object({
  name: z.string().min(1),
  providerCode: z.string().min(1).optional(),
  description: z.string().optional(),
  enabled: z.boolean().optional()
})

const groupAuthorizationSchema = z.object({
  granteeSystemAccountId: z.string().trim().min(1),
  remark: z.string().trim().max(200).optional()
})

groupsRouter.get('/', (req, res) => {
  res.json(ok(listGroups(getRequestAccessScope(req.query.systemAccountId))))
})

groupsRouter.get('/:id/authorizations', (req, res) => {
  res.json(ok(listGroupAuthorizations(req.params.id, getRequestAccessScope(req.query.systemAccountId))))
})

groupsRouter.post('/:id/authorizations', (req, res) => {
  const parsed = groupAuthorizationSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('Invalid group authorization payload'))
    return
  }
  try {
    const authorization = createGroupAuthorization(req.params.id, parsed.data, getRequestAccessScope(req.query.systemAccountId))
    clearGatewayRuntimeCache()
    res.status(201).json(ok(authorization))
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : 'Create group authorization failed'))
  }
})

groupsRouter.delete('/:id/authorizations/:authorizationId', (req, res) => {
  const authorization = revokeGroupAuthorization(req.params.id, req.params.authorizationId, getRequestAccessScope(req.query.systemAccountId))
  if (!authorization) {
    res.status(404).json({ message: 'Group authorization not found' })
    return
  }
  clearGatewayRuntimeCache()
  res.json(ok(authorization))
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
  const group = createGroup({ ...parsed.data, providerCode })
  clearGatewayRuntimeCache()
  res.status(201).json(ok(group))
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
  clearGatewayRuntimeCache()
  res.json(ok(group))
})

groupsRouter.delete('/:id', (req, res) => {
  try {
    if (!deleteGroup(req.params.id)) {
      res.status(404).json({ message: 'Group not found' })
      return
    }
    clearGatewayRuntimeCache()
    res.status(204).send()
  } catch (error) {
    res.status(400).json(badRequest(error instanceof Error ? error.message : 'Delete group failed'))
  }
})

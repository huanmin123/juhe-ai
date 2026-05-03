import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { addAccountToGroup, createAccount, listGroups, resolveProxyUrlForProfile } from '../../storage/repositories.js'
import {
  buildOpenAIOAuthCredentials,
  exchangeOpenAIAuthCode,
  extractCodeAndState,
  generateOpenAIAuthURL,
  refreshOpenAIOAuthToken
} from './openai-oauth.service.js'

export const openAIOAuthRouter = Router()

const authUrlSchema = z.object({
  redirectUri: z.string().optional(),
  clientId: z.string().optional()
})

const createFromCodeSchema = z.object({
  sessionId: z.string().min(1),
  callbackUrl: z.string().optional(),
  code: z.string().optional(),
  state: z.string().optional(),
  redirectUri: z.string().optional(),
  name: z.string().optional(),
  groupId: z.string().optional(),
  concurrencyLimit: z.number().int().min(1).optional(),
  proxyProfileId: z.string().optional(),
  errorPolicyId: z.string().nullable().optional(),
  credentialsPatch: z.record(z.unknown()).optional(),
  notes: z.string().optional()
})

const createFromRefreshTokenSchema = z.object({
  refreshToken: z.string().min(1),
  clientId: z.string().optional(),
  name: z.string().optional(),
  groupId: z.string().optional(),
  concurrencyLimit: z.number().int().min(1).optional(),
  proxyProfileId: z.string().optional(),
  errorPolicyId: z.string().nullable().optional(),
  credentialsPatch: z.record(z.unknown()).optional(),
  notes: z.string().optional()
})

openAIOAuthRouter.post('/auth-url', (req, res) => {
  const parsed = authUrlSchema.safeParse(req.body ?? {})
  if (!parsed.success) {
    res.status(400).json(badRequest('Invalid OpenAI OAuth auth-url payload'))
    return
  }
  res.json(ok(generateOpenAIAuthURL(parsed.data)))
})

openAIOAuthRouter.post('/create-from-code', async (req, res) => {
  const parsed = createFromCodeSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('Invalid OpenAI OAuth code payload'))
    return
  }
  if (parsed.data.groupId && !isOpenAIGroup(parsed.data.groupId)) {
    res.status(400).json(badRequest('Invalid account group'))
    return
  }

  try {
    const { code, state } = extractCodeAndState(parsed.data)
    const tokenInfo = await exchangeOpenAIAuthCode({
      sessionId: parsed.data.sessionId,
      code,
      state,
      redirectUri: parsed.data.redirectUri,
      proxyUrl: resolveProxyUrlForProfile(parsed.data.proxyProfileId)
    })
    const account = createAccount({
      name: parsed.data.name?.trim() || tokenInfo.email || 'OpenAI OAuth Account',
      type: 'oauth',
      credentials: {
        ...buildOpenAIOAuthCredentials(tokenInfo),
        ...(parsed.data.credentialsPatch ?? {})
      },
      status: 'active',
      concurrencyLimit: parsed.data.concurrencyLimit,
      proxyProfileId: parsed.data.proxyProfileId,
      errorPolicyId: parsed.data.errorPolicyId,
      passthroughEnabled: true,
      schedulable: true,
      notes: parsed.data.notes
    })
    if (parsed.data.groupId) {
      addAccountToGroup(parsed.data.groupId, account.id)
    }
    res.status(201).json(ok(account))
  } catch (error) {
    res.status(502).json({ message: error instanceof Error ? error.message : 'OpenAI OAuth code exchange failed' })
  }
})

openAIOAuthRouter.post('/create-from-refresh-token', async (req, res) => {
  const parsed = createFromRefreshTokenSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('Invalid OpenAI refresh token payload'))
    return
  }
  if (parsed.data.groupId && !isOpenAIGroup(parsed.data.groupId)) {
    res.status(400).json(badRequest('Invalid account group'))
    return
  }

  try {
    const tokenInfo = await refreshOpenAIOAuthToken({
      refreshToken: parsed.data.refreshToken,
      clientId: parsed.data.clientId,
      proxyUrl: resolveProxyUrlForProfile(parsed.data.proxyProfileId)
    })
    const account = createAccount({
      name: parsed.data.name?.trim() || tokenInfo.email || 'OpenAI OAuth Account',
      type: 'oauth',
      credentials: {
        ...buildOpenAIOAuthCredentials(tokenInfo, { refreshToken: parsed.data.refreshToken }),
        ...(parsed.data.credentialsPatch ?? {})
      },
      status: 'active',
      concurrencyLimit: parsed.data.concurrencyLimit,
      proxyProfileId: parsed.data.proxyProfileId,
      errorPolicyId: parsed.data.errorPolicyId,
      passthroughEnabled: true,
      schedulable: true,
      notes: parsed.data.notes
    })
    if (parsed.data.groupId) {
      addAccountToGroup(parsed.data.groupId, account.id)
    }
    res.status(201).json(ok(account))
  } catch (error) {
    res.status(502).json({ message: error instanceof Error ? error.message : 'OpenAI refresh token authorization failed' })
  }
})

function isOpenAIGroup(groupId: string): boolean {
  return listGroups().some((group) => group.id === groupId && group.providerCode === 'openai')
}

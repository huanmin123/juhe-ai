import { Router } from 'express'
import type { Response } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { DuplicateAccountCredentialError, ProxyProfileUnavailableError, clearAccountFailureState, createAccount, findAccountForTest, listGroups, resolveProxyUrlForProfile, updateAccount } from '../../storage/repositories.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { clearGatewayRuntimeCache } from '../gateway/gateway-runtime-cache.service.js'
import { operationMode, recordOperationLog, resolveOperationOwner, safeChange, viewer } from '../operation-logs/operation-log.service.js'
import {
  buildOpenAIOAuthCredentials,
  exchangeOpenAIAuthCode,
  extractCodeAndState,
  generateOpenAIAuthURL,
  refreshOpenAIOAuthToken
} from './openai-oauth.service.js'
import { refreshOpenAIOAuthAccountAccessToken } from './openai-oauth-access-token-refresh.service.js'

export const openAIOAuthRouter = Router()

const authUrlSchema = z.object({}).passthrough()

const createFromCodeSchema = z.object({
  sessionId: z.string().min(1),
  callbackUrl: z.string().optional(),
  code: z.string().optional(),
  state: z.string().optional(),
  name: z.string().optional(),
  groupId: z.string().optional(),
  concurrencyLimit: z.number().int().min(1).optional(),
  proxyProfileId: z.string().optional(),
  errorPolicyId: z.string().nullable().optional(),
  accountExpiresAt: z.string().nullable().optional(),
  account_expires_at: z.string().nullable().optional(),
  credentialsPatch: z.record(z.unknown()).optional(),
  notes: z.string().optional()
})

const createFromRefreshTokenSchema = z.object({
  refreshToken: z.string().min(1),
  name: z.string().optional(),
  groupId: z.string().optional(),
  concurrencyLimit: z.number().int().min(1).optional(),
  proxyProfileId: z.string().optional(),
  errorPolicyId: z.string().nullable().optional(),
  accountExpiresAt: z.string().nullable().optional(),
  account_expires_at: z.string().nullable().optional(),
  credentialsPatch: z.record(z.unknown()).optional(),
  notes: z.string().optional()
})

const reauthorizeFromCodeSchema = z.object({
  sessionId: z.string().min(1),
  callbackUrl: z.string().optional(),
  code: z.string().optional(),
  state: z.string().optional()
})

const reauthorizeFromRefreshTokenSchema = z.object({
  refreshToken: z.string().min(1)
})

openAIOAuthRouter.post('/auth-url', (req, res) => {
  const parsed = authUrlSchema.safeParse(req.body ?? {})
  if (!parsed.success) {
    res.status(400).json(badRequest('OpenAI OAuth 授权链接参数无效'))
    return
  }
  res.json(ok(generateOpenAIAuthURL()))
})

openAIOAuthRouter.post('/create-from-code', async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = createFromCodeSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('OpenAI OAuth 授权码参数无效'))
    return
  }
  if (parsed.data.groupId && !isOpenAIGroup(parsed.data.groupId, requestAccess)) {
    res.status(400).json(badRequest('账户分组无效'))
    return
  }

  try {
    const { code, state } = extractCodeAndState(parsed.data)
    const tokenInfo = await exchangeOpenAIAuthCode({
      sessionId: parsed.data.sessionId,
      code,
      state,
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
      accountExpiresAt: parsed.data.accountExpiresAt ?? parsed.data.account_expires_at,
      passthroughEnabled: true,
      schedulable: true,
      groupId: parsed.data.groupId,
      notes: parsed.data.notes
    }, requestAccess)
    recordOAuthCreateLog(account, requestAccess, req, 'openai_oauth.create_from_code', '通过授权码创建 OpenAI OAuth 账户')
    res.status(201).json(ok(account))
  } catch (error) {
    if (error instanceof DuplicateAccountCredentialError) {
      res.status(409).json({ message: error.message })
      return
    }
    if (error instanceof ProxyProfileUnavailableError) {
      res.status(400).json(badRequest(error.message))
      return
    }
    res.status(502).json({ message: error instanceof Error ? error.message : 'OpenAI OAuth 授权码交换失败' })
  }
})

openAIOAuthRouter.post('/create-from-refresh-token', async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = createFromRefreshTokenSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('OpenAI Refresh Token 参数无效'))
    return
  }
  if (parsed.data.groupId && !isOpenAIGroup(parsed.data.groupId, requestAccess)) {
    res.status(400).json(badRequest('账户分组无效'))
    return
  }

  try {
    const tokenInfo = await refreshOpenAIOAuthToken({
      refreshToken: parsed.data.refreshToken,
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
      accountExpiresAt: parsed.data.accountExpiresAt ?? parsed.data.account_expires_at,
      passthroughEnabled: true,
      schedulable: true,
      groupId: parsed.data.groupId,
      notes: parsed.data.notes
    }, requestAccess)
    recordOAuthCreateLog(account, requestAccess, req, 'openai_oauth.create_from_refresh_token', '通过 Refresh Token 创建 OpenAI OAuth 账户')
    res.status(201).json(ok(account))
  } catch (error) {
    if (error instanceof DuplicateAccountCredentialError) {
      res.status(409).json({ message: error.message })
      return
    }
    if (error instanceof ProxyProfileUnavailableError) {
      res.status(400).json(badRequest(error.message))
      return
    }
    res.status(502).json({ message: error instanceof Error ? error.message : 'OpenAI Refresh Token 授权失败' })
  }
})

openAIOAuthRouter.post('/accounts/:id/refresh-token', async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const account = findEditableOpenAIOAuthAccount(req.params.id, requestAccess)
  if (!account) {
    res.status(404).json({ message: 'OpenAI OAuth 账户不存在或无权操作' })
    return
  }

  const abortController = new AbortController()
  req.once('aborted', () => abortController.abort())
  res.once('close', () => {
    if (!res.writableEnded) {
      abortController.abort()
    }
  })
  try {
    const updated = await refreshOpenAIOAuthAccountAccessToken(account, { access: requestAccess, signal: abortController.signal, force: true })
    if (abortController.signal.aborted || res.writableEnded) {
      return
    }
    clearGatewayRuntimeCache()
    recordOAuthUpdateLog(account, updated, requestAccess, req, 'refresh_token', '刷新 OpenAI OAuth Token')
    res.json(ok(updated))
  } catch (error) {
    if (abortController.signal.aborted || res.writableEnded) {
      return
    }
    if (error instanceof ProxyProfileUnavailableError) {
      res.status(400).json(badRequest(error.message))
      return
    }
    res.status(502).json({ message: error instanceof Error ? error.message : 'OpenAI OAuth Token 刷新失败' })
  }
})

openAIOAuthRouter.post('/accounts/:id/reauthorize-from-code', async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = reauthorizeFromCodeSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('OpenAI OAuth 重新授权参数无效'))
    return
  }
  const account = findEditableOpenAIOAuthAccount(req.params.id, requestAccess)
  if (!account) {
    res.status(404).json({ message: 'OpenAI OAuth 账户不存在或无权操作' })
    return
  }

  try {
    const { code, state } = extractCodeAndState(parsed.data)
    const tokenInfo = await exchangeOpenAIAuthCode({
      sessionId: parsed.data.sessionId,
      code,
      state,
      proxyUrl: account.proxyProfileId ? resolveProxyUrlForProfile(account.proxyProfileId) : undefined
    })
    const updated = updateOpenAIOAuthAccountCredentials(account, tokenInfo, undefined, requestAccess)
    recordOAuthUpdateLog(account, updated, requestAccess, req, 'reauthorize_from_code', '重新授权 OpenAI OAuth 账户')
    res.json(ok(updated))
  } catch (error) {
    handleOAuthAccountUpdateError(error, res, 'OpenAI OAuth 重新授权失败')
  }
})

openAIOAuthRouter.post('/accounts/:id/reauthorize-from-refresh-token', async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = reauthorizeFromRefreshTokenSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('OpenAI Refresh Token 参数无效'))
    return
  }
  const account = findEditableOpenAIOAuthAccount(req.params.id, requestAccess)
  if (!account) {
    res.status(404).json({ message: 'OpenAI OAuth 账户不存在或无权操作' })
    return
  }

  try {
    const tokenInfo = await refreshOpenAIOAuthToken({
      refreshToken: parsed.data.refreshToken,
      clientId: stringCredential(account.credentials, 'client_id'),
      proxyUrl: account.proxyProfileId ? resolveProxyUrlForProfile(account.proxyProfileId) : undefined
    })
    const updated = updateOpenAIOAuthAccountCredentials(account, tokenInfo, { refreshToken: parsed.data.refreshToken }, requestAccess)
    recordOAuthUpdateLog(account, updated, requestAccess, req, 'reauthorize_from_refresh_token', '使用 Refresh Token 重新授权 OpenAI OAuth 账户')
    res.json(ok(updated))
  } catch (error) {
    handleOAuthAccountUpdateError(error, res, 'OpenAI OAuth Refresh Token 重新授权失败')
  }
})

function isOpenAIGroup(groupId: string, access?: AccessScope): boolean {
  return listGroups(access).some((group) => group.id === groupId && group.providerCode === 'openai')
}

function findEditableOpenAIOAuthAccount(accountId: string, access?: AccessScope) {
  const account = findAccountForTest(accountId, access)
  if (!account || account.providerCode !== 'openai' || account.type !== 'oauth' || account.permissions?.canEdit === false || account.permissions?.canViewCredentials === false) {
    return undefined
  }
  return account
}

function updateOpenAIOAuthAccountCredentials(
  account: NonNullable<ReturnType<typeof findEditableOpenAIOAuthAccount>>,
  tokenInfo: Awaited<ReturnType<typeof refreshOpenAIOAuthToken>>,
  fallback?: { refreshToken?: string },
  access?: AccessScope
) {
  const credentials = {
    ...account.credentials,
    ...buildOpenAIOAuthCredentials(tokenInfo, fallback)
  }
  const updated = updateAccount(account.id, {
    credentials
  }, access)
  if (!updated) {
    throw new Error('OpenAI OAuth 账户不存在或无法更新')
  }
  clearGatewayRuntimeCache()
  if (updated.status !== 'disabled') {
    return clearAccountFailureState(account.id, access) ?? updated
  }
  return updated
}

function handleOAuthAccountUpdateError(error: unknown, res: Response, fallbackMessage: string): void {
  if (error instanceof DuplicateAccountCredentialError) {
    res.status(409).json({ message: error.message })
    return
  }
  if (error instanceof ProxyProfileUnavailableError) {
    res.status(400).json(badRequest(error.message))
    return
  }
  res.status(502).json({ message: error instanceof Error ? error.message : fallbackMessage })
}

function recordOAuthCreateLog(
  account: ReturnType<typeof createAccount>,
  access: AccessScope | undefined,
  req: Parameters<typeof recordOperationLog>[1],
  operationKey: string,
  summaryPrefix: string
): void {
  const ownerSystemAccountId = resolveOperationOwner(account as unknown as Record<string, unknown>, access)
  recordOperationLog({
    operationScopeSystemAccountId: ownerSystemAccountId,
    mode: operationMode(access),
    module: 'openai_oauth',
    action: 'create_account',
    operationKey,
    resourceType: 'account',
    resourceId: account.id,
    resourceName: account.name,
    summary: `${summaryPrefix}：${account.name}`,
    changes: [
      safeChange('name', '名称', undefined, account.name),
      safeChange('type', '账户类型', undefined, account.type),
      safeChange('credentials', 'OAuth 凭据', undefined, account.credentials),
      safeChange('groupId', '绑定分组', undefined, account.boundGroupId),
      safeChange('proxyProfileId', '代理', undefined, account.proxyProfileId),
      safeChange('accountExpiresAt', '过期时间', undefined, account.accountExpiresAt)
    ],
    viewers: viewer(ownerSystemAccountId, 'resource_owner')
  }, req)
}

function recordOAuthUpdateLog(
  before: NonNullable<ReturnType<typeof findEditableOpenAIOAuthAccount>>,
  after: Awaited<ReturnType<typeof refreshOpenAIOAuthAccountAccessToken>> | ReturnType<typeof updateOpenAIOAuthAccountCredentials>,
  access: AccessScope | undefined,
  req: Parameters<typeof recordOperationLog>[1],
  action: string,
  summaryPrefix: string
): void {
  const ownerSystemAccountId = resolveOperationOwner(after as unknown as Record<string, unknown>, access)
  const resourceName = 'name' in after && typeof after.name === 'string' ? after.name : before.name
  const afterRecord = after as Partial<typeof before>
  recordOperationLog({
    operationScopeSystemAccountId: ownerSystemAccountId,
    mode: operationMode(access),
    module: 'openai_oauth',
    action,
    operationKey: `openai_oauth.${action}`,
    resourceType: 'account',
    resourceId: after.id,
    resourceName,
    summary: `${summaryPrefix}：${resourceName}`,
    changes: [
      safeChange('credentials', 'OAuth 凭据', before.credentials, after.credentials),
      safeChange('status', '状态', before.status, after.status),
      safeChange('cooldownUntil', '冷却结束时间', before.cooldownUntil, afterRecord.cooldownUntil),
      safeChange('lastErrorMessage', '错误信息', before.lastErrorMessage, afterRecord.lastErrorMessage)
    ],
    viewers: viewer(ownerSystemAccountId, 'resource_owner')
  }, req)
}

function stringCredential(credentials: Record<string, unknown>, key: string): string | undefined {
  const value = credentials[key]
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

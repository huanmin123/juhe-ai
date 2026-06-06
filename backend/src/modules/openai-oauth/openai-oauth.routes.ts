import { Router } from 'express'
import type { Response } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { ProxyProfileUnavailableError, clearAccountFailureState, createAccount, findAccountForTest, findGroupSummary, resolveProxyUrlForProfile, updateAccount } from '../../storage/repositories.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { bodyField, mutationGuard, normalizedText, queryField, sensitiveFingerprint, textValue } from '../deduplication/mutation-guard.middleware.js'
import { operationMode, recordOperationLog, resolveOperationOwner, runLoggedOperation, safeChange, viewer, type OperationLogRecordInput } from '../operation-logs/operation-log.service.js'
import { accountErrorPolicyValidationMessage, validateAccountErrorHandlingRules } from '../accounts/account-error-policy-validation.js'
import { sanitizeAccountCredentialCarrierResponse, sanitizeAccountResponse } from '../accounts/account-response-sanitizer.js'
import { accountStreamInterceptValidationMessage, validateAccountStreamInterceptRules } from '../accounts/account-stream-intercept-policy-validation.js'
import {
  buildOpenAIOAuthCredentials,
  exchangeOpenAIAuthCode,
  extractCodeAndState,
  generateOpenAIAuthURL,
  sanitizeOpenAIOAuthErrorMessage,
  type OpenAITokenInfo,
  refreshOpenAIOAuthToken
} from './openai-oauth.service.js'
import { OPENAI_OAUTH_TOKEN_REFRESH_FAILED_ERROR_CODE, refreshOpenAIOAuthAccountAccessToken } from './openai-oauth-access-token-refresh.service.js'

export const openAIOAuthRouter = Router()

const authUrlSchema = z.object({}).strict()
const oauthCredentialsPatchSchema = z.object({
  error_handling_rules: z.unknown().optional(),
  stream_intercept_rules: z.unknown().optional()
}).strict()

const createFromCodeSchema = z.object({
  sessionId: z.string().min(1),
  callbackUrl: z.string().min(1),
  name: z.string().trim().min(1).optional(),
  groupId: z.string().optional(),
  concurrencyLimit: z.number().int().min(1).optional(),
  priority: z.number().int().optional(),
  fallbackEnabled: z.boolean().optional(),
  supportedModels: z.array(z.string().trim().min(1)).max(500).optional(),
  proxyProfileId: z.string().optional(),
  errorPolicyId: z.string().nullable().optional(),
  accountExpiresAt: z.string().nullable().optional(),
  availabilitySchedule: z.record(z.string(), z.unknown()).nullable().optional(),
  credentialsPatch: oauthCredentialsPatchSchema.optional(),
  notes: z.string().optional()
}).strict()

const createFromRefreshTokenSchema = z.object({
  refreshToken: z.string().min(1),
  name: z.string().trim().min(1).optional(),
  groupId: z.string().optional(),
  concurrencyLimit: z.number().int().min(1).optional(),
  priority: z.number().int().optional(),
  fallbackEnabled: z.boolean().optional(),
  supportedModels: z.array(z.string().trim().min(1)).max(500).optional(),
  proxyProfileId: z.string().optional(),
  errorPolicyId: z.string().nullable().optional(),
  accountExpiresAt: z.string().nullable().optional(),
  availabilitySchedule: z.record(z.string(), z.unknown()).nullable().optional(),
  credentialsPatch: oauthCredentialsPatchSchema.optional(),
  notes: z.string().optional()
}).strict()

const reauthorizeFromCodeSchema = z.object({
  sessionId: z.string().min(1),
  callbackUrl: z.string().min(1)
}).strict()

const reauthorizeFromRefreshTokenSchema = z.object({
  refreshToken: z.string().min(1)
}).strict()

openAIOAuthRouter.post('/auth-url', (req, res) => {
  const parsed = authUrlSchema.safeParse(req.body ?? {})
  if (!parsed.success) {
    res.status(400).json(badRequest('OpenAI 授权链接参数无效'))
    return
  }
  res.json(ok(generateOpenAIAuthURL()))
})

openAIOAuthRouter.post('/create-from-code', mutationGuard({
  operationKey: 'openai_oauth.create_from_code',
  processingTtlMs: 180_000,
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    sessionId: textValue(bodyField(req, 'sessionId')),
    callbackUrl: sensitiveFingerprint(bodyField(req, 'callbackUrl'))
  })
}), async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = createFromCodeSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('OpenAI 授权码参数无效'))
    return
  }
  if (parsed.data.groupId && !isOpenAIGroup(parsed.data.groupId, requestAccess)) {
    res.status(400).json(badRequest('账户分组无效'))
    return
  }
  const errorPolicyValidationMessage = oauthCredentialsPatchValidationMessage(parsed.data.credentialsPatch)
  if (errorPolicyValidationMessage) {
    res.status(400).json(badRequest(errorPolicyValidationMessage))
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
    const account = runLoggedOperation(() => {
      const account = createAccount({
        providerCode: 'openai',
        name: parsed.data.name ?? tokenInfo.email ?? 'OpenAI OAuth Account',
        type: 'oauth',
        credentials: buildSafeOpenAIOAuthCredentials(tokenInfo, parsed.data.credentialsPatch),
        status: 'active',
        concurrencyLimit: parsed.data.concurrencyLimit,
        priority: parsed.data.priority,
        fallbackEnabled: parsed.data.fallbackEnabled,
        supportedModels: parsed.data.supportedModels,
        proxyProfileId: parsed.data.proxyProfileId,
        errorPolicyId: parsed.data.errorPolicyId,
        accountExpiresAt: parsed.data.accountExpiresAt,
        availabilitySchedule: parsed.data.availabilitySchedule,
        schedulable: true,
        groupId: parsed.data.groupId,
        notes: parsed.data.notes
      }, requestAccess)
      return {
        result: account,
        log: buildOAuthCreateLog(account, requestAccess, 'openai_oauth.create_from_code', '通过授权码创建 OpenAI OAuth 账户')
      }
    }, req)
    res.status(201).json(ok(sanitizeAccountResponse(account)))
  } catch (error) {
    if (error instanceof ProxyProfileUnavailableError) {
      res.status(400).json(badRequest(error.message))
      return
    }
    res.status(502).json({ message: oauthErrorMessage(error, 'OpenAI 授权码交换失败') })
  }
})

openAIOAuthRouter.post('/create-from-refresh-token', mutationGuard({
  operationKey: 'openai_oauth.create_from_refresh_token',
  processingTtlMs: 180_000,
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    name: normalizedText(bodyField(req, 'name')),
    refreshToken: sensitiveFingerprint(bodyField(req, 'refreshToken'))
  })
}), async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = createFromRefreshTokenSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('OpenAI 刷新令牌参数无效'))
    return
  }
  if (parsed.data.groupId && !isOpenAIGroup(parsed.data.groupId, requestAccess)) {
    res.status(400).json(badRequest('账户分组无效'))
    return
  }
  const errorPolicyValidationMessage = oauthCredentialsPatchValidationMessage(parsed.data.credentialsPatch)
  if (errorPolicyValidationMessage) {
    res.status(400).json(badRequest(errorPolicyValidationMessage))
    return
  }

  try {
    const tokenInfo = await refreshOpenAIOAuthToken({
      refreshToken: parsed.data.refreshToken,
      proxyUrl: resolveProxyUrlForProfile(parsed.data.proxyProfileId)
    })
    const account = runLoggedOperation(() => {
      const account = createAccount({
        providerCode: 'openai',
        name: parsed.data.name ?? tokenInfo.email ?? 'OpenAI OAuth Account',
        type: 'oauth',
        credentials: buildSafeOpenAIOAuthCredentials(tokenInfo, parsed.data.credentialsPatch, { refreshToken: parsed.data.refreshToken }),
        status: 'active',
        concurrencyLimit: parsed.data.concurrencyLimit,
        priority: parsed.data.priority,
        fallbackEnabled: parsed.data.fallbackEnabled,
        supportedModels: parsed.data.supportedModels,
        proxyProfileId: parsed.data.proxyProfileId,
        errorPolicyId: parsed.data.errorPolicyId,
        accountExpiresAt: parsed.data.accountExpiresAt,
        availabilitySchedule: parsed.data.availabilitySchedule,
        schedulable: true,
        groupId: parsed.data.groupId,
        notes: parsed.data.notes
      }, requestAccess)
      return {
        result: account,
        log: buildOAuthCreateLog(account, requestAccess, 'openai_oauth.create_from_refresh_token', '通过 Refresh Token 创建 OpenAI OAuth 账户')
      }
    }, req)
    res.status(201).json(ok(sanitizeAccountResponse(account)))
  } catch (error) {
    if (error instanceof ProxyProfileUnavailableError) {
      res.status(400).json(badRequest(error.message))
      return
    }
    res.status(502).json({ message: oauthErrorMessage(error, 'OpenAI 刷新令牌授权失败') })
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
  if (isBlockedOpenAIOAuthErrorAccount(account)) {
    res.status(400).json(badRequest('异常账户请先恢复异常后再操作'))
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
    recordOperationLog(buildOAuthUpdateLog(account, updated, requestAccess, 'refresh_token', '刷新 OpenAI OAuth Token'), req)
    res.json(ok(sanitizeAccountCredentialCarrierResponse(updated)))
  } catch (error) {
    if (abortController.signal.aborted || res.writableEnded) {
      return
    }
    if (error instanceof ProxyProfileUnavailableError) {
      res.status(400).json(badRequest(error.message))
      return
    }
    res.status(502).json({ message: oauthErrorMessage(error, 'OpenAI 访问令牌刷新失败') })
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
    res.status(400).json(badRequest('OpenAI 重新授权参数无效'))
    return
  }
  const account = findEditableOpenAIOAuthAccount(req.params.id, requestAccess)
  if (!account) {
    res.status(404).json({ message: 'OpenAI OAuth 账户不存在或无权操作' })
    return
  }
  if (isBlockedOpenAIOAuthErrorAccount(account)) {
    res.status(400).json(badRequest('异常账户请先恢复异常后再操作'))
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
    const updated = runLoggedOperation(() => {
      const updated = updateOpenAIOAuthAccountCredentials(account, tokenInfo, undefined, requestAccess)
      return {
        result: updated,
        log: buildOAuthUpdateLog(account, updated, requestAccess, 'reauthorize_from_code', '重新授权 OpenAI OAuth 账户')
      }
    }, req)
    res.json(ok(sanitizeAccountResponse(updated)))
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
    res.status(400).json(badRequest('OpenAI 刷新令牌参数无效'))
    return
  }
  const account = findEditableOpenAIOAuthAccount(req.params.id, requestAccess)
  if (!account) {
    res.status(404).json({ message: 'OpenAI OAuth 账户不存在或无权操作' })
    return
  }
  if (isBlockedOpenAIOAuthErrorAccount(account)) {
    res.status(400).json(badRequest('异常账户请先恢复异常后再操作'))
    return
  }

  try {
    const tokenInfo = await refreshOpenAIOAuthToken({
      refreshToken: parsed.data.refreshToken,
      clientId: stringCredential(account.credentials, 'client_id'),
      proxyUrl: account.proxyProfileId ? resolveProxyUrlForProfile(account.proxyProfileId) : undefined
    })
    const updated = runLoggedOperation(() => {
      const updated = updateOpenAIOAuthAccountCredentials(account, tokenInfo, { refreshToken: parsed.data.refreshToken }, requestAccess)
      return {
        result: updated,
        log: buildOAuthUpdateLog(account, updated, requestAccess, 'reauthorize_from_refresh_token', '使用 Refresh Token 重新授权 OpenAI OAuth 账户')
      }
    }, req)
    res.json(ok(sanitizeAccountResponse(updated)))
  } catch (error) {
    handleOAuthAccountUpdateError(error, res, 'OpenAI 刷新令牌重新授权失败')
  }
})

function isOpenAIGroup(groupId: string, access?: AccessScope): boolean {
  return findGroupSummary(groupId, access)?.providerCode === 'openai'
}

function safeOAuthCredentialsPatch(patch?: z.infer<typeof oauthCredentialsPatchSchema>): Record<string, unknown> {
  const output: Record<string, unknown> = {}
  if (patch?.error_handling_rules !== undefined) {
    output.error_handling_rules = patch.error_handling_rules
  }
  if (patch?.stream_intercept_rules !== undefined) {
    output.stream_intercept_rules = patch.stream_intercept_rules
  }
  return output
}

function oauthCredentialsPatchValidationMessage(patch?: z.infer<typeof oauthCredentialsPatchSchema>): string | undefined {
  if (patch?.error_handling_rules !== undefined) {
    const errorPolicyMessage = accountErrorPolicyValidationMessage(validateAccountErrorHandlingRules(patch.error_handling_rules))
    if (errorPolicyMessage) return errorPolicyMessage
  }
  if (patch?.stream_intercept_rules !== undefined) {
    const streamPolicyMessage = accountStreamInterceptValidationMessage(validateAccountStreamInterceptRules(patch.stream_intercept_rules))
    if (streamPolicyMessage) return streamPolicyMessage
  }
  return undefined
}

export function buildSafeOpenAIOAuthCredentials(
  tokenInfo: OpenAITokenInfo,
  patch?: z.infer<typeof oauthCredentialsPatchSchema>,
  fallback?: { refreshToken?: string }
): Record<string, unknown> {
  return {
    ...safeOAuthCredentialsPatch(patch),
    ...buildOpenAIOAuthCredentials(tokenInfo, fallback)
  }
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
  if (updated.status !== 'disabled' && (updated.status !== 'error' || updated.lastErrorCode === OPENAI_OAUTH_TOKEN_REFRESH_FAILED_ERROR_CODE)) {
    return clearAccountFailureState(account.id, access) ?? updated
  }
  return updated
}

function isBlockedOpenAIOAuthErrorAccount(account: NonNullable<ReturnType<typeof findEditableOpenAIOAuthAccount>>): boolean {
  return account.status === 'error' && account.lastErrorCode !== OPENAI_OAUTH_TOKEN_REFRESH_FAILED_ERROR_CODE
}

function handleOAuthAccountUpdateError(error: unknown, res: Response, fallbackMessage: string): void {
  if (error instanceof ProxyProfileUnavailableError) {
    res.status(400).json(badRequest(error.message))
    return
  }
  res.status(502).json({ message: oauthErrorMessage(error, fallbackMessage) })
}

function oauthErrorMessage(error: unknown, fallbackMessage: string): string {
  return sanitizeOpenAIOAuthErrorMessage(error instanceof Error ? error.message : fallbackMessage)
}

function buildOAuthCreateLog(
  account: ReturnType<typeof createAccount>,
  access: AccessScope | undefined,
  operationKey: string,
  summaryPrefix: string
): OperationLogRecordInput {
  const ownerSystemAccountId = resolveOperationOwner(account as unknown as Record<string, unknown>, access)
  return {
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
      safeChange('supportedModels', '支持模型', undefined, account.supportedModels),
      safeChange('groupId', '绑定分组', undefined, account.boundGroupId),
      safeChange('proxyProfileId', '代理', undefined, account.proxyProfileId),
      safeChange('accountExpiresAt', '过期时间', undefined, account.accountExpiresAt),
      safeChange('availabilitySchedule', '可用时段计划', undefined, account.availabilitySchedule)
    ],
    viewers: viewer(ownerSystemAccountId, 'resource_owner')
  }
}

function buildOAuthUpdateLog(
  before: NonNullable<ReturnType<typeof findEditableOpenAIOAuthAccount>>,
  after: Awaited<ReturnType<typeof refreshOpenAIOAuthAccountAccessToken>> | ReturnType<typeof updateOpenAIOAuthAccountCredentials>,
  access: AccessScope | undefined,
  action: string,
  summaryPrefix: string
): OperationLogRecordInput {
  const ownerSystemAccountId = resolveOperationOwner(after as unknown as Record<string, unknown>, access)
  const resourceName = 'name' in after && typeof after.name === 'string' ? after.name : before.name
  const afterRecord = after as Partial<typeof before>
  return {
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
      safeChange('lastErrorCode', '异常类型', before.lastErrorCode, afterRecord.lastErrorCode),
      safeChange('lastErrorMessage', '错误信息', before.lastErrorMessage, afterRecord.lastErrorMessage)
    ],
    viewers: viewer(ownerSystemAccountId, 'resource_owner')
  }
}

function stringCredential(credentials: Record<string, unknown>, key: string): string | undefined {
  const value = credentials[key]
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

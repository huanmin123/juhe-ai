import { Router } from 'express'
import type { Response } from 'express'
import { z } from 'zod'

import { XAI_OPENAI_V1_PROFILE_ID, XAI_PROVIDER_CODE, isOpenAIProtocolProfile } from '../../domain/provider-protocol.js'
import { badRequest, ok } from '../../shared/http.js'
import { getRequestLogger } from '../../shared/request-context.js'
import type { AccessScope } from '../../storage/access-scope.js'
import {
  AccountConfigRevisionConflictError,
  ProxyProfileUnavailableError,
  createAccountAsync,
  findAccountForTestAsync,
  findGroupSummaryAsync,
  listProvidersAsync,
  resolveProxyUrlForProfileAsync,
  updateAccountAsync
} from '../../storage/repositories.js'
import { dispatchPendingAccountHealthCheck } from '../accounts/account-health-check-dispatch.service.js'
import { accountErrorPolicyValidationMessage, validateAccountErrorHandlingRules } from '../accounts/account-error-policy-validation.js'
import { assertAccountGptRequestOverridesSupportedAsync } from '../accounts/account-gpt-request-overrides.validation.js'
import { sanitizeAccountCredentialCarrierResponse, sanitizeAccountResponse } from '../accounts/account-response-sanitizer.js'
import {
  accountResponseInspectionPolicyValidationMessage,
  validateAccountResponseInspectionRules
} from '../accounts/account-response-inspection-policy-validation.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import {
  bodyField,
  mutationGuard,
  normalizedText,
  queryField,
  sensitiveFingerprint,
  sortedTextValues,
  textValue
} from '../deduplication/mutation-guard.middleware.js'
import {
  operationMode,
  recordOperationLogAsync,
  resolveOperationOwner,
  runLoggedOperationAsync,
  safeChange,
  viewer,
  type OperationLogRecordInput
} from '../operation-logs/operation-log.service.js'
import {
  buildGrokOAuthCredentials,
  exchangeGrokAuthCode,
  exchangeGrokSSOToken,
  generateGrokAuthURL,
  GrokOAuthError,
  refreshGrokAuthToken,
  sanitizeGrokOAuthErrorMessage,
  type GrokOAuthTokenInfo
} from './grok-oauth.service.js'
import { normalizeGrokSSOImportTokens } from './grok-sso-device-flow.js'
import { runWithProviderOAuthRefreshLock } from '../providers/drivers/_shared/oauth-refresh-lock.js'
import { shouldRefreshGrokOAuthCredentials } from '../providers/drivers/xai/oauth-dispatch-preparation.js'

export const grokOAuthRouter = Router()

const authUrlSchema = z.object({}).strict()
const oauthCredentialsPatchSchema = z.object({
  base_url: z.string().trim().min(1).optional(),
  supported_endpoint_modes: z.array(z.string().trim().min(1)).max(20).optional(),
  error_handling_rules: z.unknown().optional(),
  response_inspection_rules: z.unknown().optional()
}).strict()

const accountModelMappingSchema = z.object({
  sourceModel: z.string().trim().min(1),
  sourceEndpointFamily: z.enum(['chat_completions', 'responses', 'messages', 'generate_content', 'stream_generate_content']),
  upstreamModel: z.string().trim().min(1),
  upstreamEndpointFamily: z.enum(['chat_completions', 'responses', 'messages', 'generate_content']),
  enabled: z.boolean().optional()
}).strict()

const managedAccountFields = {
  providerProtocolProfileId: z.string().trim().min(1),
  name: z.string().trim().min(1).optional(),
  groupId: z.string().optional(),
  concurrencyLimit: z.number().int().min(1).optional(),
  priority: z.number().int().optional(),
  status: z.enum(['active', 'pending_test', 'disabled']).optional(),
  superPriorityEnabled: z.boolean().optional(),
  fallbackEnabled: z.boolean().optional(),
  supportedModels: z.array(z.string().trim().min(1)).min(1).max(500).optional(),
  healthCheckModel: z.string().trim().min(1).optional(),
  healthCheckEndpointMode: z.enum([
    'chat_json',
    'chat_sse',
    'responses_json',
    'responses_sse',
    'messages_json',
    'messages_sse',
    'generate_content_json',
    'generate_content_sse'
  ]).optional(),
  temporaryUnavailableContinuousProbeEnabled: z.boolean().optional(),
  modelMappings: z.array(accountModelMappingSchema).max(500).optional(),
  tags: z.array(z.string().trim()).max(24).optional(),
  proxyProfileId: z.string().optional(),
  accountExpiresAt: z.string().nullable().optional(),
  availabilitySchedule: z.record(z.string(), z.unknown()).nullable().optional(),
  credentialsPatch: oauthCredentialsPatchSchema.optional(),
  notes: z.string().optional()
} as const

const createFromCodeSchema = z.object({
  sessionId: z.string().min(1),
  callbackUrl: z.string().min(1),
  ...managedAccountFields
}).strict()

const createFromRefreshTokenSchema = z.object({
  refreshToken: z.string().min(1),
  ...managedAccountFields
}).strict()

const createFromSSOSchema = z.object({
  ssoTokens: z.array(z.string().max(16_384)).max(3).optional().default([]),
  ssoToken: z.string().max(16_384).optional(),
  ...managedAccountFields
}).strict()

const reauthorizeFromCodeSchema = z.object({
  sessionId: z.string().min(1),
  callbackUrl: z.string().min(1)
}).strict()

const reauthorizeFromRefreshTokenSchema = z.object({ refreshToken: z.string().min(1) }).strict()

grokOAuthRouter.post('/auth-url', async (req, res, next) => {
  const parsed = authUrlSchema.safeParse(req.body ?? {})
  if (!parsed.success) {
    res.status(400).json(badRequest('Grok 授权链接参数无效'))
    return
  }
  try {
    res.json(ok(await generateGrokAuthURL(getRequestAccessScope()?.systemAccountId)))
  } catch (error) {
    next(error)
  }
})

grokOAuthRouter.post('/create-from-code', mutationGuard({
  operationKey: 'grok_oauth.create_from_code',
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
    res.status(400).json(badRequest('Grok 授权码参数无效'))
    return
  }
  const providerProfile = await resolveGrokOAuthProviderProfile(parsed.data.providerProtocolProfileId)
  if (!providerProfile.ok) {
    res.status(400).json(badRequest(providerProfile.message))
    return
  }
  const group = parsed.data.groupId ? await findGroupSummaryAsync(parsed.data.groupId, requestAccess) : undefined
  if (parsed.data.groupId && !isGrokOAuthGroupSummary(group)) {
    res.status(400).json(badRequest('账户分组无效'))
    return
  }
  const patchValidationMessage = oauthCredentialsPatchValidationMessage(parsed.data.credentialsPatch)
  if (patchValidationMessage) {
    res.status(400).json(badRequest(patchValidationMessage))
    return
  }

  try {
    await assertAccountGptRequestOverridesSupportedAsync({
      providerCode: XAI_PROVIDER_CODE,
      accountType: 'oauth',
      credentials: safeOAuthCredentialsPatch(parsed.data.credentialsPatch),
      supportedModels: parsed.data.supportedModels ?? providerProfile.provider.defaultSupportedModels,
      systemAccountId: requestAccess?.systemAccountFilterId ?? requestAccess?.systemAccountId
    })
    const tokenInfo = await exchangeGrokAuthCode({
      sessionId: parsed.data.sessionId,
      callbackUrl: parsed.data.callbackUrl,
      ownerSystemAccountId: requestAccess?.systemAccountId,
      proxyUrl: await resolveProxyUrlForProfileAsync(parsed.data.proxyProfileId)
    })
    const account = await runLoggedOperationAsync(async () => {
      const account = await createGrokOAuthAccount(parsed.data, providerProfile, tokenInfo, requestAccess)
      return {
        result: account,
        log: buildOAuthCreateLog(account, requestAccess, 'grok_oauth.create_from_code', '通过授权码创建 Grok OAuth 账户')
      }
    }, req)
    dispatchPendingAccountHealthCheck(account)
    res.status(201).json(ok({ id: account.id, status: account.status }))
  } catch (error) {
    handleOAuthCreateError(error, res, 'Grok 授权码交换失败')
  }
})

grokOAuthRouter.post('/create-from-refresh-token', mutationGuard({
  operationKey: 'grok_oauth.create_from_refresh_token',
  processingTtlMs: 180_000,
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    name: normalizedText(bodyField(req, 'name')),
    refreshToken: sensitiveFingerprint(bodyField(req, 'refreshToken')),
    status: 'pending_test'
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
    res.status(400).json(badRequest('Grok 刷新令牌参数无效'))
    return
  }
  const providerProfile = await resolveGrokOAuthProviderProfile(parsed.data.providerProtocolProfileId)
  if (!providerProfile.ok) {
    res.status(400).json(badRequest(providerProfile.message))
    return
  }
  const group = parsed.data.groupId ? await findGroupSummaryAsync(parsed.data.groupId, requestAccess) : undefined
  if (parsed.data.groupId && !isGrokOAuthGroupSummary(group)) {
    res.status(400).json(badRequest('账户分组无效'))
    return
  }
  const patchValidationMessage = oauthCredentialsPatchValidationMessage(parsed.data.credentialsPatch)
  if (patchValidationMessage) {
    res.status(400).json(badRequest(patchValidationMessage))
    return
  }

  try {
    await assertAccountGptRequestOverridesSupportedAsync({
      providerCode: XAI_PROVIDER_CODE,
      accountType: 'oauth',
      credentials: safeOAuthCredentialsPatch(parsed.data.credentialsPatch),
      supportedModels: parsed.data.supportedModels ?? providerProfile.provider.defaultSupportedModels,
      systemAccountId: requestAccess?.systemAccountFilterId ?? requestAccess?.systemAccountId
    })
    const tokenInfo = await refreshGrokAuthToken({
      refreshToken: parsed.data.refreshToken,
      proxyUrl: await resolveProxyUrlForProfileAsync(parsed.data.proxyProfileId)
    })
    const account = await runLoggedOperationAsync(async () => {
      const account = await createGrokOAuthAccount(parsed.data, providerProfile, tokenInfo, requestAccess, {
        refreshToken: parsed.data.refreshToken
      })
      return {
        result: account,
        log: buildOAuthCreateLog(account, requestAccess, 'grok_oauth.create_from_refresh_token', '通过 Refresh Token 创建 Grok OAuth 账户')
      }
    }, req)
    dispatchPendingAccountHealthCheck(account)
    res.status(201).json(ok({ id: account.id, status: account.status }))
  } catch (error) {
    handleOAuthCreateError(error, res, 'Grok 刷新令牌授权失败')
  }
})

grokOAuthRouter.post('/sso-to-oauth', mutationGuard({
  operationKey: 'grok_oauth.sso_to_oauth',
  processingTtlMs: 15 * 60_000,
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    ssoTokens: sensitiveFingerprint(normalizeGrokSSOImportTokens(
      sortedTextValues(bodyField(req, 'ssoTokens')),
      textValue(bodyField(req, 'ssoToken'))
    ).sort().join('\n')),
    providerProtocolProfileId: normalizedText(bodyField(req, 'providerProtocolProfileId')),
    proxyProfileId: normalizedText(bodyField(req, 'proxyProfileId'))
  })
}), async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = createFromSSOSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('Grok SSO 导入参数无效'))
    return
  }
  const tokens = normalizeGrokSSOImportTokens(parsed.data.ssoTokens, parsed.data.ssoToken)
  if (!tokens.length) {
    res.status(400).json(badRequest('Grok SSO Cookie 不能为空'))
    return
  }
  if (tokens.length > 3) {
    res.status(400).json(badRequest('Grok SSO Cookie 单次最多导入 3 个'))
    return
  }
  const providerProfile = await resolveGrokOAuthProviderProfile(parsed.data.providerProtocolProfileId)
  if (!providerProfile.ok) {
    res.status(400).json(badRequest(providerProfile.message))
    return
  }
  const group = parsed.data.groupId ? await findGroupSummaryAsync(parsed.data.groupId, requestAccess) : undefined
  if (parsed.data.groupId && !isGrokOAuthGroupSummary(group)) {
    res.status(400).json(badRequest('账户分组无效'))
    return
  }
  const patchValidationMessage = oauthCredentialsPatchValidationMessage(parsed.data.credentialsPatch)
  if (patchValidationMessage) {
    res.status(400).json(badRequest(patchValidationMessage))
    return
  }

  const abortController = new AbortController()
  req.once('aborted', () => abortController.abort())
  res.once('close', () => {
    if (!res.writableEnded) abortController.abort()
  })

  try {
    await assertAccountGptRequestOverridesSupportedAsync({
      providerCode: XAI_PROVIDER_CODE,
      accountType: 'oauth',
      credentials: safeOAuthCredentialsPatch(parsed.data.credentialsPatch),
      supportedModels: parsed.data.supportedModels ?? providerProfile.provider.defaultSupportedModels,
      systemAccountId: requestAccess?.systemAccountFilterId ?? requestAccess?.systemAccountId
    })
    const proxyUrl = await resolveProxyUrlForProfileAsync(parsed.data.proxyProfileId)
    const results = await mapWithConcurrency(tokens, 3, async (ssoToken, zeroBasedIndex) => {
      const index = zeroBasedIndex + 1
      try {
        const tokenInfo = await exchangeGrokSSOToken({ ssoToken, proxyUrl, signal: abortController.signal })
        const name = grokSSOImportAccountName(parsed.data.name, tokenInfo, index, tokens.length)
        const accountExpiresAt = grokSSOImportAccountExpiresAt(parsed.data.accountExpiresAt, tokenInfo)
        const accountInput = { ...parsed.data, name, accountExpiresAt }
        const account = await createGrokOAuthAccount(accountInput, providerProfile, tokenInfo, requestAccess)
        await recordOperationLogAsync(
          buildOAuthCreateLog(account, requestAccess, 'grok_oauth.sso_to_oauth', '通过 SSO Cookie 创建 Grok OAuth 账户'),
          req
        )
        dispatchPendingAccountHealthCheck(account)
        return {
          created: true as const,
          item: { index }
        }
      } catch (error) {
        if (abortController.signal.aborted) throw error
        const message = oauthErrorMessage(error, 'Grok SSO Cookie 转换失败')
        getRequestLogger().warn({
          event: 'grok_sso_import_item_failed',
          index,
          errorMessage: message
        }, 'Grok SSO 导入项失败')
        return {
          created: false as const,
          item: {
            index,
            error: message
          }
        }
      }
    }, abortController.signal)
    if (abortController.signal.aborted || res.writableEnded) return
    res.json(ok({
      createdCount: results.filter((result) => result.created).length,
      failed: results.filter((result) => !result.created).map((result) => result.item)
    }))
  } catch (error) {
    if (abortController.signal.aborted || res.writableEnded) return
    handleOAuthCreateError(error, res, 'Grok SSO Cookie 批量导入失败')
  }
})

grokOAuthRouter.post('/accounts/:id/refresh-token', async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const account = await findEditableGrokOAuthAccount(req.params.id, requestAccess)
  if (!account) {
    res.status(404).json({ message: 'Grok OAuth 账户不存在或无权操作' })
    return
  }
  const refreshToken = stringCredential(account.credentials, 'refresh_token')
  if (!refreshToken) {
    res.status(400).json(badRequest('Grok OAuth 账户缺少 Refresh Token'))
    return
  }

  const abortController = new AbortController()
  req.once('aborted', () => abortController.abort())
  res.once('close', () => {
    if (!res.writableEnded) abortController.abort()
  })

  try {
    const updatedAccount = await runWithProviderOAuthRefreshLock(
      XAI_PROVIDER_CODE,
      account.id,
      async (lockSignal, assertLockOwned) => {
        const current = await findEditableGrokOAuthAccount(account.id, requestAccess)
        if (!current) throw new Error('Grok OAuth 账户不存在或无权操作')
        if (oauthTokensChanged(account.credentials, current.credentials)
          && !shouldRefreshGrokOAuthCredentials(current.credentials)) {
          return current
        }
        const currentRefreshToken = stringCredential(current.credentials, 'refresh_token')
        if (!currentRefreshToken) throw new Error('Grok OAuth 账户缺少 Refresh Token')
        const tokenInfo = await refreshGrokAuthToken({
          refreshToken: currentRefreshToken,
          clientId: stringCredential(current.credentials, 'client_id'),
          proxyUrl: current.proxyProfileId ? await resolveProxyUrlForProfileAsync(current.proxyProfileId) : undefined,
          signal: lockSignal
        })
        await assertLockOwned()
        return await updateGrokOAuthAccountCredentials(current, tokenInfo, undefined, requestAccess)
      },
      { signal: abortController.signal }
    )
    if (abortController.signal.aborted || res.writableEnded) return
    await recordOperationLogAsync(
      buildOAuthUpdateLog(account, updatedAccount, requestAccess, 'refresh_token', '刷新 Grok OAuth Token'),
      req
    )
    res.json(ok(sanitizeAccountCredentialCarrierResponse(updatedAccount)))
  } catch (error) {
    if (abortController.signal.aborted || res.writableEnded) return
    handleOAuthAccountUpdateError(error, res, 'Grok 访问令牌刷新失败')
  }
})

grokOAuthRouter.post('/accounts/:id/reauthorize-from-code', async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = reauthorizeFromCodeSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('Grok 重新授权参数无效'))
    return
  }
  const account = await findEditableGrokOAuthAccount(req.params.id, requestAccess)
  if (!account) {
    res.status(404).json({ message: 'Grok OAuth 账户不存在或无权操作' })
    return
  }

  try {
    const updated = await runWithProviderOAuthRefreshLock(XAI_PROVIDER_CODE, account.id, async (lockSignal, assertLockOwned) => {
      const current = await findEditableGrokOAuthAccount(account.id, requestAccess)
      if (!current) throw new Error('Grok OAuth 账户不存在或无权操作')
      if (oauthTokensChanged(account.credentials, current.credentials)) {
        throw new AccountConfigRevisionConflictError(account.id, account.configRevision ?? 1, current.configRevision)
      }
      const tokenInfo = await exchangeGrokAuthCode({
        sessionId: parsed.data.sessionId,
        callbackUrl: parsed.data.callbackUrl,
        ownerSystemAccountId: requestAccess?.systemAccountId,
        proxyUrl: current.proxyProfileId ? await resolveProxyUrlForProfileAsync(current.proxyProfileId) : undefined,
        signal: lockSignal
      })
      await assertLockOwned()
      return await runLoggedOperationAsync(async () => {
        const result = await updateGrokOAuthAccountCredentials(current, tokenInfo, undefined, requestAccess)
        return {
          result,
          log: buildOAuthUpdateLog(current, result, requestAccess, 'reauthorize_from_code', '重新授权 Grok OAuth 账户')
        }
      }, req)
    })
    res.json(ok(sanitizeAccountResponse(updated)))
  } catch (error) {
    handleOAuthAccountUpdateError(error, res, 'Grok OAuth 重新授权失败')
  }
})

grokOAuthRouter.post('/accounts/:id/reauthorize-from-refresh-token', async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = reauthorizeFromRefreshTokenSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('Grok 刷新令牌参数无效'))
    return
  }
  const account = await findEditableGrokOAuthAccount(req.params.id, requestAccess)
  if (!account) {
    res.status(404).json({ message: 'Grok OAuth 账户不存在或无权操作' })
    return
  }

  try {
    const updated = await runWithProviderOAuthRefreshLock(XAI_PROVIDER_CODE, account.id, async (lockSignal, assertLockOwned) => {
      const current = await findEditableGrokOAuthAccount(account.id, requestAccess)
      if (!current) throw new Error('Grok OAuth 账户不存在或无权操作')
      if (oauthTokensChanged(account.credentials, current.credentials)) {
        throw new AccountConfigRevisionConflictError(account.id, account.configRevision ?? 1, current.configRevision)
      }
      const tokenInfo = await refreshGrokAuthToken({
        refreshToken: parsed.data.refreshToken,
        clientId: stringCredential(current.credentials, 'client_id'),
        proxyUrl: current.proxyProfileId ? await resolveProxyUrlForProfileAsync(current.proxyProfileId) : undefined,
        signal: lockSignal
      })
      await assertLockOwned()
      return await runLoggedOperationAsync(async () => {
        const result = await updateGrokOAuthAccountCredentials(current, tokenInfo, {
          refreshToken: parsed.data.refreshToken
        }, requestAccess)
        return {
          result,
          log: buildOAuthUpdateLog(
            current,
            result,
            requestAccess,
            'reauthorize_from_refresh_token',
            '使用 Refresh Token 重新授权 Grok OAuth 账户'
          )
        }
      }, req)
    })
    res.json(ok(sanitizeAccountResponse(updated)))
  } catch (error) {
    handleOAuthAccountUpdateError(error, res, 'Grok 刷新令牌重新授权失败')
  }
})

type GrokOAuthProvider = Awaited<ReturnType<typeof listProvidersAsync>>[number]
type GrokOAuthProviderProfile = GrokOAuthProvider['protocolProfiles'][number]
type ManagedAccountInput = z.infer<typeof createFromCodeSchema> | z.infer<typeof createFromRefreshTokenSchema> | z.infer<typeof createFromSSOSchema>

type GrokOAuthProviderProfileResult =
  | { ok: true; provider: GrokOAuthProvider; profile: GrokOAuthProviderProfile }
  | { ok: false; message: string }

async function resolveGrokOAuthProviderProfile(providerProtocolProfileId: string): Promise<GrokOAuthProviderProfileResult> {
  const provider = (await listProvidersAsync()).find((item) => item.code === XAI_PROVIDER_CODE)
  if (!provider) return { ok: false, message: `不支持的供应商：${XAI_PROVIDER_CODE}` }
  if (!provider.enabled) return { ok: false, message: `供应商已停用：${XAI_PROVIDER_CODE}` }
  const profileId = providerProtocolProfileId.trim()
  const profile = provider.protocolProfiles.find((item) => item.id === profileId)
  if (!profile || profile.providerCode !== XAI_PROVIDER_CODE) {
    return { ok: false, message: `供应商协议档案无效：${profileId}` }
  }
  if (!profile.enabled) return { ok: false, message: `供应商协议档案已停用：${profile.name}` }
  if (!isOpenAIProtocolProfile(profile) || profile.id !== XAI_OPENAI_V1_PROFILE_ID || !profile.accountTypes.includes('oauth')) {
    return { ok: false, message: `供应商协议档案 ${profile.name} 不支持 Grok OAuth` }
  }
  return { ok: true, provider, profile }
}

async function createGrokOAuthAccount(
  input: ManagedAccountInput,
  providerProfile: Extract<GrokOAuthProviderProfileResult, { ok: true }>,
  tokenInfo: GrokOAuthTokenInfo,
  access?: AccessScope,
  fallback?: { refreshToken?: string }
) {
  return await createAccountAsync({
    providerCode: XAI_PROVIDER_CODE,
    providerProtocolProfileId: providerProfile.profile.id,
    name: input.name ?? tokenInfo.email ?? 'Grok OAuth Account',
    type: 'oauth',
    credentials: buildSafeGrokOAuthCredentials(tokenInfo, input.credentialsPatch, fallback),
    status: 'pending_test',
    skipInitialHealthCheck: false,
    concurrencyLimit: input.concurrencyLimit ?? 1,
    priority: input.priority,
    superPriorityEnabled: input.superPriorityEnabled,
    fallbackEnabled: input.fallbackEnabled,
    supportedModels: input.supportedModels ?? providerProfile.provider.defaultSupportedModels,
    healthCheckModel: input.healthCheckModel,
    healthCheckEndpointMode: input.healthCheckEndpointMode,
    temporaryUnavailableContinuousProbeEnabled: input.temporaryUnavailableContinuousProbeEnabled,
    modelMappings: input.modelMappings,
    tags: input.tags,
    proxyProfileId: input.proxyProfileId,
    accountExpiresAt: input.accountExpiresAt,
    availabilitySchedule: input.availabilitySchedule,
    schedulable: false,
    groupId: input.groupId,
    notes: input.notes
  }, access)
}

function isGrokOAuthGroupSummary(group: Awaited<ReturnType<typeof findGroupSummaryAsync>> | undefined): boolean {
  return Boolean(group && group.providerCode === XAI_PROVIDER_CODE)
}

function grokSSOImportAccountName(baseName: string | undefined, tokenInfo: GrokOAuthTokenInfo, index: number, total: number): string {
  const base = baseName?.trim() || tokenInfo.email?.trim() || 'Grok OAuth Account'
  return total > 1 ? `${base} #${index}` : base
}

function grokSSOImportAccountExpiresAt(requested: string | null | undefined, tokenInfo: GrokOAuthTokenInfo): string | null | undefined {
  if (tokenInfo.refreshToken) return requested
  const tokenExpiresAt = Date.parse(tokenInfo.expiresAt)
  if (!Number.isFinite(tokenExpiresAt)) return requested
  if (!requested) return tokenInfo.expiresAt
  const requestedExpiresAt = Date.parse(requested)
  return Number.isFinite(requestedExpiresAt) && requestedExpiresAt < tokenExpiresAt ? requested : tokenInfo.expiresAt
}

async function mapWithConcurrency<T, R>(
  items: T[],
  concurrency: number,
  worker: (item: T, index: number) => Promise<R>,
  signal?: AbortSignal
): Promise<R[]> {
  const output = new Array<R>(items.length)
  let nextIndex = 0
  const workers = Array.from({ length: Math.min(concurrency, items.length) }, async () => {
    while (nextIndex < items.length) {
      if (signal?.aborted) {
        throw signal.reason instanceof Error ? signal.reason : new Error('请求已取消')
      }
      const index = nextIndex
      nextIndex += 1
      output[index] = await worker(items[index]!, index)
    }
  })
  await Promise.all(workers)
  return output
}

function safeOAuthCredentialsPatch(patch?: z.infer<typeof oauthCredentialsPatchSchema>): Record<string, unknown> {
  const output: Record<string, unknown> = {}
  if (patch?.base_url !== undefined) output.base_url = patch.base_url
  if (patch?.supported_endpoint_modes !== undefined) output.supported_endpoint_modes = patch.supported_endpoint_modes
  if (patch?.error_handling_rules !== undefined) output.error_handling_rules = patch.error_handling_rules
  if (patch?.response_inspection_rules !== undefined) output.response_inspection_rules = patch.response_inspection_rules
  return output
}

function oauthCredentialsPatchValidationMessage(patch?: z.infer<typeof oauthCredentialsPatchSchema>): string | undefined {
  if (patch?.error_handling_rules !== undefined) {
    const message = accountErrorPolicyValidationMessage(validateAccountErrorHandlingRules(patch.error_handling_rules))
    if (message) return message
  }
  if (patch?.response_inspection_rules !== undefined) {
    const message = accountResponseInspectionPolicyValidationMessage(
      validateAccountResponseInspectionRules(patch.response_inspection_rules)
    )
    if (message) return message
  }
  return undefined
}

function buildSafeGrokOAuthCredentials(
  tokenInfo: GrokOAuthTokenInfo,
  patch?: z.infer<typeof oauthCredentialsPatchSchema>,
  fallback?: { refreshToken?: string }
): Record<string, unknown> {
  return { ...buildGrokOAuthCredentials(tokenInfo, fallback), ...safeOAuthCredentialsPatch(patch) }
}

async function findEditableGrokOAuthAccount(accountId: string, access?: AccessScope) {
  const account = await findAccountForTestAsync(accountId, access)
  if (
    !account
    || account.providerCode !== XAI_PROVIDER_CODE
    || account.providerProtocolProfileId !== XAI_OPENAI_V1_PROFILE_ID
    || !isOpenAIProtocolProfile(account)
    || account.type !== 'oauth'
    || account.permissions?.canEdit === false
    || account.permissions?.canViewCredentials === false
  ) return undefined
  return account
}

async function updateGrokOAuthAccountCredentials(
  account: NonNullable<Awaited<ReturnType<typeof findEditableGrokOAuthAccount>>>,
  tokenInfo: GrokOAuthTokenInfo,
  fallback?: { refreshToken?: string },
  access?: AccessScope
) {
  const credentials = { ...account.credentials, ...buildGrokOAuthCredentials(tokenInfo, fallback) }
  const existingBaseUrl = stringCredential(account.credentials, 'base_url')
  if (existingBaseUrl) credentials.base_url = existingBaseUrl
  const updated = await updateAccountAsync(account.id, { credentials }, access, {
    expectedConfigRevision: account.configRevision ?? 1
  })
  if (!updated) throw new Error('Grok OAuth 账户不存在或无法更新')
  return updated
}

function handleOAuthCreateError(error: unknown, res: Response, fallbackMessage: string): void {
  if (error instanceof ProxyProfileUnavailableError) {
    res.status(400).json(badRequest(error.message))
    return
  }
  if (error instanceof GrokOAuthError) {
    if (error.statusCode === 400) res.status(400).json(badRequest(oauthErrorMessage(error, fallbackMessage)))
    else res.status(error.statusCode).json({ message: oauthErrorMessage(error, fallbackMessage) })
    return
  }
  if (isOAuthBusinessConflictError(error)) {
    res.status(409).json(badRequest(oauthErrorMessage(error, fallbackMessage)))
    return
  }
  res.status(502).json({ message: oauthErrorMessage(error, fallbackMessage) })
}

function handleOAuthAccountUpdateError(error: unknown, res: Response, fallbackMessage: string): void {
  handleOAuthCreateError(error, res, fallbackMessage)
}

function oauthErrorMessage(error: unknown, fallbackMessage: string): string {
  return sanitizeGrokOAuthErrorMessage(error instanceof Error ? error.message : fallbackMessage)
}

function isOAuthBusinessConflictError(error: unknown): boolean {
  return error instanceof AccountConfigRevisionConflictError
    || (error instanceof Error && error.message.includes('已存在'))
}

function buildOAuthCreateLog(
  account: Awaited<ReturnType<typeof createAccountAsync>>,
  access: AccessScope | undefined,
  operationKey: string,
  summaryPrefix: string
): OperationLogRecordInput {
  const ownerSystemAccountId = resolveOperationOwner(account as unknown as Record<string, unknown>, access)
  return {
    operationScopeSystemAccountId: ownerSystemAccountId,
    mode: operationMode(access),
    module: 'grok_oauth',
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
      safeChange('healthCheckModel', '检查模型', undefined, account.healthCheckModel),
      safeChange('temporaryUnavailableContinuousProbeEnabled', '持续恢复探活', undefined, account.temporaryUnavailableContinuousProbeEnabled),
      safeChange('modelMappings', '模型映射', undefined, account.modelMappings),
      safeChange('tags', '标签', undefined, account.tags),
      safeChange('groupId', '绑定分组', undefined, account.boundGroupId),
      safeChange('proxyProfileId', '代理', undefined, account.proxyProfileId),
      safeChange('accountExpiresAt', '过期时间', undefined, account.accountExpiresAt),
      safeChange('availabilitySchedule', '时间计划', undefined, account.availabilitySchedule)
    ],
    viewers: viewer(ownerSystemAccountId, 'resource_owner')
  }
}

function buildOAuthUpdateLog(
  before: NonNullable<Awaited<ReturnType<typeof findEditableGrokOAuthAccount>>>,
  after: NonNullable<Awaited<ReturnType<typeof updateAccountAsync>>>,
  access: AccessScope | undefined,
  action: string,
  summaryPrefix: string
): OperationLogRecordInput {
  const ownerSystemAccountId = resolveOperationOwner(after as unknown as Record<string, unknown>, access)
  return {
    operationScopeSystemAccountId: ownerSystemAccountId,
    mode: operationMode(access),
    module: 'grok_oauth',
    action,
    operationKey: `grok_oauth.${action}`,
    resourceType: 'account',
    resourceId: after.id,
    resourceName: after.name,
    summary: `${summaryPrefix}：${after.name}`,
    changes: [
      safeChange('credentials', 'OAuth 凭据', before.credentials, after.credentials),
      safeChange('status', '状态', before.status, after.status),
      safeChange('cooldownUntil', '冷却结束时间', before.cooldownUntil, after.cooldownUntil),
      safeChange('lastErrorCode', '异常类型', before.lastErrorCode, after.lastErrorCode),
      safeChange('lastErrorMessage', '错误信息', before.lastErrorMessage, after.lastErrorMessage)
    ],
    viewers: viewer(ownerSystemAccountId, 'resource_owner')
  }
}

function stringCredential(credentials: Record<string, unknown>, key: string): string | undefined {
  const value = credentials[key]
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function oauthTokensChanged(before: Record<string, unknown>, after: Record<string, unknown>): boolean {
  return stringCredential(before, 'access_token') !== stringCredential(after, 'access_token')
    || stringCredential(before, 'refresh_token') !== stringCredential(after, 'refresh_token')
}

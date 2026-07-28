import { Router } from 'express'
import type { Response } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { AccountConfigRevisionConflictError, ProxyProfileUnavailableError, createAccountAsync, findAccountForTestAsync, findGroupSummaryAsync, listProvidersAsync, resolveProxyUrlForProfileAsync, updateAccountAsync } from '../../storage/repositories.js'
import { GEMINI_PROVIDER_CODE, isGeminiProtocolProfile } from '../../domain/provider-protocol.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { getRequestAccessScope } from '../auth/request-context.js'
import { parseRequestScopeQuery } from '../auth/request-scope-query.js'
import { bodyField, mutationGuard, normalizedText, queryField, sensitiveFingerprint, textValue } from '../deduplication/mutation-guard.middleware.js'
import { operationMode, recordOperationLogAsync, resolveOperationOwner, runLoggedOperationAsync, safeChange, viewer, type OperationLogRecordInput } from '../operation-logs/operation-log.service.js'
import { sanitizeAccountCredentialCarrierResponse, sanitizeAccountResponse } from '../accounts/account-response-sanitizer.js'
import { accountErrorPolicyValidationMessage, validateAccountErrorHandlingRules } from '../accounts/account-error-policy-validation.js'
import { accountResponseInspectionPolicyValidationMessage, validateAccountResponseInspectionRules } from '../accounts/account-response-inspection-policy-validation.js'
import { assertAccountGptRequestOverridesSupportedAsync } from '../accounts/account-gpt-request-overrides.validation.js'
import { dispatchPendingAccountHealthCheck } from '../accounts/account-health-check-dispatch.service.js'
import { runWithProviderOAuthRefreshLock } from '../providers/drivers/_shared/oauth-refresh-lock.js'
import { shouldRefreshGeminiOAuthCredentials } from '../providers/drivers/gemini/oauth-dispatch-preparation.js'
import {
  GEMINI_CLI_OAUTH_CLIENT_ID,
  buildGeminiOAuthCredentials,
  exchangeGeminiAuthCode,
  generateGeminiAuthURL,
  getGeminiOAuthCapabilities,
  refreshGeminiAuthToken,
  sanitizeGeminiOAuthErrorMessage,
  type GeminiOAuthType,
  type GeminiOAuthTokenInfo
} from './gemini-oauth.service.js'

export const geminiOAuthRouter = Router()

const optionalTrimmedTextSchema = z.preprocess(
  (value) => typeof value === 'string' && !value.trim() ? undefined : value,
  z.string().trim().min(1).optional()
)
const oauthTypeSchema = z.enum(['code_assist', 'google_one', 'ai_studio'])

const authUrlSchema = z.object({
  oauthType: oauthTypeSchema.optional(),
  clientId: optionalTrimmedTextSchema,
  clientSecret: optionalTrimmedTextSchema,
  projectId: optionalTrimmedTextSchema,
  tierId: optionalTrimmedTextSchema,
  quotaProjectId: z.string().trim().min(1).optional(),
  baseUrl: z.string().trim().url().optional()
}).strict()
const oauthCredentialsPatchSchema = z.object({
  quota_project_id: z.string().trim().min(1).optional(),
  base_url: z.string().trim().url().optional(),
  supported_endpoint_modes: z.array(z.string().trim().min(1)).max(20).optional(),
  service_tier_override: z.string().trim().min(1).optional(),
  reasoning_effort_override: z.string().trim().min(1).optional(),
  error_handling_rules: z.unknown().optional(),
  response_inspection_rules: z.unknown().optional(),
  codex_responses_safe_repair_enabled: z.boolean().optional(),
  codex_responses_strict_intercept_enabled: z.boolean().optional()
}).strict()

const accountModelMappingSchema = z.object({
  sourceModel: z.string().trim().min(1),
  sourceEndpointFamily: z.enum(['chat_completions', 'responses', 'messages', 'generate_content', 'stream_generate_content']),
  upstreamModel: z.string().trim().min(1),
  upstreamEndpointFamily: z.enum(['chat_completions', 'responses', 'messages', 'generate_content']),
  enabled: z.boolean().optional()
}).strict()

const createFromCodeSchema = z.object({
  sessionId: z.string().min(1),
  callbackUrl: z.string().min(1),
  oauthType: oauthTypeSchema.optional(),
  clientId: optionalTrimmedTextSchema,
  clientSecret: optionalTrimmedTextSchema,
  projectId: optionalTrimmedTextSchema,
  tierId: optionalTrimmedTextSchema,
  quotaProjectId: z.string().trim().min(1).optional(),
  baseUrl: z.string().trim().url().optional(),
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
  healthCheckEndpointMode: z.enum(['chat_json', 'chat_sse', 'responses_json', 'responses_sse', 'messages_json', 'messages_sse', 'generate_content_json', 'generate_content_sse']).optional(),
  temporaryUnavailableContinuousProbeEnabled: z.boolean().optional(),
  modelMappings: z.array(accountModelMappingSchema).max(500).optional(),
  tags: z.array(z.string().trim()).max(24).optional(),
  proxyProfileId: z.string().optional(),
  accountExpiresAt: z.string().nullable().optional(),
  availabilitySchedule: z.record(z.string(), z.unknown()).nullable().optional(),
  credentialsPatch: oauthCredentialsPatchSchema.optional(),
  notes: z.string().optional()
}).strict()

const createFromRefreshTokenSchema = z.object({
  refreshToken: z.string().min(1),
  oauthType: oauthTypeSchema.optional(),
  clientId: optionalTrimmedTextSchema,
  clientSecret: optionalTrimmedTextSchema,
  projectId: optionalTrimmedTextSchema,
  tierId: optionalTrimmedTextSchema,
  quotaProjectId: z.string().trim().min(1).optional(),
  baseUrl: z.string().trim().url().optional(),
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
  healthCheckEndpointMode: z.enum(['chat_json', 'chat_sse', 'responses_json', 'responses_sse', 'messages_json', 'messages_sse', 'generate_content_json', 'generate_content_sse']).optional(),
  temporaryUnavailableContinuousProbeEnabled: z.boolean().optional(),
  modelMappings: z.array(accountModelMappingSchema).max(500).optional(),
  tags: z.array(z.string().trim()).max(24).optional(),
  proxyProfileId: z.string().optional(),
  accountExpiresAt: z.string().nullable().optional(),
  availabilitySchedule: z.record(z.string(), z.unknown()).nullable().optional(),
  credentialsPatch: oauthCredentialsPatchSchema.optional(),
  notes: z.string().optional()
}).strict()

const reauthorizeFromCodeSchema = z.object({
  sessionId: z.string().min(1),
  callbackUrl: z.string().min(1),
  oauthType: oauthTypeSchema.optional(),
  clientId: optionalTrimmedTextSchema,
  clientSecret: optionalTrimmedTextSchema,
  projectId: optionalTrimmedTextSchema,
  tierId: optionalTrimmedTextSchema,
  quotaProjectId: z.string().trim().min(1).optional(),
  baseUrl: z.string().trim().url().optional()
}).strict()

const reauthorizeFromRefreshTokenSchema = z.object({
  refreshToken: z.string().min(1),
  oauthType: oauthTypeSchema.optional(),
  clientId: optionalTrimmedTextSchema,
  clientSecret: optionalTrimmedTextSchema,
  projectId: optionalTrimmedTextSchema,
  tierId: optionalTrimmedTextSchema,
  quotaProjectId: z.string().trim().min(1).optional(),
  baseUrl: z.string().trim().url().optional()
}).strict()

function isGeminiOAuthGroupSummary(group: Awaited<ReturnType<typeof findGroupSummaryAsync>> | undefined): boolean {
  return Boolean(group && group.providerCode === GEMINI_PROVIDER_CODE)
}

geminiOAuthRouter.get('/capabilities', (_req, res) => {
  res.json(ok(getGeminiOAuthCapabilities()))
})

geminiOAuthRouter.post('/auth-url', async (req, res, next) => {
  const parsed = authUrlSchema.safeParse(req.body ?? {})
  if (!parsed.success) {
    res.status(400).json(badRequest('Gemini 授权链接参数无效'))
    return
  }
  try {
    res.json(ok(await generateGeminiAuthURL({
      ...parsed.data,
      ownerSystemAccountId: getRequestAccessScope()?.systemAccountId
    })))
  } catch (error) {
    next(error)
  }
})

geminiOAuthRouter.post('/create-from-code', mutationGuard({
  operationKey: 'gemini_oauth.create_from_code',
  processingTtlMs: 180_000,
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    sessionId: textValue(bodyField(req, 'sessionId')),
    callbackUrl: sensitiveFingerprint(bodyField(req, 'callbackUrl')),
    oauthType: normalizedText(bodyField(req, 'oauthType')),
    projectId: normalizedText(bodyField(req, 'projectId')),
    tierId: normalizedText(bodyField(req, 'tierId')),
    clientId: normalizedText(bodyField(req, 'clientId')),
    clientSecret: sensitiveFingerprint(bodyField(req, 'clientSecret'))
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
    res.status(400).json(badRequest('Gemini 授权码参数无效'))
    return
  }
  const providerProfile = await resolveGeminiOAuthProviderProfile(parsed.data.providerProtocolProfileId)
  if (!providerProfile.ok) {
    res.status(400).json(badRequest(providerProfile.message))
    return
  }
  const group = parsed.data.groupId ? await findGroupSummaryAsync(parsed.data.groupId, requestAccess) : undefined
  if (parsed.data.groupId && !isGeminiOAuthGroupSummary(group)) {
    res.status(400).json(badRequest('账户分组无效'))
    return
  }
  const errorPolicyValidationMessage = oauthCredentialsPatchValidationMessage(parsed.data.credentialsPatch)
  if (errorPolicyValidationMessage) {
    res.status(400).json(badRequest(errorPolicyValidationMessage))
    return
  }

  try {
    await assertAccountGptRequestOverridesSupportedAsync({
      providerCode: GEMINI_PROVIDER_CODE,
      accountType: 'google_oauth',
      credentials: safeOAuthCredentialsPatch(parsed.data.credentialsPatch),
      supportedModels: parsed.data.supportedModels ?? providerProfile.provider.defaultSupportedModels,
      systemAccountId: requestAccess?.systemAccountFilterId ?? requestAccess?.systemAccountId
    })
    const tokenInfo = await exchangeGeminiAuthCode({
      sessionId: parsed.data.sessionId,
      callbackUrl: parsed.data.callbackUrl,
      oauthType: parsed.data.oauthType,
      clientId: parsed.data.clientId,
      clientSecret: parsed.data.clientSecret,
      projectId: parsed.data.projectId,
      tierId: parsed.data.tierId,
      quotaProjectId: parsed.data.quotaProjectId,
      baseUrl: parsed.data.baseUrl,
      ownerSystemAccountId: requestAccess?.systemAccountId,
      proxyUrl: await resolveProxyUrlForProfileAsync(parsed.data.proxyProfileId)
    })
    const account = await runLoggedOperationAsync(async () => {
      const account = await createAccountAsync({
        providerCode: GEMINI_PROVIDER_CODE,
        providerProtocolProfileId: providerProfile.profile.id,
        name: parsed.data.name ?? 'Gemini OAuth Account',
        type: 'google_oauth',
        credentials: buildSafeGeminiOAuthCredentials(tokenInfo, parsed.data.credentialsPatch),
        status: 'pending_test',
        skipInitialHealthCheck: false,
        concurrencyLimit: parsed.data.concurrencyLimit,
        priority: parsed.data.priority,
        superPriorityEnabled: parsed.data.superPriorityEnabled,
        fallbackEnabled: parsed.data.fallbackEnabled,
        supportedModels: parsed.data.supportedModels ?? providerProfile.provider.defaultSupportedModels,
        healthCheckModel: parsed.data.healthCheckModel,
        healthCheckEndpointMode: parsed.data.healthCheckEndpointMode,
        temporaryUnavailableContinuousProbeEnabled: parsed.data.temporaryUnavailableContinuousProbeEnabled,
        modelMappings: parsed.data.modelMappings,
        tags: parsed.data.tags,
        proxyProfileId: parsed.data.proxyProfileId,
        accountExpiresAt: parsed.data.accountExpiresAt,
        availabilitySchedule: parsed.data.availabilitySchedule,
        schedulable: false,
        groupId: parsed.data.groupId,
        notes: parsed.data.notes
      }, requestAccess)
      return {
        result: account,
        log: buildOAuthCreateLog(account, requestAccess, 'gemini_oauth.create_from_code', '通过授权码创建 Gemini OAuth 账户')
      }
    }, req)
    dispatchPendingAccountHealthCheck(account)
    res.status(201).json(ok(sanitizeAccountResponse(account)))
  } catch (error) {
    if (error instanceof ProxyProfileUnavailableError) {
      res.status(400).json(badRequest(error.message))
      return
    }
    if (isOAuthBusinessConflictError(error)) {
      res.status(409).json(badRequest(oauthErrorMessage(error, 'Gemini 授权码交换失败')))
      return
    }
    res.status(502).json({ message: oauthErrorMessage(error, 'Gemini 授权码交换失败') })
  }
})

geminiOAuthRouter.post('/create-from-refresh-token', mutationGuard({
  operationKey: 'gemini_oauth.create_from_refresh_token',
  processingTtlMs: 180_000,
  scope: (req) => normalizedText(queryField(req, 'systemAccountId')),
  fingerprint: (req) => ({
    owner: normalizedText(queryField(req, 'systemAccountId')),
    name: normalizedText(bodyField(req, 'name')),
    refreshToken: sensitiveFingerprint(bodyField(req, 'refreshToken')),
    oauthType: normalizedText(bodyField(req, 'oauthType')),
    projectId: normalizedText(bodyField(req, 'projectId')),
    tierId: normalizedText(bodyField(req, 'tierId')),
    clientId: normalizedText(bodyField(req, 'clientId')),
    clientSecret: sensitiveFingerprint(bodyField(req, 'clientSecret')),
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
    res.status(400).json(badRequest('Gemini 刷新令牌参数无效'))
    return
  }
  const providerProfile = await resolveGeminiOAuthProviderProfile(parsed.data.providerProtocolProfileId)
  if (!providerProfile.ok) {
    res.status(400).json(badRequest(providerProfile.message))
    return
  }
  const group = parsed.data.groupId ? await findGroupSummaryAsync(parsed.data.groupId, requestAccess) : undefined
  if (parsed.data.groupId && !isGeminiOAuthGroupSummary(group)) {
    res.status(400).json(badRequest('账户分组无效'))
    return
  }
  const errorPolicyValidationMessage = oauthCredentialsPatchValidationMessage(parsed.data.credentialsPatch)
  if (errorPolicyValidationMessage) {
    res.status(400).json(badRequest(errorPolicyValidationMessage))
    return
  }
  try {
    await assertAccountGptRequestOverridesSupportedAsync({
      providerCode: GEMINI_PROVIDER_CODE,
      accountType: 'google_oauth',
      credentials: safeOAuthCredentialsPatch(parsed.data.credentialsPatch),
      supportedModels: parsed.data.supportedModels ?? providerProfile.provider.defaultSupportedModels,
      systemAccountId: requestAccess?.systemAccountFilterId ?? requestAccess?.systemAccountId
    })
    const tokenInfo = await refreshGeminiAuthToken({
      refreshToken: parsed.data.refreshToken,
      oauthType: parsed.data.oauthType,
      clientId: parsed.data.clientId,
      clientSecret: parsed.data.clientSecret,
      projectId: parsed.data.projectId,
      tierId: parsed.data.tierId,
      quotaProjectId: parsed.data.quotaProjectId ?? parsed.data.credentialsPatch?.quota_project_id,
      baseUrl: parsed.data.baseUrl ?? parsed.data.credentialsPatch?.base_url,
      proxyUrl: await resolveProxyUrlForProfileAsync(parsed.data.proxyProfileId)
    })
    const account = await runLoggedOperationAsync(async () => {
      const account = await createAccountAsync({
        providerCode: GEMINI_PROVIDER_CODE,
        providerProtocolProfileId: providerProfile.profile.id,
        name: parsed.data.name ?? 'Gemini OAuth Account',
        type: 'google_oauth',
        credentials: buildSafeGeminiOAuthCredentials(tokenInfo, parsed.data.credentialsPatch, { refreshToken: parsed.data.refreshToken }),
        status: 'pending_test',
        skipInitialHealthCheck: false,
        concurrencyLimit: parsed.data.concurrencyLimit,
        priority: parsed.data.priority,
        superPriorityEnabled: parsed.data.superPriorityEnabled,
        fallbackEnabled: parsed.data.fallbackEnabled,
        supportedModels: parsed.data.supportedModels ?? providerProfile.provider.defaultSupportedModels,
        healthCheckModel: parsed.data.healthCheckModel,
        healthCheckEndpointMode: parsed.data.healthCheckEndpointMode,
        temporaryUnavailableContinuousProbeEnabled: parsed.data.temporaryUnavailableContinuousProbeEnabled,
        modelMappings: parsed.data.modelMappings,
        tags: parsed.data.tags,
        proxyProfileId: parsed.data.proxyProfileId,
        accountExpiresAt: parsed.data.accountExpiresAt,
        availabilitySchedule: parsed.data.availabilitySchedule,
        schedulable: false,
        groupId: parsed.data.groupId,
        notes: parsed.data.notes
      }, requestAccess)
      return {
        result: account,
        log: buildOAuthCreateLog(account, requestAccess, 'gemini_oauth.create_from_refresh_token', '通过 Refresh Token 创建 Gemini OAuth 账户')
      }
    }, req)
    dispatchPendingAccountHealthCheck(account)
    res.status(201).json(ok(sanitizeAccountResponse(account)))
  } catch (error) {
    if (error instanceof ProxyProfileUnavailableError) {
      res.status(400).json(badRequest(error.message))
      return
    }
    if (isOAuthBusinessConflictError(error)) {
      res.status(409).json(badRequest(oauthErrorMessage(error, 'Gemini 刷新令牌授权失败')))
      return
    }
    res.status(502).json({ message: oauthErrorMessage(error, 'Gemini 刷新令牌授权失败') })
  }
})

geminiOAuthRouter.post('/accounts/:id/refresh-token', async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const account = await findEditableGeminiOAuthAccount(req.params.id, requestAccess)
  if (!account) {
    res.status(404).json({ message: 'Gemini OAuth 账户不存在或无权操作' })
    return
  }

  const refreshToken = stringCredential(account.credentials, 'refresh_token')
  if (!refreshToken) {
    res.status(400).json(badRequest('Gemini OAuth 账户缺少 Refresh Token'))
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
    const updatedAccount = await runWithProviderOAuthRefreshLock(
      GEMINI_PROVIDER_CODE,
      account.id,
      async (lockSignal, assertLockOwned) => {
        const current = await findEditableGeminiOAuthAccount(account.id, requestAccess)
        if (!current) throw new Error('Gemini OAuth 账户不存在或无权操作')
        if (oauthTokensChanged(account.credentials, current.credentials)
          && !shouldRefreshGeminiOAuthCredentials(current.credentials)) {
          return current
        }
        const currentRefreshToken = stringCredential(current.credentials, 'refresh_token')
        if (!currentRefreshToken) throw new Error('Gemini OAuth 账户缺少 Refresh Token')
        const tokenInfo = await refreshGeminiAuthToken({
          refreshToken: currentRefreshToken,
          oauthType: accountOAuthType(current.credentials),
          clientId: stringCredential(current.credentials, 'client_id'),
          clientSecret: stringCredential(current.credentials, 'client_secret'),
          projectId: stringCredential(current.credentials, 'project_id'),
          tierId: stringCredential(current.credentials, 'tier_id'),
          quotaProjectId: stringCredential(current.credentials, 'quota_project_id'),
          baseUrl: stringCredential(current.credentials, 'base_url'),
          scope: stringCredential(current.credentials, 'scope'),
          proxyUrl: current.proxyProfileId ? await resolveProxyUrlForProfileAsync(current.proxyProfileId) : undefined,
          signal: lockSignal
        })
        await assertLockOwned()
        return await updateGeminiOAuthAccountCredentials(current, tokenInfo, undefined, requestAccess)
      },
      { signal: abortController.signal }
    )
    if (abortController.signal.aborted || res.writableEnded) {
      return
    }
    await recordOperationLogAsync(buildOAuthUpdateLog(account, updatedAccount, requestAccess, 'refresh_token', '刷新 Gemini OAuth Token'), req)
    res.json(ok(sanitizeAccountCredentialCarrierResponse(updatedAccount)))
  } catch (error) {
    if (abortController.signal.aborted || res.writableEnded) {
      return
    }
    if (error instanceof ProxyProfileUnavailableError) {
      res.status(400).json(badRequest(error.message))
      return
    }
    if (error instanceof AccountConfigRevisionConflictError) {
      res.status(409).json(badRequest('Gemini OAuth 账户已被其他操作更新，请刷新页面后重试'))
      return
    }
    res.status(502).json({ message: oauthErrorMessage(error, 'Gemini 访问令牌刷新失败') })
  }
})

geminiOAuthRouter.post('/accounts/:id/reauthorize-from-code', async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = reauthorizeFromCodeSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('Gemini 重新授权参数无效'))
    return
  }
  const account = await findEditableGeminiOAuthAccount(req.params.id, requestAccess)
  if (!account) {
    res.status(404).json({ message: 'Gemini OAuth 账户不存在或无权操作' })
    return
  }

  try {
    const updated = await runWithProviderOAuthRefreshLock(GEMINI_PROVIDER_CODE, account.id, async (lockSignal, assertLockOwned) => {
      const current = await findEditableGeminiOAuthAccount(account.id, requestAccess)
      if (!current) throw new Error('Gemini OAuth 账户不存在或无权操作')
      if (oauthTokensChanged(account.credentials, current.credentials)) {
        throw new AccountConfigRevisionConflictError(account.id, account.configRevision ?? 1, current.configRevision)
      }
      const tokenInfo = await exchangeGeminiAuthCode({
        sessionId: parsed.data.sessionId,
        callbackUrl: parsed.data.callbackUrl,
        oauthType: parsed.data.oauthType,
        clientId: parsed.data.clientId,
        clientSecret: parsed.data.clientSecret,
        projectId: parsed.data.projectId,
        tierId: parsed.data.tierId,
        quotaProjectId: parsed.data.quotaProjectId,
        baseUrl: parsed.data.baseUrl,
        ownerSystemAccountId: requestAccess?.systemAccountId,
        proxyUrl: current.proxyProfileId ? await resolveProxyUrlForProfileAsync(current.proxyProfileId) : undefined,
        signal: lockSignal
      })
      await assertLockOwned()
      return await runLoggedOperationAsync(async () => {
        const result = await updateGeminiOAuthAccountCredentials(current, tokenInfo, undefined, requestAccess)
        return {
          result,
          log: buildOAuthUpdateLog(current, result, requestAccess, 'reauthorize_from_code', '重新授权 Gemini OAuth 账户')
        }
      }, req)
    })
    res.json(ok(sanitizeAccountResponse(updated)))
  } catch (error) {
    handleOAuthAccountUpdateError(error, res, 'Gemini OAuth 重新授权失败')
  }
})

geminiOAuthRouter.post('/accounts/:id/reauthorize-from-refresh-token', async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = reauthorizeFromRefreshTokenSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('Gemini 刷新令牌参数无效'))
    return
  }
  const account = await findEditableGeminiOAuthAccount(req.params.id, requestAccess)
  if (!account) {
    res.status(404).json({ message: 'Gemini OAuth 账户不存在或无权操作' })
    return
  }

  try {
    const updated = await runWithProviderOAuthRefreshLock(GEMINI_PROVIDER_CODE, account.id, async (lockSignal, assertLockOwned) => {
      const current = await findEditableGeminiOAuthAccount(account.id, requestAccess)
      if (!current) throw new Error('Gemini OAuth 账户不存在或无权操作')
      if (oauthTokensChanged(account.credentials, current.credentials)) {
        throw new AccountConfigRevisionConflictError(account.id, account.configRevision ?? 1, current.configRevision)
      }
      const tokenInfo = await refreshGeminiAuthToken({
        refreshToken: parsed.data.refreshToken,
        oauthType: parsed.data.oauthType ?? accountOAuthType(current.credentials),
        clientId: parsed.data.clientId ?? stringCredential(current.credentials, 'client_id'),
        clientSecret: parsed.data.clientSecret ?? stringCredential(current.credentials, 'client_secret'),
        projectId: parsed.data.projectId ?? stringCredential(current.credentials, 'project_id'),
        tierId: parsed.data.tierId ?? stringCredential(current.credentials, 'tier_id'),
        quotaProjectId: parsed.data.quotaProjectId ?? stringCredential(current.credentials, 'quota_project_id'),
        baseUrl: parsed.data.baseUrl ?? stringCredential(current.credentials, 'base_url'),
        scope: stringCredential(current.credentials, 'scope'),
        proxyUrl: current.proxyProfileId ? await resolveProxyUrlForProfileAsync(current.proxyProfileId) : undefined,
        signal: lockSignal
      })
      await assertLockOwned()
      return await runLoggedOperationAsync(async () => {
        const result = await updateGeminiOAuthAccountCredentials(current, tokenInfo, { refreshToken: parsed.data.refreshToken }, requestAccess)
        return {
          result,
          log: buildOAuthUpdateLog(current, result, requestAccess, 'reauthorize_from_refresh_token', '使用 Refresh Token 重新授权 Gemini OAuth 账户')
        }
      }, req)
    })
    res.json(ok(sanitizeAccountResponse(updated)))
  } catch (error) {
    handleOAuthAccountUpdateError(error, res, 'Gemini 刷新令牌重新授权失败')
  }
})

type GeminiOAuthProvider = Awaited<ReturnType<typeof listProvidersAsync>>[number]
type GeminiOAuthProviderProfile = GeminiOAuthProvider['protocolProfiles'][number]

type GeminiOAuthProviderProfileResult =
  | { ok: true; provider: GeminiOAuthProvider; profile: GeminiOAuthProviderProfile }
  | { ok: false; message: string }

async function resolveGeminiOAuthProviderProfile(providerProtocolProfileId: string): Promise<GeminiOAuthProviderProfileResult> {
  const provider = (await listProvidersAsync()).find((item) => item.code === GEMINI_PROVIDER_CODE)
  if (!provider) {
    return { ok: false, message: `不支持的供应商：${GEMINI_PROVIDER_CODE}` }
  }
  if (!provider.enabled) {
    return { ok: false, message: `供应商已停用：${GEMINI_PROVIDER_CODE}` }
  }
  const profileId = providerProtocolProfileId.trim()
  const profile = provider.protocolProfiles.find((item) => item.id === profileId)
  if (!profile || profile.providerCode !== GEMINI_PROVIDER_CODE) {
    return { ok: false, message: `供应商协议档案无效：${profileId}` }
  }
  if (!profile.enabled) {
    return { ok: false, message: `供应商协议档案已停用：${profile.name}` }
  }
  if (!isGeminiProtocolProfile(profile) || !profile.accountTypes.includes('google_oauth')) {
    return { ok: false, message: `供应商协议档案 ${profile.name} 不支持 Gemini OAuth` }
  }
  return { ok: true, provider, profile }
}

function safeOAuthCredentialsPatch(patch?: z.infer<typeof oauthCredentialsPatchSchema>): Record<string, unknown> {
  const output: Record<string, unknown> = {}
  if (patch?.quota_project_id !== undefined) output.quota_project_id = patch.quota_project_id
  if (patch?.base_url !== undefined) output.base_url = patch.base_url
  if (patch?.supported_endpoint_modes !== undefined) output.supported_endpoint_modes = patch.supported_endpoint_modes
  if (patch?.service_tier_override !== undefined) output.service_tier_override = patch.service_tier_override
  if (patch?.reasoning_effort_override !== undefined) output.reasoning_effort_override = patch.reasoning_effort_override
  if (patch?.error_handling_rules !== undefined) output.error_handling_rules = patch.error_handling_rules
  if (patch?.response_inspection_rules !== undefined) output.response_inspection_rules = patch.response_inspection_rules
  if (patch?.codex_responses_safe_repair_enabled !== undefined) output.codex_responses_safe_repair_enabled = patch.codex_responses_safe_repair_enabled
  if (patch?.codex_responses_strict_intercept_enabled !== undefined) output.codex_responses_strict_intercept_enabled = patch.codex_responses_strict_intercept_enabled
  return output
}

function oauthCredentialsPatchValidationMessage(patch?: z.infer<typeof oauthCredentialsPatchSchema>): string | undefined {
  if (patch?.error_handling_rules !== undefined) {
    const accountErrorPolicyMessage = accountErrorPolicyValidationMessage(validateAccountErrorHandlingRules(patch.error_handling_rules))
    if (accountErrorPolicyMessage) return accountErrorPolicyMessage
  }
  if (patch?.response_inspection_rules !== undefined) {
    const responseInspectionMessage = accountResponseInspectionPolicyValidationMessage(validateAccountResponseInspectionRules(patch.response_inspection_rules))
    if (responseInspectionMessage) return responseInspectionMessage
  }
  return undefined
}

function buildSafeGeminiOAuthCredentials(
  tokenInfo: GeminiOAuthTokenInfo,
  patch?: z.infer<typeof oauthCredentialsPatchSchema>,
  fallback?: { refreshToken?: string; quotaProjectId?: string; baseUrl?: string }
): Record<string, unknown> {
  return {
    ...buildGeminiOAuthCredentials(tokenInfo, fallback),
    ...safeOAuthCredentialsPatch(patch)
  }
}

async function findEditableGeminiOAuthAccount(accountId: string, access?: AccessScope) {
  const account = await findAccountForTestAsync(accountId, access)
  if (!account || account.providerCode !== GEMINI_PROVIDER_CODE || !isGeminiProtocolProfile(account) || account.type !== 'google_oauth' || account.permissions?.canEdit === false || account.permissions?.canViewCredentials === false) {
    return undefined
  }
  return account
}

async function updateGeminiOAuthAccountCredentials(
  account: NonNullable<Awaited<ReturnType<typeof findEditableGeminiOAuthAccount>>>,
  tokenInfo: GeminiOAuthTokenInfo,
  fallback?: { refreshToken?: string; quotaProjectId?: string; baseUrl?: string },
  access?: AccessScope
) {
  const credentials = {
    ...account.credentials,
    ...buildGeminiOAuthCredentials(tokenInfo, fallback)
  }
  const existingBaseUrl = stringCredential(account.credentials, 'base_url')
  if (existingBaseUrl) credentials.base_url = existingBaseUrl
  const updated = await updateAccountAsync(account.id, { credentials }, access, {
    expectedConfigRevision: account.configRevision ?? 1
  })
  if (!updated) {
    throw new Error('Gemini OAuth 账户不存在或无法更新')
  }
  return updated
}

function handleOAuthAccountUpdateError(error: unknown, res: Response, fallbackMessage: string): void {
  if (error instanceof ProxyProfileUnavailableError) {
    res.status(400).json(badRequest(error.message))
    return
  }
  if (error instanceof AccountConfigRevisionConflictError) {
    res.status(409).json(badRequest('Gemini OAuth 账户已被其他操作更新，请刷新页面后重试'))
    return
  }
  if (isOAuthBusinessConflictError(error)) {
    res.status(409).json(badRequest(oauthErrorMessage(error, fallbackMessage)))
    return
  }
  res.status(502).json({ message: oauthErrorMessage(error, fallbackMessage) })
}

function oauthErrorMessage(error: unknown, fallbackMessage: string): string {
  return sanitizeGeminiOAuthErrorMessage(error instanceof Error ? error.message : fallbackMessage)
}

function isOAuthBusinessConflictError(error: unknown): boolean {
  return error instanceof Error && error.message.includes('已存在')
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
    module: 'gemini_oauth',
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
      safeChange('serviceTierOverride', '服务等级覆盖', undefined, account.credentials.service_tier_override),
      safeChange('reasoningEffortOverride', '思考级别覆盖', undefined, account.credentials.reasoning_effort_override),
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
  before: NonNullable<Awaited<ReturnType<typeof findEditableGeminiOAuthAccount>>>,
  after: NonNullable<Awaited<ReturnType<typeof updateAccountAsync>>>,
  access: AccessScope | undefined,
  action: string,
  summaryPrefix: string
): OperationLogRecordInput {
  const ownerSystemAccountId = resolveOperationOwner(after as unknown as Record<string, unknown>, access)
  return {
    operationScopeSystemAccountId: ownerSystemAccountId,
    mode: operationMode(access),
    module: 'gemini_oauth',
    action,
    operationKey: `gemini_oauth.${action}`,
    resourceType: 'account',
    resourceId: after.id,
    resourceName: after.name,
    summary: `${summaryPrefix}：${after.name}`,
    changes: [
      safeChange('credentials', 'OAuth 凭据', before.credentials, after.credentials),
      safeChange('serviceTierOverride', '服务等级覆盖', before.credentials.service_tier_override, after.credentials.service_tier_override),
      safeChange('reasoningEffortOverride', '思考级别覆盖', before.credentials.reasoning_effort_override, after.credentials.reasoning_effort_override),
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

function accountOAuthType(credentials: Record<string, unknown>): GeminiOAuthType {
  const value = stringCredential(credentials, 'oauth_type')
  if (value === 'code_assist' || value === 'google_one' || value === 'ai_studio') return value
  const baseUrl = stringCredential(credentials, 'base_url')
  if (baseUrl?.includes('generativelanguage.googleapis.com')) return 'ai_studio'
  if (stringCredential(credentials, 'project_id') || baseUrl?.includes('cloudcode-pa.googleapis.com')) return 'code_assist'
  const clientId = stringCredential(credentials, 'client_id')
  if (clientId && clientId !== GEMINI_CLI_OAUTH_CLIENT_ID) return 'ai_studio'
  return 'code_assist'
}

function oauthTokensChanged(before: Record<string, unknown>, after: Record<string, unknown>): boolean {
  return stringCredential(before, 'access_token') !== stringCredential(after, 'access_token')
    || stringCredential(before, 'refresh_token') !== stringCredential(after, 'refresh_token')
}

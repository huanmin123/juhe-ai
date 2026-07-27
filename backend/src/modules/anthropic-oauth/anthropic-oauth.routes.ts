import { Router } from 'express'
import type { Response } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import { ProxyProfileUnavailableError, clearAccountFailureStateAsync, createAccountAsync, findAccountForTestAsync, findGroupSummaryAsync, listProvidersAsync, resolveProxyUrlForProfileAsync, updateAccountAsync } from '../../storage/repositories.js'
import { ANTHROPIC_PROVIDER_CODE, isAnthropicProtocolProfile } from '../../domain/provider-protocol.js'
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
import {
  buildAnthropicOAuthCredentials,
  exchangeAnthropicAuthCode,
  generateAnthropicAuthURL,
  refreshAnthropicAuthToken,
  sanitizeAnthropicOAuthErrorMessage,
  type AnthropicOAuthTokenInfo
} from './anthropic-oauth.service.js'

export const anthropicOAuthRouter = Router()

const authUrlSchema = z.object({}).strict()
const oauthCredentialsPatchSchema = z.object({
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
  callbackUrl: z.string().min(1)
}).strict()

const reauthorizeFromRefreshTokenSchema = z.object({
  refreshToken: z.string().min(1)
}).strict()

function isAnthropicOAuthGroupSummary(group: Awaited<ReturnType<typeof findGroupSummaryAsync>> | undefined): boolean {
  return Boolean(group && group.providerCode === ANTHROPIC_PROVIDER_CODE)
}

anthropicOAuthRouter.post('/auth-url', async (req, res, next) => {
  const parsed = authUrlSchema.safeParse(req.body ?? {})
  if (!parsed.success) {
    res.status(400).json(badRequest('Anthropic 授权链接参数无效'))
    return
  }
  try {
    res.json(ok(await generateAnthropicAuthURL(getRequestAccessScope()?.systemAccountId)))
  } catch (error) {
    next(error)
  }
})

anthropicOAuthRouter.post('/create-from-code', mutationGuard({
  operationKey: 'anthropic_oauth.create_from_code',
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
    res.status(400).json(badRequest('Anthropic 授权码参数无效'))
    return
  }
  const providerProfile = await resolveAnthropicOAuthProviderProfile(parsed.data.providerProtocolProfileId)
  if (!providerProfile.ok) {
    res.status(400).json(badRequest(providerProfile.message))
    return
  }
  const group = parsed.data.groupId ? await findGroupSummaryAsync(parsed.data.groupId, requestAccess) : undefined
  if (parsed.data.groupId && !isAnthropicOAuthGroupSummary(group)) {
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
      providerCode: ANTHROPIC_PROVIDER_CODE,
      accountType: 'oauth',
      credentials: safeOAuthCredentialsPatch(parsed.data.credentialsPatch),
      supportedModels: parsed.data.supportedModels ?? providerProfile.provider.defaultSupportedModels,
      systemAccountId: requestAccess?.systemAccountFilterId ?? requestAccess?.systemAccountId
    })
    const tokenInfo = await exchangeAnthropicAuthCode({
      sessionId: parsed.data.sessionId,
      callbackUrl: parsed.data.callbackUrl,
      ownerSystemAccountId: requestAccess?.systemAccountId,
      proxyUrl: await resolveProxyUrlForProfileAsync(parsed.data.proxyProfileId)
    })
    const account = await runLoggedOperationAsync(async () => {
      const account = await createAccountAsync({
        providerCode: ANTHROPIC_PROVIDER_CODE,
        providerProtocolProfileId: providerProfile.profile.id,
        name: parsed.data.name ?? tokenInfo.email ?? 'Anthropic OAuth Account',
        type: 'oauth',
        credentials: buildSafeAnthropicOAuthCredentials(tokenInfo, parsed.data.credentialsPatch),
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
        log: buildOAuthCreateLog(account, requestAccess, 'anthropic_oauth.create_from_code', '通过授权码创建 Anthropic OAuth 账户')
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
      res.status(409).json(badRequest(oauthErrorMessage(error, 'Anthropic 授权码交换失败')))
      return
    }
    res.status(502).json({ message: oauthErrorMessage(error, 'Anthropic 授权码交换失败') })
  }
})

anthropicOAuthRouter.post('/create-from-refresh-token', mutationGuard({
  operationKey: 'anthropic_oauth.create_from_refresh_token',
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
    res.status(400).json(badRequest('Anthropic 刷新令牌参数无效'))
    return
  }
  const providerProfile = await resolveAnthropicOAuthProviderProfile(parsed.data.providerProtocolProfileId)
  if (!providerProfile.ok) {
    res.status(400).json(badRequest(providerProfile.message))
    return
  }
  const group = parsed.data.groupId ? await findGroupSummaryAsync(parsed.data.groupId, requestAccess) : undefined
  if (parsed.data.groupId && !isAnthropicOAuthGroupSummary(group)) {
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
      providerCode: ANTHROPIC_PROVIDER_CODE,
      accountType: 'oauth',
      credentials: safeOAuthCredentialsPatch(parsed.data.credentialsPatch),
      supportedModels: parsed.data.supportedModels ?? providerProfile.provider.defaultSupportedModels,
      systemAccountId: requestAccess?.systemAccountFilterId ?? requestAccess?.systemAccountId
    })
    const tokenInfo = await refreshAnthropicAuthToken({
      refreshToken: parsed.data.refreshToken,
      proxyUrl: await resolveProxyUrlForProfileAsync(parsed.data.proxyProfileId)
    })
    const account = await runLoggedOperationAsync(async () => {
      const account = await createAccountAsync({
        providerCode: ANTHROPIC_PROVIDER_CODE,
        providerProtocolProfileId: providerProfile.profile.id,
        name: parsed.data.name ?? tokenInfo.email ?? 'Anthropic OAuth Account',
        type: 'oauth',
        credentials: buildSafeAnthropicOAuthCredentials(tokenInfo, parsed.data.credentialsPatch, { refreshToken: parsed.data.refreshToken }),
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
        log: buildOAuthCreateLog(account, requestAccess, 'anthropic_oauth.create_from_refresh_token', '通过 Refresh Token 创建 Anthropic OAuth 账户')
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
      res.status(409).json(badRequest(oauthErrorMessage(error, 'Anthropic 刷新令牌授权失败')))
      return
    }
    res.status(502).json({ message: oauthErrorMessage(error, 'Anthropic 刷新令牌授权失败') })
  }
})

anthropicOAuthRouter.post('/accounts/:id/refresh-token', async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const account = await findEditableAnthropicOAuthAccount(req.params.id, requestAccess)
  if (!account) {
    res.status(404).json({ message: 'Anthropic OAuth 账户不存在或无权操作' })
    return
  }

  const refreshToken = stringCredential(account.credentials, 'refresh_token')
  if (!refreshToken) {
    res.status(400).json(badRequest('Anthropic OAuth 账户缺少 Refresh Token'))
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
    const tokenInfo = await refreshAnthropicAuthToken({
      refreshToken,
      clientId: stringCredential(account.credentials, 'client_id'),
      proxyUrl: account.proxyProfileId ? await resolveProxyUrlForProfileAsync(account.proxyProfileId) : undefined,
      signal: abortController.signal
    })
    if (abortController.signal.aborted || res.writableEnded) {
      return
    }
    const updatedAccount = await updateAnthropicOAuthAccountCredentials(account, tokenInfo, undefined, requestAccess)
    if (abortController.signal.aborted || res.writableEnded) {
      return
    }
    const restoredAccount = await clearAccountFailureStateAsync(account.id, requestAccess) ?? updatedAccount
    await recordOperationLogAsync(buildOAuthUpdateLog(account, restoredAccount, requestAccess, 'refresh_token', '刷新 Anthropic OAuth Token'), req)
    res.json(ok(sanitizeAccountCredentialCarrierResponse(restoredAccount)))
  } catch (error) {
    if (abortController.signal.aborted || res.writableEnded) {
      return
    }
    if (error instanceof ProxyProfileUnavailableError) {
      res.status(400).json(badRequest(error.message))
      return
    }
    res.status(502).json({ message: oauthErrorMessage(error, 'Anthropic 访问令牌刷新失败') })
  }
})

anthropicOAuthRouter.post('/accounts/:id/reauthorize-from-code', async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = reauthorizeFromCodeSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('Anthropic 重新授权参数无效'))
    return
  }
  const account = await findEditableAnthropicOAuthAccount(req.params.id, requestAccess)
  if (!account) {
    res.status(404).json({ message: 'Anthropic OAuth 账户不存在或无权操作' })
    return
  }

  try {
    const tokenInfo = await exchangeAnthropicAuthCode({
      sessionId: parsed.data.sessionId,
      callbackUrl: parsed.data.callbackUrl,
      ownerSystemAccountId: requestAccess?.systemAccountId,
      proxyUrl: account.proxyProfileId ? await resolveProxyUrlForProfileAsync(account.proxyProfileId) : undefined
    })
    const updated = await runLoggedOperationAsync(async () => {
      const updated = await updateAnthropicOAuthAccountCredentials(account, tokenInfo, undefined, requestAccess)
      return {
        result: updated,
        log: buildOAuthUpdateLog(account, updated, requestAccess, 'reauthorize_from_code', '重新授权 Anthropic OAuth 账户')
      }
    }, req)
    res.json(ok(sanitizeAccountResponse(updated)))
  } catch (error) {
    handleOAuthAccountUpdateError(error, res, 'Anthropic OAuth 重新授权失败')
  }
})

anthropicOAuthRouter.post('/accounts/:id/reauthorize-from-refresh-token', async (req, res) => {
  const scopeQuery = parseRequestScopeQuery(req.query)
  if (!scopeQuery.success) {
    res.status(400).json(badRequest(scopeQuery.message))
    return
  }
  const requestAccess = getRequestAccessScope(scopeQuery.data.systemAccountId)
  const parsed = reauthorizeFromRefreshTokenSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('Anthropic 刷新令牌参数无效'))
    return
  }
  const account = await findEditableAnthropicOAuthAccount(req.params.id, requestAccess)
  if (!account) {
    res.status(404).json({ message: 'Anthropic OAuth 账户不存在或无权操作' })
    return
  }

  try {
    const tokenInfo = await refreshAnthropicAuthToken({
      refreshToken: parsed.data.refreshToken,
      clientId: stringCredential(account.credentials, 'client_id'),
      proxyUrl: account.proxyProfileId ? await resolveProxyUrlForProfileAsync(account.proxyProfileId) : undefined
    })
    const updated = await runLoggedOperationAsync(async () => {
      const updated = await updateAnthropicOAuthAccountCredentials(account, tokenInfo, { refreshToken: parsed.data.refreshToken }, requestAccess)
      return {
        result: updated,
        log: buildOAuthUpdateLog(account, updated, requestAccess, 'reauthorize_from_refresh_token', '使用 Refresh Token 重新授权 Anthropic OAuth 账户')
      }
    }, req)
    res.json(ok(sanitizeAccountResponse(updated)))
  } catch (error) {
    handleOAuthAccountUpdateError(error, res, 'Anthropic 刷新令牌重新授权失败')
  }
})

type AnthropicOAuthProvider = Awaited<ReturnType<typeof listProvidersAsync>>[number]
type AnthropicOAuthProviderProfile = AnthropicOAuthProvider['protocolProfiles'][number]

type AnthropicOAuthProviderProfileResult =
  | { ok: true; provider: AnthropicOAuthProvider; profile: AnthropicOAuthProviderProfile }
  | { ok: false; message: string }

async function resolveAnthropicOAuthProviderProfile(providerProtocolProfileId: string): Promise<AnthropicOAuthProviderProfileResult> {
  const provider = (await listProvidersAsync()).find((item) => item.code === ANTHROPIC_PROVIDER_CODE)
  if (!provider) {
    return { ok: false, message: `不支持的供应商：${ANTHROPIC_PROVIDER_CODE}` }
  }
  if (!provider.enabled) {
    return { ok: false, message: `供应商已停用：${ANTHROPIC_PROVIDER_CODE}` }
  }
  const profileId = providerProtocolProfileId.trim()
  const profile = provider.protocolProfiles.find((item) => item.id === profileId)
  if (!profile || profile.providerCode !== ANTHROPIC_PROVIDER_CODE) {
    return { ok: false, message: `供应商协议档案无效：${profileId}` }
  }
  if (!profile.enabled) {
    return { ok: false, message: `供应商协议档案已停用：${profile.name}` }
  }
  if (!isAnthropicProtocolProfile(profile) || !profile.accountTypes.includes('oauth')) {
    return { ok: false, message: `供应商协议档案 ${profile.name} 不支持 Anthropic OAuth` }
  }
  return { ok: true, provider, profile }
}

function safeOAuthCredentialsPatch(patch?: z.infer<typeof oauthCredentialsPatchSchema>): Record<string, unknown> {
  const output: Record<string, unknown> = {}
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

function buildSafeAnthropicOAuthCredentials(
  tokenInfo: AnthropicOAuthTokenInfo,
  patch?: z.infer<typeof oauthCredentialsPatchSchema>,
  fallback?: { refreshToken?: string }
): Record<string, unknown> {
  return {
    ...safeOAuthCredentialsPatch(patch),
    ...buildAnthropicOAuthCredentials(tokenInfo, fallback)
  }
}

async function findEditableAnthropicOAuthAccount(accountId: string, access?: AccessScope) {
  const account = await findAccountForTestAsync(accountId, access)
  if (!account || account.providerCode !== ANTHROPIC_PROVIDER_CODE || !isAnthropicProtocolProfile(account) || account.type !== 'oauth' || account.permissions?.canEdit === false || account.permissions?.canViewCredentials === false) {
    return undefined
  }
  return account
}

async function updateAnthropicOAuthAccountCredentials(
  account: NonNullable<Awaited<ReturnType<typeof findEditableAnthropicOAuthAccount>>>,
  tokenInfo: AnthropicOAuthTokenInfo,
  fallback?: { refreshToken?: string },
  access?: AccessScope
) {
  const credentials = {
    ...account.credentials,
    ...buildAnthropicOAuthCredentials(tokenInfo, fallback)
  }
  const updated = await updateAccountAsync(account.id, { credentials }, access)
  if (!updated) {
    throw new Error('Anthropic OAuth 账户不存在或无法更新')
  }
  return updated
}

function handleOAuthAccountUpdateError(error: unknown, res: Response, fallbackMessage: string): void {
  if (error instanceof ProxyProfileUnavailableError) {
    res.status(400).json(badRequest(error.message))
    return
  }
  if (isOAuthBusinessConflictError(error)) {
    res.status(409).json(badRequest(oauthErrorMessage(error, fallbackMessage)))
    return
  }
  res.status(502).json({ message: oauthErrorMessage(error, fallbackMessage) })
}

function oauthErrorMessage(error: unknown, fallbackMessage: string): string {
  return sanitizeAnthropicOAuthErrorMessage(error instanceof Error ? error.message : fallbackMessage)
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
    module: 'anthropic_oauth',
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
  before: NonNullable<Awaited<ReturnType<typeof findEditableAnthropicOAuthAccount>>>,
  after: NonNullable<Awaited<ReturnType<typeof updateAccountAsync>>>,
  access: AccessScope | undefined,
  action: string,
  summaryPrefix: string
): OperationLogRecordInput {
  const ownerSystemAccountId = resolveOperationOwner(after as unknown as Record<string, unknown>, access)
  return {
    operationScopeSystemAccountId: ownerSystemAccountId,
    mode: operationMode(access),
    module: 'anthropic_oauth',
    action,
    operationKey: `anthropic_oauth.${action}`,
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

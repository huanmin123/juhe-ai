import { normalizeOpenAIAccountClientCompatibility } from '../../domain/account-client-compatibility.js'
import {
  ANTHROPIC_MESSAGES_FAMILY,
  GEMINI_GENERATE_CONTENT_FAMILY,
  GEMINI_STREAM_GENERATE_CONTENT_FAMILY,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_RESPONSES_FAMILY,
  isAnthropicProtocolProfile,
  isGeminiProtocolProfile,
  isOpenAIProtocolProfile
} from '../../domain/provider-protocol.js'
import type {
  AccountClientCompatibility,
  AccountModelMapping,
  AccountModelMappingSourceEndpointFamily,
  AccountSummary,
  AccountSupportedEndpointMode,
  AccountTestResult
} from '../../domain/types.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { createTraceId, withRequestContext, type RequestContext } from '../../shared/request-context.js'
import {
  defaultProviderProtocolProfileAsync,
  findAccountForTestAsync,
  findOpenAIAccountForGroupAsync,
  findProviderProtocolProfileAsync,
  type OpenAIAccountSecret
} from '../../storage/repositories.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { accountTestFailureEligibleForAccount } from './account-test-failure-eligibility.js'
import { inspectAccountTestImageResponseEnvelope } from './account-test-image-response-inspection.js'
import { accountCredentialFingerprint } from './account-credential-update.js'
import { accountManualTestEndpointModes } from './account-test-endpoint-modes.js'
import {
  extractAccountTestResponseOutputText,
  parseAccountTestUpstreamErrorCode,
  parseAccountTestStreamFailureMessage,
  parseAccountTestUpstreamMessage,
  resolveAccountTestResponseDiagnostics,
  type AccountTestDiagnosticProtocol
} from './account-test-response-diagnostics.js'
import {
  accountModelCatalogIdsFromPayload,
  hasAccountModelCatalogResponseEvidence,
  hasAccountModelCatalogSuccessEvidence,
  hasAccountTestProtocolSuccessEvidence
} from './account-test-success-evidence.js'
import { accountTestProbeKind, type AccountTestProbeKind } from './account-test-probe-policy.js'
import { findProviderModelTestCatalogItemAsync } from '../model-pricing/model-catalog.service.js'
import { withRequestAuthContext } from '../auth/request-context.js'
import { handleOpenAIGatewayRequest } from '../gateway/routes.js'
import { sanitizeDiagnosticPayload } from '../gateway/diagnostics/diagnostic-sanitizer.js'
import type { GatewaySettings } from '../gateway/policy/account-error-policy.service.js'
import { flushGatewayAccountSideEffects } from '../gateway/runtime/account-side-effects.service.js'
import { isRealUpstreamAttempt, type UpstreamAttempt } from '../gateway/upstream/attempt.js'
import {
  createMemoryGatewayRequest,
  createGatewayTestRequest,
  MemoryGatewayResponse
} from '../gateway/testing/memory-gateway-http.js'
import { markGatewayUpstreamModelsProbe } from '../gateway/request/upstream-models-probe.js'
import { diagnosticResponseContextFromGatewayResponse } from '../gateway/diagnostics/diagnostic-response-context.js'
import {
  resolveOpenAIRequestModelMapping,
  type ResolvedOpenAIModelMapping
} from '../gateway/protocols/openai-v1/model-mapping.js'
import type { OpenAIGatewayTrafficSource } from '../gateway/usage/traffic-source.js'
import {
  type AccountDiagnosticAttemptProgressHandler,
  accountDiagnosticAttemptProgress,
  accountDiagnosticRetryTimeouts,
  diagnosticAccountTestGatewaySettingsOverride,
  diagnosticAttemptSignal,
  isDiagnosticTimeoutSignal
} from './account-diagnostic-retry-policy.js'
import {
  accountTestDefaultPrompt,
  createOpenAIImageGenerationTestRequest,
  accountTestModelsPathForProtocol,
  isAccountTestModelsPath,
  createAnthropicTestRequest,
  createGeminiTestRequest,
  createOpenAITestRequest
} from './account-test-request.js'

export type AccountTestInput = {
  model?: string
  prompt?: string
  signal?: AbortSignal
  groupId?: string
  systemAccountId?: string
  testEndpointMode?: AccountSupportedEndpointMode
  diagnostics?: 'full' | 'limited'
  trafficSource?: OpenAIGatewayTrafficSource
  gatewaySettingsOverride?: Partial<GatewaySettings>
  disableAccountStateMutation?: boolean
  candidateAccount?: OpenAIAccountSecret
  onDiagnosticAttemptProgress?: AccountDiagnosticAttemptProgressHandler
  onDiagnosticAttemptResult?: AccountDiagnosticAttemptResultHandler
  onUpstreamAttempt?: (attempt: UpstreamAttempt) => void
  retryAllFailures?: boolean
  forceProbeKind?: AccountTestProbeKind
  requireCatalogModelEvidence?: boolean
  shouldRetryFailure?: (result: AccountTestResult, attemptIndex: number) => boolean
  findAccountForTest?: (accountId: string, access?: AccessScope) => AccountSummary | undefined | Promise<AccountSummary | undefined>
  findOpenAIAccountForGroup?: (groupId: string, accountId: string, systemAccountId: string, options?: { includeUnavailable?: boolean; ignoreAvailability?: boolean }) => OpenAIAccountSecret | undefined | Promise<OpenAIAccountSecret | undefined>
}

export interface AccountDiagnosticAttemptResult {
  result: AccountTestResult
  signal: AbortSignal
  probeKind: AccountTestProbeKind
  attemptIndex: number
  totalAttempts: number
  upstreamAttempt?: UpstreamAttempt
  canceled: boolean
  diagnosticTimeoutExhausted: boolean
}

export type AccountDiagnosticAttemptResultHandler = (attempt: AccountDiagnosticAttemptResult) => void

export type AccountUpstreamModelCatalogResult = {
  modelIds: string[]
  requestUrl: string
  durationMs?: number
}

const accountModelCatalogPreflightTtlMs = 2 * 60 * 1000
const accountModelCatalogPreflightCache = new Map<string, number>()
const accountModelCatalogPreflightCacheMaxEntries = 512

export async function testOpenAIAccountWithDiagnosticRetries(
  account: AccountSummary,
  input: AccountTestInput = {}
): Promise<AccountTestResult> {
  const startedAt = Date.now()
  const preflightFailure = await preflightAccountModelCatalog(account, {
    ...input
  })
  if (preflightFailure) {
    return accountTestResultWithTotalDuration(preflightFailure, startedAt)
  }
  const model = await resolveAccountTestModelAsync(account, {
    explicitModel: input.model,
    systemAccountId: input.systemAccountId,
    testEndpointMode: input.testEndpointMode
  })
  const probeKind = await accountTestProbeKindAsync(account, model, {
    systemAccountId: input.systemAccountId,
    testEndpointMode: input.testEndpointMode
  })
  const timeoutSchedule = accountDiagnosticRetryTimeouts(probeKind)
  const result = await runAccountDiagnosticAttempts(account, input, {
    model,
    probeKind,
    timeoutSchedule,
    startedAt,
    retryEvent: 'account_diagnostic_test_retry_scheduled',
    retryMessage: '账户诊断请求未通过，将继续使用真实网关链路重试'
  })
  return accountTestResultWithTotalDuration(result, startedAt)
}

export async function discoverAccountUpstreamModels(
  account: AccountSummary,
  input: AccountTestInput = {}
): Promise<AccountUpstreamModelCatalogResult> {
  const startedAt = Date.now()
  const result = await runAccountDiagnosticAttempts(account, input, {
    probeKind: 'models_catalog',
    timeoutSchedule: accountDiagnosticRetryTimeouts('models_catalog'),
    startedAt,
    retryEvent: 'account_model_catalog_discovery_retry_scheduled',
    retryMessage: '账户模型目录获取未通过，将继续使用真实网关链路重试',
    forceRetryAllFailures: true,
    accountTestInput: {
      diagnostics: 'full',
      forceProbeKind: 'models_catalog',
      requireCatalogModelEvidence: false,
      disableAccountStateMutation: true
    }
  })
  if (!result.success) {
    throw new Error(result.message || '获取上游模型目录失败')
  }
  return {
    modelIds: accountModelCatalogIdsFromPayload(result.responseBody),
    requestUrl: result.requestUrl ?? accountTestModelsPathForProtocol(account.protocolCode),
    durationMs: result.durationMs
  }
}

export async function preflightAccountModelCatalog(account: AccountSummary, input: AccountTestInput): Promise<AccountTestResult | undefined> {
  const cacheKey = accountModelCatalogPreflightCacheKey(account)
  const now = Date.now()
  if (cacheKey && (accountModelCatalogPreflightCache.get(cacheKey) ?? 0) > now) return undefined

  const result = await runAccountDiagnosticAttempts(account, input, {
    probeKind: 'models_catalog',
    timeoutSchedule: accountDiagnosticRetryTimeouts('models_catalog'),
    startedAt: now,
    retryEvent: 'account_model_catalog_preflight_retry_scheduled',
    retryMessage: '账户模型目录预检未通过，将继续使用真实网关链路重试',
    forceRetryAllFailures: true,
    accountTestInput: {
      forceProbeKind: 'models_catalog',
      requireCatalogModelEvidence: false,
      disableAccountStateMutation: true
    }
  })
  if (result.success) {
    if (cacheKey) setAccountModelCatalogPreflightCache(cacheKey, now + accountModelCatalogPreflightTtlMs)
    return undefined
  }
  logger.debug({
    event: 'account_model_catalog_preflight_failed',
    accountId: account.id,
    providerCode: account.providerCode,
    statusCode: result.statusCode,
    errorCode: result.errorCode
  }, '账户模型目录预检未通过，终止真实模型测试')
  return result
}

async function runAccountDiagnosticAttempts(
  account: AccountSummary,
  input: AccountTestInput,
  options: {
    model?: string
    probeKind: AccountTestProbeKind
    timeoutSchedule: readonly number[]
    startedAt: number
    retryEvent: string
    retryMessage: string
    forceRetryAllFailures?: boolean
    accountTestInput?: Partial<AccountTestInput>
  }
): Promise<AccountTestResult> {
  let lastResult: AccountTestResult | undefined
  let everyAttemptTimedOutAfterRealUpstreamAttempt = true
  for (let attemptIndex = 0; attemptIndex < options.timeoutSchedule.length; attemptIndex += 1) {
    const timeoutMs = options.timeoutSchedule[attemptIndex] ?? options.timeoutSchedule[options.timeoutSchedule.length - 1]
    notifyDiagnosticAttemptProgress(input.onDiagnosticAttemptProgress, attemptIndex, timeoutMs, options.startedAt, options.timeoutSchedule)
    const signal = diagnosticAttemptSignal(input.signal, timeoutMs)
    let upstreamAttempt: UpstreamAttempt | undefined
    const result = await testOpenAIAccount(account, {
      ...input,
      ...options.accountTestInput,
      model: options.model,
      signal,
      forceProbeKind: options.probeKind,
      onUpstreamAttempt: (attempt) => {
        upstreamAttempt = attempt
        input.onUpstreamAttempt?.(attempt)
      },
      gatewaySettingsOverride: diagnosticAccountTestGatewaySettingsOverride(input.gatewaySettingsOverride, timeoutMs)
    })
    const timedOutAfterRealUpstreamAttempt = isDiagnosticTimeoutSignal(signal)
      && Boolean(upstreamAttempt && isRealUpstreamAttempt(upstreamAttempt))
    everyAttemptTimedOutAfterRealUpstreamAttempt &&= timedOutAfterRealUpstreamAttempt
    const diagnosticTimeoutExhausted = attemptIndex + 1 === options.timeoutSchedule.length
      && everyAttemptTimedOutAfterRealUpstreamAttempt
    notifyDiagnosticAttemptResult(input.onDiagnosticAttemptResult, {
      result,
      signal,
      probeKind: options.probeKind,
      attemptIndex,
      totalAttempts: options.timeoutSchedule.length,
      upstreamAttempt,
      canceled: signal.aborted && !isDiagnosticTimeoutSignal(signal),
      diagnosticTimeoutExhausted
    })
    lastResult = result
    const shouldRetryFailure = options.forceRetryAllFailures
      || (input.shouldRetryFailure
        ? input.shouldRetryFailure(result, attemptIndex)
        : input.retryAllFailures || result.accountFailureEligible !== false)
    if (result.success || !shouldRetryFailure || input.signal?.aborted) {
      return result
    }
    if (attemptIndex + 1 < options.timeoutSchedule.length) {
      logger.debug({
        event: options.retryEvent,
        accountId: account.id,
        accountName: account.name,
        probeKind: options.probeKind,
        attemptNumber: attemptIndex + 1,
        nextAttemptNumber: attemptIndex + 2,
        attemptTimeoutMs: timeoutMs,
        nextAttemptTimeoutMs: options.timeoutSchedule[attemptIndex + 1],
        durationMs: result.durationMs,
        totalElapsedMs: Date.now() - options.startedAt,
        traceId: result.traceId
      }, options.retryMessage)
    }
  }
  return lastResult ?? await testOpenAIAccount(account, {
    ...input,
    ...options.accountTestInput,
    model: options.model,
    forceProbeKind: options.probeKind
  })
}

function accountModelCatalogPreflightCacheKey(account: AccountSummary): string | undefined {
  const credentials = account.credentials ?? {}
  const apiKeys = Array.isArray(credentials.api_keys)
    ? credentials.api_keys.filter((value): value is string => typeof value === 'string' && Boolean(value.trim()))
    : []
  if (apiKeys.length > 1) return undefined
  const credentialFingerprint = accountCredentialFingerprint(credentials)
  if (!credentialFingerprint) return undefined
  const baseUrl = stringValue(credentials.base_url)
  return `${account.id}:${account.providerProtocolProfileId ?? ''}:${baseUrl}:${credentialFingerprint}`
}

function setAccountModelCatalogPreflightCache(key: string, expiresAt: number): void {
  if (accountModelCatalogPreflightCache.size >= accountModelCatalogPreflightCacheMaxEntries) {
    const firstKey = accountModelCatalogPreflightCache.keys().next().value
    if (typeof firstKey === 'string') accountModelCatalogPreflightCache.delete(firstKey)
  }
  accountModelCatalogPreflightCache.set(key, expiresAt)
}

function notifyDiagnosticAttemptProgress(
  handler: AccountDiagnosticAttemptProgressHandler | undefined,
  attemptIndex: number,
  timeoutMs: number,
  startedAt: number,
  timeoutSchedule: readonly number[]
): void {
  if (!handler) return
  try {
    handler(accountDiagnosticAttemptProgress(attemptIndex, timeoutMs, startedAt, timeoutSchedule))
  } catch (error) {
    logger.warn(errorLogFields(error, { event: 'account_diagnostic_attempt_progress_callback_failed' }), '账户诊断进度回调执行失败')
  }
}

function notifyDiagnosticAttemptResult(
  handler: AccountDiagnosticAttemptResultHandler | undefined,
  attempt: AccountDiagnosticAttemptResult
): void {
  if (!handler) return
  try {
    handler(attempt)
  } catch (error) {
    logger.warn(errorLogFields(error, { event: 'account_diagnostic_attempt_result_callback_failed' }), '账户诊断结果回调执行失败')
  }
}

export async function testOpenAIAccount(
  account: AccountSummary,
  input: AccountTestInput = {}
): Promise<AccountTestResult> {
  const explicitModel = stringValue(input.model)
  const prompt = stringValue(input.prompt) || accountTestDefaultPrompt
  const startedAt = Date.now()
  const limitedDiagnostics = input.diagnostics === 'limited'
  const anthropicProtocol = isAnthropicProtocolProfile(account)
  const geminiProtocol = isGeminiProtocolProfile(account)
  let testEndpointMode: AccountSupportedEndpointMode | undefined
  let testRequest: ReturnType<typeof createAnthropicTestRequest> | ReturnType<typeof createGeminiTestRequest> | ReturnType<typeof createOpenAITestRequest> | ReturnType<typeof createOpenAIImageGenerationTestRequest> | undefined
  let requestBody: Record<string, unknown> | undefined
  let requestUrl: string | undefined
  let modelMapping: ResolvedOpenAIModelMapping | undefined
  let testedModel: string | undefined
  let probeKind: AccountTestProbeKind = 'generation'
  // 非 OpenAI v1 账户不使用 OpenAI 的 clientCompatibility 规范化，避免写入无意义的 OpenAI 格式值
  const accountClientCompatibility = anthropicProtocol || geminiProtocol
    ? 'openai_standard' as const
    : normalizeOpenAIAccountClientCompatibility(
        account.providerCode,
        account.type,
        account.clientCompatibility,
        account.clientCompatibility,
        account
      )
  let clientCompatibility = accountClientCompatibility
  const modelsUrl = accountTestModelsPathForProtocol(account.protocolCode)
  const traceId = createTraceId()

  try {
    probeKind = input.forceProbeKind ?? 'generation'
    let model: string | undefined
    let supportedEndpointModes: AccountSupportedEndpointMode[] = []
    if (probeKind !== 'models_catalog') {
      model = await resolveAccountTestModelAsync(account, {
        explicitModel,
        systemAccountId: input.systemAccountId
      })
      testedModel = model
      probeKind = input.forceProbeKind ?? await accountTestProbeKindAsync(account, model, {
        systemAccountId: input.systemAccountId,
        testEndpointMode: input.testEndpointMode
      })
      supportedEndpointModes = probeKind === 'image_generation'
        ? ['images_json' as const]
        : accountManualTestEndpointModes(account)
      testEndpointMode = probeKind === 'image_generation'
        ? 'images_json'
        : resolveAccountTestEndpointMode(supportedEndpointModes, input.testEndpointMode)
      clientCompatibility = accountTestClientCompatibility(account, testEndpointMode, accountClientCompatibility)
    }
    const resolved = await resolveAccountTestCandidate(account, {
      groupId: stringValue(input.groupId),
      systemAccountId: stringValue(input.systemAccountId),
      clientCompatibility,
      candidateAccount: input.candidateAccount,
      findOpenAIAccountForGroup: input.findOpenAIAccountForGroup
    })
    const messagesTestMode = isMessagesTestEndpointMode(testEndpointMode)
    const geminiTestMode = isGeminiTestEndpointMode(testEndpointMode)
    if (probeKind === 'image_generation') {
      testRequest = createOpenAIImageGenerationTestRequest({
        explicitModel,
        fallbackModel: model ?? ''
      })
    } else if (probeKind === 'generation') {
      const generationEndpointMode = testEndpointMode!
      testRequest = messagesTestMode
        ? createAnthropicTestRequest({
          explicitModel,
          fallbackModel: model ?? '',
          prompt,
          supportedEndpointModes,
          testEndpointMode: generationEndpointMode
        })
        : geminiTestMode
          ? createGeminiTestRequest({
            explicitModel,
            fallbackModel: model ?? '',
            prompt,
            testEndpointMode: generationEndpointMode
          })
          : createOpenAITestRequest({
            explicitModel,
            fallbackModel: model ?? '',
            prompt,
            isOAuth: account.type === 'oauth',
            clientCompatibility,
            testEndpointMode: generationEndpointMode
          })
    }
    requestBody = testRequest?.body
    requestUrl = probeKind === 'models_catalog' ? modelsUrl : testRequest!.path
    const request = probeKind === 'models_catalog'
      ? markGatewayUpstreamModelsProbe(
          createMemoryGatewayRequest({ method: 'GET', path: requestUrl, signal: input.signal, serverDiagnostic: true })
        )
      : createGatewayTestRequest(
        requestUrl,
        requestBody!,
        JSON.stringify(requestBody),
        account.type === 'oauth',
        input.signal,
        clientCompatibility,
        testRequest?.headers,
        true
      )
    const diagnosticCandidate = explicitModel
      ? {
          ...resolved.account,
          supportedModels: normalizedAccountTestModels([
            ...(resolved.account.supportedModels ?? []),
            explicitModel
          ])
        }
      : resolved.account
    modelMapping = probeKind === 'generation'
      ? resolveOpenAIRequestModelMapping(request, diagnosticCandidate)
      : undefined
    const response = new MemoryGatewayResponse(startedAt)
    let diagnosticLastAttempt: UpstreamAttempt | undefined
    const context: RequestContext = {
      traceId,
      startedAt,
      method: request.method,
      path: request.path,
      originalUrl: request.originalUrl,
      clientIp: request.ip,
      systemAccountId: resolved.systemAccountId,
      groupId: resolved.groupId,
      logger: resolvedLogger(traceId)
    }

    await withRequestContext(context, () => withRequestAuthContext(undefined, () => handleOpenAIGatewayRequest(request, response.asResponse(), {
      identity: {
        systemAccountId: resolved.systemAccountId,
        groupId: resolved.groupId
      },
      candidateAccounts: [diagnosticCandidate],
      disableSessionAffinity: true,
      exposeUpstreamDiagnostics: !limitedDiagnostics,
      trafficSource: input.trafficSource ?? 'manual_account_test',
      settingsOverride: input.gatewaySettingsOverride,
      disableAccountStateMutation: input.disableAccountStateMutation ?? true,
      ignoreAccountRuntimeSuppression: true,
      forwardModelsRequestToUpstream: probeKind === 'models_catalog',
      accountProbeModel: probeKind === 'models_catalog' ? testedModel : undefined,
      onUpstreamAttemptDiagnostic: (lastAttempt) => {
        diagnosticLastAttempt = lastAttempt
        notifyUpstreamAttempt(input.onUpstreamAttempt, lastAttempt)
      },
      onUpstreamAttemptStartedDiagnostic: (startedAccount, upstreamUrl) => {
        notifyUpstreamAttempt(input.onUpstreamAttempt, {
          accountId: startedAccount.id,
          accountName: startedAccount.name,
          providerCode: startedAccount.providerCode,
          providerProtocolProfileId: startedAccount.providerProtocolProfileId,
          protocolCode: startedAccount.protocolCode,
          protocolVersion: startedAccount.protocolVersion,
          upstreamUrl
        })
      }
    })))
    if (input.signal?.aborted) {
      throw accountTestAbortError(input.signal)
    }
    await flushGatewayAccountSideEffects()
    if (input.signal?.aborted) {
      throw accountTestAbortError(input.signal)
    }

    const finalAccount = input.candidateAccount
      ? resolved.account
      : await loadOpenAIAccountForGroup(input, resolved.groupId, account.id, resolved.systemAccountId, { ignoreAvailability: true }) ?? resolved.account
    const finalSummary = input.candidateAccount
      ? account
      : await loadAccountForTest(input, account.id, { systemAccountId: resolved.systemAccountId, role: 'user' })
    const finalAccountStatus = finalSummary?.status ?? finalAccount.status
    const downstreamResponseText = response.bodyText()
    const { responseText, responseHeaders, responseTruncated } = resolveAccountTestResponseDiagnostics({
      downstreamResponseText,
      downstreamResponseHeaders: response.headersObject(),
      downstreamResponseTruncated: response.bodyTruncated(),
      upstreamAttempt: diagnosticLastAttempt
    })
    const responseContext = probeKind === 'image_generation'
      ? undefined
      : diagnosticResponseContextFromGatewayResponse(
          responseText,
          responseText === downstreamResponseText ? response.nonStreamJsonBody() : undefined,
          responseText === downstreamResponseText ? response.parsedStreamEvents() : undefined
        )
    const imageResponseInspection = probeKind === 'image_generation'
      ? inspectAccountTestImageResponseEnvelope(responseText, responseTruncated)
      : undefined
    const diagnosticProtocol: AccountTestDiagnosticProtocol = messagesTestMode
      ? 'anthropic'
      : geminiTestMode
        ? 'gemini'
        : 'openai'
    const upstreamMessage = imageResponseInspection?.errorMessage ?? (responseContext
      ? parseAccountTestUpstreamMessage(responseContext, diagnosticProtocol)
        ?? (diagnosticProtocol === 'openai' ? undefined : parseAccountTestUpstreamMessage(responseContext, 'openai'))
        ?? (responseText ? responseText.slice(0, 240) : undefined)
      : undefined)
    const upstreamErrorCode = imageResponseInspection?.errorCode
      ?? (responseContext ? parseAccountTestUpstreamErrorCode(responseContext) : undefined)
    const streamFailureMessage = responseContext
      ? parseAccountTestStreamFailureMessage(responseContext, diagnosticProtocol)
        ?? (diagnosticProtocol === 'openai' ? undefined : parseAccountTestStreamFailureMessage(responseContext, 'openai'))
      : undefined
    const outputText = responseContext
      ? extractAccountTestResponseOutputText(responseContext, diagnosticProtocol)
      : undefined
    const httpSucceeded = response.statusCode >= 200 && response.statusCode < 300
    // Image tests verify only the HTTP outcome; parsing base64 payloads adds no diagnostic value.
    const protocolSuccessEvidence = probeKind === 'image_generation'
      ? imageResponseInspection?.successEvidence === true
      : probeKind === 'models_catalog'
        ? input.requireCatalogModelEvidence === false
          ? Boolean(responseContext && hasAccountModelCatalogResponseEvidence(responseContext))
          : Boolean(responseContext && hasAccountModelCatalogSuccessEvidence(testedModel ?? '', responseContext))
        : Boolean(responseContext && testEndpointMode && hasAccountTestProtocolSuccessEvidence(testEndpointMode, responseContext))
    const success = httpSucceeded && !streamFailureMessage && protocolSuccessEvidence
    const protocolEvidenceError = httpSucceeded && !streamFailureMessage && !protocolSuccessEvidence
      ? probeKind === 'models_catalog'
        ? input.requireCatalogModelEvidence === false
          ? '上游模型目录响应格式无效'
          : `上游模型目录响应格式无效`
        : probeKind === 'image_generation'
          ? imageResponseInspection?.errorMessage
            ? '上游 Images API 返回错误响应'
            : '上游 Images API 响应缺少有效图片结果'
        : '上游返回 HTTP 2xx，但响应中缺少所选检查协议的完成证据'
      : undefined
    const protocolEvidenceErrorCode = probeKind === 'models_catalog'
      ? 'model_not_found'
      : 'invalid_protocol_success_response'
    const diagnosticStatusCode = accountTestDiagnosticStatusCode(response.statusCode, success, diagnosticLastAttempt)
    const proxyFailureMessage = !success && finalAccount.proxyProfileUnavailable ? finalAccount.proxyProfileErrorMessage : undefined
    const imageResponseBody = probeKind === 'image_generation'
      ? { response: '已省略' }
      : undefined
    const responseDiagnostics = probeKind === 'image_generation'
      ? {
          responseBody: imageResponseBody,
          responseText: JSON.stringify(imageResponseBody),
          responseTruncated
        }
      : limitedDiagnostics
        ? {}
        : {
            responseHeaders,
            responseBody: responseContext?.json,
            responseText,
            responseTruncated,
            outputText
          }
    return accountTestResultWithDiagnosticsMode(sanitizeAccountTestResult({
      accountId: account.id,
      accountName: account.name,
      providerCode: account.providerCode,
      providerProtocolProfileId: account.providerProtocolProfileId,
      protocolCode: account.protocolCode,
      protocolVersion: account.protocolVersion,
      type: account.type,
      traceId,
      success,
      statusCode: diagnosticStatusCode,
      errorCode: success
        ? undefined
        : probeKind === 'image_generation' && upstreamErrorCode
          ? upstreamErrorCode
          : protocolEvidenceError ? protocolEvidenceErrorCode : upstreamErrorCode,
      message: success
        ? accountTestSuccessMessage(account, responseTruncated, requestUrl)
        : probeKind === 'image_generation'
          ? proxyFailureMessage || protocolEvidenceError || accountTestHttpFailureMessage(diagnosticStatusCode, response.statusCode)
          : proxyFailureMessage || protocolEvidenceError || upstreamMessage || streamFailureMessage || accountTestHttpFailureMessage(diagnosticStatusCode, response.statusCode),
      model: testedModel,
      ...accountTestModelMappingFields(modelMapping),
      testEndpointMode,
      requestUrl,
      requestBody,
      ...responseDiagnostics,
      modelsUrl,
      proxyUrl: accountTestProxyMarker(account, finalAccount),
      tokenRefreshed: didRefreshToken(account, finalAccount),
      durationMs: Date.now() - startedAt,
      firstTokenMs: response.firstTokenMs(),
      accountStatusChanged: finalAccountStatus !== account.status,
      accountStatus: finalAccountStatus,
      accountFailureEligible: success
        ? false
        : accountTestFailureEligibleForAccount({
            statusCode: diagnosticStatusCode,
            errorCode: protocolEvidenceError ? protocolEvidenceErrorCode : upstreamErrorCode,
            message: proxyFailureMessage || protocolEvidenceError || upstreamMessage || streamFailureMessage
          })
    }), limitedDiagnostics)
  } catch (error) {
    const normalizedError = input.signal?.aborted ? accountTestAbortError(input.signal) : error
    const suppressDiagnostics = probeKind === 'image_generation'
    const message = suppressDiagnostics
      ? accountTestFailureMessage(account, requestUrl)
      : normalizedError instanceof Error ? normalizedError.message : accountTestFailureMessage(account, requestUrl)
    const accountFailureEligible = accountTestFailureEligible(normalizedError)
    return accountTestResultWithDiagnosticsMode(sanitizeAccountTestResult({
      accountId: account.id,
      accountName: account.name,
      providerCode: account.providerCode,
      providerProtocolProfileId: account.providerProtocolProfileId,
      protocolCode: account.protocolCode,
      protocolVersion: account.protocolVersion,
      type: account.type,
      traceId,
      success: false,
      errorCode: normalizedError instanceof AccountTestAbortError ? normalizedError.errorCode : undefined,
      message,
      model: testedModel,
      ...accountTestModelMappingFields(modelMapping),
      testEndpointMode,
      requestUrl,
      requestBody,
      ...(suppressDiagnostics ? {} : { responseText: message }),
      modelsUrl,
      proxyUrl: account.proxyProfileId ? '[configured]' : undefined,
      durationMs: Date.now() - startedAt,
      accountStatusChanged: false,
      accountStatus: account.status,
      accountFailureEligible
    }), limitedDiagnostics)
  }
}

async function accountTestProbeKindAsync(
  account: AccountSummary,
  model: string,
  input: { systemAccountId?: string; testEndpointMode?: AccountSupportedEndpointMode }
): Promise<AccountTestProbeKind> {
  const systemAccountId = stringValue(input.systemAccountId)
    || stringValue(account.ownerSystemAccountId)
    || stringValue(account.systemAccountId)
  const catalogItem = systemAccountId
    ? await findProviderModelTestCatalogItemAsync({
        providerCode: account.providerCode,
        systemAccountId,
        model,
        protocolsOnly: true
      })
    : undefined
  return accountTestProbeKind(account, {
    testEndpointMode: input.testEndpointMode,
    supportedApiProtocols: catalogItem?.supportedApiProtocols
  })
}

function notifyUpstreamAttempt(handler: AccountTestInput['onUpstreamAttempt'], attempt: UpstreamAttempt): void {
  if (!handler) return
  try {
    handler(attempt)
  } catch (error) {
    logger.warn(errorLogFields(error, { event: 'account_test_upstream_attempt_callback_failed' }), '账户测试上游尝试回调执行失败')
  }
}

async function loadAccountForTest(
  input: AccountTestInput,
  accountId: string,
  access?: AccessScope
): Promise<AccountSummary | undefined> {
  const reader = input.findAccountForTest ?? findAccountForTestAsync
  return await reader(accountId, access)
}

export async function resolveAccountTestModelAsync(
  account: Pick<AccountSummary, 'providerCode' | 'providerProtocolProfileId' | 'supportedModels' | 'healthCheckModel' | 'systemAccountId' | 'ownerSystemAccountId' | 'bindingSystemAccountId' | 'modelMappings' | 'protocolCode' | 'protocolVersion' | 'type'>,
  input: {
    explicitModel?: string
    systemAccountId?: string
    providerCode?: string
    providerProtocolProfileId?: string
    supportedModels?: string[]
    sourceFamilies?: AccountModelMappingSourceEndpointFamily[]
    testEndpointMode?: AccountSupportedEndpointMode
  } = {}
): Promise<string> {
  const explicitModel = stringValue(input.explicitModel)
  if (explicitModel) return explicitModel

  const supportedModels = normalizedAccountTestModels(input.supportedModels ?? account.supportedModels)
  const healthCheckModel = stringValue(account.healthCheckModel)
  if (!healthCheckModel) {
    throw new AccountTestConfigurationError('账户检查模型未配置')
  }
  if (!supportedModels.includes(healthCheckModel)) {
    throw new AccountTestConfigurationError(`账户检查模型不在支持模型列表中：${healthCheckModel}`)
  }
  return healthCheckModel
}

export async function preferredSystemAccountTestModelAsync(
  account: Pick<AccountSummary, 'providerCode' | 'providerProtocolProfileId' | 'supportedModels' | 'healthCheckModel' | 'systemAccountId' | 'ownerSystemAccountId' | 'bindingSystemAccountId' | 'modelMappings' | 'protocolCode' | 'protocolVersion' | 'type'>
): Promise<string> {
  return await resolveAccountTestModelAsync(account, {
    sourceFamilies: accountTestDefaultSourceFamilies(account)
  })
}

function sanitizeAccountTestResult(result: AccountTestResult): AccountTestResult {
  return sanitizeDiagnosticPayload(result)
}

function resolveAccountTestEndpointMode(
  supportedModes: AccountSupportedEndpointMode[],
  requestedMode?: AccountSupportedEndpointMode
): AccountSupportedEndpointMode {
  const allowedModes = supportedModes
  if (requestedMode) {
    if (!allowedModes.includes(requestedMode)) {
      throw new AccountTestConfigurationError(`测试请求形态不在账户上游接口能力中：${requestedMode}`)
    }
    return requestedMode
  }
  const mode = allowedModes[0]
  if (!mode) {
    throw new AccountTestConfigurationError('账户上游接口能力中没有可用于连接测试的请求形态')
  }
  return mode
}

function accountTestClientCompatibility(
  account: AccountSummary,
  testEndpointMode: AccountSupportedEndpointMode,
  accountClientCompatibility: AccountClientCompatibility
): AccountClientCompatibility {
  if (isMessagesTestEndpointMode(testEndpointMode) || isGeminiTestEndpointMode(testEndpointMode)) {
    return 'openai_standard'
  }
  if (!isOpenAIProtocolProfile(account)) {
    return 'openai_standard'
  }
  if (testEndpointMode === 'chat_json' || testEndpointMode === 'chat_sse') {
    return 'openai_standard'
  }
  if (account.type === 'oauth') {
    return 'codex_responses'
  }
  return normalizeOpenAIAccountClientCompatibility(
    account.providerCode,
    account.type,
    accountClientCompatibility,
    account.clientCompatibility,
    account
  )
}

function isMessagesTestEndpointMode(mode: AccountSupportedEndpointMode | undefined): boolean {
  return mode === 'messages_json' || mode === 'messages_sse'
}

function isGeminiTestEndpointMode(mode: AccountSupportedEndpointMode | undefined): boolean {
  return mode === 'generate_content_json' || mode === 'generate_content_sse' || mode === 'interactions_json' || mode === 'interactions_sse'
}

function accountTestDiagnosticStatusCode(downstreamStatusCode: number, success: boolean, lastAttempt?: UpstreamAttempt): number | undefined {
  if (success) {
    return downstreamStatusCode
  }
  if (isHttpStatusCode(lastAttempt?.status)) {
    return lastAttempt.status
  }
  return downstreamStatusCode >= 200 && downstreamStatusCode < 300 ? undefined : downstreamStatusCode
}

function accountTestHttpFailureMessage(statusCode: number | undefined, downstreamStatusCode: number): string {
  if (typeof statusCode === 'number') {
    return `API 返回 HTTP ${statusCode}`
  }
  return `API 返回 HTTP ${downstreamStatusCode}`
}

function isHttpStatusCode(value: unknown): value is number {
  return typeof value === 'number' && Number.isInteger(value) && value >= 100 && value <= 599
}

function accountTestResultWithDiagnosticsMode(result: AccountTestResult, limited: boolean): AccountTestResult {
  if (!limited) return result
  const message = limitedAccountTestMessage(result)
  return {
    accountId: result.accountId,
    accountName: result.accountName,
    providerCode: result.providerCode,
    providerProtocolProfileId: result.providerProtocolProfileId,
    protocolCode: result.protocolCode,
    protocolVersion: result.protocolVersion,
    type: result.type,
    traceId: result.traceId,
    success: result.success,
    statusCode: result.statusCode,
    errorCode: result.errorCode,
    message,
    model: result.model,
    upstreamModel: result.upstreamModel,
    modelMappingApplied: result.modelMappingApplied,
    modelMappingSource: result.modelMappingSource,
    sourceEndpointFamily: result.sourceEndpointFamily,
    upstreamEndpointFamily: result.upstreamEndpointFamily,
    testEndpointMode: result.testEndpointMode,
    responseBody: result.success && result.testEndpointMode === 'images_json' ? result.responseBody : undefined,
    responseText: result.success && result.testEndpointMode === 'images_json'
      ? result.responseText
      : result.success ? undefined : message,
    responseTruncated: result.success ? result.responseTruncated : undefined,
    outputText: result.success ? result.outputText : undefined,
    durationMs: result.durationMs,
    firstTokenMs: result.firstTokenMs,
    accountStatusChanged: result.accountStatusChanged,
    accountStatus: result.accountStatus,
    accountFailureEligible: result.accountFailureEligible
  }
}

function accountTestModelMappingFields(
  mapping: ResolvedOpenAIModelMapping | undefined
): Pick<AccountTestResult, 'upstreamModel' | 'modelMappingApplied' | 'modelMappingSource' | 'sourceEndpointFamily' | 'upstreamEndpointFamily'> {
  if (!mapping) {
    return {
      modelMappingApplied: false
    }
  }
  return {
    upstreamModel: mapping.upstreamModel,
    modelMappingApplied: true,
    modelMappingSource: mapping.runtimeSource ?? 'account',
    sourceEndpointFamily: mapping.sourceEndpointFamily,
    upstreamEndpointFamily: mapping.upstreamEndpointFamily
  }
}

function accountTestResultWithTotalDuration(result: AccountTestResult, startedAt: number): AccountTestResult {
  return {
    ...result,
    durationMs: Date.now() - startedAt
  }
}

function limitedAccountTestMessage(result: AccountTestResult): string {
  if (result.success) return result.message
  return '上游请求失败'
}

function accountTestAbortMessage(signal: AbortSignal): string {
  if (isAccountTestTimeoutSignal(signal)) {
    return '账户测试超时'
  }
  return '账户测试已取消'
}

function accountTestAbortError(signal: AbortSignal): AccountTestAbortError {
  return new AccountTestAbortError(accountTestAbortMessage(signal), isAccountTestTimeoutSignal(signal))
}

function isAccountTestTimeoutSignal(signal: AbortSignal): boolean {
  return isDiagnosticTimeoutSignal(signal)
}

function accountTestFailureEligible(error: unknown): boolean {
  if (error instanceof AccountTestConfigurationError) return false
  if (error instanceof AccountTestAbortError) return error.accountFailureEligible
  return true
}

class AccountTestConfigurationError extends Error {
}

class AccountTestAbortError extends Error {
  readonly accountFailureEligible: boolean
  readonly errorCode: 'server_diagnostic_timeout' | 'server_diagnostic_cancelled'

  constructor(message: string, timedOut: boolean) {
    super(message)
    this.accountFailureEligible = timedOut
    this.errorCode = timedOut ? 'server_diagnostic_timeout' : 'server_diagnostic_cancelled'
  }
}

function accountTestProxyMarker(account: AccountSummary, resolved: OpenAIAccountSecret): string | undefined {
  return account.proxyProfileId || resolved.proxyUrl || resolved.proxyProfileUnavailable ? '[configured]' : undefined
}

async function resolveAccountTestCandidate(account: AccountSummary, input: { groupId?: string; systemAccountId?: string; clientCompatibility?: AccountClientCompatibility; candidateAccount?: OpenAIAccountSecret; findOpenAIAccountForGroup?: AccountTestInput['findOpenAIAccountForGroup'] } = {}): Promise<{
  systemAccountId: string
  groupId: string
  account: OpenAIAccountSecret
}> {
  const draftCandidate = input.candidateAccount
  if (draftCandidate) {
    const systemAccountId = input.systemAccountId || draftCandidate.systemAccountId
    const groupId = input.groupId || account.boundGroupId || draftCandidate.boundGroupId
    if (!systemAccountId) {
      throw new AccountTestConfigurationError('账户归属数据异常，无法执行网关测试')
    }
    if (!groupId) {
      throw new AccountTestConfigurationError('账户未绑定可用分组，无法按客户真实链路测试')
    }
    return {
      systemAccountId,
      groupId,
      account: input.clientCompatibility ? {
        ...draftCandidate,
        clientCompatibility: input.clientCompatibility
      } : draftCandidate
    }
  }
  const systemAccountId = account.accessType === 'authorized'
    ? account.bindingSystemAccountId
    : account.ownerSystemAccountId ?? account.systemAccountId
  if (!systemAccountId) {
    throw new AccountTestConfigurationError('账户归属数据异常，无法执行网关测试')
  }
  const groupId = input.groupId || account.boundGroupId
  if (!groupId) {
    throw new AccountTestConfigurationError('账户未绑定可用分组，无法按客户真实链路测试')
  }
  const resolvedCandidate = await loadOpenAIAccountForGroup(input, groupId, account.id, systemAccountId, { ignoreAvailability: true })
  if (!resolvedCandidate) {
    throw new AccountTestConfigurationError('账户不在当前分组或凭据不可用，无法执行网关测试')
  }
  return {
    systemAccountId,
    groupId,
    account: input.clientCompatibility ? {
      ...resolvedCandidate,
      clientCompatibility: input.clientCompatibility
    } : resolvedCandidate
  }
}

function preferredMappedSourceModelForAccount(
  account: Pick<AccountSummary, 'modelMappings' | 'supportedModels'>,
  sourceFamilies: AccountModelMappingSourceEndpointFamily[]
): string | undefined {
  const supported = new Set((account.supportedModels ?? []).map((model) => stringValue(model)).filter(Boolean))
  const sourceFamilySet = new Set(sourceFamilies)
  const mapping = (account.modelMappings ?? []).find((item) => accountModelMappingUsableForTest(item, sourceFamilySet, supported))
  return mapping?.sourceModel
}

function accountModelMappingUsableForTest(
  mapping: AccountModelMapping,
  sourceFamilies: Set<AccountModelMappingSourceEndpointFamily>,
  supportedModels: Set<string>
): boolean {
  return mapping.enabled !== false
    && sourceFamilies.has(mapping.sourceEndpointFamily)
    && Boolean(stringValue(mapping.sourceModel))
    && Boolean(stringValue(mapping.upstreamModel))
    && (supportedModels.size === 0 || supportedModels.has(mapping.upstreamModel))
}

function accountTestDefaultSourceFamilies(account: Pick<AccountSummary, 'providerProtocolProfileId' | 'protocolCode' | 'protocolVersion' | 'type'>): AccountModelMappingSourceEndpointFamily[] {
  if (isAnthropicProtocolProfile(account)) return [ANTHROPIC_MESSAGES_FAMILY]
  if (isGeminiProtocolProfile(account)) return [GEMINI_STREAM_GENERATE_CONTENT_FAMILY, GEMINI_GENERATE_CONTENT_FAMILY]
  if (account.type === 'oauth') return [OPENAI_RESPONSES_FAMILY]
  return [OPENAI_CHAT_COMPLETIONS_FAMILY, OPENAI_RESPONSES_FAMILY]
}

function accountTestEndpointModeSourceFamily(mode: AccountSupportedEndpointMode): AccountModelMappingSourceEndpointFamily {
  if (mode === 'chat_json' || mode === 'chat_sse') return OPENAI_CHAT_COMPLETIONS_FAMILY
  if (mode === 'responses_json' || mode === 'responses_sse') return OPENAI_RESPONSES_FAMILY
  if (mode === 'messages_json' || mode === 'messages_sse') return ANTHROPIC_MESSAGES_FAMILY
  if (mode === 'generate_content_sse') return GEMINI_STREAM_GENERATE_CONTENT_FAMILY
  return GEMINI_GENERATE_CONTENT_FAMILY
}

async function loadOpenAIAccountForGroup(
  input: Pick<AccountTestInput, 'findOpenAIAccountForGroup'>,
  groupId: string,
  accountId: string,
  systemAccountId: string,
  options: { includeUnavailable?: boolean; ignoreAvailability?: boolean }
): Promise<OpenAIAccountSecret | undefined> {
  const reader = input.findOpenAIAccountForGroup ?? (async (targetGroupId, targetAccountId, targetSystemAccountId, targetOptions) => {
    return await findOpenAIAccountForGroupAsync(targetGroupId, targetAccountId, targetSystemAccountId, targetOptions)
  })
  return await reader(groupId, accountId, systemAccountId, options)
}

function accountTestPreferenceSystemAccountId(
  account: Pick<AccountSummary, 'systemAccountId' | 'ownerSystemAccountId' | 'bindingSystemAccountId'>,
  requestSystemAccountId?: string
): string | undefined {
  return stringValue(requestSystemAccountId)
    || stringValue(account.bindingSystemAccountId)
    || stringValue(account.ownerSystemAccountId)
    || stringValue(account.systemAccountId)
    || undefined
}

function normalizedAccountTestModels(models: string[] | undefined): string[] {
  return [...new Set((models ?? []).map((model) => stringValue(model)).filter(Boolean))]
}

function supportedAccountTestModel(model: string | undefined, supportedModels: string[]): string {
  const normalizedModel = stringValue(model)
  if (!normalizedModel) return ''
  return !supportedModels.length || supportedModels.includes(normalizedModel)
    ? normalizedModel
    : ''
}

function didRefreshToken(original: AccountSummary, resolved: OpenAIAccountSecret): boolean | undefined {
  if (original.type !== 'oauth') return false
  const before = stringValue(original.credentials.access_token)
  const after = stringValue(resolved.apiKey)
  return Boolean(after && before !== after)
}

function accountTestSuccessMessage(account: AccountSummary, responseTruncated: boolean, requestUrl: string): string {
  const protocolName = accountTestProtocolName(account, requestUrl)
  return responseTruncated
    ? `${protocolName} 测试通过（响应体过大，已截断展示）`
    : `${protocolName} 测试通过`
}

function accountTestFailureMessage(account: AccountSummary, requestUrl?: string): string {
  return `${accountTestProtocolName(account, requestUrl)} 测试失败`
}

function accountTestProtocolName(account: AccountSummary, requestUrl?: string): string {
  if (isAccountTestModelsPath(requestUrl)) return '上游模型目录'
  if (requestUrl?.includes('/images/')) return 'OpenAI Images API'
  if (isAnthropicProtocolProfile(account)) return 'Anthropic Messages'
  if (isGeminiProtocolProfile(account)) return 'Gemini GenerateContent'
  return requestUrl?.includes('/chat/completions') ? 'OpenAI Chat Completions' : 'OpenAI Responses'
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function resolvedLogger(traceId: string): RequestContext['logger'] {
  return logger.child({ source: 'account_test', traceId })
}

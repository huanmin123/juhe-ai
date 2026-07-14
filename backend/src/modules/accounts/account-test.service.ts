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
import { accountManualTestEndpointModes } from './account-test-endpoint-modes.js'
import { withRequestAuthContext } from '../auth/request-context.js'
import { handleOpenAIGatewayRequest } from '../gateway/routes.js'
import { sanitizeDiagnosticPayload } from '../gateway/diagnostics/diagnostic-sanitizer.js'
import type { GatewaySettings } from '../gateway/policy/account-error-policy.service.js'
import { flushGatewayAccountSideEffects } from '../gateway/runtime/account-side-effects.service.js'
import type { UpstreamAttempt } from '../gateway/upstream/attempt.js'
import {
  createGatewayTestRequest,
  MemoryGatewayResponse
} from '../gateway/testing/memory-gateway-http.js'
import {
  extractOpenAIResponseOutputText,
  parseOpenAIJsonBody,
  parseOpenAIStreamFailureMessage,
  parseOpenAIUpstreamMessage
} from '../gateway/protocols/openai-v1/response-parsing.js'
import {
  resolveOpenAIRequestModelMapping,
  type ResolvedOpenAIModelMapping
} from '../gateway/protocols/openai-v1/model-mapping.js'
import {
  parseAnthropicUpstreamMessage,
  parseAnthropicStreamFailureMessage,
  extractAnthropicResponseOutputText
} from '../gateway/protocols/anthropic-v1/response-parsing.js'
import type { OpenAIGatewayTrafficSource } from '../gateway/usage/traffic-source.js'
import {
  type AccountDiagnosticAttemptProgressHandler,
  accountDiagnosticAttemptProgress,
  accountDiagnosticRetryTimeoutMs,
  diagnosticAccountTestGatewaySettingsOverride,
  diagnosticAttemptSignal,
  isDiagnosticTimeoutSignal
} from './account-diagnostic-retry-policy.js'
import {
  accountTestDefaultPrompt,
  accountTestModelsPath,
  createAnthropicTestRequest,
  createGeminiTestRequest,
  createOpenAITestRequest
} from './account-test-request.js'

type AccountTestInput = {
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
  findAccountForTest?: (accountId: string, access?: AccessScope) => AccountSummary | undefined | Promise<AccountSummary | undefined>
  findOpenAIAccountForGroup?: (groupId: string, accountId: string, systemAccountId: string, options?: { includeUnavailable?: boolean; ignoreAvailability?: boolean }) => OpenAIAccountSecret | undefined | Promise<OpenAIAccountSecret | undefined>
}

export async function testOpenAIAccountWithDiagnosticRetries(
  account: AccountSummary,
  input: AccountTestInput = {}
): Promise<AccountTestResult> {
  const startedAt = Date.now()
  const model = await resolveAccountTestModelAsync(account, {
    explicitModel: input.model,
    systemAccountId: input.systemAccountId,
    testEndpointMode: input.testEndpointMode
  })
  let lastResult: AccountTestResult | undefined
  for (let attemptIndex = 0; attemptIndex < accountDiagnosticRetryTimeoutMs.length; attemptIndex += 1) {
    const timeoutMs = accountDiagnosticRetryTimeoutMs[attemptIndex] ?? accountDiagnosticRetryTimeoutMs[accountDiagnosticRetryTimeoutMs.length - 1]
    notifyDiagnosticAttemptProgress(input.onDiagnosticAttemptProgress, attemptIndex, timeoutMs, startedAt)
    const attemptSignal = diagnosticAttemptSignal(input.signal, timeoutMs)
    const result = await testOpenAIAccount(account, {
      ...input,
      model,
      signal: attemptSignal,
      gatewaySettingsOverride: diagnosticAccountTestGatewaySettingsOverride(input.gatewaySettingsOverride, timeoutMs)
    })
    lastResult = result
    if (result.success || result.accountFailureEligible === false || input.signal?.aborted) {
      return accountTestResultWithTotalDuration(result, startedAt)
    }
    if (attemptIndex + 1 < accountDiagnosticRetryTimeoutMs.length) {
      logger.debug({
        event: 'account_diagnostic_test_retry_scheduled',
        accountId: account.id,
        accountName: account.name,
        attemptNumber: attemptIndex + 1,
        nextAttemptNumber: attemptIndex + 2,
        attemptTimeoutMs: timeoutMs,
        nextAttemptTimeoutMs: accountDiagnosticRetryTimeoutMs[attemptIndex + 1],
        durationMs: result.durationMs,
        totalElapsedMs: Date.now() - startedAt,
        traceId: result.traceId
      }, '账户诊断请求未通过，将继续使用真实网关链路重试')
    }
  }
  return accountTestResultWithTotalDuration(lastResult ?? await testOpenAIAccount(account, { ...input, model }), startedAt)
}

function notifyDiagnosticAttemptProgress(
  handler: AccountDiagnosticAttemptProgressHandler | undefined,
  attemptIndex: number,
  timeoutMs: number,
  startedAt: number
): void {
  if (!handler) return
  try {
    handler(accountDiagnosticAttemptProgress(attemptIndex, timeoutMs, startedAt))
  } catch (error) {
    logger.warn(errorLogFields(error, { event: 'account_diagnostic_attempt_progress_callback_failed' }), '账户诊断进度回调执行失败')
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
  let testRequest: ReturnType<typeof createAnthropicTestRequest> | ReturnType<typeof createGeminiTestRequest> | ReturnType<typeof createOpenAITestRequest> | undefined
  let requestBody: Record<string, unknown> | undefined
  let requestUrl: string | undefined
  let modelMapping: ResolvedOpenAIModelMapping | undefined
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
  const modelsUrl = accountTestModelsPath
  const traceId = createTraceId()

  try {
    const supportedEndpointModes = accountManualTestEndpointModes(account)
    testEndpointMode = resolveAccountTestEndpointMode(supportedEndpointModes, input.testEndpointMode)
    clientCompatibility = accountTestClientCompatibility(account, testEndpointMode, accountClientCompatibility)
    const resolved = await resolveAccountTestCandidate(account, {
      groupId: stringValue(input.groupId),
      systemAccountId: stringValue(input.systemAccountId),
      clientCompatibility,
      candidateAccount: input.candidateAccount,
      findOpenAIAccountForGroup: input.findOpenAIAccountForGroup
    })
    const model = await resolveAccountTestModelAsync(account, {
      explicitModel,
      systemAccountId: input.systemAccountId,
      sourceFamilies: [accountTestEndpointModeSourceFamily(testEndpointMode)]
    })
    const messagesTestMode = isMessagesTestEndpointMode(testEndpointMode)
    const geminiTestMode = isGeminiTestEndpointMode(testEndpointMode)
    testRequest = messagesTestMode
      ? createAnthropicTestRequest({
        explicitModel,
        fallbackModel: model,
        prompt,
        supportedEndpointModes,
        testEndpointMode
      })
      : geminiTestMode
        ? createGeminiTestRequest({
          explicitModel,
          fallbackModel: model,
          prompt,
          testEndpointMode
        })
        : createOpenAITestRequest({
          explicitModel,
          fallbackModel: model,
          prompt,
          isOAuth: account.type === 'oauth',
          clientCompatibility,
          testEndpointMode
        })
    requestBody = testRequest.body
    const requestBodyText = JSON.stringify(requestBody)
    requestUrl = testRequest.path
    const request = createGatewayTestRequest(requestUrl, requestBody, requestBodyText, account.type === 'oauth', input.signal, clientCompatibility, testRequest.headers)
    const diagnosticCandidate = explicitModel
      ? {
          ...resolved.account,
          supportedModels: normalizedAccountTestModels([
            ...(resolved.account.supportedModels ?? []),
            explicitModel
          ])
        }
      : resolved.account
    modelMapping = resolveOpenAIRequestModelMapping(request, diagnosticCandidate)
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
      onUpstreamAttemptDiagnostic: (lastAttempt) => {
        diagnosticLastAttempt = lastAttempt
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
    const responseText = response.bodyText()
    const diagnosticAttemptText = diagnosticLastAttempt?.responseBodyText?.trim() ?? ''
    const upstreamMessage = messagesTestMode
      ? parseAnthropicUpstreamMessage(diagnosticAttemptText) ?? parseAnthropicUpstreamMessage(responseText) ?? parseOpenAIUpstreamMessage(diagnosticAttemptText, { rawFallback: true }) ?? parseOpenAIUpstreamMessage(responseText, { rawFallback: true })
      : geminiTestMode
        ? parseGeminiUpstreamMessage(diagnosticAttemptText) ?? parseGeminiUpstreamMessage(responseText) ?? parseOpenAIUpstreamMessage(diagnosticAttemptText, { rawFallback: true }) ?? parseOpenAIUpstreamMessage(responseText, { rawFallback: true })
      : parseOpenAIUpstreamMessage(diagnosticAttemptText, { rawFallback: true }) ?? parseOpenAIUpstreamMessage(responseText, { rawFallback: true })
    const upstreamErrorCode = parseUpstreamErrorCode(diagnosticAttemptText) ?? parseUpstreamErrorCode(responseText)
    const streamFailureMessage = messagesTestMode
      ? parseAnthropicStreamFailureMessage(responseText) ?? parseOpenAIStreamFailureMessage(responseText)
      : geminiTestMode
        ? parseGeminiStreamFailureMessage(responseText) ?? parseOpenAIStreamFailureMessage(responseText)
      : parseOpenAIStreamFailureMessage(responseText)
    const outputText = messagesTestMode
      ? extractAnthropicResponseOutputText(responseText)
      : geminiTestMode
        ? extractGeminiResponseOutputText(responseText)
      : extractOpenAIResponseOutputText(responseText)
    const success = response.statusCode >= 200 && response.statusCode < 300 && !streamFailureMessage
    const diagnosticStatusCode = accountTestDiagnosticStatusCode(response.statusCode, success, diagnosticLastAttempt)
    const responseTruncated = response.bodyTruncated()
    const proxyFailureMessage = !success && finalAccount.proxyProfileUnavailable ? finalAccount.proxyProfileErrorMessage : undefined
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
      errorCode: success ? undefined : upstreamErrorCode,
      message: success
        ? accountTestSuccessMessage(account, responseTruncated, requestUrl)
        : proxyFailureMessage || upstreamMessage || streamFailureMessage || accountTestHttpFailureMessage(diagnosticStatusCode, response.statusCode),
      model: testRequest?.model,
      ...accountTestModelMappingFields(modelMapping),
      testEndpointMode,
      requestUrl,
      requestBody,
      responseHeaders: response.headersObject(),
      responseBody: parseOpenAIJsonBody(responseText),
      responseText,
      responseTruncated,
      outputText,
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
            errorCode: upstreamErrorCode,
            message: proxyFailureMessage || upstreamMessage || streamFailureMessage
          })
    }), limitedDiagnostics)
  } catch (error) {
    const normalizedError = input.signal?.aborted ? accountTestAbortError(input.signal) : error
    const message = normalizedError instanceof Error ? normalizedError.message : accountTestFailureMessage(account, requestUrl)
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
      message,
      model: testRequest?.model,
      ...accountTestModelMappingFields(modelMapping),
      testEndpointMode,
      requestUrl,
      requestBody,
      responseText: message,
      modelsUrl,
      proxyUrl: account.proxyProfileId ? '[configured]' : undefined,
      durationMs: Date.now() - startedAt,
      accountStatusChanged: false,
      accountStatus: account.status,
      accountFailureEligible
    }), limitedDiagnostics)
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
  return mode === 'generate_content_json' || mode === 'generate_content_sse'
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
    responseText: result.success ? undefined : message,
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
  if (typeof result.statusCode === 'number') {
    return `账户测试未通过，上游返回 HTTP ${result.statusCode}；请联系授权人或管理员查看完整诊断`
  }
  return '账户测试未通过；请联系授权人或管理员查看完整诊断'
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
  constructor(message: string, readonly accountFailureEligible: boolean) {
    super(message)
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

function parseUpstreamErrorCode(bodyText: string): string | undefined {
  if (!bodyText) return undefined
  try {
    const payload = JSON.parse(bodyText) as Record<string, unknown>
    const error = typeof payload.error === 'object' && payload.error !== null
      ? payload.error as Record<string, unknown>
      : payload
    const code = stringValue(error.code)
    if (code) return code
    const type = stringValue(error.type)
    return type || undefined
  } catch {
    return undefined
  }
}

function parseGeminiUpstreamMessage(bodyText: string): string | undefined {
  for (const payload of parseGeminiPayloads(bodyText)) {
    const error = objectValue(payload.error)
    const message = stringValue(error?.message) || stringValue(payload.message)
    if (message) return message
  }
  return undefined
}

function parseGeminiStreamFailureMessage(bodyText: string): string | undefined {
  return parseGeminiUpstreamMessage(bodyText)
}

function extractGeminiResponseOutputText(bodyText: string): string | undefined {
  const parts = parseGeminiPayloads(bodyText)
    .flatMap((payload) => geminiCandidateTexts(payload))
    .map((text) => text.trim())
    .filter(Boolean)
  return parts.length ? parts.join('') : undefined
}

function parseGeminiPayloads(bodyText: string): Record<string, unknown>[] {
  const text = bodyText.trim()
  if (!text) return []
  const direct = parseJsonObject(text)
  if (direct) return [direct]
  const output: Record<string, unknown>[] = []
  for (const line of text.split(/\r?\n/)) {
    const trimmed = line.trim()
    if (!trimmed.startsWith('data:')) continue
    const data = trimmed.slice(5).trim()
    if (!data || data === '[DONE]') continue
    const parsed = parseJsonObject(data)
    if (parsed) output.push(parsed)
  }
  return output
}

function parseJsonObject(text: string): Record<string, unknown> | undefined {
  try {
    const parsed = JSON.parse(text) as unknown
    return objectValue(parsed)
  } catch {
    return undefined
  }
}

function geminiCandidateTexts(payload: Record<string, unknown>): string[] {
  const candidates = Array.isArray(payload.candidates) ? payload.candidates : []
  const output: string[] = []
  for (const candidate of candidates) {
    const content = objectValue(objectValue(candidate)?.content)
    const parts = Array.isArray(content?.parts) ? content.parts : []
    for (const part of parts) {
      const text = stringValue(objectValue(part)?.text)
      if (text) output.push(text)
    }
  }
  return output
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
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

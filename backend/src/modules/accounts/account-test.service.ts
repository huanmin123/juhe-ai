import { normalizeOpenAIAccountClientCompatibility } from '../../domain/account-client-compatibility.js'
import { normalizeOpenAIEndpointModesForRuntime } from '../../domain/openai-endpoint-modes.js'
import { normalizeAnthropicEndpointModesForRuntime } from '../../domain/anthropic-endpoint-modes.js'
import { isAnthropicProtocolProfile } from '../../domain/provider-protocol.js'
import type { AccountClientCompatibility, AccountSummary, AccountTestResult } from '../../domain/types.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { createTraceId, withRequestContext, type RequestContext } from '../../shared/request-context.js'
import {
  findProviderDefaultTestModel,
  findAccountForTest,
  findOpenAIAccountForGroup,
  type RecentOpenAIRequestShape,
  type OpenAIAccountSecret
} from '../../storage/repositories.js'
import { withRequestAuthContext } from '../auth/request-context.js'
import { handleOpenAIGatewayRequest } from '../gateway/routes.js'
import { sanitizeDiagnosticPayload } from '../gateway/audit/payload-sanitizer.js'
import type { GatewaySettings } from '../gateway/policy/account-error-policy.service.js'
import { flushGatewayAccountSideEffects } from '../gateway/runtime/account-side-effects.service.js'
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
  createOpenAITestRequest
} from './account-test-request.js'

type AccountTestInput = {
  model?: string
  prompt?: string
  signal?: AbortSignal
  groupId?: string
  systemAccountId?: string
  requestShape?: RecentOpenAIRequestShape
  diagnostics?: 'full' | 'limited'
  trafficSource?: OpenAIGatewayTrafficSource
  gatewaySettingsOverride?: Partial<GatewaySettings>
  disableAccountStateMutation?: boolean
  clientCompatibility?: AccountClientCompatibility
  candidateAccount?: OpenAIAccountSecret
  onDiagnosticAttemptProgress?: AccountDiagnosticAttemptProgressHandler
}

export async function testOpenAIAccountWithDiagnosticRetries(
  account: AccountSummary,
  input: AccountTestInput = {}
): Promise<AccountTestResult> {
  const startedAt = Date.now()
  let lastResult: AccountTestResult | undefined
  for (let attemptIndex = 0; attemptIndex < accountDiagnosticRetryTimeoutMs.length; attemptIndex += 1) {
    const timeoutMs = accountDiagnosticRetryTimeoutMs[attemptIndex] ?? accountDiagnosticRetryTimeoutMs[accountDiagnosticRetryTimeoutMs.length - 1]
    notifyDiagnosticAttemptProgress(input.onDiagnosticAttemptProgress, attemptIndex, timeoutMs, startedAt)
    const attemptSignal = diagnosticAttemptSignal(input.signal, timeoutMs)
    const result = await testOpenAIAccount(account, {
      ...input,
      signal: attemptSignal,
      gatewaySettingsOverride: diagnosticAccountTestGatewaySettingsOverride(input.gatewaySettingsOverride, timeoutMs)
    })
    lastResult = result
    if (result.success || result.accountFailureEligible === false || input.signal?.aborted) {
      return accountTestResultWithTotalDuration(result, startedAt)
    }
    if (attemptIndex + 1 < accountDiagnosticRetryTimeoutMs.length) {
      logger.info({
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
  return accountTestResultWithTotalDuration(lastResult ?? await testOpenAIAccount(account, input), startedAt)
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
  const model = explicitModel || defaultAccountTestModel(account)
  const prompt = stringValue(input.prompt) || accountTestDefaultPrompt
  const startedAt = Date.now()
  const limitedDiagnostics = input.diagnostics === 'limited'
  const anthropicProtocol = isAnthropicProtocolProfile(account)
  // Anthropic 账户不使用 OpenAI 的 clientCompatibility 规范化，避免写入无意义的 OpenAI 格式值
  const accountClientCompatibility = anthropicProtocol
    ? 'openai_standard' as const
    : normalizeOpenAIAccountClientCompatibility(
        account.providerCode,
        account.type,
        account.clientCompatibility,
        account.clientCompatibility,
        account
      )
  const clientCompatibility = anthropicProtocol
    ? 'openai_standard' as const
    : normalizeOpenAIAccountClientCompatibility(
        account.providerCode,
        account.type,
        input.clientCompatibility ?? accountClientCompatibility,
        accountClientCompatibility,
        account
      )
  // Anthropic 账户直接规范化 Anthropic 端点模式；OpenAI 账户规范化 OpenAI 端点模式
  const gatewaySupportedEndpointModes = anthropicProtocol
    ? normalizeAnthropicEndpointModesForRuntime(account.credentials.supported_endpoint_modes, {
      providerCode: account.providerCode,
      accountType: account.type,
      protocolCode: account.protocolCode,
      protocolVersion: account.protocolVersion
    })
    : normalizeOpenAIEndpointModesForRuntime(account.credentials.supported_endpoint_modes, {
      providerCode: account.providerCode,
      accountType: account.type,
      clientCompatibility
    })
  const testRequest = anthropicProtocol
    ? createAnthropicTestRequest({
      explicitModel,
      fallbackModel: model,
      prompt,
      supportedEndpointModes: gatewaySupportedEndpointModes
    })
    : createOpenAITestRequest({
      explicitModel,
      fallbackModel: model,
      prompt,
      isOAuth: account.type === 'oauth',
      clientCompatibility,
      supportedEndpointModes: gatewaySupportedEndpointModes,
      requestShape: input.requestShape
    })
  const requestBody = testRequest.body
  const requestBodyText = JSON.stringify(requestBody)
  const requestUrl = testRequest.path
  const modelsUrl = accountTestModelsPath
  const traceId = createTraceId()

  try {
    const resolved = resolveAccountTestCandidate(account, {
      groupId: stringValue(input.groupId),
      systemAccountId: stringValue(input.systemAccountId),
      clientCompatibility,
      candidateAccount: input.candidateAccount
    })
    const request = createGatewayTestRequest(requestUrl, requestBody, requestBodyText, account.type === 'oauth', input.signal)
    const response = new MemoryGatewayResponse(startedAt)
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
      candidateAccounts: [resolved.account],
      disableSessionAffinity: true,
      exposeUpstreamDiagnostics: !limitedDiagnostics,
      trafficSource: input.trafficSource ?? 'manual_account_test',
      settingsOverride: input.gatewaySettingsOverride,
      disableAccountStateMutation: input.disableAccountStateMutation ?? true
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
      : findOpenAIAccountForGroup(resolved.groupId, account.id, resolved.systemAccountId, { ignoreAvailability: true }) ?? resolved.account
    const finalSummary = input.candidateAccount
      ? account
      : findAccountForTest(account.id, { systemAccountId: resolved.systemAccountId, role: 'user' })
    const finalAccountStatus = finalSummary?.status ?? finalAccount.status
    const responseText = response.bodyText()
    const upstreamMessage = anthropicProtocol
      ? parseAnthropicUpstreamMessage(responseText) ?? parseOpenAIUpstreamMessage(responseText, { rawFallback: true })
      : parseOpenAIUpstreamMessage(responseText, { rawFallback: true })
    const upstreamErrorCode = parseUpstreamErrorCode(responseText)
    const streamFailureMessage = anthropicProtocol
      ? parseAnthropicStreamFailureMessage(responseText) ?? parseOpenAIStreamFailureMessage(responseText)
      : parseOpenAIStreamFailureMessage(responseText)
    const outputText = anthropicProtocol
      ? extractAnthropicResponseOutputText(responseText)
      : extractOpenAIResponseOutputText(responseText)
    const success = response.statusCode >= 200 && response.statusCode < 300 && !streamFailureMessage
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
      clientCompatibility: accountClientCompatibility,
      testClientCompatibility: clientCompatibility,
      success,
      statusCode: response.statusCode,
      errorCode: success ? undefined : upstreamErrorCode,
      message: success
        ? accountTestSuccessMessage(anthropicProtocol, responseTruncated)
        : proxyFailureMessage || streamFailureMessage || upstreamMessage || `API 返回 HTTP ${response.statusCode}`,
      model: testRequest.model,
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
      accountFailureEligible: !success
    }), limitedDiagnostics)
  } catch (error) {
    const normalizedError = input.signal?.aborted ? accountTestAbortError(input.signal) : error
    const message = normalizedError instanceof Error ? normalizedError.message : accountTestFailureMessage(anthropicProtocol)
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
      clientCompatibility: accountClientCompatibility,
      testClientCompatibility: clientCompatibility,
      success: false,
      message,
      model: testRequest.model,
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

export function preferredSystemAccountTestModel(account: Pick<AccountSummary, 'providerCode' | 'supportedModels' | 'lastSuccessfulTestModel'>): string {
  return stringValue(account.lastSuccessfulTestModel)
    || findProviderDefaultTestModel(account.providerCode)
    || account.supportedModels?.map((model) => stringValue(model)).find(Boolean)
    || ''
}

function sanitizeAccountTestResult(result: AccountTestResult): AccountTestResult {
  return sanitizeDiagnosticPayload(result)
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
    clientCompatibility: result.clientCompatibility,
    testClientCompatibility: result.testClientCompatibility,
    success: result.success,
    statusCode: result.statusCode,
    errorCode: result.errorCode,
    message,
    model: result.model,
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

function resolveAccountTestCandidate(account: AccountSummary, input: { groupId?: string; systemAccountId?: string; clientCompatibility?: AccountClientCompatibility; candidateAccount?: OpenAIAccountSecret } = {}): {
  systemAccountId: string
  groupId: string
  account: OpenAIAccountSecret
} {
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
  const resolvedCandidate = findOpenAIAccountForGroup(groupId, account.id, systemAccountId, { ignoreAvailability: true })
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

function defaultAccountTestModel(account: AccountSummary): string {
  return findProviderDefaultTestModel(account.providerCode)
    || account.supportedModels?.map((model) => stringValue(model)).find(Boolean)
    || ''
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

function accountTestSuccessMessage(anthropicProtocol: boolean, responseTruncated: boolean): string {
  const protocolName = anthropicProtocol ? 'Anthropic Messages' : 'OpenAI Responses'
  return responseTruncated
    ? `${protocolName} 测试通过（响应体过大，已截断展示）`
    : `${protocolName} 测试通过`
}

function accountTestFailureMessage(anthropicProtocol: boolean): string {
  return anthropicProtocol ? 'Anthropic Messages 测试失败' : 'OpenAI Responses 测试失败'
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function resolvedLogger(traceId: string): RequestContext['logger'] {
  return logger.child({ source: 'account_test', traceId })
}

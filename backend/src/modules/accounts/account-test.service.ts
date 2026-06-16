import { EventEmitter } from 'node:events'
import type { IncomingHttpHeaders } from 'node:http'
import type { Request, Response } from 'express'

import { normalizeOpenAIAccountClientCompatibility } from '../../domain/account-client-compatibility.js'
import type { AccountClientCompatibility, AccountSummary, AccountTestResult } from '../../domain/types.js'
import { BoundedBufferCollector } from '../../shared/bounded-buffer.js'
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
  extractOpenAIResponseOutputText,
  parseOpenAIJsonBody,
  parseOpenAIStreamFailureMessage,
  parseOpenAIUpstreamMessage
} from '../gateway/protocols/openai-v1/response-parsing.js'
import { OpenAIStreamInspector } from '../gateway/protocols/openai-v1/stream-inspection.js'
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
  createOpenAITestRequest
} from './account-test-request.js'

export const accountTestResponsePreviewBytes = 256 * 1024

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
  const accountClientCompatibility = normalizeOpenAIAccountClientCompatibility(
    account.providerCode,
    account.type,
    account.clientCompatibility,
    account.clientCompatibility,
    account
  )
  const clientCompatibility = normalizeOpenAIAccountClientCompatibility(
    account.providerCode,
    account.type,
    input.clientCompatibility ?? accountClientCompatibility,
    accountClientCompatibility,
    account
  )
  const testRequest = createOpenAITestRequest({
    explicitModel,
    fallbackModel: model,
    prompt,
    isOAuth: account.type === 'oauth',
    clientCompatibility,
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
    const upstreamMessage = parseOpenAIUpstreamMessage(responseText, { rawFallback: true })
    const upstreamErrorCode = parseUpstreamErrorCode(responseText)
    const streamFailureMessage = parseOpenAIStreamFailureMessage(responseText)
    const outputText = extractOpenAIResponseOutputText(responseText)
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
        ? responseTruncated ? 'OpenAI Responses 测试通过（响应体过大，已截断展示）' : 'OpenAI Responses 测试通过'
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
    const message = normalizedError instanceof Error ? normalizedError.message : 'OpenAI Responses 测试失败'
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

function createGatewayTestRequest(path: string, body: Record<string, unknown>, rawBodyText: string, isOAuth: boolean, signal?: AbortSignal): Request {
  const stream = body.stream === true
  const headers: IncomingHttpHeaders = {
    accept: stream ? isOAuth ? 'text/event-stream' : 'application/json, text/event-stream' : 'application/json',
    'content-type': 'application/json',
    'content-length': String(Buffer.byteLength(rawBodyText))
  }
  return new MemoryGatewayRequest({
    method: 'POST',
    originalUrl: path,
    path,
    headers,
    body,
    rawBody: Buffer.from(rawBodyText),
    ip: '127.0.0.1',
    signal
  }).asRequest()
}

class MemoryGatewayRequest extends EventEmitter {
  constructor(private readonly input: {
    method: string
    originalUrl: string
    path: string
    headers: IncomingHttpHeaders
    body: Record<string, unknown>
    rawBody: Buffer
    ip: string
    signal?: AbortSignal
  }) {
    super()
    if (this.input.signal?.aborted) {
      queueMicrotask(() => this.emit('aborted'))
    } else {
      this.input.signal?.addEventListener('abort', () => this.emit('aborted'), { once: true })
    }
  }

  get method(): string {
    return this.input.method
  }

  get originalUrl(): string {
    return this.input.originalUrl
  }

  get path(): string {
    return this.input.path
  }

  get headers(): IncomingHttpHeaders {
    return this.input.headers
  }

  get body(): Record<string, unknown> {
    return this.input.body
  }

  get rawBody(): Buffer {
    return this.input.rawBody
  }

  get ip(): string {
    return this.input.ip
  }

  get socket(): { remoteAddress: string } {
    return { remoteAddress: this.input.ip }
  }

  get aborted(): boolean {
    return this.input.signal?.aborted ?? false
  }

  header(name: string): string | undefined {
    const value = this.input.headers[name.toLowerCase()]
    if (Array.isArray(value)) return value.join(', ')
    return typeof value === 'string' ? value : undefined
  }

  asRequest(): Request {
    return this as unknown as Request
  }
}

export class MemoryGatewayResponse extends EventEmitter {
  statusCode = 200
  writableEnded = false
  destroyed = false
  locals: Record<string, unknown> = {}
  private readonly headers = new Map<string, string | string[]>()
  private readonly body = new BoundedBufferCollector(accountTestResponsePreviewBytes)
  private readonly streamInspector = new OpenAIStreamInspector()
  private firstOutputMs: number | undefined

  constructor(private readonly startedAt: number) {
    super()
  }

  status(code: number): this {
    this.statusCode = code
    return this
  }

  setHeader(name: string, value: number | string | readonly string[]): this {
    this.headers.set(name.toLowerCase(), Array.isArray(value) ? value.map(String) : String(value))
    return this
  }

  hasHeader(name: string): boolean {
    return this.headers.has(name.toLowerCase())
  }

  getHeaders(): Record<string, string | string[]> {
    return Object.fromEntries(this.headers.entries())
  }

  json(value: unknown): this {
    if (!this.hasHeader('content-type')) {
      this.setHeader('content-type', 'application/json; charset=utf-8')
    }
    return this.send(Buffer.from(JSON.stringify(value), 'utf8'))
  }

  send(value?: Buffer | string | object): this {
    if (Buffer.isBuffer(value)) {
      this.body.append(value)
    } else if (typeof value === 'string') {
      this.body.append(value)
    } else if (value !== undefined) {
      this.body.append(JSON.stringify(value))
    }
    return this.end()
  }

  write(value: Buffer | string | Uint8Array): boolean {
    const buffer = Buffer.isBuffer(value) ? value : Buffer.from(value)
    this.body.append(buffer)
    const inspection = this.streamInspector.pushChunk(buffer)
    if (this.firstOutputMs === undefined && inspection.outputReceived) {
      this.firstOutputMs = Date.now() - this.startedAt
    }
    return true
  }

  end(value?: Buffer | string | Uint8Array): this {
    if (value !== undefined) {
      this.write(value)
    }
    if (!this.writableEnded) {
      this.writableEnded = true
      this.emit('finish')
      this.emit('close')
    }
    return this
  }

  bodyText(): string {
    return this.body.text({ includeTruncationMarker: true })
  }

  bodyTruncated(): boolean {
    return this.body.truncated
  }

  headersObject(): Record<string, string | string[]> {
    const hiddenHeaders = new Set(['authorization', 'cookie', 'set-cookie', 'proxy-authorization'])
    const output: Record<string, string | string[]> = {}
    for (const [name, value] of this.headers) {
      output[name] = hiddenHeaders.has(name) ? '[redacted]' : value
    }
    return output
  }

  firstTokenMs(): number | undefined {
    return this.firstOutputMs
  }

  asResponse(): Response {
    return this as unknown as Response
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

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function resolvedLogger(traceId: string): RequestContext['logger'] {
  return logger.child({ source: 'account_test', traceId })
}

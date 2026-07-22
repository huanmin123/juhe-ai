import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { createTraceId, withRequestContext, type RequestContext } from '../../shared/request-context.js'
import type { OpenAIAccountSecret } from '../../storage/repositories.js'
import {
  accountDiagnosticRetryTimeoutMs,
  diagnosticAccountTestGatewaySettingsOverride,
  diagnosticAttemptSignal,
  isDiagnosticTimeoutSignal
} from '../accounts/account-diagnostic-retry-policy.js'
import { handleOpenAIGatewayRequest, type OpenAIGatewayRequestIdentity } from '../gateway/routes.js'
import {
  createMemoryGatewayRequest,
  MemoryGatewayResponse
} from '../gateway/testing/memory-gateway-http.js'
import type { UpstreamAttempt } from '../gateway/upstream/attempt.js'
import {
  bounded,
  parseUpstreamMessage,
  throwIfAborted,
} from './model-checks-parsing.js'
import {
  emptyProbeResult,
  type GatewayProbeResult
} from './model-checks-evaluation.js'
import {
  parseModelCheckProbeResponse
} from './model-checks-response-parsing.js'
import type { ModelCheckProbeProtocol } from './model-checks.profiles.js'

const probeMaxAttempts = accountDiagnosticRetryTimeoutMs.length

export type ModelCheckGatewayProbeTarget = {
  identity: OpenAIGatewayRequestIdentity
  candidateAccounts?: OpenAIAccountSecret[]
}

export type GatewayProbeInput = {
  method: 'GET' | 'POST'
  path: string
  itemKey: string
  body?: Record<string, unknown>
  responseProtocol?: ModelCheckProbeProtocol
  requestModel?: string
  expectedModel?: string
  upstreamModel?: string
  modelMappingApplied?: boolean
  modelMappingSource?: string
  sourceEndpointFamily?: string
  upstreamEndpointFamily?: string
}

type GatewayProbeProgressEvent = {
  type: 'probe_started'
  message: string
  itemKey: string
  method: 'GET' | 'POST'
  path: string
} | {
  type: 'probe_completed'
  message: string
  itemKey: string
  traceId: string
  statusCode: number
  success: boolean
  durationMs: number
  requestModel?: string
  expectedModel?: string
  upstreamModel?: string
  modelMappingApplied?: boolean
  modelMappingSource?: string
  sourceEndpointFamily?: string
  upstreamEndpointFamily?: string
  responseModel?: string
  outputPreview?: string
}

type GatewayProbeProgressReporter = (event: GatewayProbeProgressEvent) => void

export interface RunGatewayProbeOptions {
  maxAttempts?: number
}

export async function runGatewayProbe(
  target: ModelCheckGatewayProbeTarget,
  probe: GatewayProbeInput,
  signal?: AbortSignal,
  progress?: GatewayProbeProgressReporter,
  options?: RunGatewayProbeOptions
): Promise<GatewayProbeResult> {
  const startedAt = Date.now()
  const attempts: GatewayProbeResult[] = []
  const maxAttempts = normalizedProbeMaxAttempts(options?.maxAttempts)
  for (let attempt = 1; attempt <= maxAttempts; attempt += 1) {
    if (attempt > 1) {
      await waitForModelCheckProbeAttemptDelay(attempts[attempts.length - 1], signal)
    }
    const timeoutMs = accountDiagnosticRetryTimeoutMs[attempt - 1] ?? accountDiagnosticRetryTimeoutMs[accountDiagnosticRetryTimeoutMs.length - 1]
    const result = await runGatewayProbeAttempt(target, probe, signal, progress, attempt, maxAttempts, timeoutMs)
    attempts.push(result)
    if (result.success || !isRetryableProbeFailure(result) || attempt >= maxAttempts) {
      return attachProbeRetryEvidence(result, attempts, Date.now() - startedAt, maxAttempts)
    }
  }
  return attachProbeRetryEvidence(attempts[attempts.length - 1] ?? emptyProbeResult(), attempts, Date.now() - startedAt, maxAttempts)
}

async function runGatewayProbeAttempt(
  target: ModelCheckGatewayProbeTarget,
  probe: GatewayProbeInput,
  signal: AbortSignal | undefined,
  progress: GatewayProbeProgressReporter | undefined,
  attempt: number,
  maxAttempts: number,
  timeoutMs: number
): Promise<GatewayProbeResult> {
  throwIfAborted(signal)
  const attemptMessage = attempt > 1 ? `（第 ${attempt}/${maxAttempts} 次重试）` : ''
  emitGatewayProbeProgress(progress, {
    type: 'probe_started',
    message: `开始执行探针 ${probe.itemKey}${attemptMessage}`,
    itemKey: probe.itemKey,
    method: probe.method,
    path: probe.path
  })
  const startedAt = Date.now()
  const traceId = createTraceId()
  let lastUpstreamAttempt: UpstreamAttempt | undefined
  const attemptSignal = diagnosticAttemptSignal(signal, timeoutMs)
  const request = createMemoryGatewayRequest({
    method: probe.method,
    path: probe.path,
    body: probe.body,
    signal: attemptSignal
  })
  const response = new MemoryGatewayResponse(startedAt)
  const context: RequestContext = {
    traceId,
    startedAt,
    method: request.method,
    path: request.path,
    originalUrl: request.originalUrl,
    clientIp: request.ip,
    systemAccountId: target.identity.systemAccountId,
    apiKeyId: target.identity.apiKeyId,
    groupId: target.identity.groupId,
    logger: logger.child({ source: 'model_check', traceId, itemKey: probe.itemKey })
  }
  try {
    await withRequestContext(context, () => handleOpenAIGatewayRequest(request, response.asResponse(), {
      identity: target.identity,
      candidateAccounts: target.candidateAccounts,
      disableSessionAffinity: true,
      exposeUpstreamDiagnostics: false,
      disableAccountStateMutation: true,
      settingsOverride: diagnosticAccountTestGatewaySettingsOverride(undefined, timeoutMs),
      onUpstreamAttemptDiagnostic: (attemptDiagnostic) => {
        lastUpstreamAttempt = attemptDiagnostic
      }
    }))
  } catch (error) {
    if (signal?.aborted) throw error
    const responseBodyText = response.bodyText()
    const hasGatewayResponse = response.statusCode !== 200 || Boolean(responseBodyText)
    const responseErrorMessage = hasGatewayResponse ? parseUpstreamMessage(responseBodyText) : undefined
    const statusCode = attemptSignal.aborted
      ? 0
      : probeErrorStatusCode(error) || (hasGatewayResponse ? response.statusCode : 0)
    const message = attemptSignal.aborted
      ? probeAbortMessage(attemptSignal)
      : responseErrorMessage ?? probeErrorMessage(error)
    const result: GatewayProbeResult = {
      traceId,
      statusCode,
      success: false,
      durationMs: Date.now() - startedAt,
      ...probeModelFields(probe),
      bodyText: responseBodyText || message,
      bodyTruncated: hasGatewayResponse ? response.bodyTruncated() : false,
      headers: hasGatewayResponse ? response.headersObject() : {},
      errorMessage: message,
      upstreamStatusCode: upstreamAttemptStatusCode(lastUpstreamAttempt),
      retryAfterMs: upstreamAttemptRetryAfterMs(lastUpstreamAttempt),
      rateLimited: isRateLimitedProbeFailureEvidence(statusCode, responseBodyText, lastUpstreamAttempt)
    }
    emitGatewayProbeProgress(progress, {
      type: 'probe_completed',
      message: `${result.errorMessage ?? `探针执行异常，HTTP ${result.statusCode}`}${attemptMessage}`,
      itemKey: probe.itemKey,
      traceId: result.traceId,
      statusCode: result.statusCode,
      success: result.success,
      durationMs: result.durationMs,
      requestModel: result.requestModel,
      expectedModel: result.expectedModel,
      upstreamModel: result.upstreamModel,
      modelMappingApplied: result.modelMappingApplied,
      modelMappingSource: result.modelMappingSource,
      sourceEndpointFamily: result.sourceEndpointFamily,
      upstreamEndpointFamily: result.upstreamEndpointFamily
    })
    return result
  }
  throwIfAborted(signal)
  if (attemptSignal.aborted) {
    const message = probeAbortMessage(attemptSignal)
    const result: GatewayProbeResult = {
      traceId,
      statusCode: 0,
      success: false,
      durationMs: Date.now() - startedAt,
      ...probeModelFields(probe),
      bodyText: message,
      bodyTruncated: false,
      headers: {},
      errorMessage: message
    }
    emitGatewayProbeProgress(progress, {
      type: 'probe_completed',
      message: `${message}${attemptMessage}`,
      itemKey: probe.itemKey,
      traceId: result.traceId,
      statusCode: result.statusCode,
      success: result.success,
      durationMs: result.durationMs,
      requestModel: result.requestModel,
      expectedModel: result.expectedModel,
      upstreamModel: result.upstreamModel,
      modelMappingApplied: result.modelMappingApplied,
      modelMappingSource: result.modelMappingSource,
      sourceEndpointFamily: result.sourceEndpointFamily,
      upstreamEndpointFamily: result.upstreamEndpointFamily
    })
    return result
  }
  const bodyText = response.bodyText()
  const parsed = parseModelCheckProbeResponse({
    bodyText,
    protocol: probe.responseProtocol ?? 'openai_responses',
    path: probe.path
  })
  const result: GatewayProbeResult = {
    traceId,
    statusCode: response.statusCode,
    success: response.statusCode >= 200 && response.statusCode < 300 && !parsed.streamFailureMessage,
    durationMs: Date.now() - startedAt,
    ...probeModelFields(probe),
    firstTokenMs: response.firstTokenMs(),
    bodyText,
    bodyTruncated: response.bodyTruncated(),
    headers: response.headersObject(),
    json: parsed.json,
    outputText: parsed.outputText,
    model: parsed.model,
    usage: parsed.usage,
    systemFingerprint: parsed.systemFingerprint,
    errorMessage: parsed.errorMessage ?? parseUpstreamMessage(bodyText),
    upstreamStatusCode: upstreamAttemptStatusCode(lastUpstreamAttempt),
    retryAfterMs: upstreamAttemptRetryAfterMs(lastUpstreamAttempt),
    rateLimited: isRateLimitedProbeFailureEvidence(response.statusCode, bodyText, lastUpstreamAttempt)
  }
  emitGatewayProbeProgress(progress, {
    type: 'probe_completed',
    message: result.success ? `探针响应完成${attemptMessage}` : `${result.errorMessage ?? `探针响应异常，HTTP ${result.statusCode}`}${attemptMessage}`,
    itemKey: probe.itemKey,
    traceId: result.traceId,
    statusCode: result.statusCode,
    success: result.success,
    durationMs: result.durationMs,
    requestModel: result.requestModel,
    expectedModel: result.expectedModel,
    upstreamModel: result.upstreamModel,
    modelMappingApplied: result.modelMappingApplied,
    modelMappingSource: result.modelMappingSource,
    sourceEndpointFamily: result.sourceEndpointFamily,
    upstreamEndpointFamily: result.upstreamEndpointFamily,
    responseModel: result.model,
    outputPreview: bounded(result.outputText)
  })
  return result
}

function probeModelFields(probe: GatewayProbeInput): Pick<GatewayProbeResult, 'requestModel' | 'expectedModel' | 'upstreamModel' | 'modelMappingApplied' | 'modelMappingSource' | 'sourceEndpointFamily' | 'upstreamEndpointFamily'> {
  return {
    requestModel: probe.requestModel,
    expectedModel: probe.expectedModel,
    upstreamModel: probe.upstreamModel,
    modelMappingApplied: probe.modelMappingApplied,
    modelMappingSource: probe.modelMappingSource,
    sourceEndpointFamily: probe.sourceEndpointFamily,
    upstreamEndpointFamily: probe.upstreamEndpointFamily
  }
}

function isRetryableProbeFailure(result: GatewayProbeResult): boolean {
  return !result.success
}

function probeErrorStatusCode(error: unknown): number {
  const statusCode = (error as { statusCode?: unknown })?.statusCode
  return typeof statusCode === 'number' && Number.isFinite(statusCode) ? Math.trunc(statusCode) : 0
}

function probeErrorMessage(error: unknown): string {
  return error instanceof Error ? error.message : '网关探针执行异常'
}

function probeAbortMessage(signal: AbortSignal): string {
  return isDiagnosticTimeoutSignal(signal) ? '模型检测探针超时' : '模型检测已取消'
}

function attachProbeRetryEvidence(result: GatewayProbeResult, attempts: GatewayProbeResult[], durationMs: number, maxAttempts: number): GatewayProbeResult {
  if (attempts.length <= 1) return result
  const retryableFailureCount = attempts.filter((attempt) => isRetryableProbeFailure(attempt)).length
  return {
    ...result,
    durationMs,
    attemptCount: attempts.length,
    retryAttemptCount: attempts.length - 1,
    retryMaxAttempts: maxAttempts,
    retryableFailureCount,
    attemptTraceIds: attempts.map((attempt) => attempt.traceId),
    attemptStatusCodes: attempts.map((attempt) => attempt.statusCode),
    attemptUpstreamStatusCodes: attempts.map((attempt) => attempt.upstreamStatusCode ?? attempt.statusCode),
    attemptRetryAfterMs: attempts.map((attempt) => attempt.retryAfterMs ?? 0),
    attemptMessages: attempts.map((attempt) => attempt.success ? 'success' : attempt.errorMessage ?? `HTTP ${attempt.statusCode}`)
  }
}

function normalizedProbeMaxAttempts(value: number | undefined): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) return probeMaxAttempts
  return Math.max(1, Math.min(probeMaxAttempts, Math.trunc(value)))
}

async function waitForModelCheckProbeAttemptDelay(previous: GatewayProbeResult | undefined, signal?: AbortSignal): Promise<void> {
  if (!previous) return
  const delayMs = modelCheckProbeAttemptDelayMs(previous)
  if (delayMs <= 0) return
  await waitForAbortableDelay(delayMs, signal)
}

function modelCheckProbeAttemptDelayMs(previous: GatewayProbeResult): number {
  const retryAfterMs = previous.retryAfterMs ?? 0
  const configuredDelayMs = runtimeConfig.modelCheck.probeRetryDelayMs
  return Math.max(0, Math.max(Math.trunc(configuredDelayMs), Math.trunc(retryAfterMs)))
}

function upstreamAttemptStatusCode(attempt: UpstreamAttempt | undefined): number | undefined {
  return typeof attempt?.status === 'number' && Number.isFinite(attempt.status) ? Math.trunc(attempt.status) : undefined
}

function upstreamAttemptRetryAfterMs(attempt: UpstreamAttempt | undefined): number | undefined {
  const value = headerValue(attempt?.responseHeaders, 'retry-after')
  if (!value) return undefined
  const numeric = Number(value)
  if (Number.isFinite(numeric) && numeric >= 0) {
    return Math.trunc(numeric * 1000)
  }
  const timestamp = Date.parse(value)
  if (Number.isFinite(timestamp)) {
    return Math.max(0, timestamp - Date.now())
  }
  return undefined
}

function headerValue(headers: Record<string, string> | undefined, name: string): string | undefined {
  if (!headers) return undefined
  const wanted = name.toLowerCase()
  for (const [key, value] of Object.entries(headers)) {
    if (key.toLowerCase() === wanted) return value
  }
  return undefined
}

function isRateLimitedProbeFailureEvidence(statusCode: number, bodyText: string, attempt: UpstreamAttempt | undefined): boolean {
  const upstreamStatusCode = upstreamAttemptStatusCode(attempt)
  if (statusCode === 429 || upstreamStatusCode === 429) return true
  const text = `${bodyText}\n${attempt?.message ?? ''}\n${attempt?.responseBodyText ?? ''}`.toLowerCase()
  return text.includes('rate_limit') || text.includes('rate limit') || text.includes('requests-per-minute')
}

async function waitForAbortableDelay(delayMs: number, signal?: AbortSignal): Promise<void> {
  throwIfAborted(signal)
  await new Promise<void>((resolve, reject) => {
    const timeout = setTimeout(() => {
      cleanup()
      resolve()
    }, Math.max(1, Math.trunc(delayMs)))
    timeout.unref()
    const abortListener = () => {
      cleanup()
      reject(signal?.reason instanceof Error ? signal.reason : new Error('模型检测已取消'))
    }
    const cleanup = () => {
      clearTimeout(timeout)
      signal?.removeEventListener('abort', abortListener)
    }
    signal?.addEventListener('abort', abortListener, { once: true })
    if (signal?.aborted) {
      abortListener()
    }
  })
}

function emitGatewayProbeProgress(progress: GatewayProbeProgressReporter | undefined, event: GatewayProbeProgressEvent): void {
  if (!progress) return
  try {
    progress(event)
  } catch (error) {
    logger.warn({ event: 'model_check_progress_emit_failed', err: error }, '模型检测进度事件发送失败')
  }
}

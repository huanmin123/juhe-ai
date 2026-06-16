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
import {
  bounded,
  extractOpenAIResponseOutputText,
  modelFromSse,
  parseJsonRecord,
  parseOpenAIStreamFailureMessage,
  parseUpstreamMessage,
  recordValue,
  textValue,
  throwIfAborted,
  usageFromSse
} from './model-checks-parsing.js'
import {
  emptyProbeResult,
  type GatewayProbeResult
} from './model-checks-evaluation.js'

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
  responseModel?: string
  outputPreview?: string
}

type GatewayProbeProgressReporter = (event: GatewayProbeProgressEvent) => void

export async function runGatewayProbe(
  target: ModelCheckGatewayProbeTarget,
  probe: GatewayProbeInput,
  signal?: AbortSignal,
  progress?: GatewayProbeProgressReporter
): Promise<GatewayProbeResult> {
  const startedAt = Date.now()
  const attempts: GatewayProbeResult[] = []
  for (let attempt = 1; attempt <= probeMaxAttempts; attempt += 1) {
    const timeoutMs = accountDiagnosticRetryTimeoutMs[attempt - 1] ?? accountDiagnosticRetryTimeoutMs[accountDiagnosticRetryTimeoutMs.length - 1]
    const result = await runGatewayProbeAttempt(target, probe, signal, progress, attempt, timeoutMs)
    attempts.push(result)
    if (result.success || !isRetryableProbeFailure(result) || attempt >= probeMaxAttempts) {
      return attachProbeRetryEvidence(result, attempts, Date.now() - startedAt)
    }
  }
  return attachProbeRetryEvidence(attempts[attempts.length - 1] ?? emptyProbeResult(), attempts, Date.now() - startedAt)
}

async function runGatewayProbeAttempt(
  target: ModelCheckGatewayProbeTarget,
  probe: GatewayProbeInput,
  signal: AbortSignal | undefined,
  progress: GatewayProbeProgressReporter | undefined,
  attempt: number,
  timeoutMs: number
): Promise<GatewayProbeResult> {
  throwIfAborted(signal)
  const attemptMessage = attempt > 1 ? `（第 ${attempt}/${probeMaxAttempts} 次重试）` : ''
  emitGatewayProbeProgress(progress, {
    type: 'probe_started',
    message: `开始执行探针 ${probe.itemKey}${attemptMessage}`,
    itemKey: probe.itemKey,
    method: probe.method,
    path: probe.path
  })
  const startedAt = Date.now()
  const traceId = createTraceId()
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
      settingsOverride: diagnosticAccountTestGatewaySettingsOverride(undefined, timeoutMs)
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
      bodyText: responseBodyText || message,
      bodyTruncated: hasGatewayResponse ? response.bodyTruncated() : false,
      headers: hasGatewayResponse ? response.headersObject() : {},
      errorMessage: message
    }
    emitGatewayProbeProgress(progress, {
      type: 'probe_completed',
      message: `${result.errorMessage ?? `探针执行异常，HTTP ${result.statusCode}`}${attemptMessage}`,
      itemKey: probe.itemKey,
      traceId: result.traceId,
      statusCode: result.statusCode,
      success: result.success,
      durationMs: result.durationMs
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
      durationMs: result.durationMs
    })
    return result
  }
  const bodyText = response.bodyText()
  const json = parseJsonRecord(bodyText)
  const outputText = extractOpenAIResponseOutputText(bodyText)
  const result = {
    traceId,
    statusCode: response.statusCode,
    success: response.statusCode >= 200 && response.statusCode < 300 && !parseOpenAIStreamFailureMessage(bodyText),
    durationMs: Date.now() - startedAt,
    firstTokenMs: response.firstTokenMs(),
    bodyText,
    bodyTruncated: response.bodyTruncated(),
    headers: response.headersObject(),
    json,
    outputText,
    model: textValue(json?.model) ?? modelFromSse(bodyText),
    usage: recordValue(json?.usage) ?? usageFromSse(bodyText),
    errorMessage: parseUpstreamMessage(bodyText)
  }
  emitGatewayProbeProgress(progress, {
    type: 'probe_completed',
    message: result.success ? `探针响应完成${attemptMessage}` : `${result.errorMessage ?? `探针响应异常，HTTP ${result.statusCode}`}${attemptMessage}`,
    itemKey: probe.itemKey,
    traceId: result.traceId,
    statusCode: result.statusCode,
    success: result.success,
    durationMs: result.durationMs,
    responseModel: result.model,
    outputPreview: bounded(result.outputText)
  })
  return result
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

function attachProbeRetryEvidence(result: GatewayProbeResult, attempts: GatewayProbeResult[], durationMs: number): GatewayProbeResult {
  if (attempts.length <= 1) return result
  const retryableFailureCount = attempts.filter((attempt) => isRetryableProbeFailure(attempt)).length
  return {
    ...result,
    durationMs,
    attemptCount: attempts.length,
    retryAttemptCount: attempts.length - 1,
    retryMaxAttempts: probeMaxAttempts,
    retryableFailureCount,
    attemptTraceIds: attempts.map((attempt) => attempt.traceId),
    attemptStatusCodes: attempts.map((attempt) => attempt.statusCode),
    attemptMessages: attempts.map((attempt) => attempt.success ? 'success' : attempt.errorMessage ?? `HTTP ${attempt.statusCode}`)
  }
}

function emitGatewayProbeProgress(progress: GatewayProbeProgressReporter | undefined, event: GatewayProbeProgressEvent): void {
  if (!progress) return
  try {
    progress(event)
  } catch (error) {
    logger.warn({ event: 'model_check_progress_emit_failed', err: error }, '模型检测进度事件发送失败')
  }
}

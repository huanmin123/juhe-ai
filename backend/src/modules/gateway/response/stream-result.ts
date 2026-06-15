import {
  OpenAIStreamInspector,
  type OpenAIStreamInspection
} from '../protocols/openai-v1/stream-inspection.js'
import {
  LimitedBufferCapture
} from '../upstream/body.js'
import type { ParsedUsage } from '../usage/types.js'
import type { ResponseInspectionDecision } from './inspection.js'

export interface StreamPipeResult {
  completed: boolean
  message: string
  errorCode?: string
  firstTokenMs?: number
  usage: ParsedUsage
  outputReceived: boolean
  imageOutputReceived: boolean
  estimatedOutputTokens?: number
  responseBodyText?: string
  auditResponseBody?: Buffer
  auditUpstreamBody?: Buffer
  downstreamBytesWritten: number
  uncommittedResponseBody?: Buffer
  responseInspection?: ResponseInspectionDecision
  responseInspectionObservations?: ResponseInspectionDecision[]
  responseInspectionObservationOmittedCount?: number
  bodyOmission?: StreamBodyOmissionSummary
}

export interface StreamBodyOmissionSummary {
  omitted: true
  reason: 'image_stream_payload'
  message: string
  totalUpstreamBytes: number
  totalResponseBytes: number
  sseEventCount: number
  lastSseEventType?: string
  recentSseEventTypes: string[]
  imageOutputReceived: boolean
  terminalReceived: boolean
  failedReceived: boolean
}

export function streamResult(
  completed: boolean,
  message: string,
  errorCode: string | undefined,
  firstTokenMs: number | undefined,
  usage: ParsedUsage,
  responseCapture: LimitedBufferCapture,
  upstreamCapture: LimitedBufferCapture,
  diagnosticCapture: LimitedBufferCapture,
  responseInspection?: ResponseInspectionDecision,
  outputReceived = false,
  estimatedOutputTokens?: number,
  imageOutputReceived = false,
  captureSuccessPayloads = true,
  bodyOmission?: StreamBodyOmissionSummary,
  responseInspectionObservations: ResponseInspectionDecision[] = [],
  responseInspectionObservationOmittedCount = 0,
  downstreamBytesWritten = 0,
  uncommittedResponseBody?: Buffer
): StreamPipeResult {
  const responseBodyText = bodyOmission || (completed && !captureSuccessPayloads)
    ? undefined
    : diagnosticCapture.toDiagnosticText()
  const auditResponseBody = bodyOmission
    ? undefined
    : captureSuccessPayloads
      ? responseCapture.completeBuffer()
      : completed ? undefined : diagnosticCapture.completeBuffer()
  return {
    completed,
    message,
    errorCode,
    firstTokenMs,
    usage,
    outputReceived,
    imageOutputReceived,
    estimatedOutputTokens,
    responseBodyText,
    auditResponseBody,
    auditUpstreamBody: auditUpstreamBodyForResult(upstreamCapture, completed, captureSuccessPayloads, bodyOmission),
    downstreamBytesWritten,
    uncommittedResponseBody,
    responseInspection,
    responseInspectionObservations: responseInspectionObservations.length ? [...responseInspectionObservations] : undefined,
    responseInspectionObservationOmittedCount: responseInspectionObservationOmittedCount > 0 ? responseInspectionObservationOmittedCount : undefined,
    bodyOmission
  }
}

export function streamBodyOmissionSummary(
  inspection: OpenAIStreamInspection | ReturnType<OpenAIStreamInspector['snapshot']>,
  totalUpstreamBytes: number,
  totalResponseBytes: number
): StreamBodyOmissionSummary {
  return {
    omitted: true,
    reason: 'image_stream_payload',
    message: '图像流正文已省略，避免在日志和审计中保存图片字节',
    totalUpstreamBytes,
    totalResponseBytes,
    sseEventCount: inspection.eventCount,
    lastSseEventType: inspection.lastEventType,
    recentSseEventTypes: inspection.recentEventTypes,
    imageOutputReceived: inspection.imageOutputReceived,
    terminalReceived: inspection.terminalReceived,
    failedReceived: inspection.failedReceived
  }
}

function auditUpstreamBodyForResult(
  upstreamCapture: LimitedBufferCapture,
  completed: boolean,
  captureSuccessPayloads: boolean,
  bodyOmission?: StreamBodyOmissionSummary
): Buffer | undefined {
  if (bodyOmission) {
    return undefined
  }
  if (captureSuccessPayloads) {
    return upstreamCapture.completeBuffer()
  }
  if (completed) {
    return undefined
  }
  const buffer = upstreamCapture.buffer()
  return buffer.byteLength > 0 ? buffer : undefined
}

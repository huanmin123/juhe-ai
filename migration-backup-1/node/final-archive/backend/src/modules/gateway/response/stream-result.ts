import {
  LimitedBufferCapture
} from '../upstream/body.js'
import type { ParsedUsage } from '../usage/types.js'
import type { ResponseInspectionDecision } from './inspection.js'

export interface StreamPipeResult {
  completed: boolean
  /** True only when the protocol inspector observed a complete, valid frame sequence. */
  protocolValidated: boolean
  message: string
  errorCode?: string
  firstTokenMs?: number
  usage: ParsedUsage
  outputReceived: boolean
  imageOutputReceived: boolean
  estimatedOutputTokens?: number
  responseBodyText?: string
  responseResourceId?: string
  auditResponseBody?: Buffer
  auditUpstreamBody?: Buffer
  downstreamBytesWritten: number
  /** Bytes from this upstream response that were actually forwarded downstream. */
  upstreamResponseBytesWritten: number
  transportCommitted: boolean
  semanticCommitted: boolean
  uncommittedResponseBody?: Buffer
  responseInspection?: ResponseInspectionDecision
  /** An upstream failure terminal deliberately forwarded without semantic handling. */
  passthroughUpstreamFailure?: boolean
  responseInspectionObservations?: ResponseInspectionDecision[]
  responseInspectionObservationOmittedCount?: number
  bodyOmission?: StreamBodyOmissionSummary
  transportFailure?: StreamTransportFailure
  /** Local gateway processing failed before any attributable upstream transport outcome. */
  gatewayLocalFailure?: boolean
}

export interface StreamTransportFailure {
  kind: 'timeout' | 'read_incomplete'
  reason: string
}

export interface StreamBodyOmissionSummary {
  omitted: true
  reason: 'image_stream_payload' | 'image_json_payload'
  message: string
  totalUpstreamBytes: number
  totalResponseBytes: number
  sseEventCount?: number
  lastSseEventType?: string
  recentSseEventTypes?: string[]
  imageOutputReceived: boolean
  terminalReceived?: boolean
  failedReceived?: boolean
}

interface StreamInspectionSummaryInput {
  eventCount: number
  lastEventType?: string
  recentEventTypes: string[]
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
  upstreamResponseBytesWritten = downstreamBytesWritten,
  transportCommitted = downstreamBytesWritten > 0,
  semanticCommitted = outputReceived || imageOutputReceived,
  uncommittedResponseBody?: Buffer,
  responseResourceId?: string,
  protocolValidated = false,
  passthroughUpstreamFailure = false
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
    protocolValidated,
    message,
    errorCode,
    firstTokenMs,
    usage,
    outputReceived,
    imageOutputReceived,
    estimatedOutputTokens,
    responseBodyText,
    responseResourceId,
    auditResponseBody,
    auditUpstreamBody: auditUpstreamBodyForResult(upstreamCapture, completed, captureSuccessPayloads, bodyOmission),
    downstreamBytesWritten,
    upstreamResponseBytesWritten,
    transportCommitted,
    semanticCommitted,
    uncommittedResponseBody,
    responseInspection,
    passthroughUpstreamFailure: passthroughUpstreamFailure || undefined,
    responseInspectionObservations: responseInspectionObservations.length ? [...responseInspectionObservations] : undefined,
    responseInspectionObservationOmittedCount: responseInspectionObservationOmittedCount > 0 ? responseInspectionObservationOmittedCount : undefined,
    bodyOmission
  }
}

export function streamBodyOmissionSummary(
  inspection: StreamInspectionSummaryInput,
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

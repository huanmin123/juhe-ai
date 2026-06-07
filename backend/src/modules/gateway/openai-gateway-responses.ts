import type { Response } from 'express'

export interface GatewayErrorPayload {
  [key: string]: unknown
  error: {
    message: string
    type: string
    code?: string
    [key: string]: unknown
  }
}

export function gatewayErrorPayload(message: string, type: string, code?: string): GatewayErrorPayload {
  return { error: { message, type, ...(code ? { code } : {}) } }
}

export function sendGatewayJsonError(res: Response, statusCode: number, payload: GatewayErrorPayload): void {
  res.status(statusCode).json(payload)
}

export function sendGatewayErrorResponse(res: Response, statusCode: number, payload: GatewayErrorPayload): void {
  if (res.writableEnded || res.destroyed) {
    return
  }
  if (!res.headersSent) {
    sendGatewayJsonError(res, statusCode, payload)
    return
  }
  const contentType = String(res.getHeader('content-type') ?? '')
  if (isOpenAIStreamContentType(contentType)) {
    const failureEvent = writeGatewayStreamFailureEvent(res, payload.error.message)
    if (failureEvent) {
      res.write(failureEvent)
    }
  }
  res.end()
}

export function isOpenAIStreamContentType(contentType: string): boolean {
  return responseMimeType(contentType) === 'text/event-stream'
}

export function isOpenAIJsonResponseContentType(contentType: string): boolean {
  const mimeType = responseMimeType(contentType)
  return mimeType === 'application/json' || mimeType.endsWith('+json')
}

export function isOpenAIBinaryResponseContentType(contentType: string): boolean {
  const mimeType = responseMimeType(contentType)
  return mimeType === 'application/octet-stream'
    || mimeType.startsWith('image/')
    || mimeType.startsWith('audio/')
    || mimeType.startsWith('video/')
    || binaryApplicationMimeTypes.has(mimeType)
}

export function shouldHandleOpenAIUpstreamResponseAsStream(input: {
  contentType: string
  streamRequest: boolean
}): boolean {
  if (isOpenAIStreamContentType(input.contentType)) {
    return true
  }
  if (!input.streamRequest) {
    return false
  }
  if (isOpenAIJsonResponseContentType(input.contentType)) {
    return false
  }
  if (isOpenAIBinaryResponseContentType(input.contentType)) {
    return false
  }
  return true
}

function responseMimeType(contentType: string): string {
  return contentType.split(';', 1)[0]?.trim().toLowerCase() ?? ''
}

const binaryApplicationMimeTypes = new Set([
  'application/pdf',
  'application/zip',
  'application/x-zip-compressed',
  'application/gzip',
  'application/x-gzip',
  'application/x-tar',
  'application/x-7z-compressed'
])

export const gatewayStreamClientRetryErrorCode = 'upstream_retryable_error'

export const gatewayStreamClientRetryMessage = '上游流式响应在输出前失败，请重试'

export function writeGatewayStreamFailureEvent(res: Response, message: string, code?: string): Buffer | undefined {
  return buildGatewayStreamFailureEvent(message, code)
}

export function buildGatewayStreamFailureEvent(message: string, code = gatewayStreamFailureCode(message)): Buffer {
  const payload = {
    type: 'response.failed',
    response: {
      status: 'failed',
      error: {
        code,
        message
      }
    }
  }
  return Buffer.from(`event: response.failed\ndata: ${JSON.stringify(payload)}\n\n`, 'utf8')
}

export function gatewayStreamFailureCode(message: string): string {
  const normalized = message.toLowerCase()
  return normalized.includes('idle timeout')
    || normalized.includes('timeout')
    || message.includes('超时')
    || message.includes('无数据')
    || message.includes('未返回首段数据')
    || message.includes('未返回任何新数据')
    || message.includes('未返回新的有效输出')
    ? 'upstream_stream_idle_timeout'
    : 'upstream_stream_interrupted'
}

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

export interface AnthropicGatewayErrorPayload {
  [key: string]: unknown
  type: 'error'
  error: {
    type: string
    message: string
    code?: string
    [key: string]: unknown
  }
}

export type GatewayClientErrorPayload = GatewayErrorPayload | AnthropicGatewayErrorPayload
export type GatewayErrorProtocol = 'openai' | 'anthropic'

export function gatewayErrorPayload(message: string, type: string, code?: string): GatewayErrorPayload {
  return { error: { message, type, ...(code ? { code } : {}) } }
}

export function gatewayErrorPayloadForProtocol(
  payload: GatewayErrorPayload,
  protocol: GatewayErrorProtocol = 'openai'
): GatewayClientErrorPayload {
  if (protocol !== 'anthropic') {
    return payload
  }
  return {
    type: 'error',
    error: {
      type: anthropicGatewayErrorType(payload),
      message: payload.error.message,
      ...(payload.error.code ? { code: payload.error.code } : {})
    }
  }
}

export function sendGatewayJsonError(
  res: Response,
  statusCode: number,
  payload: GatewayErrorPayload,
  options: { protocol?: GatewayErrorProtocol } = {}
): void {
  res.status(statusCode).json(gatewayErrorPayloadForProtocol(payload, options.protocol))
}

export function sendGatewayErrorResponse(
  res: Response,
  statusCode: number,
  payload: GatewayErrorPayload,
  options: { protocol?: GatewayErrorProtocol } = {}
): void {
  if (res.writableEnded || res.destroyed) {
    return
  }
  if (!res.headersSent) {
    sendGatewayJsonError(res, statusCode, payload, options)
    return
  }
  const contentType = String(res.getHeader('content-type') ?? '')
  if (isOpenAIStreamContentType(contentType)) {
    const failureEvent = options.protocol === 'anthropic'
      ? buildAnthropicGatewayStreamFailureEvent(payload)
      : writeGatewayStreamFailureEvent(res, payload.error.message)
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

export function writeGatewayStreamFailureEvent(
  res: Response,
  message: string,
  code?: string,
  protocol: GatewayErrorProtocol = 'openai'
): Buffer | undefined {
  return buildGatewayStreamFailureEventForProtocol(message, code, protocol)
}

export function buildGatewayStreamFailureEventForProtocol(
  message: string,
  code?: string,
  protocol: GatewayErrorProtocol = 'openai'
): Buffer {
  if (protocol === 'anthropic') {
    return buildAnthropicGatewayStreamFailureEvent(gatewayErrorPayload(message, 'api_error', code))
  }
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

export function buildAnthropicGatewayStreamFailureEvent(payload: GatewayErrorPayload): Buffer {
  const errorPayload = gatewayErrorPayloadForProtocol(payload, 'anthropic')
  return Buffer.from(`event: error\ndata: ${JSON.stringify(errorPayload)}\n\n`, 'utf8')
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

function anthropicGatewayErrorType(payload: GatewayErrorPayload): string {
  const type = payload.error.type
  const code = payload.error.code
  if (type === 'rate_limit_exceeded') return 'rate_limit_error'
  if (type === 'invalid_request_error') return 'invalid_request_error'
  if (type === 'server_overloaded' || code === 'server_overloaded') return 'overloaded_error'
  if (type === 'authentication_error') return 'authentication_error'
  if (type === 'permission_error') return 'permission_error'
  if (type === 'not_found_error') return 'not_found_error'
  if (type === 'billing_error') return 'billing_error'
  return 'api_error'
}

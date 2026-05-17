import type { Response } from 'express'

export interface GatewayErrorPayload {
  [key: string]: unknown
  error: {
    message: string
    type: string
  }
}

export function gatewayErrorPayload(message: string, type: string): GatewayErrorPayload {
  return { error: { message, type } }
}

export function sendGatewayJsonError(res: Response, statusCode: number, payload: GatewayErrorPayload): void {
  res.status(statusCode).json(payload)
}

export function isOpenAIStreamContentType(contentType: string): boolean {
  return contentType.includes('text/event-stream') || contentType.includes('application/octet-stream')
}

export const gatewayStreamClientRetryErrorCode = 'upstream_retryable_error'

export const gatewayStreamClientRetryMessage = 'Upstream returned a retryable stream failure before output. Please retry.'

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
    || message.includes('未形成完整 SSE 事件')
    ? 'upstream_stream_idle_timeout'
    : 'upstream_stream_interrupted'
}

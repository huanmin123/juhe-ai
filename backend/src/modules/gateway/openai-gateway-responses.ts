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

export function writeGatewayStreamFailureEvent(res: Response, message: string): Buffer | undefined {
  if (res.writableEnded || res.destroyed) {
    return undefined
  }

  const payload = {
    type: 'response.failed',
    response: {
      status: 'failed',
      error: {
        code: gatewayStreamFailureCode(message),
        message
      }
    }
  }
  const buffer = Buffer.from(`event: response.failed\ndata: ${JSON.stringify(payload)}\n\n`, 'utf8')
  try {
    res.write(buffer)
    return buffer
  } catch {
    return undefined
  }
}

function gatewayStreamFailureCode(message: string): string {
  return message.toLowerCase().includes('idle timeout')
    ? 'upstream_stream_idle_timeout'
    : 'upstream_stream_interrupted'
}

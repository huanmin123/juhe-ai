import { redactKnownSecrets } from './chat-long-session-runtime.js'

export interface SafeChatStreamFailure {
  type: 'message.failed'
  code: string
  message: string
}

export function extractSafeChatStreamFailure(
  eventName: string,
  data: Record<string, unknown>,
  secrets: readonly (string | undefined)[]
): SafeChatStreamFailure {
  const rawCode = typeof data.code === 'string' ? data.code.trim() : 'gateway_stream_failed'
  const code = rawCode.replace(/[^a-zA-Z0-9_.-]/g, '_').slice(0, 128) || 'gateway_stream_failed'
  const rawMessage = typeof data.message === 'string' ? data.message : '模型请求失败'
  const message = truncateUtf8(redactKnownSecrets(rawMessage, secrets), 2_048)
  return {
    type: eventName === 'message.failed' ? 'message.failed' : 'message.failed',
    code,
    message: message || '模型请求失败'
  }
}

function truncateUtf8(value: string, maxBytes: number): string {
  if (Buffer.byteLength(value, 'utf8') <= maxBytes) return value
  let output = Buffer.from(value, 'utf8').subarray(0, maxBytes).toString('utf8')
  while (Buffer.byteLength(output, 'utf8') > maxBytes) output = output.slice(0, -1)
  return output
}

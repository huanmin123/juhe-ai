export type PublicChatGenerationErrorCode =
  | 'upstream_http_error'
  | 'upstream_stream_failed'
  | 'image_generation_failed'
  | 'stream_interrupted'
  | 'internal_generation_failed'

export interface PublicChatGenerationError {
  code: PublicChatGenerationErrorCode
  message: string
}

const publicMessages: Readonly<Record<PublicChatGenerationErrorCode, string>> = Object.freeze({
  upstream_http_error: '模型服务请求失败，请稍后重试',
  upstream_stream_failed: '模型响应中断，请重新发送',
  image_generation_failed: '图片生成失败，请重新发送',
  stream_interrupted: '生成连接已中断，请重新发送',
  internal_generation_failed: '生成任务异常结束，请重新发送'
})

const networkErrorCodes = new Set([
  'ECONNABORTED',
  'ECONNREFUSED',
  'ECONNRESET',
  'EHOSTUNREACH',
  'ENETUNREACH',
  'EPIPE',
  'ETIMEDOUT',
  'UND_ERR_CONNECT_TIMEOUT',
  'UND_ERR_SOCKET'
])

export function classifyChatGenerationError(
  error: unknown,
  phaseCode?: PublicChatGenerationErrorCode
): PublicChatGenerationError {
  const record = asRecord(error)
  const rawCode = typeof record?.code === 'string' ? record.code.trim() : ''
  const code = phaseCode ?? publicCode(rawCode)
    ?? (networkErrorCodes.has(rawCode.toUpperCase()) ? 'upstream_stream_failed' : undefined)
    ?? (isHttpStatus(record?.status) || isHttpStatus(record?.statusCode) ? 'upstream_http_error' : undefined)
    ?? 'internal_generation_failed'
  return { code, message: publicMessages[code] }
}

export function chatGenerationErrorMessage(code: PublicChatGenerationErrorCode): string {
  return publicMessages[code]
}

function publicCode(value: string): PublicChatGenerationErrorCode | undefined {
  return Object.prototype.hasOwnProperty.call(publicMessages, value)
    ? value as PublicChatGenerationErrorCode
    : undefined
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value !== null && (typeof value === 'object' || typeof value === 'function')
    ? value as Record<string, unknown>
    : undefined
}

function isHttpStatus(value: unknown): boolean {
  return Number.isInteger(value) && Number(value) >= 400 && Number(value) <= 599
}

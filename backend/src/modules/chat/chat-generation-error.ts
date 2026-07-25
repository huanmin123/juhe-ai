export type PublicChatGenerationErrorCode =
  | 'upstream_http_error'
  | 'upstream_stream_failed'
  | 'image_generation_failed'
  | 'image_generation_not_enabled'
  | 'image_generation_permission_denied'
  | 'image_generation_rate_limited'
  | 'image_generation_request_rejected'
  | 'stream_interrupted'
  | 'internal_generation_failed'

export interface PublicChatGenerationError {
  code: PublicChatGenerationErrorCode
  message: string
}

const maxPublicDiagnosticMessageLength = 1_200

const publicMessages: Readonly<Record<PublicChatGenerationErrorCode, string>> = Object.freeze({
  upstream_http_error: '模型服务请求失败，请稍后重试',
  upstream_stream_failed: '模型响应中断，请重新发送',
  image_generation_failed: '图片生成失败，请重新发送',
  image_generation_not_enabled: '图片生成失败：可用上游分组未开通图片生成功能',
  image_generation_permission_denied: '图片生成失败：上游拒绝了图片生成权限',
  image_generation_rate_limited: '图片生成失败：上游请求过于频繁，请稍后重试',
  image_generation_request_rejected: '图片生成失败：上游拒绝了本次图片参数或内容',
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
  const structuredCode = publicCode(rawCode)
  const code = phaseDetailCode(structuredCode, phaseCode) ?? phaseCode ?? structuredCode
    ?? (networkErrorCodes.has(rawCode.toUpperCase()) ? 'upstream_stream_failed' : undefined)
    ?? (isHttpStatus(record?.status) || isHttpStatus(record?.statusCode) ? 'upstream_http_error' : undefined)
    ?? 'internal_generation_failed'
  return { code, message: publicChatDiagnosticMessage(error, publicMessages[code]) }
}

export function chatGenerationErrorMessage(code: PublicChatGenerationErrorCode): string {
  return publicMessages[code]
}

export function publicChatDiagnosticMessage(error: unknown, fallback: string): string {
  const detail = sanitizeChatDiagnosticMessage(extractChatDiagnosticMessage(error))
  if (!detail || detail === fallback) return fallback
  return `${fallback}；详情：${detail}`
}

export function sanitizeChatDiagnosticMessage(value: string | undefined): string | undefined {
  if (!value) return undefined
  const sanitized = value
    .replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, ' ')
    .replace(/\bBearer\s+[A-Za-z0-9._~+/=-]+/gi, 'Bearer [REDACTED]')
    .replace(/\bBasic\s+[A-Za-z0-9+/=]+/gi, 'Basic [REDACTED]')
    .replace(/\bsk-[A-Za-z0-9_-]{8,}\b/g, 'sk-[REDACTED]')
    .replace(/\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b/g, '[JWT REDACTED]')
    .replace(/(["']?(?:authorization|api[_-]?key|access[_-]?token|refresh[_-]?token|cookie|set-cookie|client[_-]?secret)["']?\s*[:=]\s*["']?)([^\s,;"']+)/gi, '$1[REDACTED]')
    .replace(/([?&](?:api[_-]?key|access[_-]?token|refresh[_-]?token|token|key|secret)=)[^&#\s]+/gi, '$1[REDACTED]')
    .replace(/(https?:\/\/)[^\s/@]+:[^\s/@]+@/gi, '$1[REDACTED]@')
    .replace(/https?:\/\/[^\s]+/gi, '[upstream-url]')
    .replace(/\b[A-Za-z]:\\(?:[^\s<>:"|?*]+\\)*[^\s<>:"|?*]*/g, '[server-path]')
    .replace(/\s+/g, ' ')
    .trim()
  if (!sanitized) return undefined
  return sanitized.length <= maxPublicDiagnosticMessageLength
    ? sanitized
    : `${sanitized.slice(0, maxPublicDiagnosticMessageLength - 1)}…`
}

function extractChatDiagnosticMessage(error: unknown): string | undefined {
  if (typeof error === 'string') return error
  const record = asRecord(error)
  const nestedError = asRecord(record?.error)
  const cause = asRecord(record?.cause)
  const values = [
    record?.message,
    nestedError?.message,
    record?.detail,
    cause?.message
  ]
  const messages = values
    .filter((value): value is string => typeof value === 'string' && Boolean(value.trim()))
    .map((value) => value.trim())
  if (messages.length) return [...new Set(messages)].join(' | ')
  const status = isHttpStatus(record?.statusCode) ? record?.statusCode : isHttpStatus(record?.status) ? record?.status : undefined
  return status ? `HTTP ${status}` : undefined
}

function publicCode(value: string): PublicChatGenerationErrorCode | undefined {
  return Object.prototype.hasOwnProperty.call(publicMessages, value)
    ? value as PublicChatGenerationErrorCode
    : undefined
}

function phaseDetailCode(
  code: PublicChatGenerationErrorCode | undefined,
  phaseCode: PublicChatGenerationErrorCode | undefined
): PublicChatGenerationErrorCode | undefined {
  if (!code || !phaseCode) return undefined
  if (code === phaseCode) return code
  return phaseCode === 'image_generation_failed' && code.startsWith('image_generation_') ? code : undefined
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value !== null && (typeof value === 'object' || typeof value === 'function')
    ? value as Record<string, unknown>
    : undefined
}

function isHttpStatus(value: unknown): boolean {
  return Number.isInteger(value) && Number(value) >= 400 && Number(value) <= 599
}

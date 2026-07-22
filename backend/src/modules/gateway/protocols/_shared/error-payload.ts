export function parseJsonObjectErrorPayload(text: string, headers: Headers): {
  payload: Record<string, unknown>
  error: Record<string, unknown>
} | undefined {
  const trimmed = text.trim()
  if (!headers.get('content-type')?.includes('json') && !trimmed.startsWith('{')) return undefined
  try {
    const payload = JSON.parse(trimmed) as unknown
    if (!isRecord(payload)) return undefined
    const error = isRecord(payload.error) ? payload.error : payload
    return { payload, error }
  } catch {
    return undefined
  }
}

export function stringErrorField(value: unknown): string | undefined {
  if (typeof value === 'string') {
    const text = value.trim()
    return text || undefined
  }
  if (typeof value === 'number' || typeof value === 'boolean') {
    return String(value)
  }
  return undefined
}

export function firstErrorFieldText(...values: unknown[]): string | undefined {
  for (const value of values) {
    const text = errorFieldText(value)
    if (text) return text
  }
  return undefined
}

export function nestedErrorObject(value: unknown): Record<string, unknown> | undefined {
  if (!isRecord(value)) return undefined
  for (const key of ['error', 'err', 'detail', 'details', 'data']) {
    const child = value[key]
    if (isRecord(child)) return child
  }
  return undefined
}

function errorFieldText(value: unknown): string | undefined {
  const scalar = stringErrorField(value)
  if (scalar) return scalar
  if (!isRecord(value)) return undefined
  return firstErrorFieldText(
    value.message,
    value.msg,
    value.error_message,
    value.error_description,
    value.detail,
    value.reason,
    value.code,
    value.type
  )
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

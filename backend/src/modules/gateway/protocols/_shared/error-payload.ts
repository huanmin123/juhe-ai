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
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

export function headersToObject(headers: Headers): Record<string, string> {
  const output: Record<string, string> = {}
  headers.forEach((value, name) => {
    output[name] = value
  })
  return output
}

export function headersToSafeObject(headers: Headers): Record<string, string> {
  const output: Record<string, string> = {}
  headers.forEach((value, name) => {
    output[name] = sanitizeHeaderValue(name, value) as string
  })
  return output
}

export function sanitizeHeaderRecord(headers: Record<string, string | string[]>): Record<string, string | string[]> {
  const output: Record<string, string | string[]> = {}
  for (const [name, value] of Object.entries(headers)) {
    output[name] = sanitizeHeaderValue(name, value)
  }
  return output
}

export function sanitizeStringHeaderRecord(headers: Record<string, string>): Record<string, string> {
  const output: Record<string, string> = {}
  for (const [name, value] of Object.entries(headers)) {
    output[name] = sanitizeHeaderValue(name, value) as string
  }
  return output
}

export function sanitizeHeaderValue(name: string, value: string | string[]): string | string[] {
  if (!isSensitiveHeaderName(name)) {
    return value
  }
  return Array.isArray(value) ? value.map(() => '[redacted]') : '[redacted]'
}

export function isSensitiveHeaderName(name: string): boolean {
  const normalized = name.trim().toLowerCase()
  return normalized === 'authorization'
    || normalized === 'proxy-authorization'
    || normalized === 'cookie'
    || normalized === 'set-cookie'
    || normalized === 'x-api-key'
    || normalized === 'api-key'
    || normalized === 'openai-api-key'
    || normalized === 'x-goog-api-key'
    || normalized === 'x-google-api-key'
    || normalized === 'anthropic-api-key'
    || normalized === 'x-anthropic-api-key'
    || normalized === 'x-openai-api-key'
    || normalized.endsWith('-api-key')
    || normalized.endsWith('_api_key')
    || normalized.includes('secret')
    || normalized.includes('token')
    || normalized.includes('credential')
}

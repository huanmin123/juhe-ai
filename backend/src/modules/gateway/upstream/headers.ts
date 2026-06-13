export function headersToObject(headers: Headers): Record<string, string> {
  const output: Record<string, string> = {}
  headers.forEach((value, name) => {
    output[name] = value
  })
  return output
}

export function headersToSafeObject(headers: Headers): Record<string, string> {
  return headersToObject(headers)
}

export function sanitizeHeaderRecord(headers: Record<string, string | string[]>): Record<string, string | string[]> {
  return { ...headers }
}

export function sanitizeStringHeaderRecord(headers: Record<string, string>): Record<string, string> {
  return { ...headers }
}

export function sanitizeHeaderValue(_name: string, value: string | string[]): string | string[] {
  return value
}

export function isSensitiveHeaderName(_name: string): boolean {
  return false
}

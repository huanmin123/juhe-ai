const redacted = '[redacted]'
const maxRecursiveDepth = 8
const maxObjectKeys = 200
const maxArrayItems = 100
const maxJsonBodySanitizeBytes = 4 * 1024 * 1024
const sensitiveAssignmentKeyPattern = [
  'access[_-]?token',
  'api[_-]?key',
  'apikey',
  'authorization',
  'client[_-]?secret',
  'code[_-]?verifier',
  'cookie',
  'credential(?:s)?',
  'id[_-]?token',
  'key',
  'password',
  'proxy[_-]?authorization',
  'refresh[_-]?token',
  'secret',
  'session(?:id)?',
  'set[_-]?cookie',
  'token'
].join('|')
const quotedSensitiveAssignmentPattern = new RegExp(
  `(["'])(${sensitiveAssignmentKeyPattern})\\1(\\s*:\\s*)(["'])(?:\\\\.|(?!\\4)[^\\\\])*\\4`,
  'gi'
)
const bareSensitiveAssignmentPattern = new RegExp(
  `\\b(${sensitiveAssignmentKeyPattern})(\\s*(?:=|:)\\s*)([^\\s&;,)}\\]]+)`,
  'gi'
)

const sensitiveFieldNames = new Set([
  'accesstoken',
  'apikey',
  'authorization',
  'clientsecret',
  'codeverifier',
  'cookie',
  'credential',
  'credentials',
  'idtoken',
  'key',
  'password',
  'proxyauthorization',
  'refreshtoken',
  'secret',
  'session',
  'sessionid',
  'setcookie',
  'token'
])

export function sanitizeDiagnosticPayload<T>(value: T): T {
  return sanitizeValue(value, undefined, 0) as T
}

export function sanitizeAuditPayloadBody(input: {
  body: Buffer | string | undefined
  contentType?: string
  contentEncoding?: string
}): { body: Buffer | string | undefined; redacted: boolean; originalSizeBytes?: number } {
  if (input.body === undefined) {
    return { body: input.body, redacted: false }
  }
  if (!isPlainTextEncoding(input.contentEncoding)) {
    return { body: input.body, redacted: false }
  }

  const bodyBuffer = Buffer.isBuffer(input.body) ? input.body : Buffer.from(input.body, 'utf8')
  const originalSizeBytes = bodyBuffer.byteLength
  if (originalSizeBytes === 0) {
    return { body: input.body, redacted: false }
  }

  const text = bodyBuffer.toString('utf8')
  const contentType = input.contentType?.toLowerCase() ?? ''
  let nextText: string | undefined

  if (isJsonLikeText(text, contentType) && originalSizeBytes <= maxJsonBodySanitizeBytes) {
    nextText = sanitizeJsonText(text)
  } else if (contentType.includes('x-www-form-urlencoded')) {
    nextText = sanitizeFormUrlEncodedText(text)
  }

  nextText = sanitizeSensitiveString(nextText ?? text)
  if (nextText === text) {
    return { body: input.body, redacted: false }
  }

  return {
    body: Buffer.isBuffer(input.body) ? Buffer.from(nextText, 'utf8') : nextText,
    redacted: true,
    originalSizeBytes
  }
}

function sanitizeValue(value: unknown, fieldName: string | undefined, depth: number): unknown {
  if (isSensitiveFieldName(fieldName)) {
    return redacted
  }
  if (value === null || value === undefined) {
    return value
  }
  if (typeof value === 'string') {
    return sanitizeSensitiveString(value)
  }
  if (typeof value !== 'object') {
    return value
  }
  if (depth >= maxRecursiveDepth) {
    return '[truncated]'
  }
  if (Array.isArray(value)) {
    const output = value.slice(0, maxArrayItems).map((item) => sanitizeValue(item, undefined, depth + 1))
    if (value.length > maxArrayItems) output.push(`[truncated:${value.length - maxArrayItems}]`)
    return output
  }

  const output: Record<string, unknown> = {}
  let count = 0
  for (const [key, item] of Object.entries(value as Record<string, unknown>)) {
    if (count >= maxObjectKeys) {
      output.__truncated__ = true
      break
    }
    output[key] = sanitizeValue(item, key, depth + 1)
    count += 1
  }
  return output
}

function sanitizeJsonText(text: string): string | undefined {
  try {
    return JSON.stringify(sanitizeValue(JSON.parse(text) as unknown, undefined, 0))
  } catch {
    return undefined
  }
}

function sanitizeFormUrlEncodedText(text: string): string | undefined {
  try {
    const params = new URLSearchParams(text)
    let changed = false
    for (const key of [...params.keys()]) {
      const values = params.getAll(key)
      params.delete(key)
      for (const value of values) {
        const nextValue = isSensitiveFieldName(key) ? redacted : sanitizeSensitiveString(value)
        if (nextValue !== value) changed = true
        params.append(key, nextValue)
      }
    }
    return changed ? params.toString() : undefined
  } catch {
    return undefined
  }
}

function sanitizeSensitiveString(value: string): string {
  return value
    .replace(/\b([a-z][a-z0-9+.-]*:\/\/)([^/\s?#@]+)@/gi, `$1${redacted}@`)
    .replace(/\bBearer\s+[A-Za-z0-9._~+/=-]{8,}/gi, `Bearer ${redacted}`)
    .replace(/\bsk-[A-Za-z0-9_-]{8,}/g, 'sk-[redacted]')
    .replace(/\bjuis_[A-Za-z0-9_-]{8,}/g, 'juis_[redacted]')
    .replace(quotedSensitiveAssignmentPattern, (_match, keyQuote: string, key: string, separator: string, valueQuote: string) => {
      return `${keyQuote}${key}${keyQuote}${separator}${valueQuote}${redacted}${valueQuote}`
    })
    .replace(bareSensitiveAssignmentPattern, (_match, key: string, separator: string) => {
      return `${key}${separator}${redacted}`
    })
}

function isSensitiveFieldName(name: string | undefined): boolean {
  if (!name) return false
  const normalized = name.trim().toLowerCase().replace(/[^a-z0-9]/g, '')
  return sensitiveFieldNames.has(normalized)
}

function isPlainTextEncoding(contentEncoding?: string): boolean {
  const encoding = contentEncoding?.trim().toLowerCase()
  return !encoding || encoding === 'identity'
}

function isJsonLikeText(text: string, contentType: string): boolean {
  if (contentType.includes('json')) return true
  const firstChar = text.trimStart().charAt(0)
  return firstChar === '{' || firstChar === '['
}

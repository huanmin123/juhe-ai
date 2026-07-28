export function normalizeGrokSsoTokens(value: string): string[] {
  const seen = new Set<string>()
  const output: string[] = []
  for (const item of value.replace(/[\r,]/gu, '\n').split('\n')) {
    const token = normalizeGrokSsoToken(item)
    if (!token || seen.has(token)) continue
    seen.add(token)
    output.push(token)
  }
  return output
}

export function normalizeGrokSsoToken(value: string): string {
  let normalized = value.trim()
  if (normalized.toLowerCase().startsWith('cookie:')) {
    normalized = normalized.slice('cookie:'.length).trim()
  }
  for (const part of normalized.split(';')) {
    const separator = part.indexOf('=')
    if (separator < 0) continue
    const name = part.slice(0, separator).trim().toLowerCase()
    if (name === 'sso' || name === 'sso-rw') return sanitizeGrokSsoToken(part.slice(separator + 1))
  }
  const separator = normalized.indexOf(';')
  if (separator >= 0) normalized = normalized.slice(0, separator).trim()
  return sanitizeGrokSsoToken(normalized)
}

function sanitizeGrokSsoToken(value: string): string {
  return value.trim().replace(/[\r\n\0]/gu, '')
}

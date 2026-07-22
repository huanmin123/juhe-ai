export function accountNameFromBaseUrl(value: string): string {
  const input = value.trim()
  if (!input) return ''

  try {
    const url = new URL(input)
    if (url.protocol !== 'http:' && url.protocol !== 'https:') return ''
    return url.hostname
  } catch {
    return ''
  }
}

export function estimateJsonLikeBytes(value: unknown, seen = new WeakSet<object>()): number {
  if (value === null || value === undefined) return 4
  if (typeof value === 'string') return Buffer.byteLength(value, 'utf8') + 2
  if (typeof value === 'number' || typeof value === 'boolean' || typeof value === 'bigint') {
    return String(value).length
  }
  if (Buffer.isBuffer(value)) return value.byteLength
  if (value instanceof Date) return value.toISOString().length + 2
  if (Array.isArray(value)) {
    if (seen.has(value)) return 16
    seen.add(value)
    let bytes = 2
    for (const item of value) {
      bytes += estimateJsonLikeBytes(item, seen) + 1
    }
    return bytes
  }
  if (typeof value === 'object') {
    if (seen.has(value)) return 16
    seen.add(value)
    let bytes = 2
    for (const [key, item] of Object.entries(value as Record<string, unknown>)) {
      bytes += Buffer.byteLength(key, 'utf8') + 3 + estimateJsonLikeBytes(item, seen) + 1
    }
    return bytes
  }
  return 16
}

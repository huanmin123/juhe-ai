export interface JsonLikeByteEstimateOptions {
  maxBytes?: number
  maxNodes?: number
}

interface JsonLikeByteEstimateContext {
  seen: WeakSet<object>
  total: number
  nodeCount: number
  maxBytes: number
  maxNodes: number
}

const exactStringByteLengthMaxChars = 16 * 1024

export function estimateJsonLikeBytes(value: unknown, options: JsonLikeByteEstimateOptions = {}): number {
  const context: JsonLikeByteEstimateContext = {
    seen: new WeakSet<object>(),
    total: 0,
    nodeCount: 0,
    maxBytes: normalizeEstimateLimit(options.maxBytes),
    maxNodes: normalizeEstimateLimit(options.maxNodes)
  }
  visitJsonLikeValue(value, context)
  return context.total
}

function visitJsonLikeValue(value: unknown, context: JsonLikeByteEstimateContext): void {
  if (estimateLimitReached(context)) return
  context.nodeCount += 1
  if (estimateLimitReached(context)) return

  if (value === null || value === undefined) {
    addEstimatedBytes(context, 4)
    return
  }
  if (typeof value === 'string') {
    addEstimatedBytes(context, estimateStringBytes(value, context) + 2)
    return
  }
  if (typeof value === 'number' || typeof value === 'boolean' || typeof value === 'bigint') {
    addEstimatedBytes(context, String(value).length)
    return
  }
  if (Buffer.isBuffer(value)) {
    addEstimatedBytes(context, value.byteLength)
    return
  }
  if (value instanceof Date) {
    addEstimatedBytes(context, value.toISOString().length + 2)
    return
  }
  if (Array.isArray(value)) {
    if (context.seen.has(value)) {
      addEstimatedBytes(context, 16)
      return
    }
    context.seen.add(value)
    addEstimatedBytes(context, 2)
    for (const item of value) {
      visitJsonLikeValue(item, context)
      addEstimatedBytes(context, 1)
      if (estimateLimitReached(context)) return
    }
    return
  }
  if (typeof value === 'object') {
    if (context.seen.has(value)) {
      addEstimatedBytes(context, 16)
      return
    }
    context.seen.add(value)
    addEstimatedBytes(context, 2)
    const record = value as Record<string, unknown>
    for (const key in record) {
      if (!Object.prototype.hasOwnProperty.call(record, key)) continue
      addEstimatedBytes(context, estimateStringBytes(key, context) + 3)
      visitJsonLikeValue(record[key], context)
      addEstimatedBytes(context, 1)
      if (estimateLimitReached(context)) return
    }
    return
  }
  addEstimatedBytes(context, 16)
}

function addEstimatedBytes(context: JsonLikeByteEstimateContext, bytes: number): void {
  if (context.total >= context.maxBytes) return
  context.total = Math.min(context.maxBytes, context.total + Math.max(0, Math.trunc(bytes)))
}

function estimateLimitReached(context: JsonLikeByteEstimateContext): boolean {
  return context.total >= context.maxBytes || context.nodeCount >= context.maxNodes
}

function estimateStringBytes(value: string, context: JsonLikeByteEstimateContext): number {
  if (!Number.isFinite(context.maxBytes) || value.length <= exactStringByteLengthMaxChars) {
    return Buffer.byteLength(value, 'utf8')
  }
  return value.length * 4
}

function normalizeEstimateLimit(value: number | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0
    ? Math.trunc(value)
    : Number.POSITIVE_INFINITY
}

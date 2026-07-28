export function assertKnownInputKeys(input: object, allowedKeys: ReadonlySet<string>, label: string): void {
  const unknownKeys = Object.keys(input as Record<string, unknown>).filter((key) => !allowedKeys.has(key))
  if (unknownKeys.length) {
    throw new Error(`${label}包含未知字段：${unknownKeys.join('、')}`)
  }
}

export function normalizeNameOrThrow(value: unknown, message: string, maxLengthMessage = '来源系统名称不能超过 80 个字符'): string {
  if (typeof value !== 'string') {
    throw new Error(message)
  }
  const name = value.trim()
  if (!name) {
    throw new Error(message)
  }
  if (name.length > 80) {
    throw new Error(maxLengthMessage)
  }
  return name
}

export function isUniqueConstraintError(error: unknown): boolean {
  return error instanceof Error
    && (error.message.includes('UNIQUE constraint failed')
      || (error as { code?: unknown }).code === '23505')
}

export class ExternalIntegrationSourcePatchConflictError extends Error {
  constructor() {
    super('外部来源配置已被其他操作更新，请刷新后重试')
    this.name = 'ExternalIntegrationSourcePatchConflictError'
  }
}

export function nextExternalIntegrationUpdatedAt(currentUpdatedAt: string): string {
  const now = new Date().toISOString()
  if (now > currentUpdatedAt) return now
  const currentMs = Date.parse(currentUpdatedAt)
  return Number.isFinite(currentMs) ? new Date(currentMs + 1).toISOString() : now
}

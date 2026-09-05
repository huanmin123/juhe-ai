export type AccountHealthCheckTriggerReason = 'activation' | 'configuration' | 'request_failure' | 'scheduled'

/**
 * Opaque, bounded handoff from a gateway Codex turn avoidance activation to
 * the process that owns the real health-check probe. stateKey is already an
 * HMAC-derived runtime key and never contains raw request headers.
 */
export interface ClientSourceProbeFence {
  stateKey: string
  accountId: string
  sourceGeneration: number
  sourceFenceId: string
  runtimeKey: string
  probeGeneration: number
  configRevision: number
}

export function normalizeClientSourceProbeFence(value: unknown): ClientSourceProbeFence | undefined {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return undefined
  const record = value as Record<string, unknown>
  const stateKey = normalizedFenceText(record.stateKey, 512)
  const accountId = normalizedFenceText(record.accountId, 256)
  const runtimeKey = normalizedFenceText(record.runtimeKey, 1024)
  const sourceGeneration = normalizedFenceGeneration(record.sourceGeneration)
  const sourceFenceId = normalizedFenceId(record.sourceFenceId)
  const probeGeneration = normalizedFenceGeneration(record.probeGeneration)
  const configRevision = normalizedFenceGeneration(record.configRevision)
  if (!stateKey || !accountId || !runtimeKey || !sourceFenceId || sourceGeneration === undefined || probeGeneration === undefined || configRevision === undefined) {
    return undefined
  }
  return { stateKey, accountId, sourceGeneration, sourceFenceId, runtimeKey, probeGeneration, configRevision }
}

// Kept as a source-compatible alias while in-flight IPC messages settle.
export type CodexSourceProbeFence = ClientSourceProbeFence
export const normalizeCodexSourceProbeFence = normalizeClientSourceProbeFence

export function accountHealthCheckTriggerPriority(reason: AccountHealthCheckTriggerReason): number {
  switch (reason) {
    case 'activation': return 0
    case 'configuration': return 10
    case 'request_failure': return 15
    case 'scheduled': return 20
  }
}

export function isAccountHealthCheckTriggerReason(value: unknown): value is AccountHealthCheckTriggerReason {
  return value === 'activation'
    || value === 'configuration'
    || value === 'request_failure'
    || value === 'scheduled'
}

function normalizedFenceText(value: unknown, maxLength: number): string | undefined {
  if (typeof value !== 'string') return undefined
  const text = value.trim()
  return text && text.length <= maxLength ? text : undefined
}

function normalizedFenceGeneration(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 1 ? value : undefined
}

function normalizedFenceId(value: unknown): string | undefined {
  return typeof value === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value)
    ? value.toLowerCase()
    : undefined
}

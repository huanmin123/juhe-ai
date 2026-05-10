import { createHash } from 'node:crypto'

export type DeduplicationStatus = 'processing' | 'succeeded' | 'failed'

export interface DeduplicationEntry {
  key: string
  operationKey: string
  status: DeduplicationStatus
  startedAt: number
  finishedAt?: number
  expiresAt: number
}

export type DeduplicationClaimResult =
  | { claimed: true; entry: DeduplicationEntry }
  | { claimed: false; entry: DeduplicationEntry }

export interface DeduplicationClaimInput {
  key: string
  operationKey: string
  processingTtlMs?: number
}

export interface DeduplicationCompleteInput {
  key: string
  status: Extract<DeduplicationStatus, 'succeeded' | 'failed'>
  succeededTtlMs?: number
  failedTtlMs?: number
}

const defaultProcessingTtlMs = 120_000
const defaultSucceededTtlMs = 60_000
const defaultFailedTtlMs = 10_000
const maxEntries = 5_000

class OperationDeduplicationService {
  private readonly entries = new Map<string, DeduplicationEntry>()
  private nextCleanupAt = 0

  claim(input: DeduplicationClaimInput): DeduplicationClaimResult {
    const now = Date.now()
    this.cleanupIfNeeded(now)

    const existing = this.entries.get(input.key)
    if (existing && existing.expiresAt > now) {
      return { claimed: false, entry: existing }
    }

    const entry: DeduplicationEntry = {
      key: input.key,
      operationKey: input.operationKey,
      status: 'processing',
      startedAt: now,
      expiresAt: now + (input.processingTtlMs ?? defaultProcessingTtlMs)
    }
    this.entries.set(input.key, entry)
    this.trimIfNeeded(now)
    return { claimed: true, entry }
  }

  complete(input: DeduplicationCompleteInput): void {
    const entry = this.entries.get(input.key)
    if (!entry || entry.status !== 'processing') {
      return
    }

    const now = Date.now()
    entry.status = input.status
    entry.finishedAt = now
    entry.expiresAt = now + (input.status === 'succeeded'
      ? input.succeededTtlMs ?? defaultSucceededTtlMs
      : input.failedTtlMs ?? defaultFailedTtlMs)
  }

  private cleanupIfNeeded(now: number): void {
    if (now < this.nextCleanupAt && this.entries.size <= maxEntries) {
      return
    }

    for (const [key, entry] of this.entries) {
      if (entry.expiresAt <= now) {
        this.entries.delete(key)
      }
    }
    this.nextCleanupAt = now + 30_000
  }

  private trimIfNeeded(now: number): void {
    if (this.entries.size <= maxEntries) {
      return
    }

    const expiredKeys: string[] = []
    for (const [key, entry] of this.entries) {
      if (entry.expiresAt <= now) {
        expiredKeys.push(key)
      }
    }
    for (const key of expiredKeys) {
      this.entries.delete(key)
    }

    if (this.entries.size <= maxEntries) {
      return
    }

    const overflow = this.entries.size - maxEntries
    const oldestKeys = [...this.entries.values()]
      .sort((left, right) => left.expiresAt - right.expiresAt)
      .slice(0, overflow)
      .map((entry) => entry.key)
    for (const key of oldestKeys) {
      this.entries.delete(key)
    }
  }
}

export const operationDeduplicationService = new OperationDeduplicationService()

export function hashStableValue(value: unknown): string {
  return createHash('sha256').update(stableStringify(normalizeValue(value))).digest('hex')
}

export function stableStringify(value: unknown): string {
  return JSON.stringify(normalizeValue(value))
}

function normalizeValue(value: unknown): unknown {
  if (value === undefined) {
    return null
  }
  if (value === null || typeof value !== 'object') {
    return value
  }
  if (Array.isArray(value)) {
    return value.map((item) => normalizeValue(item))
  }

  const record = value as Record<string, unknown>
  return Object.fromEntries(
    Object.keys(record)
      .sort()
      .map((key) => [key, normalizeValue(record[key])])
  )
}

import type { AccountHealthJobsInputOutboxEvent } from '../../storage/account-health-jobs-input-outbox.repository.js'
import { passiveScheduleDelayMs } from '../../shared/passive-schedule-jitter.js'

export interface AccountHealthJobsInputOutboxPublisherDependencies {
  claim(leaseMs: number): Promise<AccountHealthJobsInputOutboxEvent | undefined>
  currentVersion(accountId: string): Promise<number | undefined>
  publishSnapshot(event: AccountHealthJobsInputOutboxEvent): Promise<void>
  publishTombstone(event: AccountHealthJobsInputOutboxEvent): Promise<void>
  acknowledge(event: AccountHealthJobsInputOutboxEvent): Promise<boolean>
  supersede(event: AccountHealthJobsInputOutboxEvent): Promise<boolean>
  fail(event: AccountHealthJobsInputOutboxEvent, errorCode: string, retryAt: Date): Promise<boolean>
}

export interface AccountHealthJobsInputOutboxPublisherOptions {
  leaseMs: number
  now?: () => Date
  retryBaseMs?: number
  retryMaxMs?: number
}

export type AccountHealthJobsInputOutboxPublishDisposition = 'idle' | 'published' | 'superseded' | 'retry_scheduled' | 'lease_lost'

// This is a one-event executor, not a scheduler. A DB-service owner may call
// it from a bounded loop after commit. Its only durable decisions are made by
// the outbox repository; it never probes, invokes Go, or falls back to Node's
// old health queue.
export async function publishNextAccountHealthJobsInputOutboxEvent(
  dependencies: AccountHealthJobsInputOutboxPublisherDependencies,
  options: AccountHealthJobsInputOutboxPublisherOptions
): Promise<AccountHealthJobsInputOutboxPublishDisposition> {
  const now = options.now ?? (() => new Date())
  const leaseMs = positiveInteger(options.leaseMs, 'J1 input publisher leaseMs', 1_000, 10 * 60_000)
  const retryBaseMs = positiveInteger(options.retryBaseMs ?? 1_000, 'J1 input publisher retryBaseMs', 1_000, 60_000)
  const retryMaxMs = positiveInteger(options.retryMaxMs ?? 5 * 60_000, 'J1 input publisher retryMaxMs', retryBaseMs, 60 * 60_000)
  const event = await dependencies.claim(leaseMs)
  if (!event) return 'idle'

  const currentVersion = await dependencies.currentVersion(event.accountId)
  if (currentVersion !== event.inputVersion) {
    return await dependencies.supersede(event) ? 'superseded' : 'lease_lost'
  }

  try {
    if (event.kind === 'snapshot') await dependencies.publishSnapshot(event)
    else await dependencies.publishTombstone(event)
  } catch (error) {
    const delay = retryDelayMs(event.attemptCount, retryBaseMs, retryMaxMs)
    const retryAt = new Date(now().getTime() + passiveScheduleDelayMs(delay))
    return await dependencies.fail(event, publishFailureCode(error), retryAt) ? 'retry_scheduled' : 'lease_lost'
  }
  return await dependencies.acknowledge(event) ? 'published' : 'lease_lost'
}

function publishFailureCode(error: unknown): string {
  // Outbox errors are operational evidence.  Never persist an arbitrary
  // exception message here: it can contain a credential, proxy URL, or file
  // path.  The category is enough to distinguish a retryable publish failure.
  if (error instanceof Error && error.name) return `j1_input_publish_${error.name.slice(0, 64)}`
  return 'j1_input_publish_unknown'
}

function positiveInteger(value: number, field: string, min: number, max: number): number {
  if (!Number.isSafeInteger(value) || value < min || value > max) throw new Error(`${field} 必须在 ${min}..${max}`)
  return value
}

function retryDelayMs(attemptCount: number, baseMs: number, maxMs: number): number {
  const exponent = Math.min(16, Math.max(0, attemptCount - 1))
  return Math.min(maxMs, baseMs * 2 ** exponent)
}

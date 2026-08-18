import {
  listAccountHealthJobsOutcomes,
  type AccountHealthJobsOutcome,
  type AccountHealthJobsOutcomeCursor,
  type AccountHealthJobsStoreSource
} from '../../storage/account-health-jobs-outcome.repository.js'

export interface AccountHealthJobsOutcomeDrainDependencies {
  loadCursor(): Promise<AccountHealthJobsOutcomeCursor | undefined>
  projectAndAdvance(outcome: AccountHealthJobsOutcome, cursor: AccountHealthJobsOutcomeCursor): Promise<{ cursorAdvanced: boolean }>
}

export interface AccountHealthJobsOutcomeDrainResult {
  processed: number
  lastCursor?: AccountHealthJobsOutcomeCursor
}

// This is a bounded, one-shot durable-store reader. A caller may schedule it
// only after J1 handover; it does not own probes, retries, upstream traffic or
// the jobs outcome store. Each item is projected before its cursor advances.
export async function drainAccountHealthJobsOutcomes(input: {
  source: AccountHealthJobsStoreSource
  limit: number
  dependencies: AccountHealthJobsOutcomeDrainDependencies
}): Promise<AccountHealthJobsOutcomeDrainResult> {
  const limit = normalizedLimit(input.limit)
  let cursor = await input.dependencies.loadCursor()
  let processed = 0
  while (processed < limit) {
    const pageLimit = Math.min(limit - processed, 100)
    const page = await listAccountHealthJobsOutcomes(input.source, {
      ...(cursor === undefined ? {} : { after: cursor }),
      limit: pageLimit
    })
    if (!page.length) break
    for (const outcome of page) {
      const next = { observedAt: outcome.storage_observed_at ?? outcome.observed_at, outcomeId: outcome.outcome_id }
      const applied = await input.dependencies.projectAndAdvance(outcome, next)
      if (!applied.cursorAdvanced) {
        return { processed, ...(cursor === undefined ? {} : { lastCursor: cursor }) }
      }
      cursor = next
      processed += 1
    }
    if (page.length < pageLimit) break
  }
  return { processed, ...(cursor === undefined ? {} : { lastCursor: cursor }) }
}

function normalizedLimit(value: number): number {
  if (!Number.isInteger(value) || value < 1 || value > 10_000) throw new Error('J1 outcome drain limit 必须在 1..10000')
  return value
}

import type { AccountHealthJobsOutcome } from '../../../storage/account-health-jobs-outcome.repository.js'
import { clearCodexTurnAccountAvoidanceByFenceAsync } from '../client-profiles/codex-turn-retry.service.js'
import {
  availabilityProbeSourceFenceSettlementDisposition,
  settleDispatchedAvailabilityProbeBySourceFence,
  type AvailabilityProbeOutcome
} from './availability-probe-coordinator.js'
import { settleMainProbeFenceOutcome } from './key-model-redis-store.js'

export interface AccountHealthJobsSourceFence {
  stateKey: string
  accountId: string
  sourceGeneration: number
  sourceFenceId: string
  runtimeKey: string
  probeGeneration: number
  configRevision: number
}

// This is a Gateway-side consumer of a durable Go outcome. It never dispatches
// a probe and never writes account business state; the source fence itself is
// the CAS authority that prevents an old result from settling a newer turn.
export type AccountHealthJobsSourceFenceSettlement = 'not_applicable' | 'settled' | 'retry' | 'terminal'

export async function settleAccountHealthJobsSourceFenceOutcome(outcome: AccountHealthJobsOutcome): Promise<boolean> {
  return (await settleAccountHealthJobsSourceFenceOutcomeWithDisposition(outcome)) === 'settled'
}

export async function settleAccountHealthJobsSourceFenceOutcomeWithDisposition(outcome: AccountHealthJobsOutcome): Promise<AccountHealthJobsSourceFenceSettlement> {
  if (outcome.key_model_fence) {
    const settled = await settleMainProbeFenceOutcome({
      fence: {
        capabilityHash: outcome.key_model_fence.capability_hash,
        keyFingerprint: outcome.key_model_fence.key_fingerprint,
        dispatchRevision: outcome.key_model_fence.dispatch_revision,
        ownerId: outcome.key_model_fence.owner_id
      },
      outcome: outcome.outcome,
      winnerKeyFingerprint: outcome.winner_key_fingerprint
    })
    return settled ? 'settled' : 'retry'
  }
  if (!outcome.source_fence) return 'not_applicable'
  const sourceFence: AccountHealthJobsSourceFence = {
    stateKey: outcome.source_fence.state_key,
    accountId: outcome.source_fence.account_id,
    sourceGeneration: outcome.source_fence.source_generation,
    sourceFenceId: outcome.source_fence.source_fence_id,
    runtimeKey: outcome.source_fence.runtime_key,
    probeGeneration: outcome.source_fence.probe_generation,
    configRevision: outcome.source_fence.config_revision
  }
  const probeOutcome = gatewayProbeOutcome(outcome.outcome)
  if (await settleAccountHealthJobsSourceFence(sourceFence, probeOutcome)) return 'settled'
  const disposition = await availabilityProbeSourceFenceSettlementDisposition({
    runtimeKey: sourceFence.runtimeKey,
    generation: sourceFence.probeGeneration,
    sourceFence
  })
  if (disposition.disposition === 'retry') return 'retry'
  // A terminal durable success still owns its precise avoidance fence even if
  // this Gateway no longer has the coordinator state (restart/TTL) or the
  // generation has since been replaced. The fence CAS cannot clear a newer
  // generation, so it is safe to release the original avoidance here.
  if (probeOutcome === 'success') {
    await clearCodexTurnAccountAvoidanceByFenceAsync(sourceFence)
  }
  return 'terminal'
}

// This settles a locally failed Go-owner request publication without going
// through the legacy worker IPC path. It is the same fenced Gateway runtime
// CAS used for a durable outcome; it does not probe or write business state.
export async function settleAccountHealthJobsSourceFence(
  sourceFence: AccountHealthJobsSourceFence,
  outcome: AvailabilityProbeOutcome
): Promise<boolean> {
  const settled = await settleDispatchedAvailabilityProbeBySourceFence({
    runtimeKey: sourceFence.runtimeKey,
    generation: sourceFence.probeGeneration,
    sourceFence,
    outcome
  })
  if (settled && outcome === 'success') {
    await clearCodexTurnAccountAvoidanceByFenceAsync(sourceFence)
  }
  return settled
}

export function gatewayProbeOutcome(outcome: AccountHealthJobsOutcome['outcome']): AvailabilityProbeOutcome {
  switch (outcome) {
    case 'complete_success':
      return 'success'
    case 'upstream_failure':
      return 'health_failure'
    case 'framing_complete_neutral':
      return 'unknown'
    case 'probe_task_failure':
      return 'probe_task_failure'
    case 'stale':
      return 'stale'
  }
}

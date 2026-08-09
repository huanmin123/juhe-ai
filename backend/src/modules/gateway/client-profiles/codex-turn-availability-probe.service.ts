import { errorLogFields, logger } from '../../../shared/logger.js'
import { dispatchAccountHealthCheckWithOutcome } from '../../accounts/account-health-check-dispatch.service.js'
import type { AccountHealthCheckDispatchOutcome } from '../../internal-api/account-health-check-dispatch.routes.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import { gatewayAccountRuntimeKey } from '../runtime/account-runtime-keys.js'
import {
  acquireAvailabilityProbe,
  getAvailabilityProbeState,
  releaseAvailabilityProbeForExecution,
  settleAvailabilityProbe,
  settleDispatchedAvailabilityProbeBySourceFence,
  type AvailabilityProbeOutcome
} from '../runtime/availability-probe-coordinator.js'
import {
  clearCodexTurnAccountAvoidanceByFenceAsync,
  type CodexTurnFailureRecordResult
} from './codex-turn-retry.service.js'
import type { OpenAIGatewayClientStrategyContext } from './strategy.js'
import type { CodexSourceProbeFence } from '../../accounts/account-health-check-trigger.js'

export async function runCodexTurnAvoidanceAvailabilityProbe(input: {
  account: UpstreamAccount
  strategy: OpenAIGatewayClientStrategyContext
  activation: NonNullable<CodexTurnFailureRecordResult['activation']>
  dispatch?: (accountId: string, reason: 'request_failure', traceId?: string, sourceFence?: CodexSourceProbeFence) => AccountHealthCheckDispatchOutcome
}): Promise<{ disposition: 'owner' | 'joined'; generation: number; outcome?: AvailabilityProbeOutcome }> {
  const stateKey = input.strategy.clientSourceAvoidanceStateKey?.trim()
  if (!stateKey) {
    // The activation API only exposes a source generation for a legal source
    // key. Keep this defensive guard non-mutating if a future caller breaks
    // that contract.
    return { disposition: 'joined', generation: input.activation.sourceGeneration, outcome: 'unknown' }
  }
  const coordination = await acquireAvailabilityProbe({
    accountRuntimeScope: gatewayAccountRuntimeKey(input.account),
    // Use the same actual availability lease as background health checks.
    probeKind: 'account_health_check',
    configRevision: accountConfigRevision(input.account),
    executionRole: 'source_dispatch',
    sourceFence: {
      stateKey,
      accountId: input.account.id,
      sourceGeneration: input.activation.sourceGeneration,
      sourceFenceId: input.activation.sourceFenceId
    }
  })
  await clearReplacedSettledSourceFences(coordination)
  if (coordination.disposition === 'joined') {
    // A source that joined after a successful shared probe lazily consumes the
    // settled result; non-success outcomes retain its short avoidance.
    const settled = await getAvailabilityProbeState(coordination.runtimeKey)
    if (settled?.outcome === 'success') {
      await clearCodexTurnAccountAvoidanceByFenceAsync({
        stateKey,
        accountId: input.account.id,
        sourceGeneration: input.activation.sourceGeneration,
        sourceFenceId: input.activation.sourceFenceId
      })
    }
    // A joined source still hands its fence to the worker. This is a fence
    // merge, not a follow-up probe: the worker's per-account execution record
    // attaches it to a running ordinary/source task when one exists.
    const handoff = dispatchSourceFence(input, sourceFenceForDispatch(input, coordination))
    return { disposition: 'joined', generation: coordination.generation, ...(handoff.outcome === 'rejected' ? { outcome: 'unknown' as const } : {}) }
  }

  let sourceFence: CodexSourceProbeFence | undefined
  let releasedForExecution = false
  try {
    sourceFence = sourceFenceForDispatch(input, coordination)
    // Mark the generation as dispatch-pending before sending IPC/control work.
    // A fast control rejection can then settle this exact fence instead of
    // racing the owner token and leaving the generation stranded.
    const released = await releaseAvailabilityProbeForExecution({
      runtimeKey: coordination.runtimeKey,
      generation: coordination.generation,
      ownerToken: coordination.ownerToken
    })
    if (!released) {
      await settleAvailabilityProbe({
        runtimeKey: coordination.runtimeKey,
        generation: coordination.generation,
        ownerToken: coordination.ownerToken,
        outcome: 'unknown'
      })
      return { disposition: 'owner', generation: coordination.generation, outcome: 'unknown' }
    }
    releasedForExecution = true
    const dispatch = dispatchSourceFence(input, sourceFence)
    if (dispatch.outcome === 'queued') {
      // The health worker, not the source observer, is now the only component
      // allowed to issue the upstream diagnostic and settle account health.
      return { disposition: 'owner', generation: coordination.generation }
    }
    const outcome = dispatch.outcome === 'coalesced' ? 'unknown' : 'probe_task_failure'
    await settleDispatchedAvailabilityProbeBySourceFence({
      runtimeKey: coordination.runtimeKey,
      generation: coordination.generation,
      sourceFence,
      outcome
    })
    if (dispatch.outcome === 'coalesced') {
      // A local dispatch cooldown cannot prove that the pre-existing worker
      // will acquire this generation: it may already have joined the source
      // lease and returned. Settle inconclusively instead of leaving a source
      // fence waiting behind a no-longer-runnable task.
      return { disposition: 'owner', generation: coordination.generation, outcome: 'unknown' }
    }
    return { disposition: 'owner', generation: coordination.generation, outcome: 'probe_task_failure' }
  } catch (error) {
    if (releasedForExecution && sourceFence) {
      await settleDispatchedAvailabilityProbeBySourceFence({
        runtimeKey: coordination.runtimeKey,
        generation: coordination.generation,
        sourceFence,
        outcome: 'probe_task_failure'
      })
    } else {
      await settleAvailabilityProbe({
        runtimeKey: coordination.runtimeKey,
        generation: coordination.generation,
        ownerToken: coordination.ownerToken,
        outcome: 'probe_task_failure'
      })
    }
    logger.warn(errorLogFields(error, {
      event: 'gateway_codex_turn_avoidance_probe_failed',
      accountId: input.account.id,
      sourceGeneration: input.activation.sourceGeneration
    }), 'Codex turn 避让探活未形成可靠结果，保留短期避让')
    return { disposition: 'owner', generation: coordination.generation, outcome: 'probe_task_failure' }
  }
}

async function clearReplacedSettledSourceFences(
  coordination: Awaited<ReturnType<typeof acquireAvailabilityProbe>>
): Promise<void> {
  if (coordination.disposition !== 'owner' || coordination.replacedFenceSettlement?.outcome !== 'success') return
  for (const fence of coordination.replacedFenceSettlement.sourceFences) {
    try {
      await clearCodexTurnAccountAvoidanceByFenceAsync(fence)
    } catch (error) {
      logger.warn(errorLogFields(error, {
        event: 'gateway_codex_turn_replaced_probe_fence_clear_failed',
        accountId: fence.accountId,
        sourceGeneration: fence.sourceGeneration
      }), '替换已结算探活 generation 后未能清理旧来源避让')
    }
  }
}

function sourceFenceForDispatch(
  input: Parameters<typeof runCodexTurnAvoidanceAvailabilityProbe>[0],
  coordination: { runtimeKey: string; generation: number }
): CodexSourceProbeFence {
  return {
    stateKey: input.strategy.clientSourceAvoidanceStateKey!,
    accountId: input.account.id,
    sourceGeneration: input.activation.sourceGeneration,
    sourceFenceId: input.activation.sourceFenceId,
    runtimeKey: coordination.runtimeKey,
    probeGeneration: coordination.generation,
    configRevision: accountConfigRevision(input.account)
  }
}

function dispatchSourceFence(
  input: Parameters<typeof runCodexTurnAvoidanceAvailabilityProbe>[0],
  sourceFence: CodexSourceProbeFence
): AccountHealthCheckDispatchOutcome {
  return (input.dispatch ?? dispatchAccountHealthCheckWithOutcome)(input.account.id, 'request_failure', undefined, sourceFence)
}

function accountConfigRevision(account: UpstreamAccount): number {
  const revision = account.configRevision
  return typeof revision === 'number' && Number.isFinite(revision) ? Math.max(1, Math.trunc(revision)) : 1
}

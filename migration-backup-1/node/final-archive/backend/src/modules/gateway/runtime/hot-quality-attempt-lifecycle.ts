import { getRequestLogger } from '../../../shared/request-context.js'
import { errorLogFields } from '../../../shared/logger.js'
import { observeGatewayRouting } from '../observability/routing-observability.service.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import type { OpenAIGatewayRequestLane } from '../protocols/openai-v1/request-lane.js'
import {
  getGatewayHotQualityRuntime,
  gatewayHotQualityModelFamily,
  type GatewayHotQualityRuntime
} from './hot-quality-runtime.service.js'
import {
  type HotQualityFailureScope,
  type HotQualityScope,
  type HotQualityTerminalOutcomeClass,
  type HotQualityTerminalSource
} from './hot-quality-store.js'
import { gatewayAccountRuntimeKey } from './account-runtime-keys.js'

export interface GatewayHotQualityAttemptLifecycle {
  readonly attemptId: string
  readonly scope: HotQualityScope
  markFirstByte(firstByteMs?: number): void
  recordTerminal(input: {
    outcomeClass: HotQualityTerminalOutcomeClass
    failureScope?: HotQualityFailureScope
    source?: HotQualityTerminalSource
    firstByteMs?: number
  }): Promise<void>
}

export function createGatewayHotQualityAttemptLifecycle(input: {
  runtime?: GatewayHotQualityRuntime
  attemptId: string
  account: UpstreamAccount
  requestLane: OpenAIGatewayRequestLane
  model?: string | null
  nowMs?: number
}): GatewayHotQualityAttemptLifecycle {
  const attemptId = required(input.attemptId, 'attemptId')
  const scope: HotQualityScope = {
    accountRuntimeKey: gatewayAccountRuntimeKey(input.account),
    protocolProfile: required(
      input.account.providerProtocolProfileId || `${input.account.protocolCode}:${input.account.protocolVersion}`,
      'protocolProfile'
    ),
    requestLane: input.requestLane,
    modelFamily: gatewayHotQualityModelFamily(input.model)
  }
  const runtime = input.runtime ?? getGatewayHotQualityRuntime()
  let firstByteMs: number | undefined
  let terminalPromise: Promise<void> | undefined
  const attemptReady = recordAttemptSafely(runtime, attemptId, scope, input.nowMs)

  return {
    attemptId,
    scope,
    markFirstByte(value) {
      if (firstByteMs !== undefined) return
      if (value === undefined || !Number.isFinite(value) || value < 0) return
      firstByteMs = Math.round(value)
    },
    recordTerminal(terminal) {
      if (terminalPromise) return terminalPromise
      const effectiveFirstByteMs = terminal.firstByteMs ?? firstByteMs
      terminalPromise = attemptReady.then(() => recordTerminalSafely(runtime, {
          attemptId,
          scope,
          terminalOutcomeId: `${attemptId}:terminal`,
          outcomeClass: terminal.outcomeClass,
          failureScope: terminal.failureScope ?? 'none',
          source: terminal.source ?? 'request_lifecycle',
          firstByteMs: effectiveFirstByteMs
        }, input.nowMs))
      return terminalPromise
    }
  }
}

async function recordAttemptSafely(
  runtime: GatewayHotQualityRuntime,
  attemptId: string,
  scope: HotQualityScope,
  nowMs?: number
): Promise<void> {
  observeGatewayRouting({ kind: 'attempt', outcome: 'started' }, nowMs)
  try {
    const result = await runtime.hotQualityStore.recordAttempt({ attemptId, scope, nowMs })
    observeGatewayRouting({
      kind: 'hot_quality_mutation',
      operation: 'attempt',
      status: hotQualityObservationStatus(result.status)
    }, nowMs)
  } catch (error) {
    getRequestLogger().warn(errorLogFields(error, {
      event: 'gateway_hot_quality_attempt_record_failed',
      attemptId
    }), '记录热质量 attempt 失败')
  }
}

async function recordTerminalSafely(
  runtime: GatewayHotQualityRuntime,
  input: Parameters<GatewayHotQualityRuntime['hotQualityStore']['recordTerminal']>[0],
  nowMs?: number
): Promise<void> {
  observeGatewayRouting({
    kind: 'attempt',
    outcome: terminalAttemptObservation(input.outcomeClass)
  }, nowMs)
  try {
    const result = await runtime.hotQualityStore.recordTerminal({ ...input, nowMs })
    observeGatewayRouting({
      kind: 'hot_quality_mutation',
      operation: 'terminal',
      status: hotQualityObservationStatus(result.status)
    }, nowMs)
  } catch (error) {
    getRequestLogger().warn(errorLogFields(error, {
      event: 'gateway_hot_quality_terminal_record_failed',
      attemptId: input.attemptId,
      outcomeClass: input.outcomeClass
    }), '记录热质量终态失败')
  }
}

function hotQualityObservationStatus(status: string): 'applied' | 'idempotent' | 'conflict' | 'capacity_exhausted' | 'unavailable' {
  if (status === 'applied' || status === 'idempotent') return status
  if (status.includes('capacity')) return 'capacity_exhausted'
  if (status.includes('conflict')) return 'conflict'
  return 'unavailable'
}

function terminalAttemptObservation(
  outcome: HotQualityTerminalOutcomeClass
): 'completed' | 'transport_failure' | 'unknown' | 'client_canceled' {
  if (outcome === 'completed_response' || outcome === 'explicit_policy_failure') return 'completed'
  if (outcome === 'client_cancellation') return 'client_canceled'
  if (outcome === 'upstream_response_failure' || outcome === 'unknown') return 'unknown'
  return 'transport_failure'
}

function required(value: string, name: string): string {
  const normalized = value.trim()
  if (!normalized) throw new Error(`热质量 ${name} 不能为空`)
  return normalized
}

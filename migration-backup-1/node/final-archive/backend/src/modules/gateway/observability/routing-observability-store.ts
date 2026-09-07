export type GatewayRoutingCircuitOperation =
  | 'suspect'
  | 'acquire_confirmation'
  | 'complete_confirmation'
  | 'acquire_canary'
  | 'complete_canary'
  | 'record_parent_evidence'
  | 'replace_revision'

export type GatewayRoutingCircuitMutationStatus =
  | 'applied'
  | 'idempotent'
  | 'not_found'
  | 'state_mismatch'
  | 'stale_generation'
  | 'stale_dispatch_revision'
  | 'lease_mismatch'
  | 'not_due'
  | 'capacity_exhausted'

export type GatewayRoutingObservation =
  | {
      kind: 'circuit_transition'
      from: 'CLOSED' | 'SUSPECT' | 'OPEN' | 'HALF_OPEN' | 'RECOVERING'
      to: 'CLOSED' | 'SUSPECT' | 'OPEN' | 'HALF_OPEN' | 'RECOVERING'
      source: 'transport' | 'explicit_policy' | 'recovery' | 'configuration'
    }
  | {
      kind: 'circuit_mutation'
      operation: GatewayRoutingCircuitOperation
      status: GatewayRoutingCircuitMutationStatus
      leaseKind?: 'confirmation' | 'half_open' | 'recovery'
    }
  | {
      kind: 'circuit_dispatch'
      outcome: 'blocked' | 'rebuild_blocked'
      phase: 'SUSPECT' | 'OPEN' | 'HALF_OPEN' | 'RECOVERING'
    }
  | {
      kind: 'hot_quality_mutation'
      operation: 'attempt' | 'terminal'
      status: 'applied' | 'idempotent' | 'conflict' | 'capacity_exhausted' | 'unavailable'
    }
  | {
      kind: 'exploration'
      outcome: 'reserved' | 'dispatched' | 'restored' | 'contended' | 'ineligible'
    }
  | {
      kind: 'tier_escape'
      outcome: 'applied' | 'exhausted' | 'blocked'
    }
  | {
      kind: 'attempt'
      outcome: 'started' | 'completed' | 'transport_failure' | 'unknown' | 'client_canceled'
    }
  | {
      kind: 'budget'
      outcome: 'wall_exhausted' | 'precommit_clipped' | 'client_handoff'
    }

export interface GatewayRoutingObservabilitySnapshot {
  version: 1
  recordedEvents: number
  updatedAtMs: number
  counters: Readonly<Record<string, number>>
}

export interface GatewayRoutingObservationBatchEntry {
  observation: GatewayRoutingObservation
  count: number
}

export interface GatewayRoutingObservabilityStore {
  record(observation: GatewayRoutingObservation, nowMs?: number): Promise<void>
  recordBatch(entries: readonly GatewayRoutingObservationBatchEntry[], nowMs?: number): Promise<void>
  snapshot(): Promise<GatewayRoutingObservabilitySnapshot>
}

export const gatewayRoutingObservabilityMetricCapacity = 512

export function gatewayRoutingObservationMetricKey(observation: GatewayRoutingObservation): string {
  switch (observation.kind) {
    case 'circuit_transition': return `circuit.transition.${observation.from.toLowerCase()}.${observation.to.toLowerCase()}.${observation.source}`
    case 'circuit_mutation': return `circuit.mutation.${observation.operation}.${observation.status}${observation.leaseKind ? `.${observation.leaseKind}` : ''}`
    case 'circuit_dispatch': return `circuit.dispatch.${observation.outcome}.${observation.phase.toLowerCase()}`
    case 'hot_quality_mutation': return `hot_quality.${observation.operation}.${observation.status}`
    case 'exploration': return `exploration.${observation.outcome}`
    case 'tier_escape': return `tier_escape.${observation.outcome}`
    case 'attempt': return `attempt.${observation.outcome}`
    case 'budget': return `budget.${observation.outcome}`
  }
}

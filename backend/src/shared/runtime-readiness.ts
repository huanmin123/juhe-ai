export type RuntimeReadinessBlocker = 'db_service_unavailable' | 'worker_topology_not_ready'

export interface RuntimeReadinessInput {
  dbServiceReady: boolean
  workerTopologyReady: boolean
  topologyGatesHealth: boolean
}

export interface RuntimeReadinessSnapshot {
  statusCode: 200 | 503
  status: 'ok' | 'starting'
  dbServiceReady: boolean
  workerTopologyReady: boolean
  blockers: RuntimeReadinessBlocker[]
}

/**
 * Readiness is about the Node runtime that serves traffic. Account-level
 * upstream probes (including the optional J2 Go sidecar) deliberately do not
 * participate here; those failures remain per-account degraded state.
 */
export function resolveRuntimeReadiness(input: RuntimeReadinessInput): RuntimeReadinessSnapshot {
  const blockers: RuntimeReadinessBlocker[] = []
  if (!input.dbServiceReady) blockers.push('db_service_unavailable')
  if (input.topologyGatesHealth && !input.workerTopologyReady) blockers.push('worker_topology_not_ready')
  const ready = blockers.length === 0
  return {
    statusCode: ready ? 200 : 503,
    status: ready ? 'ok' : 'starting',
    dbServiceReady: input.dbServiceReady,
    workerTopologyReady: input.workerTopologyReady,
    blockers
  }
}

import type {
  AccountRuntimeAvailability,
  PublicAccountRuntimeAvailability
} from './types.js'

export function publicAccountRuntimeAvailability(
  runtime: AccountRuntimeAvailability | PublicAccountRuntimeAvailability | undefined
): PublicAccountRuntimeAvailability | undefined {
  if (!runtime) return undefined
  return {
    status: runtime.status,
    ...(runtime.reason ? { reason: runtime.reason } : {}),
    ...(runtime.since ? { since: runtime.since } : {}),
    ...(runtime.probePresentation
      ? {
          probePresentation: {
            ...(runtime.probePresentation.lastObservation
              ? { lastObservation: { ...runtime.probePresentation.lastObservation } }
              : {}),
            schedule: { ...runtime.probePresentation.schedule }
          }
        }
      : {})
  }
}

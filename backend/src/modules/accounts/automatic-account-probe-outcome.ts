export type AutomaticAccountProbeOutcome =
  | 'complete_success'
  | 'upstream_failure'
  | 'probe_task_failure'
  | 'stale'

export function automaticAccountProbeOutcome(
  result: { success: boolean; accountFailureEligible?: boolean },
  upstreamAttemptObserved: boolean
): Exclude<AutomaticAccountProbeOutcome, 'stale'> {
  if (result.success) return 'complete_success'
  return upstreamAttemptObserved ? 'upstream_failure' : 'probe_task_failure'
}

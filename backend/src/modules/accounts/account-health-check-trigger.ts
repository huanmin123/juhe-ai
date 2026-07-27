export type AccountHealthCheckTriggerReason = 'activation' | 'configuration' | 'request_failure' | 'scheduled'

export function accountHealthCheckTriggerPriority(reason: AccountHealthCheckTriggerReason): number {
  switch (reason) {
    case 'activation': return 0
    case 'configuration': return 10
    case 'request_failure': return 15
    case 'scheduled': return 20
  }
}

export function isAccountHealthCheckTriggerReason(value: unknown): value is AccountHealthCheckTriggerReason {
  return value === 'activation'
    || value === 'configuration'
    || value === 'request_failure'
    || value === 'scheduled'
}

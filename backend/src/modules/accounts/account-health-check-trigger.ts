export type AccountHealthCheckTriggerReason = 'activation' | 'configuration' | 'scheduled'

export function accountHealthCheckTriggerPriority(reason: AccountHealthCheckTriggerReason): number {
  switch (reason) {
    case 'activation': return 0
    case 'configuration': return 10
    case 'scheduled': return 20
  }
}

export function isAccountHealthCheckTriggerReason(value: unknown): value is AccountHealthCheckTriggerReason {
  return value === 'activation' || value === 'configuration' || value === 'scheduled'
}

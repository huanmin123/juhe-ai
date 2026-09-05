export type AccountCreationStatus = 'active' | 'pending_test' | 'disabled'

export interface AccountCreationStatusInput {
  status: AccountCreationStatus
  skipInitialHealthCheck: boolean
  schedulable: boolean
}

/** Normalize the user-facing creation choice and derive the guarded write flags. */
export function accountCreationStatusInput(value: unknown): AccountCreationStatusInput {
  const status: AccountCreationStatus = value === 'active' || value === 'disabled'
    ? value
    : 'pending_test'
  const immediatelySchedulable = status === 'active'
  return {
    status,
    skipInitialHealthCheck: immediatelySchedulable,
    schedulable: immediatelySchedulable
  }
}

export interface AccountTestFailureEligibilityInput {
  statusCode?: number
  errorCode?: string
  message?: string
}

export function accountTestFailureEligibleForAccount(_input: AccountTestFailureEligibilityInput): boolean {
  // Provider-controlled status, code, and text cannot distinguish account
  // failure from a request/model-specific response.
  return true
}

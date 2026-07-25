export const EXPLICIT_ACCOUNT_ERROR_POLICY_COOLDOWN_CODE = 'explicit_account_error_policy_cooldown'
export const LEGACY_EXPLICIT_ACCOUNT_ERROR_POLICY_MESSAGE_PREFIX = '账户错误策略「'

export function isExplicitAccountErrorPolicyCooldown(
  errorCode: string | null | undefined,
  errorMessage?: string | null
): boolean {
  if (errorCode === EXPLICIT_ACCOUNT_ERROR_POLICY_COOLDOWN_CODE) return true
  return !errorCode && Boolean(errorMessage?.startsWith(LEGACY_EXPLICIT_ACCOUNT_ERROR_POLICY_MESSAGE_PREFIX))
}

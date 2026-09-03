export const EXPLICIT_ACCOUNT_ERROR_POLICY_COOLDOWN_CODE = 'explicit_account_error_policy_cooldown'
/** Account-level system quota provenance codes used to fence single-Key/OAuth cooldown writes. */
export const SYSTEM_QUOTA_GENERIC_COOLDOWN_CODE = 'system_quota_generic_cooldown'
export const SYSTEM_QUOTA_EXPLICIT_RESET_COOLDOWN_CODE = 'system_quota_explicit_reset'
export const LEGACY_EXPLICIT_ACCOUNT_ERROR_POLICY_MESSAGE_PREFIX = '账户错误策略「'
export const SYSTEM_QUOTA_EXPLICIT_RESET_POLICY_MESSAGE_PREFIX = '系统继承错误策略「'

export function isExplicitAccountErrorPolicyCooldown(
  errorCode: string | null | undefined,
  errorMessage?: string | null
): boolean {
  if (errorCode === EXPLICIT_ACCOUNT_ERROR_POLICY_COOLDOWN_CODE
    || errorCode === SYSTEM_QUOTA_EXPLICIT_RESET_COOLDOWN_CODE) return true
  return !errorCode && Boolean(
    errorMessage?.startsWith(LEGACY_EXPLICIT_ACCOUNT_ERROR_POLICY_MESSAGE_PREFIX)
      || errorMessage?.startsWith(SYSTEM_QUOTA_EXPLICIT_RESET_POLICY_MESSAGE_PREFIX)
  )
}

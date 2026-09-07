const accountCircuitCredentialIdentityKeys = [
  'api_key',
  'api_keys',
  'access_token',
  'refresh_token',
  'client_id',
  'client_secret',
  'id_token',
  'account_id',
  'chatgpt_user_id',
  'quota_project_id',
  'base_url',
  'supported_endpoint_modes'
] as const

/**
 * Returns only upstream connection identity. Routing preferences, inspection
 * rules and request overrides live in the same credentials JSON but must not
 * revive an OPEN transport circuit when they change.
 */
export function accountCircuitCredentialOwnerIdentity(
  credentials: Record<string, unknown> | undefined
): Record<string, unknown> {
  if (!credentials) return {}
  const identity: Record<string, unknown> = {}
  for (const key of accountCircuitCredentialIdentityKeys) {
    if (Object.prototype.hasOwnProperty.call(credentials, key)) {
      identity[key] = credentials[key]
    }
  }
  return identity
}

export type OpenAIGatewayTrafficSource = 'gateway' | 'manual_account_test' | 'runtime_recovery_probe' | 'cooldown_retest' | 'hybrid_scoring' | 'hybrid_quality_scoring'

export function normalizeOpenAIGatewayTrafficSource(value: unknown): OpenAIGatewayTrafficSource {
  if (value === undefined) return 'gateway'
  if (
    value === 'gateway'
    || value === 'manual_account_test'
    || value === 'runtime_recovery_probe'
    || value === 'cooldown_retest'
    || value === 'hybrid_scoring'
    || value === 'hybrid_quality_scoring'
  ) {
    return value
  }
  throw new Error(`非法网关流量来源：${String(value)}`)
}

export function isCooldownRetestTrafficSource(value: unknown): boolean {
  return normalizeOpenAIGatewayTrafficSource(value) === 'cooldown_retest'
}

export function isAccountProbeTrafficSource(value: unknown): boolean {
  const normalized = normalizeOpenAIGatewayTrafficSource(value)
  return normalized === 'runtime_recovery_probe' || normalized === 'cooldown_retest'
}

export type OpenAIGatewayTrafficSource = 'gateway' | 'manual_account_test' | 'cooldown_retest'

export function normalizeOpenAIGatewayTrafficSource(value: unknown): OpenAIGatewayTrafficSource {
  if (value === undefined) return 'gateway'
  if (value === 'gateway' || value === 'manual_account_test' || value === 'cooldown_retest') {
    return value
  }
  throw new Error(`非法网关流量来源：${String(value)}`)
}

export function isCooldownRetestTrafficSource(value: unknown): boolean {
  return normalizeOpenAIGatewayTrafficSource(value) === 'cooldown_retest'
}

export type OpenAIGatewayTrafficSource = 'gateway' | 'manual_account_test' | 'cooldown_retest'

export function normalizeOpenAIGatewayTrafficSource(value: unknown): OpenAIGatewayTrafficSource {
  return value === 'manual_account_test' || value === 'cooldown_retest' ? value : 'gateway'
}

export function isCooldownRetestTrafficSource(value: unknown): boolean {
  return normalizeOpenAIGatewayTrafficSource(value) === 'cooldown_retest'
}

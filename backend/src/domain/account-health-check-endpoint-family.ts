import type { AccountHealthCheckEndpointFamily, AccountSupportedEndpointMode } from './types.js'

export type { AccountHealthCheckEndpointFamily } from './types.js'

export const ACCOUNT_HEALTH_CHECK_ENDPOINT_FAMILIES = [
  'chat_completions',
  'responses',
  'messages',
  'generate_content'
] as const

const endpointModeByFamily: Record<AccountHealthCheckEndpointFamily, AccountSupportedEndpointMode> = {
  chat_completions: 'chat_json',
  responses: 'responses_json',
  messages: 'messages_json',
  generate_content: 'generate_content_json'
}

export function healthCheckEndpointMode(family: AccountHealthCheckEndpointFamily): AccountSupportedEndpointMode {
  return endpointModeByFamily[family]
}

export function resolveDefaultHealthCheckEndpointFamily(input: {
  providerCode: string
  providerProtocolProfileId: string
  enabledEndpointModes: readonly AccountSupportedEndpointMode[]
}): AccountHealthCheckEndpointFamily {
  const enabledFamilies = input.enabledEndpointModes
    .map(endpointFamilyFromJsonMode)
    .filter((family): family is AccountHealthCheckEndpointFamily => Boolean(family))
  const preferred = preferredHealthCheckEndpointFamily(input.providerCode, input.providerProtocolProfileId)
  if (enabledFamilies.includes(preferred)) return preferred
  const firstEnabled = enabledFamilies[0]
  if (firstEnabled) return firstEnabled
  throw new Error('账户至少需要启用一个可用于健康检查的 JSON 端点族')
}

export function resolveHealthCheckEndpointFamily(input: {
  value: unknown
  providerCode: string
  providerProtocolProfileId: string
  enabledEndpointModes: readonly AccountSupportedEndpointMode[]
}): AccountHealthCheckEndpointFamily {
  if (input.value === undefined) return resolveDefaultHealthCheckEndpointFamily(input)
  if (!ACCOUNT_HEALTH_CHECK_ENDPOINT_FAMILIES.includes(input.value as AccountHealthCheckEndpointFamily)) {
    throw new Error('账户健康检查协议族无效')
  }
  const family = input.value as AccountHealthCheckEndpointFamily
  if (!input.enabledEndpointModes.includes(healthCheckEndpointMode(family))) {
    throw new Error(`账户健康检查协议族 ${family} 未启用对应 JSON 能力`)
  }
  return family
}

function preferredHealthCheckEndpointFamily(
  providerCode: string,
  providerProtocolProfileId: string
): AccountHealthCheckEndpointFamily {
  if (providerProtocolProfileId === 'profile_gemini_native_v1beta') return 'generate_content'
  if (providerProtocolProfileId.includes('anthropic')) return 'messages'
  if (providerCode === 'anthropic') return 'messages'
  if (providerCode === 'gpt') return 'responses'
  return 'chat_completions'
}

function endpointFamilyFromJsonMode(mode: AccountSupportedEndpointMode): AccountHealthCheckEndpointFamily | undefined {
  if (mode === 'chat_json') return 'chat_completions'
  if (mode === 'responses_json') return 'responses'
  if (mode === 'messages_json') return 'messages'
  if (mode === 'generate_content_json') return 'generate_content'
  return undefined
}

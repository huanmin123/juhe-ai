import type { AccountHealthCheckEndpointMode, AccountSupportedEndpointMode } from './types.js'

export type { AccountHealthCheckEndpointMode } from './types.js'

export const ACCOUNT_HEALTH_CHECK_ENDPOINT_MODES = [
  'chat_json',
  'chat_sse',
  'responses_json',
  'responses_sse',
  'messages_json',
  'messages_sse',
  'generate_content_json',
  'generate_content_sse'
] as const

export function resolveDefaultHealthCheckEndpointMode(input: {
  providerCode: string
  providerProtocolProfileId: string
  enabledEndpointModes: readonly AccountSupportedEndpointMode[]
}): AccountHealthCheckEndpointMode {
  const enabledModes = input.enabledEndpointModes
    .filter((mode): mode is AccountHealthCheckEndpointMode => (
      ACCOUNT_HEALTH_CHECK_ENDPOINT_MODES.includes(mode as AccountHealthCheckEndpointMode)
    ))
  const preferred = preferredHealthCheckEndpointMode(input.providerCode, input.providerProtocolProfileId)
  if (enabledModes.includes(preferred)) return preferred
  const firstEnabled = enabledModes.find((mode) => mode.endsWith('_json')) ?? enabledModes[0]
  if (firstEnabled) return firstEnabled
  throw new Error('账户至少需要启用一个可用于健康检查的请求形态')
}

export function resolveHealthCheckEndpointMode(input: {
  value: unknown
  providerCode: string
  providerProtocolProfileId: string
  enabledEndpointModes: readonly AccountSupportedEndpointMode[]
}): AccountHealthCheckEndpointMode {
  if (input.value === undefined) return resolveDefaultHealthCheckEndpointMode(input)
  if (!ACCOUNT_HEALTH_CHECK_ENDPOINT_MODES.includes(input.value as AccountHealthCheckEndpointMode)) {
    throw new Error('账户健康检查请求形态无效')
  }
  const mode = input.value as AccountHealthCheckEndpointMode
  if (!input.enabledEndpointModes.includes(mode)) {
    throw new Error(`账户健康检查请求形态 ${mode} 未启用`)
  }
  return mode
}

function preferredHealthCheckEndpointMode(
  providerCode: string,
  providerProtocolProfileId: string
): AccountHealthCheckEndpointMode {
  if (providerProtocolProfileId === 'profile_gemini_native_v1beta') return 'generate_content_json'
  if (providerProtocolProfileId.includes('anthropic')) return 'messages_json'
  if (providerCode === 'anthropic') return 'messages_json'
  if (providerCode === 'gpt') return 'responses_sse'
  return 'chat_json'
}

import type { AccountHealthCheckEndpointMode, AccountSupportedEndpointMode } from '@/types/domain'

const options: Array<{ label: string; value: AccountHealthCheckEndpointMode }> = [
  { label: 'Chat Completions（JSON）', value: 'chat_json' },
  { label: 'Chat Completions（Streaming）', value: 'chat_sse' },
  { label: 'Responses API（JSON）', value: 'responses_json' },
  { label: 'Responses API（Streaming）', value: 'responses_sse' },
  { label: 'Messages API（JSON）', value: 'messages_json' },
  { label: 'Messages API（Streaming）', value: 'messages_sse' },
  { label: 'GenerateContent（JSON）', value: 'generate_content_json' },
  { label: 'GenerateContent（Streaming）', value: 'generate_content_sse' },
  { label: 'Interactions API（JSON）', value: 'interactions_json' },
  { label: 'Interactions API（SSE）', value: 'interactions_sse' }
]

const endpointModeSet = new Set<AccountHealthCheckEndpointMode>(options.map((option) => option.value))

export function isAccountHealthCheckEndpointMode(value: unknown): value is AccountHealthCheckEndpointMode {
  return endpointModeSet.has(value as AccountHealthCheckEndpointMode)
}

export function accountHealthCheckEndpointModeOptions(endpointModes: readonly AccountSupportedEndpointMode[]) {
  return options
    .filter((option) => endpointModes.includes(option.value))
}

export function defaultAccountHealthCheckEndpointMode(
  providerCode: string,
  providerProtocolProfileId: string,
  endpointModes: readonly AccountSupportedEndpointMode[]
): AccountHealthCheckEndpointMode {
  const enabled = accountHealthCheckEndpointModeOptions(endpointModes).map((option) => option.value)
  const preferred: AccountHealthCheckEndpointMode = providerProtocolProfileId === 'profile_gemini_native_v1beta'
    ? 'generate_content_json'
    : providerProtocolProfileId.includes('anthropic') || providerCode === 'anthropic'
      ? 'messages_json'
      : providerCode === 'gpt'
        ? 'responses_sse'
        : 'chat_json'
  return enabled.includes(preferred)
    ? preferred
    : enabled.find((mode) => mode.endsWith('_json')) ?? enabled[0] ?? 'chat_json'
}

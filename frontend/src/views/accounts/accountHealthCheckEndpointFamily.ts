import type { AccountHealthCheckEndpointFamily, AccountSupportedEndpointMode } from '@/types/domain'

const options: Array<{ label: string; value: AccountHealthCheckEndpointFamily; mode: AccountSupportedEndpointMode }> = [
  { label: 'Chat Completions（JSON）', value: 'chat_completions', mode: 'chat_json' },
  { label: 'Responses（JSON）', value: 'responses', mode: 'responses_json' },
  { label: 'Messages（JSON）', value: 'messages', mode: 'messages_json' },
  { label: 'GenerateContent（JSON）', value: 'generate_content', mode: 'generate_content_json' }
]

export function accountHealthCheckEndpointFamilyOptions(endpointModes: readonly AccountSupportedEndpointMode[]) {
  return options
    .filter((option) => endpointModes.includes(option.mode))
    .map(({ label, value }) => ({ label, value }))
}

export function accountHealthCheckEndpointMode(family: AccountHealthCheckEndpointFamily): AccountSupportedEndpointMode {
  return options.find((option) => option.value === family)?.mode ?? 'chat_json'
}

export function defaultAccountHealthCheckEndpointFamily(
  providerCode: string,
  providerProtocolProfileId: string,
  endpointModes: readonly AccountSupportedEndpointMode[]
): AccountHealthCheckEndpointFamily {
  const enabled = accountHealthCheckEndpointFamilyOptions(endpointModes).map((option) => option.value)
  const preferred: AccountHealthCheckEndpointFamily = providerProtocolProfileId === 'profile_gemini_native_v1beta'
    ? 'generate_content'
    : providerProtocolProfileId.includes('anthropic') || providerCode === 'anthropic'
      ? 'messages'
      : providerCode === 'gpt'
        ? 'responses'
        : 'chat_completions'
  return enabled.includes(preferred) ? preferred : enabled[0] ?? 'chat_completions'
}

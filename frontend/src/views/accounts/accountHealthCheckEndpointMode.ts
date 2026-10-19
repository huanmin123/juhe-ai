import type { AccountHealthCheckEndpointMode, AccountSupportedEndpointMode } from '@/types/domain'

const options: Array<{ label: string; value: AccountHealthCheckEndpointMode }> = [
  { label: 'Images API', value: 'images_json' },
  { label: 'Audio Speech（TTS）', value: 'audio_speech' },
  { label: 'Audio Transcription（JSON）', value: 'audio_transcription_json' },
  { label: 'Videos API（Create）', value: 'video_create' },
  { label: 'Videos API（Get）', value: 'video_get' },
  { label: 'Videos API（Content）', value: 'video_content' },
  { label: 'Videos API（Cancel）', value: 'video_cancel' },
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

// defaultAccountHealthCheckEndpointMode 是建户表单的检查形态缺省（流式优先，
// AI账户检查模型与人工测试设计 §健康检查请求形态 2026-10-19 定案）：真实流量
// 以流式为主，非流式 2xx 可能被上游伪造成空完成，故 _sse 优先、_json 次之。
export function defaultAccountHealthCheckEndpointMode(
  providerCode: string,
  providerProtocolProfileId: string,
  endpointModes: readonly AccountSupportedEndpointMode[]
): AccountHealthCheckEndpointMode {
  const enabled = accountHealthCheckEndpointModeOptions(endpointModes).map((option) => option.value)
  const preferred: AccountHealthCheckEndpointMode = providerProtocolProfileId === 'profile_gemini_native_v1beta'
    ? 'generate_content_sse'
    : providerProtocolProfileId.includes('anthropic') || providerCode === 'anthropic'
      ? 'messages_sse'
      : providerCode === 'gpt'
        ? 'responses_sse'
        : 'chat_sse'
  return enabled.includes(preferred)
    ? preferred
    : enabled.find((mode) => mode.endsWith('_sse')) ?? enabled.find((mode) => mode.endsWith('_json')) ?? enabled[0] ?? 'chat_sse'
}

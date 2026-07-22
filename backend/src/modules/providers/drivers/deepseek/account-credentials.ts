import {
  normalizeAnthropicEndpointModesForWrite
} from '../../../../domain/anthropic-endpoint-modes.js'
import {
  OPENAI_CHAT_ENDPOINT_MODES,
  normalizeOpenAIEndpointModesForWrite
} from '../../../../domain/openai-endpoint-modes.js'
import {
  DEEPSEEK_ANTHROPIC_V1_PROFILE_ID,
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  isDeepSeekProviderCode,
  isAnthropicProtocolProfile,
  isOpenAIProtocolProfile
} from '../../../../domain/provider-protocol.js'
import type { ProviderAccountCredentialDriver } from '../_shared/account-credentials.js'

const anthropicMessagesEndpointModes = ['messages_json', 'messages_sse'] as const

export const deepSeekAccountCredentialDriver: ProviderAccountCredentialDriver = {
  id: 'deepseek',
  providerCode: DEEPSEEK_PROVIDER_CODE,
  supportsContext(context) {
    return isDeepSeekProviderCode(context.providerCode)
      && (
        (
          isOpenAIProtocolProfile(context)
          && context.providerProtocolProfileId === DEEPSEEK_OPENAI_V1_PROFILE_ID
        )
        || (
          isAnthropicProtocolProfile(context)
          && context.providerProtocolProfileId === DEEPSEEK_ANTHROPIC_V1_PROFILE_ID
        )
      )
  },
  normalizeEndpointModesForWrite(value, context) {
    if (isAnthropicProtocolProfile(context) || context.providerProtocolProfileId === DEEPSEEK_ANTHROPIC_V1_PROFILE_ID) {
      const modes = normalizeAnthropicEndpointModesForWrite(value, {
        ...context,
        providerCode: DEEPSEEK_PROVIDER_CODE,
        providerProtocolProfileId: DEEPSEEK_ANTHROPIC_V1_PROFILE_ID
      })
      const unsupported = modes.filter((mode) => !anthropicMessagesEndpointModes.includes(mode as 'messages_json' | 'messages_sse'))
      if (unsupported.length) {
        throw new Error(`DeepSeek Anthropic 账户上游接口能力只支持 Messages API (JSON) 或 Messages API (Streaming)：${unsupported.join(', ')}`)
      }
      return modes
    }
    const modes = normalizeOpenAIEndpointModesForWrite(value, {
      ...context,
      providerCode: DEEPSEEK_PROVIDER_CODE
    })
    const unsupported = modes.filter((mode) => !OPENAI_CHAT_ENDPOINT_MODES.includes(mode))
    if (unsupported.length) {
      throw new Error(`DeepSeek 账户上游接口能力只支持 Chat Completion (JSON) 或 Chat Completion (Streaming)：${unsupported.join(', ')}`)
    }
    return modes
  }
}

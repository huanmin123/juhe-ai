import {
  normalizeAnthropicEndpointModesForWrite
} from '../../../../domain/anthropic-endpoint-modes.js'
import {
  OPENAI_CHAT_ENDPOINT_MODES,
  normalizeOpenAIEndpointModesForWrite
} from '../../../../domain/openai-endpoint-modes.js'
import {
  GLM_CODING_ANTHROPIC_V1_PROFILE_ID,
  GLM_CODING_OPENAI_V1_PROFILE_ID,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE,
  isGlmProviderCode,
  isAnthropicProtocolProfile,
  isOpenAIProtocolProfile
} from '../../../../domain/provider-protocol.js'
import type { ProviderAccountCredentialDriver } from '../_shared/account-credentials.js'

const glmProfileIds = new Set([GLM_GENERAL_OPENAI_V1_PROFILE_ID, GLM_CODING_OPENAI_V1_PROFILE_ID])
const glmAnthropicProfileIds = new Set([GLM_CODING_ANTHROPIC_V1_PROFILE_ID])
const anthropicMessagesEndpointModes = ['messages_json', 'messages_sse'] as const

export const glmAccountCredentialDriver: ProviderAccountCredentialDriver = {
  id: 'glm',
  providerCode: GLM_PROVIDER_CODE,
  supportsContext(context) {
    return isGlmProviderCode(context.providerCode)
      && (
        (
          isOpenAIProtocolProfile(context)
          && Boolean(context.providerProtocolProfileId && glmProfileIds.has(context.providerProtocolProfileId))
        )
        || (
          isAnthropicProtocolProfile(context)
          && Boolean(context.providerProtocolProfileId && glmAnthropicProfileIds.has(context.providerProtocolProfileId))
        )
      )
  },
  normalizeEndpointModesForWrite(value, context) {
    if (isAnthropicProtocolProfile(context) || context.providerProtocolProfileId === GLM_CODING_ANTHROPIC_V1_PROFILE_ID) {
      const modes = normalizeAnthropicEndpointModesForWrite(value, {
        ...context,
        providerCode: GLM_PROVIDER_CODE,
        providerProtocolProfileId: GLM_CODING_ANTHROPIC_V1_PROFILE_ID
      })
      const unsupported = modes.filter((mode) => !anthropicMessagesEndpointModes.includes(mode as 'messages_json' | 'messages_sse'))
      if (unsupported.length) {
        throw new Error(`智谱 GLM Coding Anthropic 账户上游接口能力只支持 Messages API (JSON) 或 Messages API (Streaming)：${unsupported.join(', ')}`)
      }
      return modes
    }
    const modes = normalizeOpenAIEndpointModesForWrite(value, {
      ...context,
      providerCode: GLM_PROVIDER_CODE
    })
    const unsupported = modes.filter((mode) => !OPENAI_CHAT_ENDPOINT_MODES.includes(mode))
    if (unsupported.length) {
      const chatCapabilityName = context.providerProtocolProfileId === GLM_CODING_OPENAI_V1_PROFILE_ID
        ? 'OpenAI Chat Completions'
        : '对话补全'
      throw new Error(`智谱 GLM 账户上游接口能力只支持 ${chatCapabilityName} (JSON) 或 ${chatCapabilityName} (Streaming)：${unsupported.join(', ')}`)
    }
    return modes
  }
}

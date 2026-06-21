import {
  OPENAI_CHAT_ENDPOINT_MODES,
  normalizeOpenAIEndpointModesForWrite
} from '../../../../domain/openai-endpoint-modes.js'
import {
  GLM_CODING_OPENAI_V1_PROFILE_ID,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE,
  isGlmProviderCode,
  isOpenAIProtocolProfile
} from '../../../../domain/provider-protocol.js'
import type { ProviderAccountCredentialDriver } from '../_shared/account-credentials.js'

const glmProfileIds = new Set([GLM_GENERAL_OPENAI_V1_PROFILE_ID, GLM_CODING_OPENAI_V1_PROFILE_ID])

export const glmAccountCredentialDriver: ProviderAccountCredentialDriver = {
  id: 'glm',
  providerCode: GLM_PROVIDER_CODE,
  supportsContext(context) {
    return isGlmProviderCode(context.providerCode)
      && isOpenAIProtocolProfile(context)
      && (!context.providerProtocolProfileId || glmProfileIds.has(context.providerProtocolProfileId))
  },
  normalizeEndpointModesForWrite(value, context) {
    const modes = normalizeOpenAIEndpointModesForWrite(value, {
      ...context,
      providerCode: GLM_PROVIDER_CODE
    })
    const unsupported = modes.filter((mode) => !OPENAI_CHAT_ENDPOINT_MODES.includes(mode))
    if (unsupported.length) {
      throw new Error(`智谱 GLM 账户接口能力只支持 Chat JSON 或 Chat SSE：${unsupported.join(', ')}`)
    }
    return modes
  }
}

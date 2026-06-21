import {
  OPENAI_CHAT_ENDPOINT_MODES,
  normalizeOpenAIEndpointModesForWrite
} from '../../../../domain/openai-endpoint-modes.js'
import {
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE,
  isDeepSeekProviderCode,
  isOpenAIProtocolProfile
} from '../../../../domain/provider-protocol.js'
import type { ProviderAccountCredentialDriver } from '../_shared/account-credentials.js'

export const deepSeekAccountCredentialDriver: ProviderAccountCredentialDriver = {
  id: 'deepseek',
  providerCode: DEEPSEEK_PROVIDER_CODE,
  supportsContext(context) {
    return isDeepSeekProviderCode(context.providerCode)
      && isOpenAIProtocolProfile(context)
      && (!context.providerProtocolProfileId || context.providerProtocolProfileId === DEEPSEEK_OPENAI_V1_PROFILE_ID)
  },
  normalizeEndpointModesForWrite(value, context) {
    const modes = normalizeOpenAIEndpointModesForWrite(value, {
      ...context,
      providerCode: DEEPSEEK_PROVIDER_CODE
    })
    const unsupported = modes.filter((mode) => !OPENAI_CHAT_ENDPOINT_MODES.includes(mode))
    if (unsupported.length) {
      throw new Error(`DeepSeek 账户接口能力只支持 Chat JSON 或 Chat SSE：${unsupported.join(', ')}`)
    }
    return modes
  }
}

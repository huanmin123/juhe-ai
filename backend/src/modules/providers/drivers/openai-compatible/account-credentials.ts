import { normalizeOpenAIEndpointModesForWrite } from '../../../../domain/openai-endpoint-modes.js'
import {
  GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
  GEMINI_PROVIDER_CODE,
  OPENAI_COMPATIBLE_PROVIDER_CODE,
  isOpenAIProtocolProfile
} from '../../../../domain/provider-protocol.js'
import type { ProviderAccountCredentialDriver } from '../_shared/account-credentials.js'

function isOpenAICompatibleCredentialContext(context: Parameters<ProviderAccountCredentialDriver['supportsContext']>[0]): boolean {
  const profileId = context.providerProtocolProfileId
  return context.providerCode === OPENAI_COMPATIBLE_PROVIDER_CODE
    || (context.providerCode === GEMINI_PROVIDER_CODE && profileId === GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID)
}

export const openAICompatibleAccountCredentialDriver: ProviderAccountCredentialDriver = {
  id: 'openai-compatible',
  providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
  supportsContext(context) {
    return isOpenAICompatibleCredentialContext(context)
      && isOpenAIProtocolProfile(context)
  },
  normalizeEndpointModesForWrite(value, context) {
    return normalizeOpenAIEndpointModesForWrite(value, context)
  }
}

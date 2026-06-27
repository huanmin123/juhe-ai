import { normalizeAnthropicEndpointModesForWrite } from '../../../../domain/anthropic-endpoint-modes.js'
import { normalizeGeminiEndpointModesForWrite } from '../../../../domain/gemini-endpoint-modes.js'
import { normalizeOpenAIEndpointModesForWrite } from '../../../../domain/openai-endpoint-modes.js'
import {
  HYBRID_PROVIDER_CODE,
  isAnthropicProtocolProfile,
  isGeminiProtocolProfile,
  isHybridProviderCode,
  isOpenAIProtocolProfile
} from '../../../../domain/provider-protocol.js'
import type { ProviderAccountCredentialDriver } from '../_shared/account-credentials.js'

export const hybridAccountCredentialDriver: ProviderAccountCredentialDriver = {
  id: 'hybrid',
  providerCode: HYBRID_PROVIDER_CODE,
  supportsContext(context) {
    return isHybridProviderCode(context.providerCode)
      && (isOpenAIProtocolProfile(context) || isAnthropicProtocolProfile(context) || isGeminiProtocolProfile(context))
  },
  normalizeEndpointModesForWrite(value, context) {
    if (isAnthropicProtocolProfile(context)) {
      return normalizeAnthropicEndpointModesForWrite(value, context)
    }
    if (isGeminiProtocolProfile(context)) {
      return normalizeGeminiEndpointModesForWrite(value, context)
    }
    return normalizeOpenAIEndpointModesForWrite(value, context)
  }
}

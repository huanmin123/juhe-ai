import { normalizeGeminiEndpointModesForWrite } from '../../../../domain/gemini-endpoint-modes.js'
import {
  GEMINI_PROVIDER_CODE,
  isGeminiProtocolProfile
} from '../../../../domain/provider-protocol.js'
import type { ProviderAccountCredentialDriver } from '../_shared/account-credentials.js'

export const geminiAccountCredentialDriver: ProviderAccountCredentialDriver = {
  id: 'gemini',
  providerCode: GEMINI_PROVIDER_CODE,
  supportsContext(context) {
    return context.providerCode === GEMINI_PROVIDER_CODE
      && isGeminiProtocolProfile(context)
  },
  normalizeEndpointModesForWrite(value, context) {
    return normalizeGeminiEndpointModesForWrite(value, context)
  }
}

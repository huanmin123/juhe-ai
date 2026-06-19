import { normalizeOpenAIEndpointModesForWrite } from '../../../../domain/openai-endpoint-modes.js'
import {
  OPENAI_COMPATIBLE_PROVIDER_CODE,
  isOpenAIProtocolProfile
} from '../../../../domain/provider-protocol.js'
import type { ProviderAccountCredentialDriver } from '../_shared/account-credentials.js'

export const openAICompatibleAccountCredentialDriver: ProviderAccountCredentialDriver = {
  id: 'openai-compatible',
  providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
  supportsContext(context) {
    return context.providerCode === OPENAI_COMPATIBLE_PROVIDER_CODE
      && isOpenAIProtocolProfile(context)
  },
  normalizeEndpointModesForWrite(value, context) {
    return normalizeOpenAIEndpointModesForWrite(value, context)
  }
}

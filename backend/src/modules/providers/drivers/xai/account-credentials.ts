import { normalizeOpenAIEndpointModesForWrite } from '../../../../domain/openai-endpoint-modes.js'
import {
  XAI_OPENAI_V1_PROFILE_ID,
  XAI_PROVIDER_CODE,
  isOpenAIProtocolProfile,
  isXaiProviderCode
} from '../../../../domain/provider-protocol.js'
import type { ProviderAccountCredentialDriver } from '../_shared/account-credentials.js'

export const xaiAccountCredentialDriver: ProviderAccountCredentialDriver = {
  id: 'xai',
  providerCode: XAI_PROVIDER_CODE,
  supportsContext(context) {
    return context.accountType === 'api_key'
      && isXaiProviderCode(context.providerCode)
      && isOpenAIProtocolProfile(context)
      && context.providerProtocolProfileId === XAI_OPENAI_V1_PROFILE_ID
  },
  normalizeEndpointModesForWrite(value, context) {
    return normalizeOpenAIEndpointModesForWrite(value, {
      ...context,
      providerCode: XAI_PROVIDER_CODE,
      providerProtocolProfileId: XAI_OPENAI_V1_PROFILE_ID,
      accountType: 'api_key'
    })
  }
}

import { normalizeOpenAIEndpointModesForWrite } from '../../../../domain/openai-endpoint-modes.js'
import {
  GPT_VENDOR_CODE,
  isGptVendorCode,
  isOpenAIProtocolProfile
} from '../../../../domain/provider-protocol.js'
import type { ProviderAccountCredentialDriver } from '../_shared/account-credentials.js'

export const gptAccountCredentialDriver: ProviderAccountCredentialDriver = {
  id: 'gpt',
  providerCode: GPT_VENDOR_CODE,
  supportsContext(context) {
    return isGptVendorCode(context.providerCode)
      && isOpenAIProtocolProfile(context)
  },
  normalizeEndpointModesForWrite(value, context) {
    return normalizeOpenAIEndpointModesForWrite(value, context)
  }
}

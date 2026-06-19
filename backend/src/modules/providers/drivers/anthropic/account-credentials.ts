import { normalizeAnthropicEndpointModesForWrite } from '../../../../domain/anthropic-endpoint-modes.js'
import {
  ANTHROPIC_PROVIDER_CODE,
  isAnthropicProtocolProfile
} from '../../../../domain/provider-protocol.js'
import type { ProviderAccountCredentialDriver } from '../_shared/account-credentials.js'

export const anthropicAccountCredentialDriver: ProviderAccountCredentialDriver = {
  id: 'anthropic',
  providerCode: ANTHROPIC_PROVIDER_CODE,
  supportsContext(context) {
    return context.providerCode === ANTHROPIC_PROVIDER_CODE
      && isAnthropicProtocolProfile(context)
  },
  normalizeEndpointModesForWrite(value, context) {
    return normalizeAnthropicEndpointModesForWrite(value, context)
  }
}

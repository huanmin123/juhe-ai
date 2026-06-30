import type { ProviderProtocolProfileDefinition } from '../../domain/provider-protocol.js'
import {
  isModelCheckSupportedAccountProfile,
  modelCheckSupportedProtocolLabel
} from './model-checks.profiles.js'

export function isModelCheckSupportedProtocolProfile(profile: ProviderProtocolProfileDefinition | undefined): boolean {
  return isModelCheckSupportedAccountProfile(profile)
}

export { modelCheckSupportedProtocolLabel }

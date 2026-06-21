import type {
  AccountClientCompatibility,
  AccountSupportedEndpointMode,
  ProviderCode
} from '../../../../domain/types.js'

export interface ProviderAccountCredentialContext {
  providerCode?: ProviderCode | string
  accountType?: string
  clientCompatibility?: AccountClientCompatibility
  protocolCode?: string
  protocolVersion?: string
  providerProtocolProfileId?: string
}

export interface ProviderAccountCredentialDriver {
  id: string
  providerCode: ProviderCode
  supportsContext(context: ProviderAccountCredentialContext): boolean
  normalizeEndpointModesForWrite(
    value: unknown,
    context: ProviderAccountCredentialContext
  ): AccountSupportedEndpointMode[]
}

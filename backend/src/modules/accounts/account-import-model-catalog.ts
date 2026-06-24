import { type AccountModelMapping, type AccountSupportedEndpointMode, type ProviderDefinition } from '../../domain/types.js'
import {
  assertAccountModelMappingUpstreamsAllowedBySupportedModels,
  normalizeAccountModelMappingsForProvider,
  normalizeAccountSupportedModelsForProvider
} from '../../storage/repositories.js'
import { errorMessage } from './account-import-field-parser.js'

export interface AccountImportModelCatalogContext {
  providerByCode: Map<string, ProviderDefinition>
  targetSystemAccountId?: string
}

export interface AccountImportModelCatalogAccount {
  providerCode: string
  providerProtocolProfileId?: string
  credentials?: Record<string, unknown>
  supportedModels?: string[]
  modelMappings?: AccountModelMapping[]
  messages: string[]
}

export function validateAccountModelCatalogFields(
  account: AccountImportModelCatalogAccount,
  context: AccountImportModelCatalogContext
): void {
  if (!account.providerCode || !context.providerByCode.has(account.providerCode) || !context.targetSystemAccountId) {
    return
  }
  try {
    account.supportedModels = normalizeAccountSupportedModelsForProvider(
      account.supportedModels,
      account.providerCode,
      context.targetSystemAccountId
    )
    account.modelMappings = normalizeAccountModelMappingsForProvider(
      account.modelMappings,
      account.providerCode,
      context.targetSystemAccountId,
      context.providerByCode.get(account.providerCode)?.protocolProfiles.find((profile) => profile.id === account.providerProtocolProfileId),
      {
        supportedEndpointModes: Array.isArray(account.credentials?.supported_endpoint_modes)
          ? account.credentials.supported_endpoint_modes as AccountSupportedEndpointMode[]
          : undefined
      }
    )
    assertAccountModelMappingUpstreamsAllowedBySupportedModels(account.modelMappings ?? [], account.supportedModels ?? [])
  } catch (error) {
    account.messages.push(errorMessage(error))
  }
}

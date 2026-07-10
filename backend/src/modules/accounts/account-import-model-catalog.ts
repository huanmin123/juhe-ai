import { type AccountModelMapping, type AccountSupportedEndpointMode, type ProviderDefinition } from '../../domain/types.js'
import {
  assertAccountModelMappingUpstreamsAllowedBySupportedModels,
  assertAccountSupportedModelsRequired,
  normalizeAccountModelMappingsForProvider,
  normalizeAccountModelMappingsForProviderAsync,
  normalizeAccountSupportedModelsForProvider,
  normalizeAccountSupportedModelsForProviderAsync
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
  healthCheckModel?: string
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
    const provider = context.providerByCode.get(account.providerCode)
    account.supportedModels = normalizeAccountSupportedModelsForProvider(
      account.supportedModels?.length ? account.supportedModels : provider?.defaultSupportedModels,
      account.providerCode,
      context.targetSystemAccountId
    )
    assertAccountSupportedModelsRequired(account.supportedModels ?? [])
    assertImportedAccountHealthCheckModel(account.healthCheckModel, account.supportedModels ?? [])
    account.modelMappings = normalizeAccountModelMappingsForProvider(
      account.modelMappings,
      account.providerCode,
      context.targetSystemAccountId,
      provider?.protocolProfiles.find((profile) => profile.id === account.providerProtocolProfileId),
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

export async function validateAccountModelCatalogFieldsAsync(
  account: AccountImportModelCatalogAccount,
  context: AccountImportModelCatalogContext
): Promise<void> {
  if (!account.providerCode || !context.providerByCode.has(account.providerCode) || !context.targetSystemAccountId) {
    return
  }
  try {
    const provider = context.providerByCode.get(account.providerCode)
    account.supportedModels = await normalizeAccountSupportedModelsForProviderAsync(
      account.supportedModels?.length ? account.supportedModels : provider?.defaultSupportedModels,
      account.providerCode,
      context.targetSystemAccountId
    )
    assertAccountSupportedModelsRequired(account.supportedModels ?? [])
    assertImportedAccountHealthCheckModel(account.healthCheckModel, account.supportedModels ?? [])
    account.modelMappings = await normalizeAccountModelMappingsForProviderAsync(
      account.modelMappings,
      account.providerCode,
      context.targetSystemAccountId,
      provider?.protocolProfiles.find((profile) => profile.id === account.providerProtocolProfileId),
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

function assertImportedAccountHealthCheckModel(healthCheckModel: string | undefined, supportedModels: readonly string[]): void {
  if (!healthCheckModel || supportedModels.includes(healthCheckModel)) return
  throw new Error('账户 healthCheckModel 必须属于 supportedModels')
}

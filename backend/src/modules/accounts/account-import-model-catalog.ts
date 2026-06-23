import { type AccountModelMapping, type ProviderDefinition } from '../../domain/types.js'
import {
  assertAccountModelMappingSourcesAllowedBySupportedModels,
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
      context.targetSystemAccountId
    )
    assertAccountModelMappingSourcesAllowedBySupportedModels(account.modelMappings ?? [], account.supportedModels ?? [])
  } catch (error) {
    account.messages.push(errorMessage(error))
  }
}

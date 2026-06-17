import { type AccountAvailabilitySchedule, type AccountClientCompatibility, type AccountModelMapping, type AccountType } from '../../domain/types.js'
import { type AccountImportStatus } from './account-import-field-parser.js'

export interface AccountImportCreatePayloadAccount {
  providerCode: string
  providerProtocolProfileId?: string
  clientCompatibility?: AccountClientCompatibility
  name: string
  type: AccountType
  status: AccountImportStatus
  credentials: Record<string, unknown>
  concurrencyLimit?: number
  priority?: number
  superPriorityEnabled?: boolean
  fallbackEnabled?: boolean
  supportedModels?: string[]
  modelMappings?: AccountModelMapping[]
  tags?: string[]
  accountExpiresAt?: string
  availabilitySchedule?: AccountAvailabilitySchedule
  notes?: string
}

export interface AccountImportCreatePayloadOptions {
  groupId?: string
  proxyProfileId?: string
}

export function buildAccountImportCreatePayload(
  account: AccountImportCreatePayloadAccount,
  options: AccountImportCreatePayloadOptions = {}
): Record<string, unknown> {
  const payload: Record<string, unknown> = {
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    clientCompatibility: account.clientCompatibility,
    name: account.name,
    type: account.type,
    status: accountImportCreateStatus(account.status),
    credentials: account.credentials
  }
  if (options.groupId !== undefined) payload.groupId = options.groupId
  if (options.proxyProfileId !== undefined) payload.proxyProfileId = options.proxyProfileId
  if (account.concurrencyLimit !== undefined) payload.concurrencyLimit = account.concurrencyLimit
  if (account.priority !== undefined) payload.priority = account.priority
  if (account.superPriorityEnabled !== undefined) payload.superPriorityEnabled = account.superPriorityEnabled
  if (account.fallbackEnabled !== undefined) payload.fallbackEnabled = account.fallbackEnabled
  if (account.supportedModels !== undefined) payload.supportedModels = account.supportedModels
  if (account.modelMappings !== undefined) payload.modelMappings = account.modelMappings
  if (account.tags !== undefined) payload.tags = account.tags
  if (account.accountExpiresAt !== undefined) payload.accountExpiresAt = account.accountExpiresAt
  if (account.availabilitySchedule !== undefined) payload.availabilitySchedule = account.availabilitySchedule
  if (account.notes !== undefined) payload.notes = account.notes
  return payload
}

function accountImportCreateStatus(status: AccountImportStatus): AccountImportStatus {
  return status === 'active' ? 'pending_test' : status
}

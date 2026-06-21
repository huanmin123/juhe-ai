import { type AccountAvailabilitySchedule, type AccountClientCompatibility, type AccountModelMapping, type AccountType } from '../../domain/types.js'
import { normalizeOpenAIAccountClientCompatibility } from '../../domain/account-client-compatibility.js'
import { resolveProviderProtocolProfileIdFromConnectionType } from '../../domain/provider-connection-type.js'
import { assertOpenAIEndpointModesCompatible } from '../../domain/openai-endpoint-modes.js'
import { assertAnthropicEndpointModesCompatible } from '../../domain/anthropic-endpoint-modes.js'
import { isAnthropicProtocolProfile, isOpenAIProtocolProfile } from '../../domain/provider-protocol.js'
import type { AccountSupportedEndpointMode } from '../../domain/types.js'
import { normalizeAccountCredentialsForWrite } from '../../storage/repositories.js'
import {
  appendUnknownFieldMessages,
  errorMessage,
  importAccountKeys,
  importAvailabilityScheduleInput,
  isRecord,
  normalizeImportAvailabilitySchedule,
  normalizeStatus,
  optionalAccountTagsField,
  optionalBooleanField,
  optionalDateTimeField,
  optionalModelMappingsField,
  optionalNonNegativeIntegerField,
  optionalPositiveIntegerField,
  optionalStringArrayField,
  optionalTextField,
  type AccountImportStatus
} from './account-import-field-parser.js'
import { type AccountImportGroupCreateMap } from './account-import-plan.js'
import {
  resolveAccountGroup,
  resolveAccountProxy,
  type AccountImportProxyReferencePlan,
  type AccountImportResourceContext
} from './account-import-resource-resolver.js'
import {
  validateImportAccountProviderAndBasics,
  type AccountImportProviderContext
} from './account-import-provider-resolver.js'
import { validateAccountModelCatalogFields } from './account-import-model-catalog.js'
import type { AccountImportItem } from './account-import.service.js'

export interface NormalizedImportAccount {
  index: number
  ref?: string
  name: string
  providerCode: string
  providerProtocolProfileId?: string
  connectionType?: string
  protocolCode?: string
  protocolVersion?: string
  clientCompatibility?: AccountClientCompatibility
  type: AccountType
  status: AccountImportStatus
  credentials: Record<string, unknown>
  groupId?: string
  groupName?: string
  proxyRef?: string
  proxyProfileId?: string
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
  messages: string[]
  warnings: string[]
}

export interface AccountImportAccountPlan {
  source: NormalizedImportAccount
  item: AccountImportItem
  groupId?: string
  proxyProfileId?: string
}

export type AccountImportAccountPlanContext = AccountImportResourceContext & AccountImportProviderContext

export function planImportAccount(
  value: unknown,
  index: number,
  context: AccountImportAccountPlanContext,
  proxyByRef: Map<string, AccountImportProxyReferencePlan>,
  groupIdsByKey: Map<string, string>,
  groupNamesToCreate: AccountImportGroupCreateMap
): AccountImportAccountPlan {
  const item: AccountImportItem = { index, action: 'create', messages: [], warnings: [] }
  const source: NormalizedImportAccount = {
    index,
    name: '',
    providerCode: '',
    type: 'api_key',
    status: 'active',
    credentials: {},
    messages: item.messages,
    warnings: item.warnings
  }
  if (!isRecord(value)) {
    item.action = 'failed'
    item.messages.push('账户配置必须是对象')
    return { source, item }
  }
  appendUnknownFieldMessages(value, importAccountKeys, '账户配置', item.messages)
  source.ref = optionalTextField(value, 'ref', '账户 ref', item.messages)
  source.name = optionalTextField(value, 'name', '账户名称', item.messages) ?? ''
  source.providerCode = optionalTextField(value, 'providerCode', '账户 providerCode', item.messages) ?? ''
  source.providerProtocolProfileId = optionalTextField(value, 'providerProtocolProfileId', '账户 providerProtocolProfileId', item.messages)
  source.connectionType = optionalTextField(value, 'connectionType', '账户 connectionType', item.messages)
  source.clientCompatibility = optionalTextField(value, 'clientCompatibility', '账户 clientCompatibility', item.messages) as AccountClientCompatibility | undefined
  if (!source.providerCode) {
    item.messages.push('账户 providerCode 不能为空')
  }
  const typeInput = optionalTextField(value, 'type', '账户 type', item.messages)
  if (typeInput) {
    source.type = typeInput
  } else {
    item.messages.push('账户 type 不能为空')
  }
  const rawStatus = optionalTextField(value, 'status', '账户 status', item.messages)
  if (rawStatus !== undefined) {
    const normalizedStatus = normalizeStatus(rawStatus)
    if (normalizedStatus) {
      source.status = normalizedStatus
    } else {
      item.messages.push(`账户状态不支持：${rawStatus}`)
    }
  } else {
    item.messages.push('账户 status 不能为空')
  }
  source.groupId = optionalTextField(value, 'groupId', '账户 groupId', item.messages)
  source.groupName = optionalTextField(value, 'groupName', '账户 groupName', item.messages)
  source.proxyRef = optionalTextField(value, 'proxyRef', '账户 proxyRef', item.messages)
  source.proxyProfileId = optionalTextField(value, 'proxyProfileId', '账户 proxyProfileId', item.messages)
  source.concurrencyLimit = optionalPositiveIntegerField(value, 'concurrencyLimit', '账户 concurrencyLimit', item.messages)
  source.priority = optionalNonNegativeIntegerField(value, 'priority', '账户 priority', item.messages)
  source.superPriorityEnabled = optionalBooleanField(value, 'superPriorityEnabled', '账户 superPriorityEnabled', item.messages)
  source.fallbackEnabled = optionalBooleanField(value, 'fallbackEnabled', '账户 fallbackEnabled', item.messages)
  source.supportedModels = optionalStringArrayField(value, 'supportedModels', '账户 supportedModels', item.messages)
  source.modelMappings = optionalModelMappingsField(value, 'modelMappings', '账户 modelMappings', item.messages)
  source.tags = optionalAccountTagsField(value, 'tags', '账户 tags', item.messages)
  source.accountExpiresAt = optionalDateTimeField(value, 'accountExpiresAt', '账户 accountExpiresAt', item.messages)
  const availabilityScheduleInput = importAvailabilityScheduleInput(value)
  source.availabilitySchedule = availabilityScheduleInput.present
    ? normalizeImportAvailabilitySchedule(availabilityScheduleInput.value, item.messages)
    : undefined
  source.notes = optionalTextField(value, 'notes', '账户 notes', item.messages)
  applyImportAccountProtocolProfileDefaults(source, context)
  try {
    const clientCompatibility = normalizeOpenAIAccountClientCompatibility(
      source.providerCode,
      source.type,
      source.clientCompatibility,
      'openai_standard',
      {
        providerCode: source.providerCode,
        providerProtocolProfileId: source.providerProtocolProfileId,
        protocolCode: source.protocolCode,
        protocolVersion: source.protocolVersion
      }
    )
    source.clientCompatibility = clientCompatibility
    source.credentials = normalizeAccountCredentialsForWrite(source.type, value.credentials, {
      providerCode: source.providerCode,
      accountType: source.type,
      clientCompatibility,
      providerProtocolProfileId: source.providerProtocolProfileId,
      protocolCode: source.protocolCode,
      protocolVersion: source.protocolVersion
    })
  } catch (error) {
    item.messages.push(errorMessage(error))
  }

  item.ref = source.ref
  item.name = source.name
  item.providerCode = source.providerCode
  item.connectionType = source.connectionType
  item.groupName = source.groupName
  item.groupId = source.groupId
  item.proxyRef = source.proxyRef

  validateImportAccountProviderAndBasics(source, context)
  normalizeImportAccountEffectiveClientCompatibility(source)
  validateImportAccountEndpointModes(source)
  item.providerProtocolProfileId = source.providerProtocolProfileId
  item.protocolCode = source.protocolCode
  item.protocolVersion = source.protocolVersion
  item.accountType = source.type
  validateAccountModelCatalogFields(source, context)
  const groupId = resolveAccountGroup(source, context, groupIdsByKey, groupNamesToCreate, item)
  const proxyProfileId = resolveAccountProxy(source, proxyByRef, item)
  if (item.messages.length > 0) {
    item.action = 'failed'
  }
  return {
    source,
    item,
    groupId,
    proxyProfileId
  }
}

function normalizeImportAccountEffectiveClientCompatibility(account: NormalizedImportAccount): void {
  try {
    account.clientCompatibility = normalizeOpenAIAccountClientCompatibility(
      account.providerCode,
      account.type,
      account.clientCompatibility,
      'openai_standard',
      {
        providerCode: account.providerCode,
        providerProtocolProfileId: account.providerProtocolProfileId,
        protocolCode: account.protocolCode,
        protocolVersion: account.protocolVersion
      }
    )
  } catch (error) {
    account.messages.push(errorMessage(error))
  }
}

function validateImportAccountEndpointModes(account: NormalizedImportAccount): void {
  if (!Array.isArray(account.credentials.supported_endpoint_modes)) return
  try {
    const clientCompatibility = normalizeOpenAIAccountClientCompatibility(
      account.providerCode,
      account.type,
      account.clientCompatibility,
      'openai_standard',
      {
        providerCode: account.providerCode,
        providerProtocolProfileId: account.providerProtocolProfileId,
        protocolCode: account.protocolCode,
        protocolVersion: account.protocolVersion
      }
    )
    assertImportEndpointModesCompatible(account, {
      modes: account.credentials.supported_endpoint_modes as AccountSupportedEndpointMode[],
      accountType: account.type,
      clientCompatibility
    })
  } catch (error) {
    account.messages.push(errorMessage(error))
  }
}

function applyImportAccountProtocolProfileDefaults(account: NormalizedImportAccount, context: AccountImportProviderContext): void {
  const provider = context.providerByCode.get(account.providerCode)
  if (!provider) return
  let requestedProfileId: string | undefined
  try {
    requestedProfileId = resolveProviderProtocolProfileIdFromConnectionType({
      providerCode: account.providerCode,
      providerProtocolProfileId: account.providerProtocolProfileId,
      connectionType: account.connectionType
    })
  } catch (error) {
    account.messages.push(errorMessage(error))
    return
  }
  const profile = requestedProfileId
    ? provider.protocolProfiles.find((item) => item.id === requestedProfileId)
    : provider.protocolProfiles.find((item) => item.id === provider.defaultProtocolProfileId)
      ?? provider.protocolProfiles.find((item) => item.enabled)
      ?? provider.protocolProfiles[0]
  if (!profile || profile.providerCode !== account.providerCode) return
  account.providerProtocolProfileId = profile.id
  account.protocolCode = profile.protocolCode
  account.protocolVersion = profile.protocolVersion
}

function assertImportEndpointModesCompatible(
  account: Pick<NormalizedImportAccount, 'providerCode' | 'providerProtocolProfileId' | 'protocolCode' | 'protocolVersion'>,
  input: {
    modes: readonly AccountSupportedEndpointMode[]
    accountType?: string
    clientCompatibility: AccountClientCompatibility
  }
): void {
  if (isAnthropicProtocolProfile(account)) {
    assertAnthropicEndpointModesCompatible({
      modes: input.modes,
      accountType: input.accountType
    })
    return
  }
  if (isOpenAIProtocolProfile(account)) {
    assertOpenAIEndpointModesCompatible({
      modes: input.modes,
      providerCode: account.providerCode,
      providerProtocolProfileId: account.providerProtocolProfileId,
      accountType: input.accountType,
      clientCompatibility: input.clientCompatibility
    })
  }
}


import type { AccountSummary } from '../../domain/types.js'
import type {
  PublicAccountPushInput,
  PublicAccountUpdateInput,
  PublicApiKeyAddInput,
  PublicApiKeyUpdateInput,
  PublicGroupUpdateInput
} from './external-public-account-push.types.js'

export function accountCreateInputForPush(input: PublicAccountPushInput, providerCode: string, providerProtocolProfileId: string, groupId: string): Record<string, unknown> {
  return {
    ...accountWriteInputForPush(input),
    providerCode,
    providerProtocolProfileId,
    type: input.type,
    groupId,
    status: input.status === 'disabled' ? 'disabled' : 'pending_test',
    schedulable: false
  }
}

export function accountPartialUpdateInputForPush(input: PublicAccountUpdateInput, current: AccountSummary): Record<string, unknown> {
  const payload: Record<string, unknown> = {}
  if (hasPublicInput(input, 'name')) {
    const name = normalizedText(input.name)
    if (!name) {
      throw new Error('账户名称不能为空')
    }
    payload.name = name
  }
  if (hasPublicInput(input, 'apiKey') || hasPublicInput(input, 'baseUrl')) {
    payload.credentials = accountCredentialsForPartialUpdate(input, current)
  }
  if (hasPublicInput(input, 'supportedModels')) {
    payload.supportedModels = normalizedStringList(input.supportedModels) ?? []
  }
  if (hasPublicInput(input, 'clientCompatibility')) {
    payload.clientCompatibility = input.clientCompatibility
  }
  if (hasPublicInput(input, 'status')) {
    payload.status = input.status === 'disabled' ? 'disabled' : 'active'
    payload.schedulable = input.status !== 'disabled'
  }
  if (hasPublicInput(input, 'concurrencyLimit')) {
    payload.concurrencyLimit = boundedInteger(input.concurrencyLimit, 1, 100_000)
  }
  if (hasPublicInput(input, 'priority')) {
    payload.priority = boundedInteger(input.priority, 0, 100_000) ?? 0
  }
  if (hasPublicInput(input, 'notes')) {
    payload.notes = pushNotes(input)
  }
  if (hasPublicInput(input, 'availabilitySchedule')) {
    payload.availabilitySchedule = input.availabilitySchedule
  }
  if (Object.keys(payload).length === 0) {
    throw new Error('账号修改至少提供一个要修改的字段')
  }
  return payload
}

export function publicApiKeyPayload(input: PublicApiKeyAddInput | PublicApiKeyUpdateInput, partial = false): Record<string, unknown> {
  const payload: Record<string, unknown> = {}
  if ('name' in input && input.name !== undefined) payload.name = input.name
  if ('description' in input && input.description !== undefined) payload.description = input.description
  if ('status' in input && input.status !== undefined) payload.status = input.status
  if ('expiresAt' in input && input.expiresAt !== undefined) payload.expiresAt = input.expiresAt
  if ('quotaLimits' in input && input.quotaLimits !== undefined) payload.quotaLimits = input.quotaLimits
  if ('availabilitySchedule' in input && input.availabilitySchedule !== undefined) payload.availabilitySchedule = input.availabilitySchedule
  if ('availabilityScheduleActive' in input && input.availabilityScheduleActive !== undefined) payload.availabilityScheduleActive = input.availabilityScheduleActive
  if ('groupRouteStrategy' in input && input.groupRouteStrategy !== undefined) payload.groupRouteStrategy = input.groupRouteStrategy
  if (input.groupBindings?.length) {
    payload.groupBindings = input.groupBindings
  }
  if (!partial && !payload.groupBindings) {
    throw new Error('API Key 至少需要绑定一个分组')
  }
  if (partial && Object.keys(payload).length === 0) {
    throw new Error('API Key 修改至少提供一个要修改的字段')
  }
  return payload
}

export function publicGroupUpdatePayload(input: PublicGroupUpdateInput): Record<string, unknown> {
  const payload: Record<string, unknown> = {}
  if (input.name !== undefined) payload.name = input.name
  if (input.providerCode !== undefined) payload.providerCode = input.providerCode
  if (input.providerProtocolProfileId !== undefined) payload.providerProtocolProfileId = input.providerProtocolProfileId
  if (input.description !== undefined) payload.description = input.description
  if (input.enabled !== undefined) payload.enabled = input.enabled
  if (input.groupType !== undefined) payload.groupType = input.groupType
  if (Object.keys(payload).length === 0) {
    throw new Error('分组修改至少提供一个要修改的字段')
  }
  return payload
}

function accountWriteInputForPush(input: PublicAccountPushInput): Record<string, unknown> {
  const name = normalizedText(input.name)
  const baseUrl = normalizedText(input.baseUrl)
  const apiKey = normalizedText(input.apiKey)
  if (!name) {
    throw new Error('账户名称不能为空')
  }
  if (!baseUrl) {
    throw new Error('Base URL 不能为空')
  }
  if (!apiKey) {
    throw new Error('API Key 不能为空')
  }

  const payload: Record<string, unknown> = {
    name,
    credentials: {
      api_key: apiKey,
      base_url: baseUrl
    }
  }
  if (hasPublicInput(input, 'supportedModels')) {
    payload.supportedModels = normalizedStringList(input.supportedModels) ?? []
  }
  if (hasPublicInput(input, 'clientCompatibility')) {
    payload.clientCompatibility = input.clientCompatibility
  }
  if (hasPublicInput(input, 'status')) {
    payload.status = input.status === 'disabled' ? 'disabled' : 'active'
    payload.schedulable = input.status !== 'disabled'
  }
  if (hasPublicInput(input, 'concurrencyLimit')) {
    payload.concurrencyLimit = boundedInteger(input.concurrencyLimit, 1, 100_000)
  }
  if (hasPublicInput(input, 'priority')) {
    payload.priority = boundedInteger(input.priority, 0, 100_000) ?? 0
  }
  if (hasPublicInput(input, 'notes')) {
    payload.notes = pushNotes(input)
  }
  if (hasPublicInput(input, 'availabilitySchedule')) {
    payload.availabilitySchedule = input.availabilitySchedule
  }
  return payload
}

function accountCredentialsForPartialUpdate(input: PublicAccountUpdateInput, current: AccountSummary): Record<string, unknown> {
  const currentCredentials = current.credentials as Record<string, unknown>
  const apiKey = hasPublicInput(input, 'apiKey')
    ? normalizedText(input.apiKey)
    : normalizedText(currentCredentials.api_key)
  const baseUrl = hasPublicInput(input, 'baseUrl')
    ? normalizedText(input.baseUrl)
    : normalizedText(currentCredentials.base_url)
  if (!baseUrl) {
    throw new Error('Base URL 不能为空')
  }
  if (!apiKey) {
    throw new Error('API Key 不能为空')
  }
  return {
    ...currentCredentials,
    api_key: apiKey,
    base_url: baseUrl
  }
}

function pushNotes(input: Pick<PublicAccountPushInput, 'notes'>): string | undefined {
  return normalizedText(input.notes)
}

function normalizedText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function normalizedStringList(values: readonly string[] | undefined): string[] | undefined {
  const normalized = [...new Set((values ?? []).map((item) => item.trim()).filter(Boolean))]
  return normalized.length ? normalized : undefined
}

function boundedInteger(value: unknown, min: number, max: number): number | undefined {
  if (value === undefined) {
    return undefined
  }
  if (typeof value !== 'number' || !Number.isInteger(value) || value < min || value > max) {
    throw new Error(`数值字段必须是 ${min} 到 ${max} 之间的整数`)
  }
  return value
}

function hasPublicInput(input: object, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(input, key)
}

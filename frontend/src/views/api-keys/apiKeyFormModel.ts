import { displayGroupName, type GroupSelection } from '@/shared/groupLabelCache'
import type { ApiKeyAvailabilitySchedule, ApiKeyGroupBindingSummary } from '@/types/domain'
import { createTimeScheduleForm, type TimeScheduleForm } from '@/views/shared/timeSchedule'
import { apiKeyScheduleLabel } from './apiKeyFormatters'

export type ApiKeyGroupBindingFormStatus = 'active' | 'disabled'

export interface ApiKeyGroupBindingFormRow {
  key: string
  groupId: string
  group?: GroupSelection
  providerCode?: string
  providerProtocolProfileId?: string
  groupEnabled?: boolean
  weight: number
  status: ApiKeyGroupBindingFormStatus
}

export type ApiKeyAvailabilityScheduleForm = TimeScheduleForm<ApiKeyAvailabilitySchedule>

export interface ApiKeyGroupBindingMetadata {
  providerCode?: string
  providerProtocolProfileId?: string
  groupEnabled?: boolean
}

export interface ApiKeyGroupBindingPayload {
  groupId: string
  priority: number
  weight: number
  status: ApiKeyGroupBindingFormStatus
}

const apiKeyScheduleWindowKeyPrefix = 'api_key_schedule_window'
let groupBindingFormKeySeed = 0

export function createGroupBindingFormRow(
  group?: GroupSelection,
  status: ApiKeyGroupBindingFormStatus = 'active',
  weight = 1,
  metadata: ApiKeyGroupBindingMetadata = {}
): ApiKeyGroupBindingFormRow {
  return {
    key: nextGroupBindingFormKey(),
    groupId: group?.id ?? '',
    group,
    providerCode: metadata.providerCode,
    providerProtocolProfileId: metadata.providerProtocolProfileId,
    groupEnabled: metadata.groupEnabled,
    weight: normalizeGroupBindingWeight(weight),
    status: normalizeGroupBindingStatus(status)
  }
}

export function createExistingGroupBindingFormRow(binding: ApiKeyGroupBindingSummary): ApiKeyGroupBindingFormRow {
  const group = {
    id: binding.groupId,
    name: displayGroupName(binding.groupName, binding.groupId)
  }
  return {
    key: nextGroupBindingFormKey(),
    groupId: group.id,
    group,
    providerCode: binding.providerCode,
    providerProtocolProfileId: binding.providerProtocolProfileId,
    groupEnabled: binding.groupEnabled,
    weight: normalizeExistingGroupBindingWeight(binding.weight),
    status: normalizeGroupBindingStatus(binding.status)
  }
}

export function createApiKeyTimeScheduleForm(schedule?: ApiKeyAvailabilitySchedule): ApiKeyAvailabilityScheduleForm {
  return createTimeScheduleForm<ApiKeyAvailabilitySchedule>(schedule, {
    label: apiKeyScheduleLabel,
    keyPrefix: apiKeyScheduleWindowKeyPrefix
  })
}

export function normalizedGroupBindingPayload(bindings: ApiKeyGroupBindingFormRow[]): ApiKeyGroupBindingPayload[] {
  return bindings.map((binding, index) => ({
    groupId: binding.groupId.trim(),
    priority: index + 1,
    weight: normalizeGroupBindingWeight(binding.weight),
    status: normalizeGroupBindingStatus(binding.status)
  }))
}

function normalizeGroupBindingWeight(value: unknown): number {
  if (value === undefined || value === null) return 1
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 1 || value > 100) {
    throw new Error('API Key 分组权重必须是 1-100 之间的整数')
  }
  return value
}

function normalizeExistingGroupBindingWeight(value: unknown): number {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 1 || value > 100) {
    throw new Error('API Key 分组权重必须是 1-100 之间的整数')
  }
  return value
}

function normalizeGroupBindingStatus(value: unknown): ApiKeyGroupBindingFormStatus {
  if (value === 'active' || value === 'disabled') return value
  throw new Error('API Key 分组绑定状态异常，请清理后再编辑')
}

function nextGroupBindingFormKey(): string {
  groupBindingFormKeySeed += 1
  return `binding_${Date.now()}_${groupBindingFormKeySeed}`
}

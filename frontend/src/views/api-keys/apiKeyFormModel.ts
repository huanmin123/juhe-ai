import type { ApiKeyAvailabilitySchedule } from '@/types/domain'
import { createTimeScheduleForm, type TimeScheduleForm } from '@/views/shared/timeSchedule'
import { apiKeyScheduleLabel } from './apiKeyFormatters'

export type ApiKeyAvailabilityScheduleForm = TimeScheduleForm<ApiKeyAvailabilitySchedule>

const apiKeyScheduleWindowKeyPrefix = 'api_key_schedule_window'

export function createApiKeyTimeScheduleForm(schedule?: ApiKeyAvailabilitySchedule): ApiKeyAvailabilityScheduleForm {
  return createTimeScheduleForm<ApiKeyAvailabilitySchedule>(schedule, {
    label: apiKeyScheduleLabel,
    keyPrefix: apiKeyScheduleWindowKeyPrefix
  })
}

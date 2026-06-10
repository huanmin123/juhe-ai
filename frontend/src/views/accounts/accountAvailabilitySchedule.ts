import type { AccountAvailabilitySchedule } from '@/types/domain'
import {
  buildTimeSchedulePayload,
  createTimeScheduleForm,
  timeScheduleFormFingerprint,
  timeScheduleSummary,
  timeScheduleTagColor,
  validateTimeScheduleForm,
  type TimeScheduleForm
} from '@/views/shared/timeSchedule'

const accountScheduleLabel = '账户时间计划'
const accountScheduleWindowKeyPrefix = 'account_schedule_window'

export type AccountAvailabilityScheduleForm = TimeScheduleForm<AccountAvailabilitySchedule>
export type AccountAvailabilitySchedulePayload = AccountAvailabilitySchedule

export function createAccountAvailabilityScheduleForm(schedule?: AccountAvailabilitySchedule): AccountAvailabilityScheduleForm {
  return createTimeScheduleForm<AccountAvailabilitySchedule>(schedule, {
    label: accountScheduleLabel,
    keyPrefix: accountScheduleWindowKeyPrefix
  })
}

export function validateAccountAvailabilityScheduleForm(schedule: AccountAvailabilityScheduleForm): string | undefined {
  return validateTimeScheduleForm(schedule)
}

export function buildAccountAvailabilitySchedulePayload(schedule: AccountAvailabilityScheduleForm): AccountAvailabilitySchedulePayload | null {
  return buildTimeSchedulePayload<AccountAvailabilitySchedule>(schedule)
}

export function accountAvailabilityScheduleFormFingerprint(schedule: AccountAvailabilityScheduleForm): string {
  return timeScheduleFormFingerprint(schedule)
}

export function accountScheduleSummary(schedule?: AccountAvailabilitySchedule): string {
  return timeScheduleSummary(schedule, { label: accountScheduleLabel })
}

export function accountScheduleTagColor(schedule?: AccountAvailabilitySchedule): string {
  return timeScheduleTagColor(schedule, { label: accountScheduleLabel })
}

import type { AccountAvailabilitySchedule } from '../domain/types.js'
import {
  apiKeyAvailabilityScheduleStatus,
  apiKeyAvailabilityScheduleJson,
  evaluateApiKeyAvailabilitySchedule,
  nextApiKeyAvailabilityScheduleCheckAt,
  normalizeApiKeyAvailabilitySchedule,
  parseApiKeyAvailabilityScheduleJson
} from './api-key-availability-schedule.js'
import type { AccountStatus } from '../domain/types.js'

export function accountAvailabilityScheduleFromRequest(input: Record<string, unknown>): AccountAvailabilitySchedule | undefined {
  return normalizeAccountAvailabilitySchedule(input.availabilitySchedule)
}

export function accountAvailabilityScheduleJson(schedule: AccountAvailabilitySchedule | undefined): string | null {
  return apiKeyAvailabilityScheduleJson(schedule)
}

export function parseAccountAvailabilityScheduleJson(value: string | null | undefined): AccountAvailabilitySchedule | undefined {
  return parseApiKeyAvailabilityScheduleJson(value)
}

export function isAccountAvailabilityScheduleInputPresent(input: Record<string, unknown>): boolean {
  return Object.prototype.hasOwnProperty.call(input, 'availabilitySchedule')
}

export function isAccountAvailabilityScheduleAllowed(value: string | null | undefined, now = new Date()): boolean {
  const schedule = parseAccountAvailabilityScheduleJson(value)
  if (!schedule) return true
  return evaluateApiKeyAvailabilitySchedule(schedule, now).allowed
}

export function accountAvailabilityScheduleStatus(schedule: AccountAvailabilitySchedule | undefined, now = new Date()): 'active' | 'disabled' | undefined {
  return apiKeyAvailabilityScheduleStatus(schedule, now)
}

export function accountStatusForScheduleMutation(input: {
  requestedStatus: AccountStatus
  schedule: AccountAvailabilitySchedule | undefined
  now: Date
}): AccountStatus {
  if (input.requestedStatus !== 'active' && input.requestedStatus !== 'disabled') {
    return input.requestedStatus
  }
  return accountAvailabilityScheduleStatus(input.schedule, input.now) ?? input.requestedStatus
}

export function nextAccountAvailabilityScheduleCheckAt(schedule: AccountAvailabilitySchedule | undefined, now = new Date()): string | null {
  return nextApiKeyAvailabilityScheduleCheckAt(schedule, now)
}

function normalizeAccountAvailabilitySchedule(input: unknown): AccountAvailabilitySchedule | undefined {
  try {
    return normalizeApiKeyAvailabilitySchedule(input)
  } catch (error) {
    if (error instanceof Error) {
      throw new Error(error.message.replace(/^API Key 时间计划/, '账户时间计划'))
    }
    throw error
  }
}

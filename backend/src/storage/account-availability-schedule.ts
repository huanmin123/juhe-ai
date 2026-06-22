import type { AccountAvailabilitySchedule } from '../domain/types.js'
import {
  apiKeyAvailabilityScheduleJson,
  evaluateApiKeyAvailabilitySchedule,
  nextApiKeyAvailabilityScheduleCheckAt,
  normalizeApiKeyAvailabilitySchedule,
  parseApiKeyAvailabilityScheduleJson
} from './api-key-availability-schedule.js'

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


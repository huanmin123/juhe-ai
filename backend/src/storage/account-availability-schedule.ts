import type { AccountAvailabilitySchedule } from '../domain/types.js'
import {
  apiKeyAvailabilityScheduleJson,
  evaluateApiKeyAvailabilitySchedule,
  normalizeApiKeyAvailabilitySchedule,
  parseApiKeyAvailabilityScheduleJson
} from './api-key-availability-schedule.js'

export function accountAvailabilityScheduleFromRequest(input: Record<string, unknown>): AccountAvailabilitySchedule | undefined {
  return normalizeAccountAvailabilitySchedule(input.availabilitySchedule ?? input.availability_schedule)
}

export function accountAvailabilityScheduleJson(schedule: AccountAvailabilitySchedule | undefined): string | null {
  return apiKeyAvailabilityScheduleJson(schedule)
}

export function parseAccountAvailabilityScheduleJson(value: string | null | undefined): AccountAvailabilitySchedule | undefined {
  return parseApiKeyAvailabilityScheduleJson(value)
}

export function isAccountAvailabilityScheduleInputPresent(input: Record<string, unknown>): boolean {
  return Object.prototype.hasOwnProperty.call(input, 'availabilitySchedule')
    || Object.prototype.hasOwnProperty.call(input, 'availability_schedule')
}

export function isAccountAvailabilityScheduleAllowed(value: string | null | undefined, now = new Date()): boolean {
  const schedule = parseAccountAvailabilityScheduleJson(value)
  if (!schedule) return true
  return evaluateApiKeyAvailabilitySchedule(schedule, now).allowed
}

function normalizeAccountAvailabilitySchedule(input: unknown): AccountAvailabilitySchedule | undefined {
  try {
    return normalizeApiKeyAvailabilitySchedule(input)
  } catch (error) {
    if (error instanceof Error) {
      throw new Error(error.message.replace(/^API Key 自动启停计划/, '账户自动启停计划'))
    }
    throw error
  }
}

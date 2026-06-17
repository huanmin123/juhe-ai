import type { AccountAvailabilitySchedule, ApiKeyAvailabilitySchedule } from '../../../../domain/types.js'
import { dayMs } from '../shared.js'

const allDaysOfWeek = [1, 2, 3, 4, 5, 6, 7]
const timezone = 'UTC'

export function activeAccountAvailabilitySchedule(): AccountAvailabilitySchedule {
  return activeSchedule()
}

export function inactiveAccountAvailabilitySchedule(): AccountAvailabilitySchedule {
  return inactiveSchedule()
}

export function activeApiKeyAvailabilitySchedule(): ApiKeyAvailabilitySchedule {
  return activeSchedule()
}

export function inactiveApiKeyAvailabilitySchedule(): ApiKeyAvailabilitySchedule {
  return inactiveSchedule()
}

function activeSchedule(): ApiKeyAvailabilitySchedule {
  return {
    enabled: true,
    timezone,
    mode: 'allow_windows',
    windows: [
      {
        daysOfWeek: allDaysOfWeek,
        start: '00:00',
        end: '12:00'
      },
      {
        daysOfWeek: allDaysOfWeek,
        start: '12:00',
        end: '00:00'
      }
    ]
  }
}

function inactiveSchedule(): ApiKeyAvailabilitySchedule {
  return {
    enabled: true,
    timezone,
    mode: 'allow_windows',
    windows: [
      {
        daysOfWeek: allDaysOfWeek,
        start: '09:00',
        end: '18:00'
      }
    ],
    dateRange: {
      startDate: dateKey(-30),
      endDate: dateKey(-1)
    }
  }
}

function dateKey(offsetDays: number): string {
  return new Date(Date.now() + offsetDays * dayMs).toISOString().slice(0, 10)
}

import { parseAccountAvailabilityScheduleJson } from './account-availability-schedule.js'
import { evaluateApiKeyAvailabilitySchedule } from './api-key-availability-schedule.js'
import {
  beginDatabaseTransaction,
  commitDatabaseTransaction,
  getBusinessDatabase,
  nowIso,
  rollbackDatabaseTransaction
} from './database.js'

interface ScheduledAccountAvailabilityRow {
  id: string
  availability_schedule_json: string | null
  availability_schedule_active: number
}

interface ScheduledAccountAvailabilityUpdate {
  id: string
  active: number
}

export interface AccountAvailabilityScheduleStatusSyncResult {
  scanned: number
  activated: number
  disabled: number
  unchanged: number
  invalid: number
  changedIds: string[]
  invalidIds: string[]
}

export function syncAccountAvailabilityScheduleStatuses(now = new Date()): AccountAvailabilityScheduleStatusSyncResult {
  const database = getBusinessDatabase()
  const rows = database
    .prepare(`
      SELECT id, availability_schedule_json, availability_schedule_active
      FROM accounts
      WHERE availability_schedule_json IS NOT NULL
        AND deleted_at IS NULL
      ORDER BY updated_at ASC, id ASC
    `)
    .all() as unknown as ScheduledAccountAvailabilityRow[]
  const result: AccountAvailabilityScheduleStatusSyncResult = {
    scanned: rows.length,
    activated: 0,
    disabled: 0,
    unchanged: 0,
    invalid: 0,
    changedIds: [],
    invalidIds: []
  }
  const updates: ScheduledAccountAvailabilityUpdate[] = []

  for (const row of rows) {
    try {
      const schedule = parseAccountAvailabilityScheduleJson(row.availability_schedule_json)
      const active = evaluateApiKeyAvailabilitySchedule(schedule, now).allowed ? 1 : 0
      if (active === row.availability_schedule_active) {
        result.unchanged += 1
        continue
      }
      updates.push({ id: row.id, active })
    } catch {
      result.invalid += 1
      result.invalidIds.push(row.id)
      if (row.availability_schedule_active !== 0) {
        updates.push({ id: row.id, active: 0 })
      }
    }
  }

  if (!updates.length) {
    return result
  }

  const updatedAt = Number.isFinite(now.getTime()) ? now.toISOString() : nowIso()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const updateStatus = database.prepare(`
      UPDATE accounts
      SET availability_schedule_active = ?, updated_at = ?
      WHERE id = ?
        AND availability_schedule_json IS NOT NULL
        AND deleted_at IS NULL
        AND availability_schedule_active <> ?
    `)
    for (const update of updates) {
      const changes = updateStatus.run(update.active, updatedAt, update.id, update.active).changes ?? 0
      if (changes <= 0) {
        result.unchanged += 1
        continue
      }
      result.changedIds.push(update.id)
      if (update.active === 1) {
        result.activated += 1
      } else {
        result.disabled += 1
      }
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    throw error
  }

  return result
}

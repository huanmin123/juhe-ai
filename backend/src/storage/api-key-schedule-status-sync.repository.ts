import {
  dueApiKeyAvailabilityScheduleEvent,
  parseApiKeyAvailabilityScheduleJson
} from './api-key-availability-schedule.js'
import {
  beginDatabaseTransaction,
  commitDatabaseTransaction,
  getBusinessDatabase,
  nowIso,
  rollbackDatabaseTransaction
} from './database.js'

interface ScheduledApiKeyStatusRow {
  id: string
  availability_schedule_json: string | null
  availability_schedule_active: number
}

interface ScheduledApiKeyStatusUpdate {
  id: string
  active: number
  eventKey?: string
  status?: 'active' | 'disabled'
}

export interface ApiKeyScheduleStatusSyncResult {
  scanned: number
  activated: number
  disabled: number
  unchanged: number
  skipped: number
  invalid: number
  changedIds: string[]
  invalidIds: string[]
}

export function syncApiKeyAvailabilityScheduleStatuses(now = new Date()): ApiKeyScheduleStatusSyncResult {
  const database = getBusinessDatabase()
  const rows = database
    .prepare(`
      SELECT id, availability_schedule_json, availability_schedule_active
      FROM api_keys
      WHERE availability_schedule_json IS NOT NULL
      ORDER BY updated_at ASC, id ASC
    `)
    .all() as unknown as ScheduledApiKeyStatusRow[]
  const result: ApiKeyScheduleStatusSyncResult = {
    scanned: rows.length,
    activated: 0,
    disabled: 0,
    unchanged: 0,
    skipped: 0,
    invalid: 0,
    changedIds: [],
    invalidIds: []
  }
  const updates: ScheduledApiKeyStatusUpdate[] = []

  for (const row of rows) {
    try {
      const schedule = parseApiKeyAvailabilityScheduleJson(row.availability_schedule_json)
      const event = dueApiKeyAvailabilityScheduleEvent(schedule, now)
      if (!event) {
        result.unchanged += 1
        continue
      }
      updates.push({
        id: row.id,
        active: event.status === 'active' ? 1 : 0,
        eventKey: `${row.id}:${event.eventKey}`,
        status: event.status
      })
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
    const insertEvent = database.prepare(`
      INSERT OR IGNORE INTO api_key_schedule_status_events (event_key, api_key_id, status, executed_at)
      VALUES (?, ?, ?, ?)
    `)
    const updateStatus = database.prepare(`
      UPDATE api_keys
      SET availability_schedule_active = ?, updated_at = ?
      WHERE id = ?
        AND availability_schedule_json IS NOT NULL
        AND availability_schedule_active <> ?
    `)
    for (const update of updates) {
      if (update.eventKey && update.status) {
        const eventChanges = insertEvent.run(update.eventKey, update.id, update.status, updatedAt).changes ?? 0
        if (eventChanges <= 0) {
          result.skipped += 1
          continue
        }
      }
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

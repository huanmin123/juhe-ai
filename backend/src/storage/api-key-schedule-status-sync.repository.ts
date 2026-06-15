import {
  apiKeyAvailabilityScheduleStatus,
  latestApiKeyAvailabilityScheduleStartEvent,
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
  status: 'active' | 'disabled'
  availability_schedule_json: string | null
}

interface ScheduledApiKeyStatusUpdate {
  id: string
  status: 'active' | 'disabled'
  eventKey?: string
}

interface ScheduledApiKeyStatusEvent {
  id: string
  status: 'active'
  eventKey: string
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
  ensureApiKeyScheduleStatusEventTable(database)
  const rows = database
    .prepare(`
      SELECT id, status, availability_schedule_json
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
  const events: ScheduledApiKeyStatusEvent[] = []

  for (const row of rows) {
    try {
      const schedule = parseApiKeyAvailabilityScheduleJson(row.availability_schedule_json)
      const status = apiKeyAvailabilityScheduleStatus(schedule, now)
      if (!status) {
        result.unchanged += 1
        continue
      }
      if (status === 'active') {
        const event = latestApiKeyAvailabilityScheduleStartEvent(schedule, now)
        if (!event) {
          result.unchanged += 1
          continue
        }
        const eventKey = apiKeyScheduleStatusEventKey(row.id, row.availability_schedule_json, event.eventKey)
        if (hasApiKeyScheduleStatusEvent(database, eventKey)) {
          if (status === row.status) {
            result.unchanged += 1
          } else {
            result.skipped += 1
          }
          continue
        }
        if (status === row.status) {
          events.push({ id: row.id, status, eventKey })
          result.unchanged += 1
          continue
        }
        updates.push({ id: row.id, status, eventKey })
        continue
      }
      if (status === row.status) {
        result.unchanged += 1
        continue
      }
      updates.push({ id: row.id, status })
    } catch {
      result.invalid += 1
      result.invalidIds.push(row.id)
    }
  }

  if (!updates.length && !events.length) {
    return result
  }

  const updatedAt = Number.isFinite(now.getTime()) ? now.toISOString() : nowIso()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const updateStatus = database.prepare(`
      UPDATE api_keys
      SET status = ?, updated_at = ?
      WHERE id = ?
        AND availability_schedule_json IS NOT NULL
        AND status <> ?
    `)
    const insertEvent = database.prepare(`
      INSERT OR IGNORE INTO api_key_schedule_status_events (event_key, api_key_id, status, executed_at)
      VALUES (?, ?, ?, ?)
    `)
    for (const event of events) {
      insertEvent.run(event.eventKey, event.id, event.status, updatedAt)
    }
    for (const update of updates) {
      if (update.eventKey) {
        const inserted = insertEvent.run(update.eventKey, update.id, update.status, updatedAt).changes ?? 0
        if (inserted <= 0) {
          result.skipped += 1
          continue
        }
      }
      const changes = updateStatus.run(update.status, updatedAt, update.id, update.status).changes ?? 0
      if (changes <= 0) {
        result.unchanged += 1
        continue
      }
      result.changedIds.push(update.id)
      if (update.status === 'active') {
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

function ensureApiKeyScheduleStatusEventTable(database = getBusinessDatabase()): void {
  database.exec(`
    CREATE TABLE IF NOT EXISTS api_key_schedule_status_events (
      event_key TEXT PRIMARY KEY,
      api_key_id TEXT NOT NULL,
      status TEXT NOT NULL,
      executed_at TEXT NOT NULL
    );
    CREATE INDEX IF NOT EXISTS idx_api_key_schedule_status_events_api_key
      ON api_key_schedule_status_events(api_key_id, executed_at DESC);
  `)
}

function hasApiKeyScheduleStatusEvent(database: ReturnType<typeof getBusinessDatabase>, eventKey: string): boolean {
  const row = database.prepare('SELECT event_key FROM api_key_schedule_status_events WHERE event_key = ? LIMIT 1').get(eventKey) as { event_key?: string } | undefined
  return Boolean(row?.event_key)
}

function apiKeyScheduleStatusEventKey(apiKeyId: string, scheduleJson: string | null, eventKey: string): string {
  return `${apiKeyId}:${scheduleJson ?? ''}:${eventKey}`
}

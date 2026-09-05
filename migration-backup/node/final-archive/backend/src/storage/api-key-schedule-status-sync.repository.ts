import {
  dueApiKeyAvailabilityScheduleEvent,
  nextApiKeyAvailabilityScheduleCheckAt,
  parseApiKeyAvailabilityScheduleJson
} from './api-key-availability-schedule.js'
import { runtimeConfig } from '../config/runtime.js'
import { notifyGatewayApiKeyValidationCacheInvalidationAsync } from '../shared/gateway-cache-invalidation.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import {
  beginDatabaseTransaction,
  commitDatabaseTransaction,
  getBusinessDatabase,
  nowIso,
  rollbackDatabaseTransaction
} from './database.js'
import { invalidateGatewayApiKeyCacheById } from './gateway-api-key.repository.js'
import { getPostgresPool } from './postgres-client.js'

interface ScheduledApiKeyStatusRow {
  id: string
  status: 'active' | 'disabled'
  availability_schedule_json: string | null
  availability_schedule_next_check_at: string | null
}

interface ScheduledApiKeyStatusUpdate {
  id: string
  nextCheckAt: string | null
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

const availabilityScheduleStatusSyncBatchLimit = runtimeConfig.background.apiKeyScheduleSyncBatchLimit
const businessSchemaName = 'juhe_business'

export function syncApiKeyAvailabilityScheduleStatuses(now = new Date()): ApiKeyScheduleStatusSyncResult {
  const database = getBusinessDatabase()
  const updatedAt = Number.isFinite(now.getTime()) ? now.toISOString() : nowIso()
  const rows = listScheduledApiKeyStatusRows(database, updatedAt)
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
      const nextCheckAt = nextApiKeyAvailabilityScheduleCheckAt(schedule, now)
      const event = dueApiKeyAvailabilityScheduleEvent(schedule, now)
      if (!event) {
        updates.push({ id: row.id, nextCheckAt })
        result.unchanged += 1
        continue
      }
      const nextStatus = event.status
      updates.push({
        id: row.id,
        nextCheckAt,
        eventKey: `${row.id}:${event.eventKey}`,
        status: nextStatus
      })
    } catch {
      result.invalid += 1
      result.invalidIds.push(row.id)
      if (row.status !== 'disabled') {
        updates.push({ id: row.id, nextCheckAt: null, status: 'disabled' })
      } else {
        updates.push({ id: row.id, nextCheckAt: null })
      }
    }
  }

  if (!updates.length) {
    return result
  }

  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const insertEvent = database.prepare(`
      INSERT OR IGNORE INTO api_key_schedule_status_events (event_key, api_key_id, status, executed_at)
      VALUES (?, ?, ?, ?)
    `)
    const updateStatus = database.prepare(`
      UPDATE api_keys
      SET status = ?, availability_schedule_next_check_at = ?, updated_at = ?
      WHERE id = ?
        AND availability_schedule_json IS NOT NULL
        AND status <> ?
    `)
    const updateNextCheck = database.prepare(`
      UPDATE api_keys
      SET availability_schedule_next_check_at = ?
      WHERE id = ?
        AND availability_schedule_json IS NOT NULL
        AND COALESCE(availability_schedule_next_check_at, '') <> COALESCE(?, '')
    `)
    for (const update of updates) {
      if (update.eventKey && update.status) {
        const eventChanges = insertEvent.run(update.eventKey, update.id, update.status, updatedAt).changes ?? 0
        if (eventChanges <= 0) {
          result.skipped += 1
          updateNextCheck.run(update.nextCheckAt, update.id, update.nextCheckAt)
          continue
        }
      }
      if (update.status === undefined) {
        updateNextCheck.run(update.nextCheckAt, update.id, update.nextCheckAt)
        continue
      }
      const changes = updateStatus.run(update.status, update.nextCheckAt, updatedAt, update.id, update.status).changes ?? 0
      if (changes <= 0) {
        updateNextCheck.run(update.nextCheckAt, update.id, update.nextCheckAt)
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

  invalidateChangedApiKeyCaches(result.changedIds)
  return result
}

export async function syncApiKeyAvailabilityScheduleStatusesAsync(now = new Date()): Promise<ApiKeyScheduleStatusSyncResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    const result = syncApiKeyAvailabilityScheduleStatuses(now)
    await invalidateChangedApiKeyCachesAsync(result.changedIds)
    return result
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const updatedAt = Number.isFinite(now.getTime()) ? now.toISOString() : nowIso()
  const rows = await listScheduledApiKeyStatusRowsAsync(client, updatedAt)
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
      const nextCheckAt = nextApiKeyAvailabilityScheduleCheckAt(schedule, now)
      const event = dueApiKeyAvailabilityScheduleEvent(schedule, now)
      if (!event) {
        updates.push({ id: row.id, nextCheckAt })
        result.unchanged += 1
        continue
      }
      const nextStatus = event.status
      updates.push({
        id: row.id,
        nextCheckAt,
        eventKey: `${row.id}:${event.eventKey}`,
        status: nextStatus
      })
    } catch {
      result.invalid += 1
      result.invalidIds.push(row.id)
      if (row.status !== 'disabled') {
        updates.push({ id: row.id, nextCheckAt: null, status: 'disabled' })
      } else {
        updates.push({ id: row.id, nextCheckAt: null })
      }
    }
  }

  if (!updates.length) {
    return result
  }

  await client.transaction(async (tx) => {
    for (const update of updates) {
      if (update.eventKey && update.status) {
        const eventChanges = await tx.execute(`
          INSERT INTO ${apiKeyScheduleStatusTable(tx, 'api_key_schedule_status_events')} (event_key, api_key_id, status, executed_at)
          VALUES (?, ?, ?, ?)
          ON CONFLICT(event_key) DO NOTHING
        `, [update.eventKey, update.id, update.status, updatedAt])
        if (eventChanges.changes <= 0) {
          result.skipped += 1
          await updateApiKeyNextCheckAtAsync(tx, update)
          continue
        }
      }
      if (update.status === undefined) {
        await updateApiKeyNextCheckAtAsync(tx, update)
        continue
      }
      const changes = await tx.execute(`
        UPDATE ${apiKeyScheduleStatusTable(tx, 'api_keys')}
        SET status = ?, availability_schedule_next_check_at = ?, updated_at = ?
        WHERE id = ?
          AND availability_schedule_json IS NOT NULL
          AND status <> ?
      `, [update.status, update.nextCheckAt, updatedAt, update.id, update.status])
      if (changes.changes <= 0) {
        await updateApiKeyNextCheckAtAsync(tx, update)
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
  })

  await invalidateChangedApiKeyCachesAsync(result.changedIds)
  return result
}

function listScheduledApiKeyStatusRows(database: ReturnType<typeof getBusinessDatabase>, dueAt: string): ScheduledApiKeyStatusRow[] {
  const selectColumns = 'id, status, availability_schedule_json, availability_schedule_next_check_at'
  return database
    .prepare(`
      SELECT ${selectColumns}
      FROM api_keys
      WHERE availability_schedule_json IS NOT NULL
        AND (
          availability_schedule_next_check_at IS NULL
          OR availability_schedule_next_check_at <= ?
        )
      ORDER BY availability_schedule_next_check_at IS NOT NULL ASC, availability_schedule_next_check_at ASC, id ASC
      LIMIT ?
    `)
    .all(dueAt, availabilityScheduleStatusSyncBatchLimit) as unknown as ScheduledApiKeyStatusRow[]
}

async function listScheduledApiKeyStatusRowsAsync(client: DatabaseClient, dueAt: string): Promise<ScheduledApiKeyStatusRow[]> {
  const selectColumns = 'id, status, availability_schedule_json, availability_schedule_next_check_at'
  return client.query<ScheduledApiKeyStatusRow>(`
    SELECT ${selectColumns}
    FROM ${apiKeyScheduleStatusTable(client, 'api_keys')}
    WHERE availability_schedule_json IS NOT NULL
      AND (
        availability_schedule_next_check_at IS NULL
        OR availability_schedule_next_check_at <= ?
      )
    ORDER BY availability_schedule_next_check_at IS NOT NULL ASC, availability_schedule_next_check_at ASC, id ASC
    LIMIT ?
  `, [dueAt, availabilityScheduleStatusSyncBatchLimit])
}

async function updateApiKeyNextCheckAtAsync(client: DatabaseClient, update: Pick<ScheduledApiKeyStatusUpdate, 'id' | 'nextCheckAt'>): Promise<void> {
  await client.execute(`
    UPDATE ${apiKeyScheduleStatusTable(client, 'api_keys')}
    SET availability_schedule_next_check_at = ?
    WHERE id = ?
      AND availability_schedule_json IS NOT NULL
      AND COALESCE(availability_schedule_next_check_at, '') <> COALESCE(?, '')
  `, [update.nextCheckAt, update.id, update.nextCheckAt])
}

function apiKeyScheduleStatusTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function invalidateChangedApiKeyCaches(ids: string[]): void {
  for (const id of ids) {
    invalidateGatewayApiKeyCacheById(id)
  }
}

async function invalidateChangedApiKeyCachesAsync(ids: string[]): Promise<void> {
  if (!ids.length) return
  await notifyGatewayApiKeyValidationCacheInvalidationAsync(undefined, 'api_key_schedule_status_changed')
}

import { strict as assert } from 'node:assert'
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-runtime-log-timestamp-normalization-'))
const logDirectory = join(tempRoot, 'logs')
mkdirSync(logDirectory)

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
runtimeConfig.log.directory = logDirectory
runtimeConfig.log.fileEnabled = false
runtimeConfig.log.consoleEnabled = false

const [databaseModule, runtimeLogsRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/runtime-logs.repository.js')
])

const offsetTime = '2026-07-14T18:45:12.345+08:00'
const offsetCreatedAt = '2026-07-14T04:15:12.345-06:30'
const rfc1123Time = 'Tue, 14 Jul 2026 10:45:12 GMT'
const rfc1123CreatedAt = 'Tue, 14 Jul 2026 10:45:13 GMT'
const canonicalTime = '2026-07-14T10:45:14.567Z'
const canonicalCreatedAt = '2026-07-14T10:45:15.678Z'

try {
  const beforeFallback = Date.now()
  runtimeLogsRepository.createRuntimeLogsBatch([
    runtimeLog('rtlog_timestamp_offset', offsetTime, offsetCreatedAt),
    runtimeLog('rtlog_timestamp_rfc1123', rfc1123Time, rfc1123CreatedAt),
    runtimeLog('rtlog_timestamp_canonical', canonicalTime, canonicalCreatedAt),
    runtimeLog('rtlog_timestamp_invalid_time', 'invalid-time-text', offsetCreatedAt),
    runtimeLog('rtlog_timestamp_invalid_created_at', offsetTime, 'invalid-created-at-text'),
    runtimeLog('rtlog_timestamp_empty', '', ''),
    runtimeLog('rtlog_timestamp_missing_created_at', rfc1123Time)
  ])
  const afterFallback = Date.now()

  assert.deepEqual(repositoryTimestamps('rtlog_timestamp_offset'), [
    new Date(Date.parse(offsetTime)).toISOString(),
    new Date(Date.parse(offsetCreatedAt)).toISOString()
  ])
  assert.deepEqual(repositoryTimestamps('rtlog_timestamp_rfc1123'), [
    new Date(Date.parse(rfc1123Time)).toISOString(),
    new Date(Date.parse(rfc1123CreatedAt)).toISOString()
  ])
  assert.deepEqual(repositoryTimestamps('rtlog_timestamp_canonical'), [canonicalTime, canonicalCreatedAt])

  const invalidTime = repositoryTimestamps('rtlog_timestamp_invalid_time')
  assertFallbackTimestamp(invalidTime[0], beforeFallback, afterFallback)
  assert.equal(invalidTime[1], new Date(Date.parse(offsetCreatedAt)).toISOString())

  const invalidCreatedAt = repositoryTimestamps('rtlog_timestamp_invalid_created_at')
  assert.equal(invalidCreatedAt[0], new Date(Date.parse(offsetTime)).toISOString())
  assertFallbackTimestamp(invalidCreatedAt[1], beforeFallback, afterFallback)

  const empty = repositoryTimestamps('rtlog_timestamp_empty')
  assertFallbackTimestamp(empty[0], beforeFallback, afterFallback)
  assert.equal(empty[0], empty[1], 'Invalid time and createdAt must share one now fallback')

  const missingCreatedAt = repositoryTimestamps('rtlog_timestamp_missing_created_at')
  assert.equal(missingCreatedAt[0], new Date(Date.parse(rfc1123Time)).toISOString())
  assertFallbackTimestamp(missingCreatedAt[1], beforeFallback, afterFallback)

  const storedRows = databaseModule.getDatasetDatabase()
    .prepare('SELECT time, created_at FROM runtime_logs ORDER BY id ASC')
    .all() as Array<{ time: string; created_at: string }>
  assert.equal(storedRows.length, 7)
  for (const row of storedRows) {
    assert.equal(row.time, new Date(Date.parse(row.time)).toISOString(), 'Stored time must be canonical UTC millisecond ISO')
    assert.equal(row.created_at, new Date(Date.parse(row.created_at)).toISOString(), 'Stored created_at must be canonical UTC millisecond ISO')
  }
  assert(!storedRows.some((row) => row.time === 'invalid-time-text' || row.created_at === 'invalid-created-at-text'))

  console.log('Runtime log timestamp normalization regression passed')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function runtimeLog(id: string, time: string, createdAt?: string) {
  return { id, time, level: 'info', rawJson: '{}', createdAt }
}

function repositoryTimestamps(id: string): [string, string] {
  const row = runtimeLogsRepository.getRuntimeLogDetail(id)
  assert(row, `Expected repository read for ${id}`)
  return [row.time, row.createdAt]
}

function assertFallbackTimestamp(value: string, lowerBound: number, upperBound: number): void {
  assert.equal(value, new Date(Date.parse(value)).toISOString(), 'Fallback must be canonical UTC millisecond ISO')
  const timestamp = Date.parse(value)
  assert(timestamp >= lowerBound && timestamp <= upperBound, 'Fallback must be generated during repository write')
}

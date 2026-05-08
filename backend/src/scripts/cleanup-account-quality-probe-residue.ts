import { copyFileSync, existsSync, mkdirSync } from 'node:fs'
import { basename, dirname, extname, join, resolve } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'

const databasePath = runtimeConfig.databasePath
const backupPath = backupDatabasePath(databasePath)
const obsoleteSettingKeys = [
  'accountQualityActiveProbeEnabled',
  'accountQualityProbeBatchSize',
  'accountQualityProbeIntervalSeconds',
  'accountQualityProbeModel',
  'accountQualityProbePrompt'
]

if (!existsSync(databasePath)) {
  console.log(`数据库不存在，无需清理：${databasePath}`)
  process.exit(0)
}

mkdirSync(dirname(backupPath), { recursive: true })

const database = new DatabaseSync(databasePath)
try {
  database.exec('PRAGMA busy_timeout = 5000;')
  database.exec('PRAGMA wal_checkpoint(TRUNCATE);')

  const hasLastProbeAt = tableExists(database, 'account_quality_scores') && columnExists(database, 'account_quality_scores', 'last_probe_at')
  const obsoleteSettingsCount = countObsoleteSettings(database)
  const shouldMutate = hasLastProbeAt || obsoleteSettingsCount > 0
  if (shouldMutate) {
    copyFileSync(databasePath, backupPath)
  }

  const deletedSettings = deleteObsoleteSettings(database)
  const droppedLastProbeAt = dropLastProbeAtColumn(database)
  database.exec('PRAGMA optimize;')

  console.log(JSON.stringify({
    databasePath,
    backupPath: shouldMutate ? backupPath : undefined,
    deletedSettings,
    droppedLastProbeAt
  }, null, 2))
} finally {
  database.close()
}

function countObsoleteSettings(database: DatabaseSync): number {
  const row = database
    .prepare(`
      SELECT COUNT(*) AS total
      FROM system_settings
      WHERE key IN (${obsoleteSettingKeys.map(() => '?').join(', ')})
        OR key LIKE 'accountQuality%Probe%'
        OR key LIKE 'accountQuality%probe%'
    `)
    .get(...obsoleteSettingKeys) as unknown as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function deleteObsoleteSettings(database: DatabaseSync): number {
  const placeholders = obsoleteSettingKeys.map(() => '?').join(', ')
  const result = database
    .prepare(`
      DELETE FROM system_settings
      WHERE key IN (${placeholders})
        OR key LIKE 'accountQuality%Probe%'
        OR key LIKE 'accountQuality%probe%'
    `)
    .run(...obsoleteSettingKeys)
  return Number(result.changes ?? 0)
}

function dropLastProbeAtColumn(database: DatabaseSync): boolean {
  if (!tableExists(database, 'account_quality_scores') || !columnExists(database, 'account_quality_scores', 'last_probe_at')) {
    return false
  }
  database.exec('ALTER TABLE account_quality_scores DROP COLUMN last_probe_at;')
  return true
}

function tableExists(database: DatabaseSync, tableName: string): boolean {
  const row = database
    .prepare("SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?")
    .get(tableName) as unknown as { name?: string } | undefined
  return row?.name === tableName
}

function columnExists(database: DatabaseSync, tableName: string, columnName: string): boolean {
  const rows = database.prepare(`PRAGMA table_info(${tableName})`).all() as unknown as Array<{ name?: string }>
  return rows.some((row) => row.name === columnName)
}

function backupDatabasePath(path: string): string {
  const directory = join(dirname(path), 'backups')
  const extension = extname(path)
  const name = basename(path, extension)
  const timestamp = new Date().toISOString().replace(/[:.]/g, '-')
  return resolve(directory, `${name}.before-account-quality-probe-cleanup.${timestamp}${extension || '.sqlite3'}`)
}

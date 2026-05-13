import { dirname, resolve } from 'node:path'
import { existsSync, mkdirSync, statSync } from 'node:fs'
import { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'
import { applyRecordSchema } from '../storage/schema.js'

const recordTableNames = [
  'account_quality_minute_stats',
  'group_account_stats',
  'account_quality_scores',
  'usage_records',
  'audit_logs',
  'audit_log_attempts',
  'audit_payload_blobs',
  'audit_payload_refs',
  'audit_error_groups',
  'operation_logs',
  'operation_log_targets',
  'operation_log_viewers',
  'runtime_logs',
  'runtime_log_search',
  'account_usage_snapshots',
  'usage_stats_totals',
  'usage_stats_minute',
  'usage_stats_hourly',
  'usage_stats_daily',
  'usage_stats_weekly',
  'usage_stats_monthly',
  'usage_model_minute',
  'usage_model_hourly',
  'usage_model_daily',
  'usage_model_weekly',
  'usage_model_monthly',
  'usage_error_minute',
  'usage_error_hourly',
  'usage_error_daily',
  'usage_error_weekly',
  'usage_error_monthly',
  'usage_latency_minute',
  'usage_latency_hourly',
  'usage_latency_daily',
  'usage_latency_weekly',
  'usage_latency_monthly',
  'usage_rank_snapshots',
  'stats_job_state',
  'system_metrics_samples',
  'system_metrics_hourly',
  'database_storage_snapshots',
  'table_storage_snapshots'
] as const

interface ScriptOptions {
  confirm: boolean
  discardRecords: boolean
  allowNonEmptyRecords: boolean
  vacuumInto?: string
}

interface TablePlan {
  tableName: string
  exists: boolean
  rows: number
}

const options = parseOptions(process.argv.slice(2))
const businessPath = runtimeConfig.databasePath
const recordPath = runtimeConfig.recordDatabasePath

function main(): void {
  if (resolve(businessPath) === resolve(recordPath)) {
    throw new Error('业务库和记录库路径不能相同')
  }
  mkdirSync(dirname(recordPath), { recursive: true })
  if (options.vacuumInto) {
    mkdirSync(dirname(resolve(options.vacuumInto)), { recursive: true })
  }

  const businessDatabase = new DatabaseSync(businessPath)
  const recordDatabase = new DatabaseSync(recordPath)
  try {
    applyRecordSchema(recordDatabase)
    const plans = recordTableNames.map((tableName) => ({
      tableName,
      exists: tableExists(businessDatabase, tableName),
      rows: tableExists(businessDatabase, tableName) ? tableRowCount(businessDatabase, tableName) : 0
    }))
    printPlan(plans)
    if (!options.confirm) {
      console.log('当前为 dry-run；确认停机、备份完成后追加 --confirm 才会复制并清理业务库里的记录表。')
      return
    }
    if (options.discardRecords) {
      console.log('已选择丢弃旧记录表数据：不会把业务库中的日志、统计和审计数据复制到记录库。')
    } else {
      assertRecordDatabaseEmpty(recordDatabase, options.allowNonEmptyRecords)
      copyRecordTables(recordDatabase, plans.filter((plan) => plan.exists))
    }
    dropRecordTablesFromBusinessDatabase(businessDatabase, plans.filter((plan) => plan.exists))
    if (options.vacuumInto) {
      vacuumBusinessDatabaseInto(businessDatabase, options.vacuumInto)
    }
    console.log('分库处理完成。请确认记录库数据和 compact 业务库后，再按部署文档切换文件。')
  } finally {
    recordDatabase.close()
    businessDatabase.close()
  }
}

function copyRecordTables(recordDatabase: DatabaseSync, plans: TablePlan[]): void {
  recordDatabase.exec(`ATTACH DATABASE ${sqlString(businessPath)} AS source`)
  recordDatabase.exec('BEGIN')
  try {
    for (const plan of plans) {
      const targetColumns = tableColumns(recordDatabase, 'main', plan.tableName)
      const sourceColumns = tableColumns(recordDatabase, 'source', plan.tableName)
      const sharedColumns = targetColumns.filter((column) => sourceColumns.includes(column))
      if (!sharedColumns.length) {
        console.log(`跳过 ${plan.tableName}：源表和目标表没有共同字段`)
        continue
      }
      recordDatabase.prepare(`
        INSERT INTO ${quotedIdentifier(plan.tableName)} (${sharedColumns.map(quotedIdentifier).join(', ')})
        SELECT ${sharedColumns.map(quotedIdentifier).join(', ')}
        FROM source.${quotedIdentifier(plan.tableName)}
      `).run()
      console.log(`已复制 ${plan.tableName}：${plan.rows} 行`)
    }
    recordDatabase.exec('COMMIT')
  } catch (error) {
    recordDatabase.exec('ROLLBACK')
    throw error
  } finally {
    recordDatabase.exec('DETACH DATABASE source')
  }
}

function dropRecordTablesFromBusinessDatabase(database: DatabaseSync, plans: TablePlan[]): void {
  database.exec('PRAGMA foreign_keys = OFF')
  database.exec('BEGIN')
  try {
    for (const plan of [...plans].reverse()) {
      database.exec(`DROP TABLE IF EXISTS ${quotedIdentifier(plan.tableName)}`)
      console.log(`已从业务库移除记录表：${plan.tableName}`)
    }
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  } finally {
    database.exec('PRAGMA foreign_keys = ON')
  }
}

function vacuumBusinessDatabaseInto(database: DatabaseSync, outputPath: string): void {
  const resolvedPath = resolve(outputPath)
  if (existsSync(resolvedPath)) {
    throw new Error(`VACUUM INTO 目标文件已存在：${resolvedPath}`)
  }
  database.exec(`VACUUM INTO ${sqlString(resolvedPath)}`)
  console.log(`已生成压缩业务库：${resolvedPath}`)
}

function assertRecordDatabaseEmpty(database: DatabaseSync, allowNonEmptyRecords: boolean): void {
  if (allowNonEmptyRecords) return
  const nonEmptyTables = recordTableNames
    .filter((tableName) => tableExists(database, tableName))
    .map((tableName) => ({ tableName, rows: tableRowCount(database, tableName) }))
    .filter((row) => row.rows > 0)
  if (nonEmptyTables.length) {
    const names = nonEmptyTables.slice(0, 10).map((row) => `${row.tableName}=${row.rows}`).join(', ')
    throw new Error(`记录库已有数据，已中止：${names}。确认要追加导入时使用 --allow-non-empty-records。`)
  }
}

function printPlan(plans: TablePlan[]): void {
  console.log(`业务库：${businessPath} (${formatBytes(fileSize(businessPath))})`)
  console.log(`记录库：${recordPath} (${formatBytes(fileSize(recordPath))})`)
  const existing = plans.filter((plan) => plan.exists)
  if (!existing.length) {
    console.log('业务库中未发现需要迁出的记录表。')
    return
  }
  console.log('将迁出的记录表：')
  for (const plan of existing) {
    console.log(`- ${plan.tableName}: ${plan.rows} 行`)
  }
  if (options.vacuumInto) {
    console.log(`将额外生成压缩业务库：${resolve(options.vacuumInto)}`)
  }
  if (options.discardRecords) {
    console.log('本次会丢弃旧记录表数据，不复制到记录库。')
  }
}

function tableExists(database: DatabaseSync, tableName: string): boolean {
  const row = database.prepare("SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = ? LIMIT 1").get(tableName) as unknown
  return Boolean(row)
}

function tableRowCount(database: DatabaseSync, tableName: string): number {
  try {
    const row = database.prepare(`SELECT COUNT(*) AS count FROM ${quotedIdentifier(tableName)}`).get() as unknown as { count?: number } | undefined
    return Number(row?.count ?? 0)
  } catch {
    return 0
  }
}

function tableColumns(database: DatabaseSync, schemaName: 'main' | 'source', tableName: string): string[] {
  const rows = database.prepare(`PRAGMA ${schemaName}.table_info(${quotedIdentifier(tableName)})`).all() as unknown as Array<{ name?: string }>
  return rows.map((row) => row.name).filter((name): name is string => Boolean(name))
}

function parseOptions(args: string[]): ScriptOptions {
  return {
    confirm: args.includes('--confirm'),
    discardRecords: args.includes('--discard-records'),
    allowNonEmptyRecords: args.includes('--allow-non-empty-records'),
    vacuumInto: optionValue(args, '--vacuum-into')
  }
}

function optionValue(args: string[], name: string): string | undefined {
  const prefix = `${name}=`
  const inline = args.find((arg) => arg.startsWith(prefix))
  if (inline) return inline.slice(prefix.length)
  const index = args.indexOf(name)
  return index >= 0 ? args[index + 1] : undefined
}

function quotedIdentifier(value: string): string {
  return `"${value.replace(/"/g, '""')}"`
}

function sqlString(value: string): string {
  return `'${value.replace(/'/g, "''")}'`
}

function fileSize(path: string): number | undefined {
  try {
    return existsSync(path) ? statSync(path).size : undefined
  } catch {
    return undefined
  }
}

function formatBytes(value?: number): string {
  if (value === undefined) return '不存在'
  if (value >= 1024 ** 3) return `${(value / 1024 ** 3).toFixed(2)} GB`
  if (value >= 1024 ** 2) return `${(value / 1024 ** 2).toFixed(1)} MB`
  if (value >= 1024) return `${(value / 1024).toFixed(1)} KB`
  return `${value} B`
}

main()

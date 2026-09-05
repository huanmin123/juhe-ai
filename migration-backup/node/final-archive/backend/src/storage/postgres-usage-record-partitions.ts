import type { DatabaseClient } from './database-client.js'

export interface PostgresUsageRecordPartition {
  partitionName: string
  startDate: string
  endDate: string
}

const usageRecordPartitionPrefix = 'usage_records_'
const ensuredPartitionDateKeys = new Set<string>()

export function usageRecordPartitionDateKeyFromIso(value: string | undefined | null): string | undefined {
  const match = /^(\d{4})-(\d{2})-(\d{2})/.exec(String(value ?? '').trim())
  if (!match) return undefined
  const date = new Date(Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3])))
  if (
    date.getUTCFullYear() !== Number(match[1])
    || date.getUTCMonth() !== Number(match[2]) - 1
    || date.getUTCDate() !== Number(match[3])
  ) {
    return undefined
  }
  return `${match[1]}${match[2]}${match[3]}`
}

export function usageRecordPartitionDateKeyFromId(id: string | undefined | null): string | undefined {
  const match = /^usage_(\d{8})_s\d+_/.exec(String(id ?? '').trim())
  return match ? normalizeDateKey(match[1]) : undefined
}

export function postgresUsageRecordPartitionName(dateKey: string): string {
  const normalized = normalizeDateKey(dateKey)
  if (!normalized) {
    throw new Error(`使用记录分区日期无效：${dateKey}`)
  }
  return `${usageRecordPartitionPrefix}${normalized}`
}

export function postgresUsageRecordPartitionBounds(dateKey: string): { startDate: string; endDate: string } {
  const normalized = normalizeDateKey(dateKey)
  if (!normalized) {
    throw new Error(`使用记录分区日期无效：${dateKey}`)
  }
  const startDate = `${normalized.slice(0, 4)}-${normalized.slice(4, 6)}-${normalized.slice(6, 8)}`
  return {
    startDate,
    endDate: nextIsoDate(startDate)
  }
}

export async function ensurePostgresUsageRecordPartitions(client: DatabaseClient, createdAts: readonly string[]): Promise<void> {
  const dateKeys = [...new Set(createdAts.map(usageRecordPartitionDateKeyFromIso).filter((value): value is string => Boolean(value)))]
  for (const dateKey of dateKeys) {
    if (ensuredPartitionDateKeys.has(dateKey)) continue
    const partitionName = postgresUsageRecordPartitionName(dateKey)
    const bounds = postgresUsageRecordPartitionBounds(dateKey)
    await client.execute(`
      CREATE TABLE IF NOT EXISTS juhe_usage.${quoteIdentifier(partitionName)}
      PARTITION OF juhe_usage.usage_records
      FOR VALUES FROM ('${bounds.startDate}') TO ('${bounds.endDate}')
    `)
    ensuredPartitionDateKeys.add(dateKey)
  }
}

export async function listPostgresUsageRecordPartitions(client: DatabaseClient): Promise<PostgresUsageRecordPartition[]> {
  const rows = await client.query<{ partition_name?: string | null; partition_bound?: string | null }>(`
    SELECT child.relname AS partition_name,
           pg_get_expr(child.relpartbound, child.oid) AS partition_bound
    FROM pg_inherits inherit
    JOIN pg_class parent ON parent.oid = inherit.inhparent
    JOIN pg_namespace parent_namespace ON parent_namespace.oid = parent.relnamespace
    JOIN pg_class child ON child.oid = inherit.inhrelid
    WHERE parent_namespace.nspname = 'juhe_usage'
      AND parent.relname = 'usage_records'
      AND child.relname LIKE 'usage_records_%'
    ORDER BY child.relname ASC
  `)
  return rows
    .map((row) => parseUsageRecordPartition(row.partition_name, row.partition_bound))
    .filter((row): row is PostgresUsageRecordPartition => Boolean(row))
}

export async function countPostgresUsageRecordPartitionRows(client: DatabaseClient, partitionName: string): Promise<number> {
  const normalized = normalizePartitionName(partitionName)
  const row = await client.one<{ total?: number | string | null }>(`
    SELECT COUNT(*) AS total
    FROM juhe_usage.${quoteIdentifier(normalized)}
  `)
  return Number(row?.total ?? 0)
}

export async function dropPostgresUsageRecordPartition(client: DatabaseClient, partitionName: string): Promise<void> {
  const normalized = normalizePartitionName(partitionName)
  await client.execute(`ALTER TABLE juhe_usage.usage_records DETACH PARTITION juhe_usage.${quoteIdentifier(normalized)}`)
  await client.execute(`DROP TABLE IF EXISTS juhe_usage.${quoteIdentifier(normalized)}`)
  const dateKey = normalized.slice(usageRecordPartitionPrefix.length)
  ensuredPartitionDateKeys.delete(dateKey)
}

export function postgresUsageRecordPartitionPruningClauseForId(id: string): { clause: string; params: string[] } {
  const dateKey = usageRecordPartitionDateKeyFromId(id)
  if (!dateKey) return { clause: '', params: [] }
  const bounds = postgresUsageRecordPartitionBounds(dateKey)
  return {
    clause: 'AND ur.created_at >= ? AND ur.created_at < ?',
    params: [bounds.startDate, bounds.endDate]
  }
}

function parseUsageRecordPartition(partitionName: string | null | undefined, partitionBound: string | null | undefined): PostgresUsageRecordPartition | undefined {
  const normalizedName = normalizePartitionName(partitionName)
  const match = /FOR VALUES FROM \('(\d{4}-\d{2}-\d{2})'\) TO \('(\d{4}-\d{2}-\d{2})'\)/.exec(String(partitionBound ?? ''))
  if (!match) return undefined
  return {
    partitionName: normalizedName,
    startDate: match[1],
    endDate: match[2]
  }
}

function normalizePartitionName(partitionName: string | null | undefined): string {
  const normalized = String(partitionName ?? '').trim()
  if (!new RegExp(`^${usageRecordPartitionPrefix}\\d{8}$`).test(normalized)) {
    throw new Error(`使用记录分区表名无效：${partitionName}`)
  }
  return normalized
}

function normalizeDateKey(value: string): string | undefined {
  const normalized = value.trim()
  if (!/^\d{8}$/.test(normalized)) return undefined
  const year = Number(normalized.slice(0, 4))
  const month = Number(normalized.slice(4, 6))
  const day = Number(normalized.slice(6, 8))
  const date = new Date(Date.UTC(year, month - 1, day))
  if (date.getUTCFullYear() !== year || date.getUTCMonth() !== month - 1 || date.getUTCDate() !== day) return undefined
  return normalized
}

function nextIsoDate(dateKey: string): string {
  const date = new Date(`${dateKey}T00:00:00.000Z`)
  date.setUTCDate(date.getUTCDate() + 1)
  return date.toISOString().slice(0, 10)
}

function quoteIdentifier(identifier: string): string {
  return `"${identifier.replace(/"/g, '""')}"`
}

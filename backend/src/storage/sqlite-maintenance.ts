import type { DatabaseSync } from 'node:sqlite'

export interface SqliteWalCheckpointResult {
  label: string
  busy: number
  log: number
  checkpointed: number
}

export function checkpointSqliteWal(database: DatabaseSync, label: string): SqliteWalCheckpointResult | undefined {
  if (database.isTransaction) {
    return undefined
  }
  const row = database
    .prepare('PRAGMA wal_checkpoint(PASSIVE)')
    .get() as { busy?: number; log?: number; checkpointed?: number } | undefined
  database.exec('PRAGMA optimize')
  return {
    label,
    busy: numberValue(row?.busy),
    log: numberValue(row?.log),
    checkpointed: numberValue(row?.checkpointed)
  }
}

function numberValue(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

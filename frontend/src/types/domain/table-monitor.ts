export type MonitoredDatabaseRole = 'business' | 'dataset' | 'stats'

export interface DatabaseStorageSnapshotSummary {
  databaseRole: MonitoredDatabaseRole
  databasePath: string
  sampledAt: string
  fileBytes?: number
  walBytes?: number
  shmBytes?: number
  pageSize?: number
  pageCount?: number
  freelistCount?: number
  usedBytes?: number
  freeBytes?: number
  tableCount?: number
  indexCount?: number
}

export interface TableStorageSnapshotSummary {
  databaseRole: MonitoredDatabaseRole
  tableName: string
  sampledAt: string
  rowCount?: number
  tableBytes?: number
  indexBytes?: number
  totalBytes?: number
  pageCount?: number
  indexCount: number
  growthBytes1h?: number
  growthRows1h?: number
  growthBytes24h?: number
  growthRows24h?: number
}

export interface TableStorageOverview {
  sampledAt?: string
  databases: DatabaseStorageSnapshotSummary[]
  tables: TableStorageSnapshotSummary[]
}

export interface UsageRecordsCleanupResult {
  cutoffAt: string
  deletedRows: number
  batches: number
  batchSize: number
  maxBatches: number
  hasMore: boolean
  queued?: boolean
  eligibleRows?: number
  jobId?: string
  submittedAt?: string
  blockedReason?: string
}

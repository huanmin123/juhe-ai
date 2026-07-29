export type MonitoredDatabaseRole = 'business' | 'dataset' | 'usage-catalog' | 'stats' | 'codex-context-state'

export interface DatabaseStorageSnapshotSummary {
  databaseRole: MonitoredDatabaseRole
  databasePath: string
  sampledAt: string
  fileBytes?: number
  walBytes?: number
  shmBytes?: number
  freeBytes?: number
  tableCount?: number
}

export interface DatabaseStorageHistoryPoint {
  databaseRole: MonitoredDatabaseRole
  sampledAt: string
  fileBytes?: number
  walBytes?: number
  freeBytes?: number
  tableCount?: number
}

export interface TableStorageHistoryPoint {
  sampledAt: string
  rowCount?: number
  totalBytes?: number
}

export interface TableStorageOverviewSummary {
  databaseRole: MonitoredDatabaseRole
  tableName: string
  sampledAt: string
  tableKind?: string
  parentTableName?: string
  isPartition?: boolean
  isArchive?: boolean
  rowCount?: number
  tableBytes?: number
  indexBytes?: number
  indexToTableRatio?: number
  totalBytes?: number
  growthBytes1h?: number
  growthRows1h?: number
  growthBytes24h?: number
  growthRows24h?: number
}

export interface TableStorageOverview {
  sampledAt?: string
  databases: DatabaseStorageSnapshotSummary[]
  tables: TableStorageOverviewSummary[]
  page: number
  pageSize: number
  total: number
  hasMore: boolean
}

export interface NonBusinessDataCleanupResult {
  cutoffAt: string
  queued: boolean
  jobId?: string
  submittedAt?: string
  blockedReason?: string
}

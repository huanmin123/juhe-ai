export type MonitoredDatabaseRole = 'business' | 'records'

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
  recordMaintenance?: TableRecordMaintenanceOverview
  tables: TableStorageSnapshotSummary[]
}

export interface TableRecordMaintenanceOverview {
  apiKeyRecordCleanup?: ApiKeyRecordCleanupQueueSummary
}

export interface ApiKeyRecordCleanupQueueSummary {
  pendingTargets: number
  blockedTargets: number
  failedTargets: number
  oldestCreatedAt?: string
  lastAttemptAt?: string
}

export interface ApiKeyRecordCleanupQueueTarget {
  apiKeyId: string
  systemAccountId: string
  createdAt: string
  updatedAt: string
  attemptCount: number
  lastAttemptAt?: string
  lastBlockedReason?: string
  lastErrorMessage?: string
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

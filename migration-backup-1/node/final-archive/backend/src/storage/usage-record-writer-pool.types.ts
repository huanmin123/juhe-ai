import type {
  UsageRecordShardLocation,
  UsageRecordShardWriteResult,
  UsageRecordShardWriteRow
} from './usage-record-shards.js'

export type UsageRecordWriterOperation =
  | {
    type: 'write_usage_records'
    location: UsageRecordShardLocation
    rows: UsageRecordShardWriteRow[]
  }

export type UsageRecordWriterOperationResult<T extends UsageRecordWriterOperation = UsageRecordWriterOperation> =
  T extends { type: 'write_usage_records' } ? UsageRecordShardWriteResult :
  unknown

export interface UsageRecordWriterWorkerMessage {
  requestId: string
  operation: UsageRecordWriterOperation
}

export interface UsageRecordWriterWorkerResponse {
  requestId: string
  ok: boolean
  result?: unknown
  errorMessage?: string
}

import type {
  CodexContextCompactReadResult,
  CodexContextCompactStateIndex,
  CodexContextCompactStateIndexInput,
  CodexContextExpiredStateCleanupResult,
  CodexContextResponseChainReadResult,
  CodexContextResponseStateIndex,
  CodexContextResponseStateIndexInput
} from './codex-context-state.repository.js'
import type { RuntimeConfig } from '../config/runtime.js'

export type CodexContextStateWriterRuntimePatch = Pick<RuntimeConfig,
  | 'databasePath'
  | 'datasetDatabasePath'
  | 'usageCatalogDatabasePath'
  | 'statsDatabasePath'
  | 'usageShardRoot'
  | 'codexContextRoot'
  | 'codexContextStateShardRoot'
  | 'codexContextStateShardCount'
  | 'codexContextStateWriterPoolEnabled'
  | 'codexContextStateWriterPoolSize'
  | 'codexContextStateWriterQueueMaxItems'
  | 'processRole'
  | 'workerRole'
  | 'log'
>

export type CodexContextStateWriterOperation =
  | {
    type: 'save_response_session'
    input: CodexContextResponseStateIndex
  }
  | {
    type: 'save_response_sessions'
    inputs: CodexContextResponseStateIndex[]
  }
  | {
    type: 'save_response_row'
    input: CodexContextResponseStateIndex
  }
  | {
    type: 'save_response_rows'
    inputs: CodexContextResponseStateIndex[]
  }
  | {
    type: 'save_compact_session'
    input: CodexContextCompactStateIndex
  }
  | {
    type: 'save_compact_sessions'
    inputs: CodexContextCompactStateIndex[]
  }
  | {
    type: 'save_compact_row'
    input: CodexContextCompactStateIndex
  }
  | {
    type: 'save_compact_rows'
    inputs: CodexContextCompactStateIndex[]
  }
  | {
    type: 'read_response_row'
    responseId: string
  }
  | {
    type: 'read_compact_row'
    compactId: string
  }
  | {
    type: 'touch_session'
    sessionId: string
    now: string
    refreshExpiresAt: string
  }
  | {
    type: 'touch_sessions'
    touches: Array<{ sessionId: string; now: string; refreshExpiresAt: string }>
  }
  | {
    type: 'touch_response_rows'
    responseIds: string[]
    now: string
    refreshExpiresAt: string
  }
  | {
    type: 'touch_compact_row'
    compactId: string
    now: string
    refreshExpiresAt: string
  }
  | {
    type: 'touch_compact_rows'
    touches: Array<{ compactId: string; now: string; refreshExpiresAt: string }>
  }
  | {
    type: 'cleanup_expired_states'
    expiredBefore?: string
    limit?: number
  }
  | {
    type: 'cleanup_expired_states_shard'
    shardIndex: number
    expiredBefore?: string
    limit?: number
  }

export type CodexContextStateWriterOperationResult<T extends CodexContextStateWriterOperation = CodexContextStateWriterOperation> =
  T extends { type: 'save_response_session' } ? CodexContextResponseStateIndex :
  T extends { type: 'save_response_sessions' } ? { saved: number } :
  T extends { type: 'save_response_row' } ? CodexContextResponseStateIndex :
  T extends { type: 'save_response_rows' } ? { saved: number } :
  T extends { type: 'save_compact_session' } ? CodexContextCompactStateIndex :
  T extends { type: 'save_compact_sessions' } ? { saved: number } :
  T extends { type: 'save_compact_row' } ? CodexContextCompactStateIndex :
  T extends { type: 'save_compact_rows' } ? { saved: number } :
  T extends { type: 'read_response_row' } ? CodexContextResponseStateIndex | undefined :
  T extends { type: 'read_compact_row' } ? CodexContextCompactStateIndex | undefined :
  T extends { type: 'touch_session' } ? { touched: true } :
  T extends { type: 'touch_sessions' } ? { touched: number } :
  T extends { type: 'touch_response_rows' } ? { touched: number } :
  T extends { type: 'touch_compact_row' } ? { touched: true } :
  T extends { type: 'touch_compact_rows' } ? { touched: number } :
  T extends { type: 'cleanup_expired_states' } ? CodexContextExpiredStateCleanupResult :
  T extends { type: 'cleanup_expired_states_shard' } ? CodexContextExpiredStateCleanupResult :
  unknown

export interface CodexContextStateWriterWorkerMessage {
  requestId: string
  operation: CodexContextStateWriterOperation
}

export interface CodexContextStateWriterWorkerResponse {
  requestId: string
  ok: boolean
  result?: unknown
  errorMessage?: string
}

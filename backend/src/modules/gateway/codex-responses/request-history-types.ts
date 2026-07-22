import type { CodexContractRevision } from './contract-types.js'

export type ResponsesPersistenceScope =
  | 'none'
  | 'account'
  | 'upstream_bucket'
  | 'provider_global'
  | 'websocket_connection'

export interface CodexHistorySanitizerContext {
  store: boolean
  sourceScopeKey?: string
  targetScopeKey?: string
  targetPersistenceScope: ResponsesPersistenceScope
  contractRevision: CodexContractRevision
}

export interface CodexHistorySanitizerResult {
  items: unknown[]
  changed: boolean
  removedIdCount: number
  issueCodes: string[]
}

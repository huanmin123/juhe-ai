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
}

export interface CodexHistorySanitizerResult {
  items: unknown[]
  changed: boolean
  removedIdCount: number
  issueCodes: string[]
}

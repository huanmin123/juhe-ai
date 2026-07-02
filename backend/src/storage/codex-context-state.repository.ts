import type { DatabaseSync, StatementSync } from 'node:sqlite'

import {
  codexContextStateShardIndexForKey,
  codexContextStateShardIndexes,
  getCodexContextStateShardDatabase,
  nowIso,
  runInDatabaseTransaction
} from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'

export interface CodexContextStateBoundary {
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  providerCode: string
}

export interface CodexContextPayloadReference {
  storageKey: string
  storageOffsetBytes: number
  sha256: string
  rawSizeBytes: number
  compressedSizeBytes: number
  compression: 'gzip'
  schemaVersion: number
}

export interface CodexContextResponseStateIndexInput extends CodexContextStateBoundary, CodexContextPayloadReference {
  responseId: string
  sessionId: string
  previousResponseId?: string
  upstreamAccountId?: string
  model?: string
  upstreamModel?: string
  createdAt?: string
  expiresAt: string
}

export interface CodexContextResponseStateIndex extends CodexContextStateBoundary, CodexContextPayloadReference {
  responseId: string
  sessionId: string
  previousResponseId?: string
  upstreamAccountId?: string
  model?: string
  upstreamModel?: string
  createdAt: string
  updatedAt: string
  lastUsedAt: string
  expiresAt: string
}

export interface CodexContextCompactStateIndexInput extends CodexContextStateBoundary, CodexContextPayloadReference {
  compactId: string
  sessionId: string
  sourceResponseId?: string
  summaryDigest: string
  upstreamAccountId?: string
  model?: string
  upstreamModel?: string
  createdAt?: string
  expiresAt: string
}

export interface CodexContextCompactStateIndex extends CodexContextStateBoundary, CodexContextPayloadReference {
  compactId: string
  sessionId: string
  sourceResponseId?: string
  summaryDigest: string
  upstreamAccountId?: string
  model?: string
  upstreamModel?: string
  createdAt: string
  updatedAt: string
  lastUsedAt: string
  expiresAt: string
}

export type CodexContextResponseChainReadResult =
  | {
    outcome: 'found'
    sessionId: string
    responses: CodexContextResponseStateIndex[]
  }
  | {
    outcome: 'not_found' | 'expired' | 'boundary_mismatch' | 'chain_too_deep' | 'chain_broken'
    responseId: string
    sessionId?: string
  }

export type CodexContextCompactReadResult =
  | {
    outcome: 'found'
    compact: CodexContextCompactStateIndex
  }
  | {
    outcome: 'not_found' | 'expired' | 'boundary_mismatch'
    compactId: string
    sessionId?: string
  }

export interface CodexContextExpiredStateCleanupResult {
  deletedSessions: number
  deletedResponses: number
  deletedCompacts: number
  storageKeys: string[]
  hasMore: boolean
}

interface CodexContextSessionRow {
  id: string
  expires_at: string
}

interface CodexContextResponseStateRow {
  response_id: string
  session_id: string
  previous_response_id?: string | null
  system_account_id: string
  api_key_id?: string | null
  group_id: string
  provider_code: string
  upstream_account_id?: string | null
  model?: string | null
  upstream_model?: string | null
  storage_key: string
  storage_offset_bytes: number | bigint
  sha256: string
  raw_size_bytes: number | bigint
  compressed_size_bytes: number | bigint
  compression: string
  schema_version: number | bigint
  created_at: string
  updated_at: string
  last_used_at: string
  expires_at: string
}

interface CodexContextCompactStateRow {
  compact_id: string
  session_id: string
  source_response_id?: string | null
  summary_digest: string
  system_account_id: string
  api_key_id?: string | null
  group_id: string
  provider_code: string
  upstream_account_id?: string | null
  model?: string | null
  upstream_model?: string | null
  storage_key: string
  storage_offset_bytes: number | bigint
  sha256: string
  raw_size_bytes: number | bigint
  compressed_size_bytes: number | bigint
  compression: string
  schema_version: number | bigint
  created_at: string
  updated_at: string
  last_used_at: string
  expires_at: string
}

interface CodexContextSessionUpsertInput {
  sessionId: string
  boundary: CodexContextStateBoundary
  sourceResponseId?: string
  latestResponseId?: string
  latestCompactId?: string
  now: string
  expiresAt: string
}

export function saveCodexContextResponseStateIndex(input: CodexContextResponseStateIndexInput): CodexContextResponseStateIndex {
  const now = input.createdAt ?? nowIso()
  const row = normalizeCodexContextResponseStateIndexInput(input, now)
  upsertCodexContextResponseSessionIndex(row)
  saveCodexContextResponseStateIndexRow(row)
  return row
}

export function saveCodexContextResponseStateIndexRow(row: CodexContextResponseStateIndex): CodexContextResponseStateIndex {
  const database = responseDatabase(row.responseId)
  const statement = prepareCodexContextResponseStateIndexStatement(database)
  runInDatabaseTransaction(() => {
    insertCodexContextResponseStateIndexRow(statement, row)
  }, database)
  return row
}

export function saveCodexContextResponseStateIndexRows(rows: CodexContextResponseStateIndex[]): CodexContextResponseStateIndex[] {
  const rowsByShard = groupRowsByShard(rows, (row) => row.responseId)
  for (const [shardIndex, shardRows] of rowsByShard) {
    const database = getCodexContextStateShardDatabase(shardIndex)
    const statement = prepareCodexContextResponseStateIndexStatement(database)
    runInDatabaseTransaction(() => {
      for (const row of shardRows) {
        insertCodexContextResponseStateIndexRow(statement, row)
      }
    }, database)
  }
  return rows
}

export function saveCodexContextCompactStateIndex(input: CodexContextCompactStateIndexInput): CodexContextCompactStateIndex {
  const now = input.createdAt ?? nowIso()
  const row = normalizeCodexContextCompactStateIndexInput(input, now)
  upsertCodexContextCompactSessionIndex(row)
  saveCodexContextCompactStateIndexRow(row)
  return row
}

export function saveCodexContextCompactStateIndexRow(row: CodexContextCompactStateIndex): CodexContextCompactStateIndex {
  const database = compactDatabase(row.compactId)
  const statement = prepareCodexContextCompactStateIndexStatement(database)
  runInDatabaseTransaction(() => {
    insertCodexContextCompactStateIndexRow(statement, row)
  }, database)
  return row
}

export function saveCodexContextCompactStateIndexRows(rows: CodexContextCompactStateIndex[]): CodexContextCompactStateIndex[] {
  const rowsByShard = groupRowsByShard(rows, (row) => row.compactId)
  for (const [shardIndex, shardRows] of rowsByShard) {
    const database = getCodexContextStateShardDatabase(shardIndex)
    const statement = prepareCodexContextCompactStateIndexStatement(database)
    runInDatabaseTransaction(() => {
      for (const row of shardRows) {
        insertCodexContextCompactStateIndexRow(statement, row)
      }
    }, database)
  }
  return rows
}

export function readCodexContextResponseStateChain(input: {
  responseId: string
  boundary: CodexContextStateBoundary
  maxDepth?: number
  now?: string
  refreshExpiresAt?: string
}): CodexContextResponseChainReadResult {
  const responseId = normalizedRequiredText(input.responseId, 'responseId')
  const now = input.now ?? nowIso()
  const maxDepth = Math.max(1, Math.min(Math.trunc(input.maxDepth ?? 64), 256))
  const rows: CodexContextResponseStateIndex[] = []
  let cursor: string | undefined = responseId
  for (let depth = 0; cursor && depth < maxDepth; depth += 1) {
    const mapped = readCodexContextResponseStateRow(cursor)
    if (!mapped) {
      return {
        outcome: rows.length === 0 ? 'not_found' : 'chain_broken',
        responseId: cursor
      }
    }
    if (mapped.expiresAt < now) {
      return {
        outcome: 'expired',
        responseId: mapped.responseId,
        sessionId: mapped.sessionId
      }
    }
    if (!matchesBoundary(mapped, input.boundary)) {
      return {
        outcome: 'boundary_mismatch',
        responseId: mapped.responseId,
        sessionId: mapped.sessionId
      }
    }
    rows.push(mapped)
    cursor = mapped.previousResponseId
  }
  if (cursor) {
    return {
      outcome: 'chain_too_deep',
      responseId: cursor,
      sessionId: rows[0]?.sessionId
    }
  }
  const orderedRows = rows.reverse()
  touchCodexContextResponseChain(orderedRows, now, input.refreshExpiresAt ?? now)
  return {
    outcome: 'found',
    sessionId: orderedRows[0]?.sessionId ?? responseId,
    responses: orderedRows
  }
}

export function readCodexContextCompactState(input: {
  compactId: string
  boundary: CodexContextStateBoundary
  now?: string
  refreshExpiresAt?: string
}): CodexContextCompactReadResult {
  const compactId = normalizedRequiredText(input.compactId, 'compactId')
  const now = input.now ?? nowIso()
  const mapped = readCodexContextCompactStateRow(compactId)
  if (!mapped) {
    return { outcome: 'not_found', compactId }
  }
  if (mapped.expiresAt < now) {
    return { outcome: 'expired', compactId, sessionId: mapped.sessionId }
  }
  if (!matchesBoundary(mapped, input.boundary)) {
    return { outcome: 'boundary_mismatch', compactId, sessionId: mapped.sessionId }
  }
  touchCodexContextCompact(mapped, now, input.refreshExpiresAt ?? now)
  return { outcome: 'found', compact: mapped }
}

export function cleanupExpiredCodexContextStates(input: {
  expiredBefore?: string
  limit?: number
} = {}): CodexContextExpiredStateCleanupResult {
  const expiredBefore = input.expiredBefore ?? nowIso()
  const limit = Math.max(1, Math.min(Math.trunc(input.limit ?? 1000), 10000))
  const expiredSessions = selectExpiredSessions(expiredBefore, limit)
  return cleanupExpiredCodexContextStateSessionRows(expiredSessions, expiredBefore)
}

export async function saveCodexContextResponseStateIndexAsync(input: CodexContextResponseStateIndexInput): Promise<CodexContextResponseStateIndex> {
  const now = input.createdAt ?? nowIso()
  const row = normalizeCodexContextResponseStateIndexInput(input, now)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  await client.transaction(async (tx) => {
    await upsertCodexContextSessionIndexAsync(tx, {
      sessionId: row.sessionId,
      boundary: row,
      sourceResponseId: row.previousResponseId ? undefined : row.responseId,
      latestResponseId: row.responseId,
      latestCompactId: undefined,
      now: row.updatedAt,
      expiresAt: row.expiresAt
    })
    await insertCodexContextResponseStateIndexRowAsync(tx, row)
  })
  return row
}

export async function saveCodexContextCompactStateIndexAsync(input: CodexContextCompactStateIndexInput): Promise<CodexContextCompactStateIndex> {
  const now = input.createdAt ?? nowIso()
  const row = normalizeCodexContextCompactStateIndexInput(input, now)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  await client.transaction(async (tx) => {
    await upsertCodexContextSessionIndexAsync(tx, {
      sessionId: row.sessionId,
      boundary: row,
      sourceResponseId: undefined,
      latestResponseId: undefined,
      latestCompactId: row.compactId,
      now: row.updatedAt,
      expiresAt: row.expiresAt
    })
    await insertCodexContextCompactStateIndexRowAsync(tx, row)
  })
  return row
}

export async function readCodexContextResponseStateChainAsync(input: {
  responseId: string
  boundary: CodexContextStateBoundary
  maxDepth?: number
  now?: string
  refreshExpiresAt?: string
}): Promise<CodexContextResponseChainReadResult> {
  const responseId = normalizedRequiredText(input.responseId, 'responseId')
  const now = input.now ?? nowIso()
  const maxDepth = Math.max(1, Math.min(Math.trunc(input.maxDepth ?? 64), 256))
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows: CodexContextResponseStateIndex[] = []
  let cursor: string | undefined = responseId
  for (let depth = 0; cursor && depth < maxDepth; depth += 1) {
    const mapped = await readCodexContextResponseStateRowAsync(client, cursor)
    if (!mapped) {
      return {
        outcome: rows.length === 0 ? 'not_found' : 'chain_broken',
        responseId: cursor
      }
    }
    if (mapped.expiresAt < now) {
      return {
        outcome: 'expired',
        responseId: mapped.responseId,
        sessionId: mapped.sessionId
      }
    }
    if (!matchesBoundary(mapped, input.boundary)) {
      return {
        outcome: 'boundary_mismatch',
        responseId: mapped.responseId,
        sessionId: mapped.sessionId
      }
    }
    rows.push(mapped)
    cursor = mapped.previousResponseId
  }
  if (cursor) {
    return {
      outcome: 'chain_too_deep',
      responseId: cursor,
      sessionId: rows[0]?.sessionId
    }
  }
  const orderedRows = rows.reverse()
  await touchCodexContextResponseChainAsync(client, orderedRows, now, input.refreshExpiresAt ?? now)
  return {
    outcome: 'found',
    sessionId: orderedRows[0]?.sessionId ?? responseId,
    responses: orderedRows
  }
}

export async function readCodexContextCompactStateAsync(input: {
  compactId: string
  boundary: CodexContextStateBoundary
  now?: string
  refreshExpiresAt?: string
}): Promise<CodexContextCompactReadResult> {
  const compactId = normalizedRequiredText(input.compactId, 'compactId')
  const now = input.now ?? nowIso()
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const mapped = await readCodexContextCompactStateRowAsync(client, compactId)
  if (!mapped) {
    return { outcome: 'not_found', compactId }
  }
  if (mapped.expiresAt < now) {
    return { outcome: 'expired', compactId, sessionId: mapped.sessionId }
  }
  if (!matchesBoundary(mapped, input.boundary)) {
    return { outcome: 'boundary_mismatch', compactId, sessionId: mapped.sessionId }
  }
  await touchCodexContextCompactAsync(client, mapped, now, input.refreshExpiresAt ?? now)
  return { outcome: 'found', compact: mapped }
}

export async function cleanupExpiredCodexContextStatesAsync(input: {
  expiredBefore?: string
  limit?: number
} = {}): Promise<CodexContextExpiredStateCleanupResult> {
  const expiredBefore = input.expiredBefore ?? nowIso()
  const limit = Math.max(1, Math.min(Math.trunc(input.limit ?? 1000), 10000))
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const expiredRows = await client.query<CodexContextSessionRow>(`
    SELECT id, expires_at
    FROM ${codexContextTable(client, 'codex_context_sessions')}
    WHERE expires_at < ?
    ORDER BY expires_at ASC, id ASC
    LIMIT ?
  `, [expiredBefore, limit + 1])
  const rows = expiredRows.slice(0, limit)
  if (!rows.length) {
    return { deletedSessions: 0, deletedResponses: 0, deletedCompacts: 0, storageKeys: [], hasMore: false }
  }
  return cleanupExpiredCodexContextStateSessionRowsAsync(client, rows, expiredBefore, expiredRows.length > limit)
}

export function cleanupExpiredCodexContextStatesInShard(input: {
  shardIndex: number
  expiredBefore?: string
  limit?: number
}): CodexContextExpiredStateCleanupResult {
  const expiredBefore = input.expiredBefore ?? nowIso()
  const limit = Math.max(1, Math.min(Math.trunc(input.limit ?? 1000), 10000))
  const shardIndex = normalizedShardIndex(input.shardIndex)
  const expiredSessions = selectExpiredSessions(expiredBefore, limit, [shardIndex])
  return cleanupExpiredCodexContextStateSessionRows(expiredSessions, expiredBefore)
}

function cleanupExpiredCodexContextStateSessionRows(
  expiredSessions: {
    rows: Array<CodexContextSessionRow & { shardIndex: number }>
    hasMore: boolean
  },
  expiredBefore: string
): CodexContextExpiredStateCleanupResult {
  if (expiredSessions.rows.length === 0) {
    return {
      deletedSessions: 0,
      deletedResponses: 0,
      deletedCompacts: 0,
      storageKeys: [],
      hasMore: false
    }
  }

  const candidateStorageKeys = new Set<string>()
  const sessionIds = expiredSessions.rows.map((row) => row.id)
  const deletedResponses = deleteExpiredRowsBySessionIds('codex_context_responses', sessionIds, expiredBefore, candidateStorageKeys)
  const deletedCompacts = deleteExpiredRowsBySessionIds('codex_context_compacts', sessionIds, expiredBefore, candidateStorageKeys)
  const remainingExpiresAtBySessionId = selectRemainingSessionExpiresAtBySessionIds(sessionIds)
  const deletedSessions = deleteOrRefreshSessionRows(expiredSessions.rows, remainingExpiresAtBySessionId, nowIso())
  const storageKeys = filterUnreferencedStorageKeys(candidateStorageKeys)
  return {
    deletedSessions,
    deletedResponses,
    deletedCompacts,
    storageKeys,
    hasMore: expiredSessions.hasMore
  }
}

export function upsertCodexContextResponseSessionIndex(row: CodexContextResponseStateIndex): void {
  upsertCodexContextSession({
    sessionId: row.sessionId,
    boundary: row,
    sourceResponseId: row.previousResponseId ? undefined : row.responseId,
    latestResponseId: row.responseId,
    latestCompactId: undefined,
    now: row.updatedAt,
    expiresAt: row.expiresAt
  })
}

export function upsertCodexContextResponseSessionIndexes(rows: CodexContextResponseStateIndex[]): void {
  upsertCodexContextSessionBatch(rows.map((row) => ({
    sessionId: row.sessionId,
    boundary: row,
    sourceResponseId: row.previousResponseId ? undefined : row.responseId,
    latestResponseId: row.responseId,
    latestCompactId: undefined,
    now: row.updatedAt,
    expiresAt: row.expiresAt
  })))
}

export function upsertCodexContextCompactSessionIndex(row: CodexContextCompactStateIndex): void {
  upsertCodexContextSession({
    sessionId: row.sessionId,
    boundary: row,
    sourceResponseId: undefined,
    latestResponseId: undefined,
    latestCompactId: row.compactId,
    now: row.updatedAt,
    expiresAt: row.expiresAt
  })
}

export function upsertCodexContextCompactSessionIndexes(rows: CodexContextCompactStateIndex[]): void {
  upsertCodexContextSessionBatch(rows.map((row) => ({
    sessionId: row.sessionId,
    boundary: row,
    sourceResponseId: undefined,
    latestResponseId: undefined,
    latestCompactId: row.compactId,
    now: row.updatedAt,
    expiresAt: row.expiresAt
  })))
}

export function readCodexContextResponseStateRow(responseId: string): CodexContextResponseStateIndex | undefined {
  const normalizedResponseId = normalizedRequiredText(responseId, 'responseId')
  const row = responseDatabase(normalizedResponseId)
    .prepare('SELECT * FROM codex_context_responses WHERE response_id = ?')
    .get(normalizedResponseId) as CodexContextResponseStateRow | undefined
  return row ? mapResponseStateRow(row) : undefined
}

export function readCodexContextCompactStateRow(compactId: string): CodexContextCompactStateIndex | undefined {
  const normalizedCompactId = normalizedRequiredText(compactId, 'compactId')
  const row = compactDatabase(normalizedCompactId)
    .prepare('SELECT * FROM codex_context_compacts WHERE compact_id = ?')
    .get(normalizedCompactId) as CodexContextCompactStateRow | undefined
  return row ? mapCompactStateRow(row) : undefined
}

async function readCodexContextResponseStateRowAsync(client: DatabaseClient, responseId: string): Promise<CodexContextResponseStateIndex | undefined> {
  const normalizedResponseId = normalizedRequiredText(responseId, 'responseId')
  const row = await client.one<CodexContextResponseStateRow>(`
    SELECT *
    FROM ${codexContextTable(client, 'codex_context_responses')}
    WHERE response_id = ?
    LIMIT 1
  `, [normalizedResponseId])
  return row ? mapResponseStateRow(row) : undefined
}

async function readCodexContextCompactStateRowAsync(client: DatabaseClient, compactId: string): Promise<CodexContextCompactStateIndex | undefined> {
  const normalizedCompactId = normalizedRequiredText(compactId, 'compactId')
  const row = await client.one<CodexContextCompactStateRow>(`
    SELECT *
    FROM ${codexContextTable(client, 'codex_context_compacts')}
    WHERE compact_id = ?
    LIMIT 1
  `, [normalizedCompactId])
  return row ? mapCompactStateRow(row) : undefined
}

export function touchCodexContextSessionState(sessionId: string, now: string, refreshExpiresAt: string): void {
  const normalizedSessionId = normalizedRequiredText(sessionId, 'sessionId')
  const database = sessionDatabase(normalizedSessionId)
  const statement = prepareCodexContextSessionTouchStatement(database)
  runInDatabaseTransaction(() => {
    touchCodexContextSessionStateRow(statement, {
      sessionId: normalizedSessionId,
      now,
      refreshExpiresAt
    })
  }, database)
}

export function touchCodexContextSessionStates(touches: Array<{ sessionId: string; now: string; refreshExpiresAt: string }>): void {
  const normalizedTouches = coalesceSessionTouches(touches)
  const touchesByShard = groupRowsByShard(normalizedTouches, (touch) => touch.sessionId)
  for (const [shardIndex, shardTouches] of touchesByShard) {
    const database = getCodexContextStateShardDatabase(shardIndex)
    const statement = prepareCodexContextSessionTouchStatement(database)
    runInDatabaseTransaction(() => {
      for (const touch of shardTouches) {
        touchCodexContextSessionStateRow(statement, touch)
      }
    }, database)
  }
}

export function touchCodexContextResponseStateRows(responseIds: string[], now: string, refreshExpiresAt: string): void {
  updateRowsByResponseIds(responseIds, now, refreshExpiresAt)
}

export function touchCodexContextCompactStateRow(compactId: string, now: string, refreshExpiresAt: string): void {
  const normalizedCompactId = normalizedRequiredText(compactId, 'compactId')
  const compactDb = compactDatabase(normalizedCompactId)
  const statement = prepareCodexContextCompactTouchStatement(compactDb)
  runInDatabaseTransaction(() => {
    touchCodexContextCompactStateIndexRow(statement, {
      compactId: normalizedCompactId,
      now,
      refreshExpiresAt
    })
  }, compactDb)
}

export function touchCodexContextCompactStateRows(touches: Array<{ compactId: string; now: string; refreshExpiresAt: string }>): void {
  const normalizedTouches = coalesceCompactTouches(touches)
  const touchesByShard = groupRowsByShard(normalizedTouches, (touch) => touch.compactId)
  for (const [shardIndex, shardTouches] of touchesByShard) {
    const database = getCodexContextStateShardDatabase(shardIndex)
    const statement = prepareCodexContextCompactTouchStatement(database)
    runInDatabaseTransaction(() => {
      for (const touch of shardTouches) {
        touchCodexContextCompactStateIndexRow(statement, touch)
      }
    }, database)
  }
}

function upsertCodexContextSession(input: CodexContextSessionUpsertInput): void {
  const database = sessionDatabase(input.sessionId)
  const statement = prepareCodexContextSessionUpsertStatement(database)
  runInDatabaseTransaction(() => {
    upsertCodexContextSessionRow(statement, input)
  }, database)
}

function upsertCodexContextSessionBatch(inputs: CodexContextSessionUpsertInput[]): void {
  const inputsByShard = groupRowsByShard(inputs, (input) => input.sessionId)
  for (const [shardIndex, shardInputs] of inputsByShard) {
    const database = getCodexContextStateShardDatabase(shardIndex)
    const statement = prepareCodexContextSessionUpsertStatement(database)
    runInDatabaseTransaction(() => {
      for (const input of shardInputs) {
        upsertCodexContextSessionRow(statement, input)
      }
    }, database)
  }
}

function touchCodexContextResponseChain(rows: CodexContextResponseStateIndex[], now: string, refreshExpiresAt: string): void {
  if (rows.length === 0) return
  const sessionId = rows[0]?.sessionId
  if (sessionId) {
    touchCodexContextSessionState(sessionId, now, refreshExpiresAt)
  }
  touchCodexContextResponseStateRows(rows.map((row) => row.responseId), now, refreshExpiresAt)
}

function touchCodexContextCompact(row: CodexContextCompactStateIndex, now: string, refreshExpiresAt: string): void {
  touchCodexContextCompactStateRow(row.compactId, now, refreshExpiresAt)
  touchCodexContextSessionState(row.sessionId, now, refreshExpiresAt)
}

async function touchCodexContextResponseChainAsync(client: DatabaseClient, rows: CodexContextResponseStateIndex[], now: string, refreshExpiresAt: string): Promise<void> {
  if (rows.length === 0) return
  const sessionId = rows[0]?.sessionId
  await client.transaction(async (tx) => {
    if (sessionId) {
      await tx.execute(`
        UPDATE ${codexContextTable(tx, 'codex_context_sessions')}
        SET last_used_at = ?, updated_at = ?, expires_at = ?
        WHERE id = ?
      `, [now, now, refreshExpiresAt, sessionId])
    }
    const responseIds = rows.map((row) => row.responseId)
    for (const chunk of chunkValues(responseIds, 900)) {
      await tx.execute(`
        UPDATE ${codexContextTable(tx, 'codex_context_responses')}
        SET last_used_at = ?, updated_at = ?, expires_at = ?
        WHERE response_id IN (${chunk.map(() => '?').join(', ')})
      `, [now, now, refreshExpiresAt, ...chunk])
    }
  })
}

async function touchCodexContextCompactAsync(client: DatabaseClient, row: CodexContextCompactStateIndex, now: string, refreshExpiresAt: string): Promise<void> {
  await client.transaction(async (tx) => {
    await tx.execute(`
      UPDATE ${codexContextTable(tx, 'codex_context_compacts')}
      SET last_used_at = ?, updated_at = ?, expires_at = ?
      WHERE compact_id = ?
    `, [now, now, refreshExpiresAt, row.compactId])
    await tx.execute(`
      UPDATE ${codexContextTable(tx, 'codex_context_sessions')}
      SET last_used_at = ?, updated_at = ?, expires_at = ?
      WHERE id = ?
    `, [now, now, refreshExpiresAt, row.sessionId])
  })
}

function prepareCodexContextSessionUpsertStatement(database: DatabaseSync): StatementSync {
  return database.prepare(`
    INSERT INTO codex_context_sessions (
      id, system_account_id, api_key_id, group_id, provider_code,
      source_response_id, latest_response_id, latest_compact_id,
      created_at, updated_at, last_used_at, expires_at
    )
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(id) DO UPDATE SET
      source_response_id = COALESCE(codex_context_sessions.source_response_id, excluded.source_response_id),
      latest_response_id = COALESCE(excluded.latest_response_id, codex_context_sessions.latest_response_id),
      latest_compact_id = COALESCE(excluded.latest_compact_id, codex_context_sessions.latest_compact_id),
      updated_at = excluded.updated_at,
      last_used_at = excluded.last_used_at,
      expires_at = excluded.expires_at
  `)
}

function upsertCodexContextSessionRow(statement: StatementSync, input: CodexContextSessionUpsertInput): void {
  statement.run(
    input.sessionId,
    input.boundary.systemAccountId,
    input.boundary.apiKeyId ?? null,
    input.boundary.groupId,
    input.boundary.providerCode,
    input.sourceResponseId ?? null,
    input.latestResponseId ?? null,
    input.latestCompactId ?? null,
    input.now,
    input.now,
    input.now,
    input.expiresAt
  )
}

async function upsertCodexContextSessionIndexAsync(client: DatabaseClient, input: CodexContextSessionUpsertInput): Promise<void> {
  await client.execute(`
    INSERT INTO ${codexContextTable(client, 'codex_context_sessions')} (
      id, system_account_id, api_key_id, group_id, provider_code,
      source_response_id, latest_response_id, latest_compact_id,
      created_at, updated_at, last_used_at, expires_at
    )
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(id) DO UPDATE SET
      source_response_id = COALESCE(codex_context_sessions.source_response_id, excluded.source_response_id),
      latest_response_id = COALESCE(excluded.latest_response_id, codex_context_sessions.latest_response_id),
      latest_compact_id = COALESCE(excluded.latest_compact_id, codex_context_sessions.latest_compact_id),
      updated_at = excluded.updated_at,
      last_used_at = excluded.last_used_at,
      expires_at = excluded.expires_at
  `, [
    input.sessionId,
    input.boundary.systemAccountId,
    input.boundary.apiKeyId ?? null,
    input.boundary.groupId,
    input.boundary.providerCode,
    input.sourceResponseId ?? null,
    input.latestResponseId ?? null,
    input.latestCompactId ?? null,
    input.now,
    input.now,
    input.now,
    input.expiresAt
  ])
}

function prepareCodexContextResponseStateIndexStatement(database: DatabaseSync): StatementSync {
  return database.prepare(`
    INSERT INTO codex_context_responses (
      response_id, session_id, previous_response_id, system_account_id, api_key_id, group_id,
      provider_code,
      upstream_account_id, model, upstream_model, storage_key, storage_offset_bytes, sha256,
      raw_size_bytes, compressed_size_bytes, compression, schema_version,
      created_at, updated_at, last_used_at, expires_at
    )
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(response_id) DO UPDATE SET
      session_id = excluded.session_id,
      previous_response_id = excluded.previous_response_id,
      upstream_account_id = excluded.upstream_account_id,
      model = excluded.model,
      upstream_model = excluded.upstream_model,
      storage_key = excluded.storage_key,
      storage_offset_bytes = excluded.storage_offset_bytes,
      sha256 = excluded.sha256,
      raw_size_bytes = excluded.raw_size_bytes,
      compressed_size_bytes = excluded.compressed_size_bytes,
      compression = excluded.compression,
      schema_version = excluded.schema_version,
      updated_at = excluded.updated_at,
      last_used_at = excluded.last_used_at,
      expires_at = excluded.expires_at
  `)
}

function insertCodexContextResponseStateIndexRow(statement: StatementSync, row: CodexContextResponseStateIndex): void {
  statement.run(
    row.responseId,
    row.sessionId,
    row.previousResponseId ?? null,
    row.systemAccountId,
    row.apiKeyId ?? null,
    row.groupId,
    row.providerCode,
    row.upstreamAccountId ?? null,
    row.model ?? null,
    row.upstreamModel ?? null,
    row.storageKey,
    row.storageOffsetBytes,
    row.sha256,
    row.rawSizeBytes,
    row.compressedSizeBytes,
    row.compression,
    row.schemaVersion,
    row.createdAt,
    row.updatedAt,
    row.lastUsedAt,
    row.expiresAt
  )
}

async function insertCodexContextResponseStateIndexRowAsync(client: DatabaseClient, row: CodexContextResponseStateIndex): Promise<void> {
  await client.execute(`
    INSERT INTO ${codexContextTable(client, 'codex_context_responses')} (
      response_id, session_id, previous_response_id, system_account_id, api_key_id, group_id,
      provider_code,
      upstream_account_id, model, upstream_model, storage_key, storage_offset_bytes, sha256,
      raw_size_bytes, compressed_size_bytes, compression, schema_version,
      created_at, updated_at, last_used_at, expires_at
    )
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(response_id) DO UPDATE SET
      session_id = excluded.session_id,
      previous_response_id = excluded.previous_response_id,
      upstream_account_id = excluded.upstream_account_id,
      model = excluded.model,
      upstream_model = excluded.upstream_model,
      storage_key = excluded.storage_key,
      storage_offset_bytes = excluded.storage_offset_bytes,
      sha256 = excluded.sha256,
      raw_size_bytes = excluded.raw_size_bytes,
      compressed_size_bytes = excluded.compressed_size_bytes,
      compression = excluded.compression,
      schema_version = excluded.schema_version,
      updated_at = excluded.updated_at,
      last_used_at = excluded.last_used_at,
      expires_at = excluded.expires_at
  `, [
    row.responseId,
    row.sessionId,
    row.previousResponseId ?? null,
    row.systemAccountId,
    row.apiKeyId ?? null,
    row.groupId,
    row.providerCode,
    row.upstreamAccountId ?? null,
    row.model ?? null,
    row.upstreamModel ?? null,
    row.storageKey,
    row.storageOffsetBytes,
    row.sha256,
    row.rawSizeBytes,
    row.compressedSizeBytes,
    row.compression,
    row.schemaVersion,
    row.createdAt,
    row.updatedAt,
    row.lastUsedAt,
    row.expiresAt
  ])
}

function prepareCodexContextCompactStateIndexStatement(database: DatabaseSync): StatementSync {
  return database.prepare(`
    INSERT INTO codex_context_compacts (
      compact_id, session_id, source_response_id, summary_digest, system_account_id, api_key_id, group_id,
      provider_code,
      upstream_account_id, model, upstream_model, storage_key, storage_offset_bytes, sha256,
      raw_size_bytes, compressed_size_bytes, compression, schema_version,
      created_at, updated_at, last_used_at, expires_at
    )
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(compact_id) DO UPDATE SET
      session_id = excluded.session_id,
      source_response_id = excluded.source_response_id,
      summary_digest = excluded.summary_digest,
      upstream_account_id = excluded.upstream_account_id,
      model = excluded.model,
      upstream_model = excluded.upstream_model,
      storage_key = excluded.storage_key,
      storage_offset_bytes = excluded.storage_offset_bytes,
      sha256 = excluded.sha256,
      raw_size_bytes = excluded.raw_size_bytes,
      compressed_size_bytes = excluded.compressed_size_bytes,
      compression = excluded.compression,
      schema_version = excluded.schema_version,
      updated_at = excluded.updated_at,
      last_used_at = excluded.last_used_at,
      expires_at = excluded.expires_at
  `)
}

function insertCodexContextCompactStateIndexRow(statement: StatementSync, row: CodexContextCompactStateIndex): void {
  statement.run(
    row.compactId,
    row.sessionId,
    row.sourceResponseId ?? null,
    row.summaryDigest,
    row.systemAccountId,
    row.apiKeyId ?? null,
    row.groupId,
    row.providerCode,
    row.upstreamAccountId ?? null,
    row.model ?? null,
    row.upstreamModel ?? null,
    row.storageKey,
    row.storageOffsetBytes,
    row.sha256,
    row.rawSizeBytes,
    row.compressedSizeBytes,
    row.compression,
    row.schemaVersion,
    row.createdAt,
    row.updatedAt,
    row.lastUsedAt,
    row.expiresAt
  )
}

async function insertCodexContextCompactStateIndexRowAsync(client: DatabaseClient, row: CodexContextCompactStateIndex): Promise<void> {
  await client.execute(`
    INSERT INTO ${codexContextTable(client, 'codex_context_compacts')} (
      compact_id, session_id, source_response_id, summary_digest, system_account_id, api_key_id, group_id,
      provider_code,
      upstream_account_id, model, upstream_model, storage_key, storage_offset_bytes, sha256,
      raw_size_bytes, compressed_size_bytes, compression, schema_version,
      created_at, updated_at, last_used_at, expires_at
    )
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(compact_id) DO UPDATE SET
      session_id = excluded.session_id,
      source_response_id = excluded.source_response_id,
      summary_digest = excluded.summary_digest,
      upstream_account_id = excluded.upstream_account_id,
      model = excluded.model,
      upstream_model = excluded.upstream_model,
      storage_key = excluded.storage_key,
      storage_offset_bytes = excluded.storage_offset_bytes,
      sha256 = excluded.sha256,
      raw_size_bytes = excluded.raw_size_bytes,
      compressed_size_bytes = excluded.compressed_size_bytes,
      compression = excluded.compression,
      schema_version = excluded.schema_version,
      updated_at = excluded.updated_at,
      last_used_at = excluded.last_used_at,
      expires_at = excluded.expires_at
  `, [
    row.compactId,
    row.sessionId,
    row.sourceResponseId ?? null,
    row.summaryDigest,
    row.systemAccountId,
    row.apiKeyId ?? null,
    row.groupId,
    row.providerCode,
    row.upstreamAccountId ?? null,
    row.model ?? null,
    row.upstreamModel ?? null,
    row.storageKey,
    row.storageOffsetBytes,
    row.sha256,
    row.rawSizeBytes,
    row.compressedSizeBytes,
    row.compression,
    row.schemaVersion,
    row.createdAt,
    row.updatedAt,
    row.lastUsedAt,
    row.expiresAt
  ])
}

function prepareCodexContextSessionTouchStatement(database: DatabaseSync): StatementSync {
  return database.prepare(`
    UPDATE codex_context_sessions
    SET last_used_at = ?, updated_at = ?, expires_at = ?
    WHERE id = ?
  `)
}

function touchCodexContextSessionStateRow(
  statement: StatementSync,
  touch: { sessionId: string; now: string; refreshExpiresAt: string }
): void {
  statement.run(touch.now, touch.now, touch.refreshExpiresAt, touch.sessionId)
}

function prepareCodexContextCompactTouchStatement(database: DatabaseSync): StatementSync {
  return database.prepare(`
    UPDATE codex_context_compacts
    SET last_used_at = ?, updated_at = ?, expires_at = ?
    WHERE compact_id = ?
  `)
}

function touchCodexContextCompactStateIndexRow(
  statement: StatementSync,
  touch: { compactId: string; now: string; refreshExpiresAt: string }
): void {
  statement.run(touch.now, touch.now, touch.refreshExpiresAt, touch.compactId)
}

function coalesceSessionTouches(touches: Array<{ sessionId: string; now: string; refreshExpiresAt: string }>): Array<{ sessionId: string; now: string; refreshExpiresAt: string }> {
  const latestBySessionId = new Map<string, { sessionId: string; now: string; refreshExpiresAt: string }>()
  for (const touch of touches) {
    const sessionId = normalizedRequiredText(touch.sessionId, 'sessionId')
    const normalizedTouch = { ...touch, sessionId }
    const existing = latestBySessionId.get(sessionId)
    if (!existing || normalizedTouch.refreshExpiresAt > existing.refreshExpiresAt || normalizedTouch.now > existing.now) {
      latestBySessionId.set(sessionId, normalizedTouch)
    }
  }
  return [...latestBySessionId.values()]
}

function coalesceCompactTouches(touches: Array<{ compactId: string; now: string; refreshExpiresAt: string }>): Array<{ compactId: string; now: string; refreshExpiresAt: string }> {
  const latestByCompactId = new Map<string, { compactId: string; now: string; refreshExpiresAt: string }>()
  for (const touch of touches) {
    const compactId = normalizedRequiredText(touch.compactId, 'compactId')
    const normalizedTouch = { ...touch, compactId }
    const existing = latestByCompactId.get(compactId)
    if (!existing || normalizedTouch.refreshExpiresAt > existing.refreshExpiresAt || normalizedTouch.now > existing.now) {
      latestByCompactId.set(compactId, normalizedTouch)
    }
  }
  return [...latestByCompactId.values()]
}

function updateRowsByResponseIds(responseIds: string[], now: string, refreshExpiresAt: string): void {
  const grouped = groupKeysByShard(responseIds)
  for (const [shardIndex, ids] of grouped) {
    const database = getCodexContextStateShardDatabase(shardIndex)
    runInDatabaseTransaction(() => {
      for (const chunk of chunkValues(ids, 900)) {
        const placeholders = sqlPlaceholders(chunk.length)
        database.prepare(`
          UPDATE codex_context_responses
          SET last_used_at = ?, updated_at = ?, expires_at = ?
          WHERE response_id IN (${placeholders})
        `).run(now, now, refreshExpiresAt, ...chunk)
      }
    }, database)
  }
}

function selectExpiredSessions(expiredBefore: string, limit: number, shardIndexes = codexContextStateShardIndexes()): {
  rows: Array<CodexContextSessionRow & { shardIndex: number }>
  hasMore: boolean
} {
  const rows: Array<CodexContextSessionRow & { shardIndex: number }> = []
  let hasMore = false
  for (const shardIndex of shardIndexes) {
    const remaining = limit - rows.length
    if (remaining <= 0) {
      hasMore = true
      break
    }
    const database = getCodexContextStateShardDatabase(shardIndex)
    const shardRows = database.prepare(`
      SELECT id, expires_at
      FROM codex_context_sessions
      WHERE expires_at < ?
      ORDER BY expires_at ASC, id ASC
      LIMIT ?
    `).all(expiredBefore, remaining + 1) as unknown as CodexContextSessionRow[]
    if (shardRows.length > remaining) {
      hasMore = true
    }
    rows.push(...shardRows.slice(0, remaining).map((row) => ({ ...row, shardIndex })))
  }
  return { rows, hasMore }
}

function deleteExpiredRowsBySessionIds(
  table: 'codex_context_responses' | 'codex_context_compacts',
  sessionIds: string[],
  expiredBefore: string,
  storageKeys: Set<string>
): number {
  let deleted = 0
  for (const shardIndex of codexContextStateShardIndexes()) {
    const database = getCodexContextStateShardDatabase(shardIndex)
    for (const chunk of chunkValues(sessionIds, 900)) {
      runInDatabaseTransaction(() => {
        const placeholders = sqlPlaceholders(chunk.length)
        const rows = database
          .prepare(`SELECT storage_key FROM ${table} WHERE session_id IN (${placeholders}) AND expires_at < ?`)
          .all(...chunk, expiredBefore) as Array<{ storage_key?: string }>
        deleted += rows.length
        for (const row of rows) {
          const key = String(row.storage_key ?? '').trim()
          if (key) storageKeys.add(key)
        }
        database.prepare(`DELETE FROM ${table} WHERE session_id IN (${placeholders}) AND expires_at < ?`).run(...chunk, expiredBefore)
      }, database)
    }
  }
  return deleted
}

function selectRemainingSessionExpiresAtBySessionIds(sessionIds: string[]): Map<string, string> {
  const expiresAtBySessionId = new Map<string, string>()
  for (const table of ['codex_context_responses', 'codex_context_compacts'] as const) {
    for (const shardIndex of codexContextStateShardIndexes()) {
      const database = getCodexContextStateShardDatabase(shardIndex)
      for (const chunk of chunkValues(sessionIds, 900)) {
        const placeholders = sqlPlaceholders(chunk.length)
        const rows = database.prepare(`
          SELECT session_id, MAX(expires_at) AS expires_at
          FROM ${table}
          WHERE session_id IN (${placeholders})
          GROUP BY session_id
        `).all(...chunk) as Array<{ session_id?: string; expires_at?: string }>
        for (const row of rows) {
          const sessionId = String(row.session_id ?? '').trim()
          const expiresAt = String(row.expires_at ?? '').trim()
          if (!sessionId || !expiresAt) continue
          const existing = expiresAtBySessionId.get(sessionId)
          if (!existing || expiresAt > existing) {
            expiresAtBySessionId.set(sessionId, expiresAt)
          }
        }
      }
    }
  }
  return expiresAtBySessionId
}

function deleteOrRefreshSessionRows(
  rows: Array<CodexContextSessionRow & { shardIndex: number }>,
  remainingExpiresAtBySessionId: Map<string, string>,
  now: string
): number {
  let deleted = 0
  const deleteGrouped = new Map<number, string[]>()
  const refreshGrouped = new Map<number, Array<{ id: string; expiresAt: string }>>()
  for (const row of rows) {
    const remainingExpiresAt = remainingExpiresAtBySessionId.get(row.id)
    if (remainingExpiresAt) {
      const existing = refreshGrouped.get(row.shardIndex)
      if (existing) {
        existing.push({ id: row.id, expiresAt: remainingExpiresAt })
      } else {
        refreshGrouped.set(row.shardIndex, [{ id: row.id, expiresAt: remainingExpiresAt }])
      }
      continue
    }
    const existing = deleteGrouped.get(row.shardIndex)
    if (existing) {
      existing.push(row.id)
    } else {
      deleteGrouped.set(row.shardIndex, [row.id])
    }
  }
  for (const [shardIndex, values] of refreshGrouped) {
    const database = getCodexContextStateShardDatabase(shardIndex)
    runInDatabaseTransaction(() => {
      const statement = database.prepare(`
        UPDATE codex_context_sessions
        SET updated_at = ?, expires_at = ?
        WHERE id = ?
      `)
      for (const value of values) {
        statement.run(now, value.expiresAt, value.id)
      }
    }, database)
  }
  for (const [shardIndex, ids] of deleteGrouped) {
    const database = getCodexContextStateShardDatabase(shardIndex)
    runInDatabaseTransaction(() => {
      for (const chunk of chunkValues(ids, 900)) {
        const placeholders = sqlPlaceholders(chunk.length)
        const result = database.prepare(`DELETE FROM codex_context_sessions WHERE id IN (${placeholders})`).run(...chunk)
        deleted += Number(result.changes ?? 0)
      }
    }, database)
  }
  return deleted
}

function filterUnreferencedStorageKeys(storageKeys: Set<string>): string[] {
  const deletable = new Set(storageKeys)
  if (deletable.size === 0) return []
  const keys = [...deletable]
  for (const table of ['codex_context_responses', 'codex_context_compacts'] as const) {
    for (const shardIndex of codexContextStateShardIndexes()) {
      const database = getCodexContextStateShardDatabase(shardIndex)
      for (const chunk of chunkValues(keys, 900)) {
        const remaining = chunk.filter((key) => deletable.has(key))
        if (remaining.length === 0) continue
        const placeholders = sqlPlaceholders(remaining.length)
        const rows = database.prepare(`
          SELECT DISTINCT storage_key
          FROM ${table}
          WHERE storage_key IN (${placeholders})
        `).all(...remaining) as Array<{ storage_key?: string }>
        for (const row of rows) {
          const key = String(row.storage_key ?? '').trim()
          if (key) deletable.delete(key)
        }
      }
    }
  }
  return [...deletable]
}

async function cleanupExpiredCodexContextStateSessionRowsAsync(
  client: DatabaseClient,
  rows: CodexContextSessionRow[],
  expiredBefore: string,
  hasMore: boolean
): Promise<CodexContextExpiredStateCleanupResult> {
  const candidateStorageKeys = new Set<string>()
  const sessionIds = rows.map((row) => row.id)
  let deletedResponses = 0
  let deletedCompacts = 0
  let deletedSessions = 0
  await client.transaction(async (tx) => {
    deletedResponses = await deleteExpiredCodexContextRowsBySessionIdsAsync(tx, 'codex_context_responses', sessionIds, expiredBefore, candidateStorageKeys)
    deletedCompacts = await deleteExpiredCodexContextRowsBySessionIdsAsync(tx, 'codex_context_compacts', sessionIds, expiredBefore, candidateStorageKeys)
    const remainingExpiresAtBySessionId = await selectRemainingCodexContextSessionExpiresAtBySessionIdsAsync(tx, sessionIds)
    for (const row of rows) {
      const remainingExpiresAt = remainingExpiresAtBySessionId.get(row.id)
      if (remainingExpiresAt) {
        await tx.execute(`
          UPDATE ${codexContextTable(tx, 'codex_context_sessions')}
          SET updated_at = ?, expires_at = ?
          WHERE id = ?
        `, [nowIso(), remainingExpiresAt, row.id])
      } else {
        const result = await tx.execute(`
          DELETE FROM ${codexContextTable(tx, 'codex_context_sessions')}
          WHERE id = ?
        `, [row.id])
        deletedSessions += result.changes
      }
    }
  })
  const storageKeys = await filterUnreferencedStorageKeysAsync(client, candidateStorageKeys)
  return {
    deletedSessions,
    deletedResponses,
    deletedCompacts,
    storageKeys,
    hasMore
  }
}

async function deleteExpiredCodexContextRowsBySessionIdsAsync(
  client: DatabaseClient,
  table: 'codex_context_responses' | 'codex_context_compacts',
  sessionIds: string[],
  expiredBefore: string,
  storageKeys: Set<string>
): Promise<number> {
  let deleted = 0
  for (const chunk of chunkValues(sessionIds, 900)) {
    const placeholders = chunk.map(() => '?').join(', ')
    const rows = await client.query<{ storage_key?: string | null }>(`
      SELECT storage_key
      FROM ${codexContextTable(client, table)}
      WHERE session_id IN (${placeholders})
        AND expires_at < ?
    `, [...chunk, expiredBefore])
    deleted += rows.length
    for (const row of rows) {
      const key = String(row.storage_key ?? '').trim()
      if (key) storageKeys.add(key)
    }
    await client.execute(`
      DELETE FROM ${codexContextTable(client, table)}
      WHERE session_id IN (${placeholders})
        AND expires_at < ?
    `, [...chunk, expiredBefore])
  }
  return deleted
}

async function selectRemainingCodexContextSessionExpiresAtBySessionIdsAsync(client: DatabaseClient, sessionIds: string[]): Promise<Map<string, string>> {
  const expiresAtBySessionId = new Map<string, string>()
  for (const table of ['codex_context_responses', 'codex_context_compacts'] as const) {
    for (const chunk of chunkValues(sessionIds, 900)) {
      const rows = await client.query<{ session_id?: string | null; expires_at?: string | null }>(`
        SELECT session_id, MAX(expires_at) AS expires_at
        FROM ${codexContextTable(client, table)}
        WHERE session_id IN (${chunk.map(() => '?').join(', ')})
        GROUP BY session_id
      `, chunk)
      for (const row of rows) {
        const sessionId = String(row.session_id ?? '').trim()
        const expiresAt = String(row.expires_at ?? '').trim()
        if (!sessionId || !expiresAt) continue
        const existing = expiresAtBySessionId.get(sessionId)
        if (!existing || expiresAt > existing) {
          expiresAtBySessionId.set(sessionId, expiresAt)
        }
      }
    }
  }
  return expiresAtBySessionId
}

async function filterUnreferencedStorageKeysAsync(client: DatabaseClient, storageKeys: Set<string>): Promise<string[]> {
  const deletable = new Set(storageKeys)
  if (deletable.size === 0) return []
  const keys = [...deletable]
  for (const table of ['codex_context_responses', 'codex_context_compacts'] as const) {
    for (const chunk of chunkValues(keys, 900)) {
      const remaining = chunk.filter((key) => deletable.has(key))
      if (!remaining.length) continue
      const rows = await client.query<{ storage_key?: string | null }>(`
        SELECT DISTINCT storage_key
        FROM ${codexContextTable(client, table)}
        WHERE storage_key IN (${remaining.map(() => '?').join(', ')})
      `, remaining)
      for (const row of rows) {
        const key = String(row.storage_key ?? '').trim()
        if (key) deletable.delete(key)
      }
    }
  }
  return [...deletable]
}

function codexContextTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_codex_context', tableName)
    : client.dialect.quoteIdentifier(tableName)
}

export function normalizeCodexContextResponseStateIndexInput(input: CodexContextResponseStateIndexInput, now: string): CodexContextResponseStateIndex {
  return {
    responseId: normalizedRequiredText(input.responseId, 'responseId'),
    sessionId: normalizedRequiredText(input.sessionId, 'sessionId'),
    previousResponseId: normalizedOptionalText(input.previousResponseId),
    systemAccountId: normalizedRequiredText(input.systemAccountId, 'systemAccountId'),
    apiKeyId: normalizedOptionalText(input.apiKeyId),
    groupId: normalizedRequiredText(input.groupId, 'groupId'),
    providerCode: normalizedRequiredText(input.providerCode, 'providerCode'),
    upstreamAccountId: normalizedOptionalText(input.upstreamAccountId),
    model: normalizedOptionalText(input.model),
    upstreamModel: normalizedOptionalText(input.upstreamModel),
    ...normalizePayloadReference(input),
    createdAt: now,
    updatedAt: now,
    lastUsedAt: now,
    expiresAt: normalizedRequiredText(input.expiresAt, 'expiresAt')
  }
}

export function normalizeCodexContextCompactStateIndexInput(input: CodexContextCompactStateIndexInput, now: string): CodexContextCompactStateIndex {
  return {
    compactId: normalizedRequiredText(input.compactId, 'compactId'),
    sessionId: normalizedRequiredText(input.sessionId, 'sessionId'),
    sourceResponseId: normalizedOptionalText(input.sourceResponseId),
    summaryDigest: normalizedRequiredText(input.summaryDigest, 'summaryDigest'),
    systemAccountId: normalizedRequiredText(input.systemAccountId, 'systemAccountId'),
    apiKeyId: normalizedOptionalText(input.apiKeyId),
    groupId: normalizedRequiredText(input.groupId, 'groupId'),
    providerCode: normalizedRequiredText(input.providerCode, 'providerCode'),
    upstreamAccountId: normalizedOptionalText(input.upstreamAccountId),
    model: normalizedOptionalText(input.model),
    upstreamModel: normalizedOptionalText(input.upstreamModel),
    ...normalizePayloadReference(input),
    createdAt: now,
    updatedAt: now,
    lastUsedAt: now,
    expiresAt: normalizedRequiredText(input.expiresAt, 'expiresAt')
  }
}

function normalizePayloadReference(input: CodexContextPayloadReference): CodexContextPayloadReference {
  return {
    storageKey: normalizedRequiredText(input.storageKey, 'storageKey'),
    storageOffsetBytes: positiveInteger(input.storageOffsetBytes, 'storageOffsetBytes'),
    sha256: normalizedRequiredText(input.sha256, 'sha256'),
    rawSizeBytes: positiveInteger(input.rawSizeBytes, 'rawSizeBytes'),
    compressedSizeBytes: positiveInteger(input.compressedSizeBytes, 'compressedSizeBytes'),
    compression: 'gzip',
    schemaVersion: positiveInteger(input.schemaVersion, 'schemaVersion')
  }
}

function mapResponseStateRow(row: CodexContextResponseStateRow): CodexContextResponseStateIndex {
  return {
    responseId: String(row.response_id),
    sessionId: String(row.session_id),
    previousResponseId: normalizedOptionalText(row.previous_response_id),
    systemAccountId: String(row.system_account_id),
    apiKeyId: normalizedOptionalText(row.api_key_id),
    groupId: String(row.group_id),
    providerCode: String(row.provider_code),
    upstreamAccountId: normalizedOptionalText(row.upstream_account_id),
    model: normalizedOptionalText(row.model),
    upstreamModel: normalizedOptionalText(row.upstream_model),
    storageKey: String(row.storage_key),
    storageOffsetBytes: Number(row.storage_offset_bytes),
    sha256: String(row.sha256),
    rawSizeBytes: Number(row.raw_size_bytes),
    compressedSizeBytes: Number(row.compressed_size_bytes),
    compression: row.compression === 'gzip' ? 'gzip' : 'gzip',
    schemaVersion: Number(row.schema_version),
    createdAt: String(row.created_at),
    updatedAt: String(row.updated_at),
    lastUsedAt: String(row.last_used_at),
    expiresAt: String(row.expires_at)
  }
}

function mapCompactStateRow(row: CodexContextCompactStateRow): CodexContextCompactStateIndex {
  return {
    compactId: String(row.compact_id),
    sessionId: String(row.session_id),
    sourceResponseId: normalizedOptionalText(row.source_response_id),
    summaryDigest: String(row.summary_digest),
    systemAccountId: String(row.system_account_id),
    apiKeyId: normalizedOptionalText(row.api_key_id),
    groupId: String(row.group_id),
    providerCode: String(row.provider_code),
    upstreamAccountId: normalizedOptionalText(row.upstream_account_id),
    model: normalizedOptionalText(row.model),
    upstreamModel: normalizedOptionalText(row.upstream_model),
    storageKey: String(row.storage_key),
    storageOffsetBytes: Number(row.storage_offset_bytes),
    sha256: String(row.sha256),
    rawSizeBytes: Number(row.raw_size_bytes),
    compressedSizeBytes: Number(row.compressed_size_bytes),
    compression: row.compression === 'gzip' ? 'gzip' : 'gzip',
    schemaVersion: Number(row.schema_version),
    createdAt: String(row.created_at),
    updatedAt: String(row.updated_at),
    lastUsedAt: String(row.last_used_at),
    expiresAt: String(row.expires_at)
  }
}

function matchesBoundary(row: CodexContextStateBoundary, boundary: CodexContextStateBoundary): boolean {
  return row.systemAccountId === boundary.systemAccountId
    && (row.apiKeyId ?? '') === (boundary.apiKeyId ?? '')
    && row.groupId === boundary.groupId
    && row.providerCode === boundary.providerCode
}

function sessionDatabase(sessionId: string): DatabaseSync {
  return getCodexContextStateShardDatabase(codexContextStateShardIndexForKey(sessionId))
}

function responseDatabase(responseId: string): DatabaseSync {
  return getCodexContextStateShardDatabase(codexContextStateShardIndexForKey(responseId))
}

function compactDatabase(compactId: string): DatabaseSync {
  return getCodexContextStateShardDatabase(codexContextStateShardIndexForKey(compactId))
}

function groupKeysByShard(keys: string[]): Map<number, string[]> {
  const grouped = new Map<number, string[]>()
  for (const key of keys) {
    const shardIndex = codexContextStateShardIndexForKey(key)
    const existing = grouped.get(shardIndex)
    if (existing) {
      existing.push(key)
    } else {
      grouped.set(shardIndex, [key])
    }
  }
  return grouped
}

function groupRowsByShard<T>(rows: T[], keyForRow: (row: T) => string): Map<number, T[]> {
  const grouped = new Map<number, T[]>()
  for (const row of rows) {
    const shardIndex = codexContextStateShardIndexForKey(keyForRow(row))
    const existing = grouped.get(shardIndex)
    if (existing) {
      existing.push(row)
    } else {
      grouped.set(shardIndex, [row])
    }
  }
  return grouped
}

function normalizedShardIndex(value: number): number {
  const count = codexContextStateShardIndexes().length
  if (count <= 1) return 0
  const normalized = Math.trunc(value) % count
  return normalized >= 0 ? normalized : normalized + count
}

function normalizedRequiredText(value: unknown, label: string): string {
  const text = normalizedOptionalText(value)
  if (!text) {
    throw new Error(`${label} 不能为空`)
  }
  return text
}

function normalizedOptionalText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim().length > 0 ? value.trim() : undefined
}

function positiveInteger(value: unknown, label: string): number {
  const number = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(number) || number < 0) {
    throw new Error(`${label} 必须是非负整数`)
  }
  return Math.trunc(number)
}

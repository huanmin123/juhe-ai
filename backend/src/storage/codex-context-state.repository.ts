import type { DatabaseSync } from 'node:sqlite'

import {
  codexContextStateShardIndexForKey,
  codexContextStateShardIndexes,
  getCodexContextStateShardDatabase,
  nowIso,
  runInDatabaseTransaction
} from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'

export interface CodexContextStateBoundary {
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  providerCode: string
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
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
  provider_protocol_profile_id: string
  protocol_code: string
  protocol_version: string
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
  provider_protocol_profile_id: string
  protocol_code: string
  protocol_version: string
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

export function saveCodexContextResponseStateIndex(input: CodexContextResponseStateIndexInput): CodexContextResponseStateIndex {
  const now = input.createdAt ?? nowIso()
  const row = normalizeResponseIndexInput(input, now)
  upsertCodexContextSession({
    sessionId: row.sessionId,
    boundary: row,
    sourceResponseId: row.previousResponseId ? undefined : row.responseId,
    latestResponseId: row.responseId,
    latestCompactId: undefined,
    now,
    expiresAt: row.expiresAt
  })

  const database = responseDatabase(row.responseId)
  runInDatabaseTransaction(() => {
    database.prepare(`
      INSERT INTO codex_context_responses (
        response_id, session_id, previous_response_id, system_account_id, api_key_id, group_id,
        provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
        upstream_account_id, model, upstream_model, storage_key, storage_offset_bytes, sha256,
        raw_size_bytes, compressed_size_bytes, compression, schema_version,
        created_at, updated_at, last_used_at, expires_at
      )
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
    `).run(
      row.responseId,
      row.sessionId,
      row.previousResponseId ?? null,
      row.systemAccountId,
      row.apiKeyId ?? null,
      row.groupId,
      row.providerCode,
      row.providerProtocolProfileId,
      row.protocolCode,
      row.protocolVersion,
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
      now,
      now,
      row.expiresAt
    )
  }, database)
  return row
}

export function saveCodexContextCompactStateIndex(input: CodexContextCompactStateIndexInput): CodexContextCompactStateIndex {
  const now = input.createdAt ?? nowIso()
  const row = normalizeCompactIndexInput(input, now)
  upsertCodexContextSession({
    sessionId: row.sessionId,
    boundary: row,
    sourceResponseId: undefined,
    latestResponseId: undefined,
    latestCompactId: row.compactId,
    now,
    expiresAt: row.expiresAt
  })

  const database = compactDatabase(row.compactId)
  runInDatabaseTransaction(() => {
    database.prepare(`
      INSERT INTO codex_context_compacts (
        compact_id, session_id, source_response_id, summary_digest, system_account_id, api_key_id, group_id,
        provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
        upstream_account_id, model, upstream_model, storage_key, storage_offset_bytes, sha256,
        raw_size_bytes, compressed_size_bytes, compression, schema_version,
        created_at, updated_at, last_used_at, expires_at
      )
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
    `).run(
      row.compactId,
      row.sessionId,
      row.sourceResponseId ?? null,
      row.summaryDigest,
      row.systemAccountId,
      row.apiKeyId ?? null,
      row.groupId,
      row.providerCode,
      row.providerProtocolProfileId,
      row.protocolCode,
      row.protocolVersion,
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
      now,
      now,
      row.expiresAt
    )
  }, database)
  return row
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
    const row = responseDatabase(cursor).prepare('SELECT * FROM codex_context_responses WHERE response_id = ?').get(cursor) as CodexContextResponseStateRow | undefined
    if (!row) {
      return {
        outcome: rows.length === 0 ? 'not_found' : 'chain_broken',
        responseId: cursor
      }
    }
    const mapped = mapResponseStateRow(row)
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
  const row = compactDatabase(compactId).prepare('SELECT * FROM codex_context_compacts WHERE compact_id = ?').get(compactId) as CodexContextCompactStateRow | undefined
  if (!row) {
    return { outcome: 'not_found', compactId }
  }
  const mapped = mapCompactStateRow(row)
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
  if (expiredSessions.rows.length === 0) {
    return {
      deletedSessions: 0,
      deletedResponses: 0,
      deletedCompacts: 0,
      storageKeys: [],
      hasMore: false
    }
  }

  const storageKeys = new Set<string>()
  const sessionIds = expiredSessions.rows.map((row) => row.id)
  const deletedResponses = deleteRowsBySessionIds('codex_context_responses', sessionIds, storageKeys)
  const deletedCompacts = deleteRowsBySessionIds('codex_context_compacts', sessionIds, storageKeys)
  const deletedSessions = deleteSessionRows(expiredSessions.rows)
  return {
    deletedSessions,
    deletedResponses,
    deletedCompacts,
    storageKeys: [...storageKeys],
    hasMore: expiredSessions.hasMore
  }
}

function upsertCodexContextSession(input: {
  sessionId: string
  boundary: CodexContextStateBoundary
  sourceResponseId?: string
  latestResponseId?: string
  latestCompactId?: string
  now: string
  expiresAt: string
}): void {
  const database = sessionDatabase(input.sessionId)
  runInDatabaseTransaction(() => {
    database.prepare(`
      INSERT INTO codex_context_sessions (
        id, system_account_id, api_key_id, group_id, provider_code, provider_protocol_profile_id,
        protocol_code, protocol_version, source_response_id, latest_response_id, latest_compact_id,
        created_at, updated_at, last_used_at, expires_at
      )
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(id) DO UPDATE SET
        source_response_id = COALESCE(codex_context_sessions.source_response_id, excluded.source_response_id),
        latest_response_id = COALESCE(excluded.latest_response_id, codex_context_sessions.latest_response_id),
        latest_compact_id = COALESCE(excluded.latest_compact_id, codex_context_sessions.latest_compact_id),
        updated_at = excluded.updated_at,
        last_used_at = excluded.last_used_at,
        expires_at = excluded.expires_at
    `).run(
      input.sessionId,
      input.boundary.systemAccountId,
      input.boundary.apiKeyId ?? null,
      input.boundary.groupId,
      input.boundary.providerCode,
      input.boundary.providerProtocolProfileId,
      input.boundary.protocolCode,
      input.boundary.protocolVersion,
      input.sourceResponseId ?? null,
      input.latestResponseId ?? null,
      input.latestCompactId ?? null,
      input.now,
      input.now,
      input.now,
      input.expiresAt
    )
  }, database)
}

function touchCodexContextResponseChain(rows: CodexContextResponseStateIndex[], now: string, refreshExpiresAt: string): void {
  if (rows.length === 0) return
  const sessionId = rows[0]?.sessionId
  if (sessionId) {
    const database = sessionDatabase(sessionId)
    runInDatabaseTransaction(() => {
      database.prepare(`
        UPDATE codex_context_sessions
        SET last_used_at = ?, updated_at = ?, expires_at = ?
        WHERE id = ?
      `).run(now, now, refreshExpiresAt, sessionId)
    }, database)
  }
  updateRowsByResponseIds(rows.map((row) => row.responseId), now, refreshExpiresAt)
}

function touchCodexContextCompact(row: CodexContextCompactStateIndex, now: string, refreshExpiresAt: string): void {
  const compactDb = compactDatabase(row.compactId)
  runInDatabaseTransaction(() => {
    compactDb.prepare(`
      UPDATE codex_context_compacts
      SET last_used_at = ?, updated_at = ?, expires_at = ?
      WHERE compact_id = ?
    `).run(now, now, refreshExpiresAt, row.compactId)
  }, compactDb)

  const sessionDb = sessionDatabase(row.sessionId)
  runInDatabaseTransaction(() => {
    sessionDb.prepare(`
      UPDATE codex_context_sessions
      SET last_used_at = ?, updated_at = ?, expires_at = ?
      WHERE id = ?
    `).run(now, now, refreshExpiresAt, row.sessionId)
  }, sessionDb)
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

function selectExpiredSessions(expiredBefore: string, limit: number): {
  rows: Array<CodexContextSessionRow & { shardIndex: number }>
  hasMore: boolean
} {
  const rows: Array<CodexContextSessionRow & { shardIndex: number }> = []
  let hasMore = false
  for (const shardIndex of codexContextStateShardIndexes()) {
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

function deleteRowsBySessionIds(
  table: 'codex_context_responses' | 'codex_context_compacts',
  sessionIds: string[],
  storageKeys: Set<string>
): number {
  let deleted = 0
  for (const shardIndex of codexContextStateShardIndexes()) {
    const database = getCodexContextStateShardDatabase(shardIndex)
    runInDatabaseTransaction(() => {
      for (const chunk of chunkValues(sessionIds, 900)) {
        const placeholders = sqlPlaceholders(chunk.length)
        const rows = database
          .prepare(`SELECT storage_key FROM ${table} WHERE session_id IN (${placeholders})`)
          .all(...chunk) as Array<{ storage_key?: string }>
        deleted += rows.length
        for (const row of rows) {
          const key = String(row.storage_key ?? '').trim()
          if (key) storageKeys.add(key)
        }
        database.prepare(`DELETE FROM ${table} WHERE session_id IN (${placeholders})`).run(...chunk)
      }
    }, database)
  }
  return deleted
}

function deleteSessionRows(rows: Array<CodexContextSessionRow & { shardIndex: number }>): number {
  let deleted = 0
  const grouped = new Map<number, string[]>()
  for (const row of rows) {
    const existing = grouped.get(row.shardIndex)
    if (existing) {
      existing.push(row.id)
    } else {
      grouped.set(row.shardIndex, [row.id])
    }
  }
  for (const [shardIndex, ids] of grouped) {
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

function normalizeResponseIndexInput(input: CodexContextResponseStateIndexInput, now: string): CodexContextResponseStateIndex {
  return {
    responseId: normalizedRequiredText(input.responseId, 'responseId'),
    sessionId: normalizedRequiredText(input.sessionId, 'sessionId'),
    previousResponseId: normalizedOptionalText(input.previousResponseId),
    systemAccountId: normalizedRequiredText(input.systemAccountId, 'systemAccountId'),
    apiKeyId: normalizedOptionalText(input.apiKeyId),
    groupId: normalizedRequiredText(input.groupId, 'groupId'),
    providerCode: normalizedRequiredText(input.providerCode, 'providerCode'),
    providerProtocolProfileId: normalizedRequiredText(input.providerProtocolProfileId, 'providerProtocolProfileId'),
    protocolCode: normalizedRequiredText(input.protocolCode, 'protocolCode'),
    protocolVersion: normalizedRequiredText(input.protocolVersion, 'protocolVersion'),
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

function normalizeCompactIndexInput(input: CodexContextCompactStateIndexInput, now: string): CodexContextCompactStateIndex {
  return {
    compactId: normalizedRequiredText(input.compactId, 'compactId'),
    sessionId: normalizedRequiredText(input.sessionId, 'sessionId'),
    sourceResponseId: normalizedOptionalText(input.sourceResponseId),
    summaryDigest: normalizedRequiredText(input.summaryDigest, 'summaryDigest'),
    systemAccountId: normalizedRequiredText(input.systemAccountId, 'systemAccountId'),
    apiKeyId: normalizedOptionalText(input.apiKeyId),
    groupId: normalizedRequiredText(input.groupId, 'groupId'),
    providerCode: normalizedRequiredText(input.providerCode, 'providerCode'),
    providerProtocolProfileId: normalizedRequiredText(input.providerProtocolProfileId, 'providerProtocolProfileId'),
    protocolCode: normalizedRequiredText(input.protocolCode, 'protocolCode'),
    protocolVersion: normalizedRequiredText(input.protocolVersion, 'protocolVersion'),
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
    providerProtocolProfileId: String(row.provider_protocol_profile_id),
    protocolCode: String(row.protocol_code),
    protocolVersion: String(row.protocol_version),
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
    providerProtocolProfileId: String(row.provider_protocol_profile_id),
    protocolCode: String(row.protocol_code),
    protocolVersion: String(row.protocol_version),
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
    && row.providerProtocolProfileId === boundary.providerProtocolProfileId
    && row.protocolCode === boundary.protocolCode
    && row.protocolVersion === boundary.protocolVersion
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

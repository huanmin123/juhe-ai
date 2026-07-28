import { createHash, randomUUID } from 'node:crypto'
import { createReadStream, existsSync, mkdirSync, renameSync, statSync, unlinkSync, writeFileSync } from 'node:fs'
import { access, mkdir as mkdirAsync, rename, stat, unlink as unlinkAsync, writeFile as writeFileAsync } from 'node:fs/promises'
import { dirname, isAbsolute, relative, resolve } from 'node:path'
import type { DatabaseSync } from 'node:sqlite'
import { promisify } from 'node:util'
import { createGunzip, gunzip, gunzipSync, gzip, gzipSync } from 'node:zlib'

import { runtimeConfig } from '../config/runtime.js'
import { getDatasetDatabase, newId } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { optionalString } from './value-utils.js'

export type StoredAuditPayloadCompression = 'none' | 'gzip'
export type AuditPayloadBlobStorageStatus = 'not_saved' | 'metadata_missing' | 'file_missing' | 'available'

export interface PreparedAuditPayloadBlob {
  sha256: string
  rawSizeBytes: number
  compressedSizeBytes: number
  contentType: string
  contentEncoding?: string
  compression: StoredAuditPayloadCompression
  bytes: Buffer
}

export interface AuditPayloadBlobWindow {
  bytes?: Buffer
  offset: number
  limit: number
  totalBytes: number
  nextOffset?: number
  truncated: boolean
  storageStatus: AuditPayloadBlobStorageStatus
}

export interface AuditHeadersBlobDetail {
  headers?: Record<string, string | string[]>
  storageStatus: AuditPayloadBlobStorageStatus
}

interface StoredPayloadBlobMeta {
  storageKey: string
  compression: StoredAuditPayloadCompression
  rawSizeBytes: number
  compressedSizeBytes: number
}

type AuditPayloadBlobRow = Record<string, unknown>
type AuditPayloadBlobStatement = ReturnType<DatabaseSync['prepare']>

interface DeletedPostgresAuditPayloadBlobRow extends AuditPayloadBlobRow {
  id?: unknown
  storage_key?: unknown
}

export interface AuditPayloadBlobStatements {
  selectExisting: AuditPayloadBlobStatement
  insertBlob: AuditPayloadBlobStatement
  incrementReference: AuditPayloadBlobStatement
}

export interface AuditPayloadBlobPersistencePlan {
  blobId: string
  storageKey: string
  existing: boolean
  shouldWriteFile: boolean
  compression?: StoredAuditPayloadCompression
  compressedSizeBytes?: number
}

export interface AuditPayloadBlobCleanupResult {
  deletedRows: number
  deletedFiles: number
}

const auditBlobCompressionThresholdBytes = 4 * 1024
const auditPayloadDefaultReadLimitBytes = 256 * 1024
const auditPayloadMaxReadLimitBytes = 1024 * 1024
const auditBlobCompressionMaxBytes = auditPayloadMaxReadLimitBytes
const auditBlobCleanupDeleteConcurrency = 64
const auditBlobUnreferencedGraceMs = 5 * 60 * 1000
const gzipAsync = promisify(gzip)
const gunzipAsync = promisify(gunzip)

export function prepareAuditPayloadBlob(
  input: Buffer | undefined,
  contentType?: string,
  contentEncoding?: string
): PreparedAuditPayloadBlob | undefined {
  if (!input) return undefined
  const rawSizeBytes = input.byteLength
  const sha256 = createHash('sha256').update(input).digest('hex')
  const normalizedContentType = normalizeBlobContentType(contentType)
  const compressed = compressPayloadBytes(input, normalizedContentType, contentEncoding)
  return {
    sha256,
    rawSizeBytes,
    compressedSizeBytes: compressed.bytes.byteLength,
    contentType: normalizedContentType,
    contentEncoding,
    compression: compressed.compression,
    bytes: compressed.bytes
  }
}

export async function prepareAuditPayloadBlobAsync(
  input: Buffer | undefined,
  contentType?: string,
  contentEncoding?: string
): Promise<PreparedAuditPayloadBlob | undefined> {
  if (!input) return undefined
  const rawSizeBytes = input.byteLength
  const sha256 = createHash('sha256').update(input).digest('hex')
  const normalizedContentType = normalizeBlobContentType(contentType)
  const compressed = await compressPayloadBytesAsync(input, normalizedContentType, contentEncoding)
  return {
    sha256,
    rawSizeBytes,
    compressedSizeBytes: compressed.bytes.byteLength,
    contentType: normalizedContentType,
    contentEncoding,
    compression: compressed.compression,
    bytes: compressed.bytes
  }
}

export function persistAuditPayloadBlob(
  database: DatabaseSync,
  blob: PreparedAuditPayloadBlob | undefined,
  timestamp: string,
  createdStorageKeys: string[],
  statements = prepareAuditPayloadBlobStatements(database)
): string | null {
  if (!blob) return null

  const existing = statements.selectExisting
    .get(blob.sha256, blob.rawSizeBytes, blob.contentType) as AuditPayloadBlobRow | undefined
  const existingId = optionalString(existing?.id)
  if (existingId) {
    writeAuditPayloadBlobFileForPlanSync(blob, auditPayloadBlobPlanFromRow(existingId, existing ?? {}))
    return existingId
  }

  const id = newId('audblob')
  const storageKey = storageKeyForBlob(id, blob.compression)
  writeBlobFileAtomically(storageKey, blob.bytes)
  createdStorageKeys.push(storageKey)
  statements.insertBlob.run(
    id,
    blob.sha256,
    blob.rawSizeBytes,
    blob.compressedSizeBytes,
    blob.contentType,
    blob.contentEncoding ?? null,
    blob.compression,
    storageKey,
    timestamp,
    timestamp,
    timestamp
  )
  return id
}

export async function writeAuditPayloadBlobFileForPlan(
  blob: PreparedAuditPayloadBlob | undefined,
  plan: AuditPayloadBlobPersistencePlan | undefined
): Promise<void> {
  if (!blob || !plan?.storageKey || !plan.shouldWriteFile) return
  const bytes = await payloadBlobBytesForPlan(blob, plan)
  const expectedSize = plan.compressedSizeBytes ?? bytes.byteLength
  if (plan.existing && await blobStorageFileMatchesExpectedSize(plan.storageKey, expectedSize)) return
  if (bytes.byteLength !== expectedSize) {
    throw new Error(`审计 payload 修复内容尺寸与元数据不一致：expected=${expectedSize}, actual=${bytes.byteLength}`)
  }
  await writeBlobFileAtomicallyAsync(plan.storageKey, bytes)
}

export function writeAuditPayloadBlobFileForPlanSync(
  blob: PreparedAuditPayloadBlob | undefined,
  plan: AuditPayloadBlobPersistencePlan | undefined
): void {
  if (!blob || !plan?.storageKey || !plan.shouldWriteFile) return
  const bytes = payloadBlobBytesForPlanSync(blob, plan)
  const expectedSize = plan.compressedSizeBytes ?? bytes.byteLength
  if (plan.existing && blobStorageFileMatchesExpectedSizeSync(plan.storageKey, expectedSize)) return
  if (bytes.byteLength !== expectedSize) {
    throw new Error(`审计 payload 修复内容尺寸与元数据不一致：expected=${expectedSize}, actual=${bytes.byteLength}`)
  }
  writeBlobFileAtomically(plan.storageKey, bytes)
}

export function prepareAuditPayloadBlobStatements(database: DatabaseSync): AuditPayloadBlobStatements {
  return {
    selectExisting: database.prepare('SELECT id, storage_key, compression, compressed_size_bytes FROM audit_payload_blobs WHERE sha256 = ? AND raw_size_bytes = ? AND content_type = ?'),
    insertBlob: database.prepare(`
      INSERT INTO audit_payload_blobs (
        id, sha256, raw_size_bytes, compressed_size_bytes, content_type, content_encoding, compression,
        storage_key, ref_count, first_seen_at, last_seen_at, created_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)
    `),
    incrementReference: database.prepare(`
      UPDATE audit_payload_blobs
      SET ref_count = ref_count + 1,
          first_seen_at = CASE WHEN ref_count = 0 OR first_seen_at > ? THEN ? ELSE first_seen_at END,
          last_seen_at = CASE WHEN ref_count = 0 OR last_seen_at < ? THEN ? ELSE last_seen_at END
      WHERE id = ?
    `)
  }
}

export function incrementAuditPayloadBlobReference(
  blobId: string | null,
  timestamp: string,
  statements: AuditPayloadBlobStatements
): void {
  if (!blobId) return
  statements.incrementReference.run(timestamp, timestamp, timestamp, timestamp, blobId)
}

export function cleanupUnreferencedAuditPayloadBlobs(limit = 1000): number {
  assertSqliteAuditPayloadBlobCleanup('cleanupUnreferencedAuditPayloadBlobs')
  const database = getDatasetDatabase()
  const rows = listUnreferencedAuditPayloadBlobRows(database, limit)
  if (rows.length === 0) return 0
  const deletedRows = deleteUnreferencedAuditPayloadBlobRowsSqlite(database, rows)
  for (const row of deletedRows) {
    deleteBlobFile(optionalString(row.storage_key))
  }
  return deletedRows.length
}

export function cleanupUnreferencedAuditPayloadBlobsByIds(blobIds: string[], limit = 1000): number {
  assertSqliteAuditPayloadBlobCleanup('cleanupUnreferencedAuditPayloadBlobsByIds')
  const database = getDatasetDatabase()
  const rows = listUnreferencedAuditPayloadBlobRowsByIds(database, blobIds, limit)
  if (rows.length === 0) return 0
  const deletedRows = deleteUnreferencedAuditPayloadBlobRowsSqlite(database, rows)
  for (const row of deletedRows) {
    deleteBlobFile(optionalString(row.storage_key))
  }
  return deletedRows.length
}

export async function cleanupUnreferencedAuditPayloadBlobsAsync(limit = 1000): Promise<number> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return cleanupUnreferencedAuditPayloadBlobsPostgresAsync(limit)
  }
  const database = getDatasetDatabase()
  const rows = listUnreferencedAuditPayloadBlobRows(database, limit)
  if (rows.length === 0) return 0
  const deletedRows = deleteUnreferencedAuditPayloadBlobRowsSqlite(database, rows)
  await deleteBlobFilesAsync(deletedRows.map((row) => optionalString(row.storage_key)))
  return deletedRows.length
}

export async function cleanupUnreferencedAuditPayloadBlobsByIdsAsync(blobIds: string[], limit = 1000): Promise<number> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return cleanupUnreferencedAuditPayloadBlobsByIdsPostgresAsync(blobIds, limit)
  }
  const database = getDatasetDatabase()
  const rows = listUnreferencedAuditPayloadBlobRowsByIds(database, blobIds, limit)
  if (rows.length === 0) return 0
  const deletedRows = deleteUnreferencedAuditPayloadBlobRowsSqlite(database, rows)
  await deleteBlobFilesAsync(deletedRows.map((row) => optionalString(row.storage_key)))
  return deletedRows.length
}

export async function cleanupAuditPayloadBlobsBeforeAsync(cutoffCreatedAt: string, limit = 1000): Promise<AuditPayloadBlobCleanupResult> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return cleanupAuditPayloadBlobsBeforePostgresAsync(cutoffCreatedAt, limit)
  }
  const database = getDatasetDatabase()
  const rows = listAuditPayloadBlobRowsBefore(database, cutoffCreatedAt, limit)
  if (rows.length === 0) {
    return {
      deletedRows: 0,
      deletedFiles: 0
    }
  }

  const deletedRows = deleteUnreferencedAuditPayloadBlobRowsSqlite(database, rows)
  const deletedFiles = await deleteBlobFilesAsync(deletedRows.map((row) => optionalString(row.storage_key)))
  return {
    deletedRows: deletedRows.length,
    deletedFiles
  }
}

export function cleanupCreatedAuditBlobFiles(storageKeys: string[]): void {
  for (const storageKey of storageKeys) {
    deleteBlobFile(storageKey)
  }
}

export async function cleanupCreatedAuditBlobFilesAsync(storageKeys: string[]): Promise<void> {
  await Promise.all(storageKeys.map((storageKey) => deleteBlobFileStrictAsync(storageKey)))
}

export async function readAuditPayloadBlobWindow(
  blobId: string | undefined,
  options: { offset?: number; limit?: number; full?: boolean }
): Promise<AuditPayloadBlobWindow> {
  assertAuditPayloadBlobSqliteOnly('readAuditPayloadBlobWindow')
  const full = options.full === true
  const requestedOffset = normalizePayloadReadOffset(options.offset)
  const requestedLimit = full ? 0 : normalizePayloadReadLimit(options.limit)
  if (!blobId) {
    return emptyPayloadBlobWindow(full ? 0 : requestedOffset, requestedLimit, 0, 'not_saved')
  }
  const meta = loadPayloadBlobMeta(blobId)
  if (!meta) {
    return emptyPayloadBlobWindow(full ? 0 : requestedOffset, requestedLimit, 0, 'metadata_missing')
  }
  const offset = full ? 0 : requestedOffset
  const limit = full ? meta.rawSizeBytes : requestedLimit
  const filePath = blobFilePath(meta.storageKey)
  if (!await fileExists(filePath)) {
    return emptyPayloadBlobWindow(offset, limit, meta.rawSizeBytes, 'file_missing')
  }
  const bytes = meta.compression === 'gzip'
    ? await readGzipPayloadWindow(filePath, offset, limit, meta.rawSizeBytes)
    : await readPlainPayloadWindow(filePath, offset, limit, meta.rawSizeBytes)
  const bytesReturned = bytes?.byteLength ?? 0
  const nextOffset = offset + bytesReturned
  const truncated = nextOffset < meta.rawSizeBytes
  return {
    bytes,
    offset,
    limit,
    totalBytes: meta.rawSizeBytes,
    nextOffset: !full && truncated && bytesReturned > 0 ? nextOffset : undefined,
    truncated,
    storageStatus: 'available'
  }
}

function assertAuditPayloadBlobSqliteOnly(operation: string): void {
  if (runtimeConfig.databaseDriver !== 'sqlite') {
    throw new Error(`${operation} 仅支持 SQLite 本地读取；PostgreSQL 模式必须使用对应的 WithClient async driver 路径`)
  }
}

async function cleanupUnreferencedAuditPayloadBlobsPostgresAsync(limit = 1000): Promise<number> {
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows = await deleteUnreferencedAuditPayloadBlobRowsPostgresAsync(client, {
    cutoffCreatedAt: new Date(Date.now() - auditBlobUnreferencedGraceMs).toISOString(),
    limit: Math.max(1, Math.trunc(limit))
  })
  if (rows.length === 0) return 0
  await deleteBlobFilesAsync(rows.map((row) => optionalString(row.storage_key)))
  return rows.length
}

async function cleanupUnreferencedAuditPayloadBlobsByIdsPostgresAsync(blobIds: string[], limit = 1000): Promise<number> {
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const ids = uniqueAuditPayloadBlobIds(blobIds).slice(0, Math.max(1, Math.trunc(limit)))
  if (ids.length === 0) return 0
  const rows = await deleteUnreferencedAuditPayloadBlobRowsPostgresAsync(client, {
    ids,
    limit: ids.length
  })
  if (rows.length === 0) return 0
  await deleteBlobFilesAsync(rows.map((row) => optionalString(row.storage_key)))
  return rows.length
}

async function cleanupAuditPayloadBlobsBeforePostgresAsync(cutoffCreatedAt: string, limit = 1000): Promise<AuditPayloadBlobCleanupResult> {
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows = await deleteUnreferencedAuditPayloadBlobRowsPostgresAsync(client, {
    cutoffCreatedAt,
    limit: Math.max(1, Math.trunc(limit))
  })
  if (rows.length === 0) {
    return {
      deletedRows: 0,
      deletedFiles: 0
    }
  }
  const deletedFiles = await deleteBlobFilesAsync(rows.map((row) => optionalString(row.storage_key)))
  return {
    deletedRows: rows.length,
    deletedFiles
  }
}

async function deleteUnreferencedAuditPayloadBlobRowsPostgresAsync(
  client: DatabaseClient,
  options: { ids?: string[]; cutoffCreatedAt?: string; limit: number }
): Promise<DeletedPostgresAuditPayloadBlobRow[]> {
  const clauses: string[] = []
  const params: unknown[] = []
  if (options.ids?.length) {
    clauses.push('b.id = ANY(?::text[])')
    params.push(options.ids)
  }
  if (options.cutoffCreatedAt) {
    clauses.push('b.created_at < ?')
    params.push(options.cutoffCreatedAt)
  }
  const scopeClause = clauses.length > 0 ? `${clauses.join(' AND ')} AND` : ''
  params.push(Math.max(1, Math.trunc(options.limit)))
  return await client.transaction(async (tx) => {
      const candidates = await tx.query<AuditPayloadBlobRow>(`
        SELECT b.id, b.storage_key
        FROM juhe_dataset.audit_payload_blobs b
        WHERE ${scopeClause}
          NOT EXISTS (
            SELECT 1
            FROM juhe_dataset.audit_payload_refs r
            WHERE r.headers_blob_id = b.id OR r.body_blob_id = b.id
          )
        ORDER BY b.created_at ASC, b.id ASC
        LIMIT ?
        FOR UPDATE OF b SKIP LOCKED
      `, params)
      const candidateIds = candidates.map((row) => optionalString(row.id)).filter((id): id is string => Boolean(id))
      if (candidateIds.length === 0) return []

      const deleted = await tx.query<DeletedPostgresAuditPayloadBlobRow>(`
        DELETE FROM juhe_dataset.audit_payload_blobs b
        WHERE b.id = ANY(?::text[])
          AND NOT EXISTS (
            SELECT 1
            FROM juhe_dataset.audit_payload_refs r
            WHERE r.headers_blob_id = b.id OR r.body_blob_id = b.id
          )
        RETURNING b.id, b.storage_key
      `, [candidateIds])
      return deleted
    })
}

export async function readAuditPayloadBlobWindowWithClient(
  client: DatabaseClient,
  blobId: string | undefined,
  options: { offset?: number; limit?: number; full?: boolean }
): Promise<AuditPayloadBlobWindow> {
  const full = options.full === true
  const requestedOffset = normalizePayloadReadOffset(options.offset)
  const requestedLimit = full ? 0 : normalizePayloadReadLimit(options.limit)
  if (!blobId) {
    return emptyPayloadBlobWindow(full ? 0 : requestedOffset, requestedLimit, 0, 'not_saved')
  }
  const meta = await loadPayloadBlobMetaWithClient(client, blobId)
  if (!meta) {
    return emptyPayloadBlobWindow(full ? 0 : requestedOffset, requestedLimit, 0, 'metadata_missing')
  }
  const offset = full ? 0 : requestedOffset
  const limit = full ? meta.rawSizeBytes : requestedLimit
  const filePath = blobFilePath(meta.storageKey)
  if (!await fileExists(filePath)) {
    return emptyPayloadBlobWindow(offset, limit, meta.rawSizeBytes, 'file_missing')
  }
  const bytes = meta.compression === 'gzip'
    ? await readGzipPayloadWindow(filePath, offset, limit, meta.rawSizeBytes)
    : await readPlainPayloadWindow(filePath, offset, limit, meta.rawSizeBytes)
  const bytesReturned = bytes?.byteLength ?? 0
  const nextOffset = offset + bytesReturned
  const truncated = nextOffset < meta.rawSizeBytes
  return {
    bytes,
    offset,
    limit,
    totalBytes: meta.rawSizeBytes,
    nextOffset: !full && truncated && bytesReturned > 0 ? nextOffset : undefined,
    truncated,
    storageStatus: 'available'
  }
}

export async function readAuditHeadersBlobDetail(blobId: string | undefined): Promise<AuditHeadersBlobDetail> {
  const window = await readAuditPayloadBlobWindow(blobId, {
    offset: 0,
    limit: auditPayloadMaxReadLimitBytes
  })
  if (!window.bytes) return { storageStatus: window.storageStatus }
  try {
    return {
      headers: JSON.parse(window.bytes.toString('utf8')) as Record<string, string | string[]>,
      storageStatus: window.storageStatus
    }
  } catch {
    return { storageStatus: window.storageStatus }
  }
}

export async function readAuditHeadersBlobDetailWithClient(client: DatabaseClient, blobId: string | undefined): Promise<AuditHeadersBlobDetail> {
  const window = await readAuditPayloadBlobWindowWithClient(client, blobId, {
    offset: 0,
    limit: auditPayloadMaxReadLimitBytes
  })
  if (!window.bytes) return { storageStatus: window.storageStatus }
  try {
    return {
      headers: JSON.parse(window.bytes.toString('utf8')) as Record<string, string | string[]>,
      storageStatus: window.storageStatus
    }
  } catch {
    return { storageStatus: window.storageStatus }
  }
}

export async function readAuditHeadersBlob(blobId: string | undefined): Promise<Record<string, string | string[]> | undefined> {
  return (await readAuditHeadersBlobDetail(blobId)).headers
}

export function auditPayloadBodyDetail(buffer: Buffer | undefined): { bodyText?: string; bodyBase64?: string } {
  if (!buffer) return {}
  return isUtf8Text(buffer)
    ? { bodyText: buffer.toString('utf8') }
    : { bodyBase64: buffer.toString('base64') }
}

function compressPayloadBytes(
  input: Buffer,
  contentType: string,
  contentEncoding?: string
): { bytes: Buffer; compression: StoredAuditPayloadCompression } {
  if (input.byteLength < auditBlobCompressionThresholdBytes || !isCompressiblePayload(contentType, contentEncoding)) {
    return { bytes: input, compression: 'none' }
  }
  if (input.byteLength > auditBlobCompressionMaxBytes) {
    return { bytes: input, compression: 'none' }
  }
  try {
    const compressed = gzipSync(input)
    return compressed.byteLength < input.byteLength
      ? { bytes: compressed, compression: 'gzip' }
      : { bytes: input, compression: 'none' }
  } catch {
    return { bytes: input, compression: 'none' }
  }
}

async function compressPayloadBytesAsync(
  input: Buffer,
  contentType: string,
  contentEncoding?: string
): Promise<{ bytes: Buffer; compression: StoredAuditPayloadCompression }> {
  if (input.byteLength < auditBlobCompressionThresholdBytes || !isCompressiblePayload(contentType, contentEncoding)) {
    return { bytes: input, compression: 'none' }
  }
  if (input.byteLength > auditBlobCompressionMaxBytes) {
    return { bytes: input, compression: 'none' }
  }
  try {
    const compressed = await gzipAsync(input)
    return compressed.byteLength < input.byteLength
      ? { bytes: compressed, compression: 'gzip' }
      : { bytes: input, compression: 'none' }
  } catch {
    return { bytes: input, compression: 'none' }
  }
}

function isCompressiblePayload(contentType: string, contentEncoding?: string): boolean {
  const encoding = contentEncoding?.trim().toLowerCase()
  if (encoding && encoding !== 'identity') {
    return false
  }
  const type = contentType.toLowerCase()
  return type.includes('json')
    || type.includes('text')
    || type.includes('xml')
    || type.includes('event-stream')
    || type.includes('javascript')
    || type.includes('x-www-form-urlencoded')
}

function loadPayloadBlobMeta(blobId: string): StoredPayloadBlobMeta | undefined {
  const row = getDatasetDatabase()
    .prepare('SELECT storage_key, compression, raw_size_bytes, compressed_size_bytes FROM audit_payload_blobs WHERE id = ?')
    .get(blobId) as AuditPayloadBlobRow | undefined
  const storageKey = optionalString(row?.storage_key)
  if (!storageKey) return undefined
  return {
    storageKey,
    compression: optionalString(row?.compression) === 'gzip' ? 'gzip' : 'none',
    rawSizeBytes: Math.max(0, Number(row?.raw_size_bytes ?? 0)),
    compressedSizeBytes: Math.max(0, Number(row?.compressed_size_bytes ?? 0))
  }
}

async function loadPayloadBlobMetaWithClient(client: DatabaseClient, blobId: string): Promise<StoredPayloadBlobMeta | undefined> {
  const row = await client.one<AuditPayloadBlobRow>(`
    SELECT storage_key, compression, raw_size_bytes, compressed_size_bytes
    FROM juhe_dataset.audit_payload_blobs
    WHERE id = ?
  `, [blobId])
  const storageKey = optionalString(row?.storage_key)
  if (!storageKey) return undefined
  return {
    storageKey,
    compression: optionalString(row?.compression) === 'gzip' ? 'gzip' : 'none',
    rawSizeBytes: Math.max(0, Number(row?.raw_size_bytes ?? 0)),
    compressedSizeBytes: Math.max(0, Number(row?.compressed_size_bytes ?? 0))
  }
}

function normalizeBlobContentType(value?: string): string {
  const text = value?.trim()
  return text || 'application/octet-stream'
}

function storageKeyForBlob(id: string, compression: StoredAuditPayloadCompression): string {
  const suffix = compression === 'gzip' ? 'gz' : 'blob'
  return `${id.slice(0, 2)}/${id}.${suffix}`
}

function blobFilePath(storageKey: string): string {
  // Keep audit payload files beside the configured dataset store. This keeps
  // isolated development instances self-contained instead of writing under
  // the repository's default backend/data directory.
  const auditBlobRoot = resolve(dirname(runtimeConfig.datasetDatabasePath), 'audit', 'blobs')
  const target = resolve(auditBlobRoot, storageKey)
  const relativePath = relative(auditBlobRoot, target)
  if (relativePath.startsWith('..') || isAbsolute(relativePath)) {
    throw new Error('审计 payload 存储路径非法')
  }
  return target
}

function writeBlobFileAtomically(storageKey: string, bytes: Buffer): void {
  const filePath = blobFilePath(storageKey)
  mkdirSync(dirname(filePath), { recursive: true })
  const temporaryPath = `${filePath}.${process.pid}.${randomUUID()}.tmp`
  try {
    writeFileSync(temporaryPath, bytes, { flag: 'wx' })
    renameSync(temporaryPath, filePath)
  } catch (error) {
    try {
      unlinkSync(temporaryPath)
    } catch {
    }
    throw error
  }
}

async function writeBlobFileAtomicallyAsync(storageKey: string, bytes: Buffer): Promise<void> {
  const filePath = blobFilePath(storageKey)
  await mkdirAsync(dirname(filePath), { recursive: true })
  const temporaryPath = `${filePath}.${process.pid}.${randomUUID()}.tmp`
  try {
    await writeFileAsync(temporaryPath, bytes, { flag: 'wx' })
    await rename(temporaryPath, filePath)
  } catch (error) {
    await unlinkAsync(temporaryPath).catch(() => undefined)
    throw error
  }
}

async function blobStorageFileMatchesExpectedSize(storageKey: string, expectedSize: number): Promise<boolean> {
  try {
    const metadata = await stat(blobFilePath(storageKey))
    return metadata.isFile() && metadata.size === Math.max(0, Math.trunc(expectedSize))
  } catch {
    return false
  }
}

function blobStorageFileMatchesExpectedSizeSync(storageKey: string, expectedSize: number): boolean {
  try {
    const metadata = statSync(blobFilePath(storageKey))
    return metadata.isFile() && metadata.size === Math.max(0, Math.trunc(expectedSize))
  } catch {
    return false
  }
}

function auditPayloadBlobPlanFromRow(existingId: string, row: AuditPayloadBlobRow): AuditPayloadBlobPersistencePlan {
  const storageKey = optionalString(row.storage_key) ?? ''
  return {
    blobId: existingId,
    storageKey,
    existing: true,
    shouldWriteFile: Boolean(storageKey),
    compression: storedAuditPayloadCompression(row.compression),
    compressedSizeBytes: nonNegativeInteger(row.compressed_size_bytes)
  }
}

async function payloadBlobBytesForPlan(
  blob: PreparedAuditPayloadBlob,
  plan: AuditPayloadBlobPersistencePlan
): Promise<Buffer> {
  const targetCompression = plan.compression ?? blob.compression
  if (targetCompression === blob.compression) return blob.bytes
  return targetCompression === 'gzip'
    ? await gzipAsync(blob.bytes)
    : await gunzipAsync(blob.bytes)
}

function payloadBlobBytesForPlanSync(
  blob: PreparedAuditPayloadBlob,
  plan: AuditPayloadBlobPersistencePlan
): Buffer {
  const targetCompression = plan.compression ?? blob.compression
  if (targetCompression === blob.compression) return blob.bytes
  return targetCompression === 'gzip'
    ? gzipSync(blob.bytes)
    : gunzipSync(blob.bytes)
}

function storedAuditPayloadCompression(value: unknown): StoredAuditPayloadCompression | undefined {
  return value === 'gzip' || value === 'none' ? value : undefined
}

function nonNegativeInteger(value: unknown): number | undefined {
  const number = Number(value)
  return Number.isFinite(number) && number >= 0 ? Math.trunc(number) : undefined
}

function listUnreferencedAuditPayloadBlobRows(database: DatabaseSync, limit: number): AuditPayloadBlobRow[] {
  return database
    .prepare(`
      SELECT b.id, b.storage_key
      FROM audit_payload_blobs b
      WHERE NOT EXISTS (
        SELECT 1
        FROM audit_payload_refs r
        WHERE r.headers_blob_id = b.id OR r.body_blob_id = b.id
      )
      ORDER BY b.created_at ASC, b.id ASC
      LIMIT ?
    `)
    .all(Math.max(1, Math.trunc(limit))) as AuditPayloadBlobRow[]
}

function deleteUnreferencedAuditPayloadBlobRowsSqlite(
  database: DatabaseSync,
  candidates: AuditPayloadBlobRow[]
): AuditPayloadBlobRow[] {
  const ids = candidates.map((row) => optionalString(row.id)).filter((id): id is string => Boolean(id))
  if (ids.length === 0) return []
  const placeholders = ids.map(() => '?').join(',')
  return database.prepare(`
    DELETE FROM audit_payload_blobs
    WHERE id IN (${placeholders})
      AND NOT EXISTS (
        SELECT 1
        FROM audit_payload_refs r
        WHERE r.headers_blob_id = audit_payload_blobs.id OR r.body_blob_id = audit_payload_blobs.id
      )
    RETURNING id, storage_key
  `).all(...ids) as AuditPayloadBlobRow[]
}

function listUnreferencedAuditPayloadBlobRowsByIds(database: DatabaseSync, blobIds: string[], limit: number): AuditPayloadBlobRow[] {
  const ids = uniqueAuditPayloadBlobIds(blobIds).slice(0, Math.max(1, Math.trunc(limit)))
  if (ids.length === 0) return []
  const placeholders = ids.map(() => '?').join(',')
  return database
    .prepare(`
      SELECT b.id, b.storage_key
      FROM audit_payload_blobs b
      WHERE b.id IN (${placeholders})
        AND NOT EXISTS (
          SELECT 1
          FROM audit_payload_refs r
          WHERE r.headers_blob_id = b.id
        )
        AND NOT EXISTS (
          SELECT 1
          FROM audit_payload_refs r
          WHERE r.body_blob_id = b.id
        )
      ORDER BY b.created_at ASC, b.id ASC
    `)
    .all(...ids) as AuditPayloadBlobRow[]
}

function listAuditPayloadBlobRowsBefore(database: DatabaseSync, cutoffCreatedAt: string, limit: number): AuditPayloadBlobRow[] {
  return database
    .prepare(`
      SELECT b.id, b.storage_key
      FROM audit_payload_blobs b
      WHERE b.created_at < ?
        AND NOT EXISTS (
          SELECT 1
          FROM audit_payload_refs r
          WHERE r.headers_blob_id = b.id OR r.body_blob_id = b.id
        )
      ORDER BY b.created_at ASC, b.id ASC
      LIMIT ?
    `)
    .all(cutoffCreatedAt, Math.max(1, Math.trunc(limit))) as AuditPayloadBlobRow[]
}

function deleteBlobFile(storageKey: string | undefined): void {
  if (!storageKey) return
  try {
    const filePath = blobFilePath(storageKey)
    if (existsSync(filePath)) {
      unlinkSync(filePath)
    }
  } catch {
  }
}

async function deleteBlobFilesAsync(storageKeys: Array<string | undefined>): Promise<number> {
  let deleted = 0
  for (let offset = 0; offset < storageKeys.length; offset += auditBlobCleanupDeleteConcurrency) {
    const chunk = storageKeys.slice(offset, offset + auditBlobCleanupDeleteConcurrency)
    const counts = await Promise.all(chunk.map((storageKey) => deleteBlobFileAsync(storageKey)))
    deleted += counts.reduce((sum, count) => sum + count, 0)
  }
  return deleted
}

async function deleteBlobFileAsync(storageKey: string | undefined): Promise<number> {
  if (!storageKey) return 0
  try {
    const filePath = blobFilePath(storageKey)
    await unlinkAsync(filePath)
    return 1
  } catch {
    return 0
  }
}

async function deleteBlobFileStrictAsync(storageKey: string | undefined): Promise<void> {
  if (!storageKey) return
  try {
    await unlinkAsync(blobFilePath(storageKey))
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error
  }
}

function assertSqliteAuditPayloadBlobCleanup(operation: string): void {
  if (runtimeConfig.databaseDriver === 'postgres') {
    throw new Error(`高性能模式禁止调用 SQLite 审计 payload 清理入口：${operation}`)
  }
}

async function fileExists(filePath: string): Promise<boolean> {
  try {
    await access(filePath)
    return true
  } catch {
    return false
  }
}

function normalizePayloadReadOffset(value: unknown): number {
  const number = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(number) || number <= 0) return 0
  return Math.trunc(number)
}

function normalizePayloadReadLimit(value: unknown): number {
  const number = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(number) || number <= 0) return auditPayloadDefaultReadLimitBytes
  return Math.min(auditPayloadMaxReadLimitBytes, Math.max(1, Math.trunc(number)))
}

function uniqueAuditPayloadBlobIds(blobIds: string[]): string[] {
  return [...new Set(blobIds.map((id) => id.trim()).filter(Boolean))]
}

function emptyPayloadBlobWindow(
  offset: number,
  limit: number,
  totalBytes: number,
  storageStatus: AuditPayloadBlobStorageStatus
): AuditPayloadBlobWindow {
  return {
    offset,
    limit,
    totalBytes,
    truncated: offset < totalBytes,
    storageStatus
  }
}

async function readPlainPayloadWindow(
  filePath: string,
  offset: number,
  limit: number,
  totalBytes: number
): Promise<Buffer | undefined> {
  if (offset >= totalBytes || limit <= 0) return undefined
  const end = Math.min(totalBytes - 1, offset + limit - 1)
  return readStreamWindow(createReadStream(filePath, { start: offset, end }), limit)
}

async function readGzipPayloadWindow(
  filePath: string,
  offset: number,
  limit: number,
  totalBytes: number
): Promise<Buffer | undefined> {
  if (offset >= totalBytes || limit <= 0) return undefined
  const source = createReadStream(filePath)
  return readStreamWindow(source.pipe(createGunzip()), limit, offset, [source])
}

function readStreamWindow(
  stream: NodeJS.ReadableStream,
  limit: number,
  skipBytes = 0,
  linkedStreams: NodeJS.ReadableStream[] = []
): Promise<Buffer | undefined> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = []
    let skipped = 0
    let collected = 0
    let settled = false

    const cleanup = (): void => {
      stream.off('data', onData)
      stream.off('end', onEnd)
      stream.off('error', onError)
      stream.off('close', onClose)
    }
    const finish = (): void => {
      if (settled) return
      settled = true
      cleanup()
      if ('destroy' in stream && typeof stream.destroy === 'function') {
        stream.destroy()
      }
      for (const linkedStream of linkedStreams) {
        if ('destroy' in linkedStream && typeof linkedStream.destroy === 'function') {
          linkedStream.destroy()
        }
      }
      resolve(chunks.length > 0 ? Buffer.concat(chunks, collected) : undefined)
    }
    const onData = (chunk: Buffer | string): void => {
      let buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)
      if (skipBytes > skipped) {
        const remainingSkip = skipBytes - skipped
        if (buffer.byteLength <= remainingSkip) {
          skipped += buffer.byteLength
          return
        }
        buffer = buffer.subarray(remainingSkip)
        skipped = skipBytes
      }
      if (buffer.byteLength === 0) return
      const remaining = limit - collected
      if (remaining <= 0) {
        finish()
        return
      }
      const slice = buffer.byteLength > remaining ? buffer.subarray(0, remaining) : buffer
      chunks.push(slice)
      collected += slice.byteLength
      if (collected >= limit) {
        finish()
      }
    }
    const onEnd = (): void => finish()
    const onClose = (): void => finish()
    const onError = (error: Error): void => {
      if (settled) return
      settled = true
      cleanup()
      reject(error)
    }

    stream.on('data', onData)
    stream.once('end', onEnd)
    stream.once('error', onError)
    stream.once('close', onClose)
  })
}

function isUtf8Text(buffer: Buffer): boolean {
  return buffer.toString('utf8').includes('\uFFFD') === false
}

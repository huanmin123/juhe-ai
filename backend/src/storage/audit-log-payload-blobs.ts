import { createHash } from 'node:crypto'
import { createReadStream, existsSync, mkdirSync, unlinkSync, writeFileSync } from 'node:fs'
import { access, mkdir as mkdirAsync, unlink as unlinkAsync, writeFile as writeFileAsync } from 'node:fs/promises'
import { dirname, isAbsolute, relative, resolve } from 'node:path'
import type { DatabaseSync } from 'node:sqlite'
import { promisify } from 'node:util'
import { createGunzip, gzip, gzipSync } from 'node:zlib'

import { backendRoot } from '../config/runtime.js'
import { getDatasetDatabase, newId } from './database.js'
import { optionalString } from './value-utils.js'

export type StoredAuditPayloadCompression = 'none' | 'gzip'

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
}

interface StoredPayloadBlobMeta {
  storageKey: string
  compression: StoredAuditPayloadCompression
  rawSizeBytes: number
  compressedSizeBytes: number
}

type AuditPayloadBlobRow = Record<string, unknown>
type AuditPayloadBlobStatement = ReturnType<DatabaseSync['prepare']>

export interface AuditPayloadBlobStatements {
  selectExisting: AuditPayloadBlobStatement
  updateExisting: AuditPayloadBlobStatement
  insertBlob: AuditPayloadBlobStatement
}

export interface AuditPayloadBlobPersistencePlan {
  blobId: string
  storageKey: string
  existing: boolean
  shouldWriteFile: boolean
}

const auditBlobRoot = resolve(backendRoot, 'data', 'audit', 'blobs')
const auditBlobCompressionThresholdBytes = 4 * 1024
const auditPayloadDefaultReadLimitBytes = 256 * 1024
const auditPayloadMaxReadLimitBytes = 1024 * 1024
const auditBlobCompressionMaxBytes = auditPayloadMaxReadLimitBytes
const gzipAsync = promisify(gzip)

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
    statements.updateExisting.run(timestamp, existingId)
    writeBlobFileIfMissing(optionalString(existing?.storage_key), blob.bytes)
    return existingId
  }

  const id = newId('audblob')
  const storageKey = storageKeyForBlob(id, blob.compression)
  writeBlobFile(storageKey, blob.bytes)
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

export async function persistAuditPayloadBlobAsync(
  database: DatabaseSync,
  blob: PreparedAuditPayloadBlob | undefined,
  timestamp: string,
  createdStorageKeys: string[],
  statements = prepareAuditPayloadBlobStatements(database)
): Promise<string | null> {
  if (!blob) return null

  const existing = statements.selectExisting
    .get(blob.sha256, blob.rawSizeBytes, blob.contentType) as AuditPayloadBlobRow | undefined
  const existingId = optionalString(existing?.id)
  if (existingId) {
    statements.updateExisting.run(timestamp, existingId)
    await writeBlobFileIfMissingAsync(optionalString(existing?.storage_key), blob.bytes)
    return existingId
  }

  const id = newId('audblob')
  const storageKey = storageKeyForBlob(id, blob.compression)
  await writeBlobFileAsync(storageKey, blob.bytes)
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

export function planAuditPayloadBlobPersistence(
  database: DatabaseSync,
  blob: PreparedAuditPayloadBlob | undefined,
  statements = prepareAuditPayloadBlobStatements(database)
): AuditPayloadBlobPersistencePlan | undefined {
  if (!blob) return undefined
  const existing = statements.selectExisting
    .get(blob.sha256, blob.rawSizeBytes, blob.contentType) as AuditPayloadBlobRow | undefined
  const existingId = optionalString(existing?.id)
  if (existingId) {
    return {
      blobId: existingId,
      storageKey: optionalString(existing?.storage_key) ?? '',
      existing: true,
      shouldWriteFile: false
    }
  }
  const blobId = newId('audblob')
  return {
    blobId,
    storageKey: storageKeyForBlob(blobId, blob.compression),
    existing: false,
    shouldWriteFile: true
  }
}

export async function writeAuditPayloadBlobFileForPlan(
  blob: PreparedAuditPayloadBlob | undefined,
  plan: AuditPayloadBlobPersistencePlan | undefined
): Promise<void> {
  if (!blob || !plan?.storageKey || !plan.shouldWriteFile) return
  await writeBlobFileAsync(plan.storageKey, blob.bytes)
}

export function applyAuditPayloadBlobPersistencePlan(
  blob: PreparedAuditPayloadBlob | undefined,
  plan: AuditPayloadBlobPersistencePlan | undefined,
  timestamp: string,
  createdStorageKeys: string[],
  statements: AuditPayloadBlobStatements
): string | null {
  if (!blob || !plan) return null
  if (plan.existing) {
    statements.updateExisting.run(timestamp, plan.blobId)
    return plan.blobId
  }
  statements.insertBlob.run(
    plan.blobId,
    blob.sha256,
    blob.rawSizeBytes,
    blob.compressedSizeBytes,
    blob.contentType,
    blob.contentEncoding ?? null,
    blob.compression,
    plan.storageKey,
    timestamp,
    timestamp,
    timestamp
  )
  createdStorageKeys.push(plan.storageKey)
  return plan.blobId
}

export function prepareAuditPayloadBlobStatements(database: DatabaseSync): AuditPayloadBlobStatements {
  return {
    selectExisting: database.prepare('SELECT id, storage_key FROM audit_payload_blobs WHERE sha256 = ? AND raw_size_bytes = ? AND content_type = ?'),
    updateExisting: database.prepare('UPDATE audit_payload_blobs SET ref_count = ref_count + 1, last_seen_at = ? WHERE id = ?'),
    insertBlob: database.prepare(`
      INSERT INTO audit_payload_blobs (
        id, sha256, raw_size_bytes, compressed_size_bytes, content_type, content_encoding, compression,
        storage_key, ref_count, first_seen_at, last_seen_at, created_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
    `)
  }
}

export function cleanupUnreferencedAuditPayloadBlobs(limit = 1000): number {
  const database = getDatasetDatabase()
  const rows = database
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
  if (rows.length === 0) return 0

  const ids = rows.map((row) => String(row.id)).filter(Boolean)
  for (const row of rows) {
    deleteBlobFile(optionalString(row.storage_key))
  }
  const placeholders = ids.map(() => '?').join(',')
  const result = database.prepare(`DELETE FROM audit_payload_blobs WHERE id IN (${placeholders})`).run(...ids)
  return Number(result.changes ?? 0)
}

export function cleanupCreatedAuditBlobFiles(storageKeys: string[]): void {
  for (const storageKey of storageKeys) {
    deleteBlobFile(storageKey)
  }
}

export async function cleanupCreatedAuditBlobFilesAsync(storageKeys: string[]): Promise<void> {
  await Promise.all(storageKeys.map((storageKey) => deleteBlobFileAsync(storageKey)))
}

export async function readAuditPayloadBlobWindow(
  blobId: string | undefined,
  options: { offset?: number; limit?: number }
): Promise<AuditPayloadBlobWindow> {
  const offset = normalizePayloadReadOffset(options.offset)
  const limit = normalizePayloadReadLimit(options.limit)
  if (!blobId) {
    return emptyPayloadBlobWindow(offset, limit)
  }
  const meta = loadPayloadBlobMeta(blobId)
  if (!meta) {
    return emptyPayloadBlobWindow(offset, limit)
  }
  const filePath = blobFilePath(meta.storageKey)
  if (!await fileExists(filePath)) {
    return emptyPayloadBlobWindow(offset, limit, meta.rawSizeBytes)
  }
  const bytes = meta.compression === 'gzip'
    ? await readGzipPayloadWindow(filePath, offset, limit, meta.rawSizeBytes)
    : await readPlainPayloadWindow(filePath, offset, limit, meta.rawSizeBytes)
  const nextOffset = offset + (bytes?.byteLength ?? 0)
  const truncated = nextOffset < meta.rawSizeBytes
  return {
    bytes,
    offset,
    limit,
    totalBytes: meta.rawSizeBytes,
    nextOffset: truncated ? nextOffset : undefined,
    truncated
  }
}

export async function readAuditHeadersBlob(blobId: string | undefined): Promise<Record<string, string | string[]> | undefined> {
  const bytes = (await readAuditPayloadBlobWindow(blobId, {
    offset: 0,
    limit: auditPayloadMaxReadLimitBytes
  })).bytes
  if (!bytes) return undefined
  try {
    return JSON.parse(bytes.toString('utf8')) as Record<string, string | string[]>
  } catch {
    return undefined
  }
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

function normalizeBlobContentType(value?: string): string {
  const text = value?.trim()
  return text || 'application/octet-stream'
}

function storageKeyForBlob(id: string, compression: StoredAuditPayloadCompression): string {
  const suffix = compression === 'gzip' ? 'gz' : 'blob'
  return `${id.slice(0, 2)}/${id}.${suffix}`
}

function blobFilePath(storageKey: string): string {
  const target = resolve(auditBlobRoot, storageKey)
  const relativePath = relative(auditBlobRoot, target)
  if (relativePath.startsWith('..') || isAbsolute(relativePath)) {
    throw new Error('审计 payload 存储路径非法')
  }
  return target
}

function writeBlobFile(storageKey: string, bytes: Buffer): void {
  const filePath = blobFilePath(storageKey)
  mkdirSync(dirname(filePath), { recursive: true })
  writeFileSync(filePath, bytes)
}

async function writeBlobFileAsync(storageKey: string, bytes: Buffer): Promise<void> {
  const filePath = blobFilePath(storageKey)
  await mkdirAsync(dirname(filePath), { recursive: true })
  await writeFileAsync(filePath, bytes)
}

function writeBlobFileIfMissing(storageKey: string | undefined, bytes: Buffer): void {
  if (!storageKey) return
  const filePath = blobFilePath(storageKey)
  if (existsSync(filePath)) return
  mkdirSync(dirname(filePath), { recursive: true })
  writeFileSync(filePath, bytes)
}

async function writeBlobFileIfMissingAsync(storageKey: string | undefined, bytes: Buffer): Promise<void> {
  if (!storageKey) return
  const filePath = blobFilePath(storageKey)
  if (await fileExists(filePath)) return
  await mkdirAsync(dirname(filePath), { recursive: true })
  await writeFileAsync(filePath, bytes)
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

async function deleteBlobFileAsync(storageKey: string | undefined): Promise<void> {
  if (!storageKey) return
  try {
    const filePath = blobFilePath(storageKey)
    await unlinkAsync(filePath)
  } catch {
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

function emptyPayloadBlobWindow(offset: number, limit: number, totalBytes = 0): AuditPayloadBlobWindow {
  return {
    offset,
    limit,
    totalBytes,
    truncated: offset < totalBytes
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

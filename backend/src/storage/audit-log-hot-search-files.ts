import { spawn } from 'node:child_process'
import { appendFileSync, createReadStream, createWriteStream, mkdirSync } from 'node:fs'
import { once } from 'node:events'
import { createInterface } from 'node:readline'
import { pipeline } from 'node:stream/promises'
import { access, appendFile, opendir, rename, stat, unlink } from 'node:fs/promises'
import { dirname, join, resolve } from 'node:path'

import { runtimeConfig } from '../config/runtime.js'
import { errorLogFields, logger } from '../shared/logger.js'
import type { AuditLogInput } from './audit-logs.repository.js'
import { nonPersistedAuditTrafficSources, normalizePersistedAuditTrafficSource } from './audit-log-traffic-source.js'

export interface AuditHotSearchOptions {
  keywords: string[]
  limit?: number
  startAt?: string
  endAt?: string
}

export interface AuditHotSearchFileResult {
  available: boolean
  elapsedMs: number
  keywords: string[]
  startAt: string
  endAt: string
  limit: number
  auditLogIds: string[]
  truncated: boolean
  scannedFileCount: number
  message?: string
}

export interface AuditHotSearchFileCleanupOptions {
  maxFiles?: number
  maxRunMs?: number
}

interface AuditHotSearchLine {
  auditLogId?: string
  createdAt?: string
  traceId?: string
  trafficSource?: string
  sequence?: number
  text?: string
}

interface HotSearchFile {
  path: string
  fileName: string
  bucketStartMs: number
  bucketEndMs: number
  mtimeMs: number
}

interface RgMatchEvent {
  type?: string
  data?: {
    lines?: { text?: string }
  }
}

type RgStopReason = 'match_parse_limit' | 'timeout'

interface RgSearchResult {
  exitState: 'matched' | 'no-match'
  stopReason?: RgStopReason
}

const hotSearchFilePrefix = 'audit-hot-'
const hotSearchFileSuffix = '.ndjson'
const hotSearchBucketMs = 60 * 60 * 1000
const defaultHotSearchLimit = 100
const maxHotSearchLimit = 100
const maxHotSearchKeywords = 10
const minHotSearchKeywordLength = 2
const maxHotSearchKeywordLength = 128
const maxHotSearchChunkChars = 12_000
const maxHotSearchLineChars = 20_000
const maxRgJsonLineLength = maxHotSearchLineChars + 8_000
const maxRgSearchMs = 15_000
const maxRgStderrLength = 2_000
const maxRgParsedMatchEvents = 2_000
const maxConcurrentHotSearches = 1
const maxSearchFileScanEntries = 2_000
const searchFileScanYieldEvery = 100
const cleanupFileScanYieldEvery = 100
const maxCleanupFileBytes = 32 * 1024 * 1024
const defaultCleanupMaxFiles = 1_000
const defaultCleanupMaxRunMs = 5_000
const defaultHotSearchWindowMs = 60 * 60 * 1000
let activeHotSearches = 0
const hotSearchFileLocks = new Map<string, Promise<void>>()
const activeHotSearchFileLocks = new Set<string>()

export function appendAuditHotSearchEntries(inputs: AuditLogInput[]): void {
  if (inputs.length === 0) return
  try {
    const batches = buildHotSearchFileBatches(inputs)
    if (batches.size === 0) return
    mkdirSync(auditHotSearchRoot(), { recursive: true })
    for (const [filePath, lines] of batches) {
      appendHotSearchFileSync(filePath, lines.join(''))
    }
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'audit_hot_search_append_failed',
      count: inputs.length
    }), '审计热搜索镜像写入失败')
  }
}

export async function appendAuditHotSearchEntriesAsync(inputs: AuditLogInput[]): Promise<void> {
  if (inputs.length === 0) return
  try {
    const batches = buildHotSearchFileBatches(inputs)
    if (batches.size === 0) return
    mkdirSync(auditHotSearchRoot(), { recursive: true })
    await Promise.all([...batches.entries()].map(([filePath, lines]) => withHotSearchFileLock(filePath, async () => {
      await appendFile(filePath, lines.join(''), 'utf8')
    })))
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'audit_hot_search_append_failed',
      count: inputs.length
    }), '审计热搜索镜像写入失败')
  }
}

export async function grepAuditHotSearchFiles(options: AuditHotSearchOptions): Promise<AuditHotSearchFileResult> {
  const startedAt = performance.now()
  const keywordInput = normalizeHotSearchKeywords(options.keywords)
  const keywords = keywordInput.keywords
  const limit = normalizeHotSearchLimit(options.limit)
  const timeRange = normalizeHotSearchTimeRange(options)
  const baseResult = (): Omit<AuditHotSearchFileResult, 'available' | 'auditLogIds' | 'truncated' | 'elapsedMs' | 'scannedFileCount'> => ({
    keywords,
    startAt: timeRange.startAt,
    endAt: timeRange.endAt,
    limit
  })

  if (!keywords.length) {
    return {
      ...baseResult(),
      available: true,
      elapsedMs: Math.round(performance.now() - startedAt),
      auditLogIds: [],
      truncated: false,
      scannedFileCount: 0,
      message: keywordInput.shortKeywordCount > 0
        ? `审计内容搜索关键字至少需要 ${minHotSearchKeywordLength} 个字符，请输入更具体的关键字。`
        : '请输入要搜索的审计内容关键字'
    }
  }

  if (!acquireHotSearchSlot()) {
    return unavailableHotSearchResult(baseResult(), startedAt, '已有审计内容搜索正在运行，请稍后重试。')
  }

  try {
    let files: HotSearchFile[]
    try {
      files = await listHotSearchFiles(timeRange.startMs, timeRange.endMs)
    } catch (error) {
      logger.warn(errorLogFields(error, { event: 'audit_hot_search_file_list_failed' }), '审计内容热搜文件列表读取失败')
      return unavailableHotSearchResult(
        baseResult(),
        startedAt,
        '审计内容搜索文件读取失败，审计内容搜索暂不可用。'
      )
    }
    if (!files.length) {
      return {
        ...baseResult(),
        available: true,
        elapsedMs: Math.round(performance.now() - startedAt),
        auditLogIds: [],
        truncated: false,
        scannedFileCount: 0,
        message: '最近 1 小时没有可搜索的审计内容'
      }
    }

    const rgExecutable = await resolveRgExecutable()
    if (!rgExecutable) {
      return unavailableHotSearchResult(
        baseResult(),
        startedAt,
        '当前运行环境未找到 rg，审计内容搜索不可用。请确认部署时已成功安装后端生产依赖 @vscode/ripgrep。'
      )
    }

    try {
      return await searchHotFilesWithRg({
        ...baseResult(),
        rgExecutable,
        files,
        startMs: timeRange.startMs,
        endMs: timeRange.endMs,
        startedAt
      })
    } catch (error) {
      return unavailableHotSearchResult(
        baseResult(),
        startedAt,
        error instanceof Error && error.message ? error.message : 'rg 执行失败，审计内容搜索暂不可用。'
      )
    }
  } finally {
    releaseHotSearchSlot()
  }
}

export async function cleanupAuditHotSearchFilesBefore(cutoffCreatedAt: string, options: AuditHotSearchFileCleanupOptions = {}): Promise<number> {
  const cutoffMs = Date.parse(cutoffCreatedAt)
  if (!Number.isFinite(cutoffMs)) return 0
  const maxFiles = positiveInteger(options.maxFiles, defaultCleanupMaxFiles)
  const maxRunMs = positiveInteger(options.maxRunMs, defaultCleanupMaxRunMs)
  const startedAt = Date.now()
  let deleted = 0
  let scanned = 0
  try {
    const directory = await opendir(auditHotSearchRoot())
    for await (const entry of directory) {
      if (scanned >= maxFiles || Date.now() - startedAt >= maxRunMs) {
        break
      }
      if (!entry.isFile()) continue
      scanned += 1
      if (scanned % cleanupFileScanYieldEvery === 0) {
        await yieldToEventLoop()
      }
      const bucketStartMs = hotSearchBucketStartMsFromFileName(entry.name)
      if (bucketStartMs === undefined) continue
      const filePath = join(auditHotSearchRoot(), entry.name)
      if (bucketStartMs + hotSearchBucketMs > cutoffMs) continue
      try {
        await withHotSearchFileLock(filePath, async () => {
          await unlink(filePath)
          try {
            await unlink(pendingHotSearchFilePath(filePath))
          } catch (error) {
            if (!(error && typeof error === 'object' && (error as { code?: unknown }).code === 'ENOENT')) throw error
          }
        })
        deleted += 1
      } catch (error) {
        logger.warn(errorLogFields(error, { event: 'audit_hot_search_file_delete_failed', fileName: entry.name }), '审计热搜索文件删除失败')
      }
    }
  } catch (error) {
    logger.warn(errorLogFields(error, { event: 'audit_hot_search_file_cleanup_failed' }), '审计热搜索文件清理目录读取失败')
    return deleted
  }
  return deleted
}

export async function cleanupNonPersistedAuditHotSearchEntries(options: AuditHotSearchFileCleanupOptions = {}): Promise<number> {
  const maxFiles = positiveInteger(options.maxFiles, defaultCleanupMaxFiles)
  const maxRunMs = positiveInteger(options.maxRunMs, defaultCleanupMaxRunMs)
  const startedAt = Date.now()
  const currentBucketStartMs = hotSearchBucketStartMs(Date.now())
  let cleaned = 0
  let scanned = 0
  try {
    const directory = await opendir(auditHotSearchRoot())
    for await (const entry of directory) {
      if (scanned >= maxFiles || Date.now() - startedAt >= maxRunMs) break
      if (!entry.isFile()) continue
      scanned += 1
      if (scanned % cleanupFileScanYieldEvery === 0) await yieldToEventLoop()
      const pendingMainPath = pendingHotSearchMainFilePath(entry.name)
      if (pendingMainPath) {
        try {
          await withHotSearchFileLock(pendingMainPath, () => recoverPendingHotSearchFile(pendingMainPath, join(auditHotSearchRoot(), entry.name)))
          cleaned += 1
        } catch (error) {
          logger.warn(errorLogFields(error, {
            event: 'audit_hot_search_pending_recovery_failed',
            fileName: entry.name
          }), '审计热搜索旁路文件恢复失败')
        }
        continue
      }
      const bucketStartMs = hotSearchBucketStartMsFromFileName(entry.name)
      if (bucketStartMs === undefined || bucketStartMs >= currentBucketStartMs) continue
      try {
        const filePath = join(auditHotSearchRoot(), entry.name)
        const before = await stat(filePath)
        if (before.size > maxCleanupFileBytes) {
          logger.warn({ event: 'audit_hot_search_non_persisted_cleanup_file_too_large', fileName: entry.name, size: before.size }, '审计热搜索文件超过单文件清理上限，跳过本轮')
          continue
        }
        const changed = await withHotSearchFileLock(filePath, () => removeNonPersistedHotSearchEntries(filePath))
        if (changed) cleaned += 1
      } catch (error) {
        logger.warn(errorLogFields(error, {
          event: 'audit_hot_search_non_persisted_cleanup_failed',
          fileName: entry.name
        }), '审计热搜索后台来源清理失败')
      }
    }
  } catch (error) {
    logger.warn(errorLogFields(error, { event: 'audit_hot_search_non_persisted_cleanup_directory_failed' }), '审计热搜索后台来源清理目录读取失败')
    return cleaned
  }
  return cleaned
}

async function removeNonPersistedHotSearchEntries(filePath: string): Promise<boolean> {
  const tempPath = `${filePath}.tmp-${process.pid}-${Date.now()}`
  const input = createReadStream(filePath, { encoding: 'utf8' })
  const reader = createInterface({ input, crlfDelay: Infinity })
  const output = createWriteStream(tempPath, { encoding: 'utf8' })
  let changed = false
  try {
    for await (const line of reader) {
      if (!line.trim()) continue
      const parsed = parseHotSearchLine(line.trim())
      const trafficSource = parsed.trafficSource ?? legacyHotSearchTrafficSource(parsed.text)
      if (trafficSource !== undefined) {
        const normalized = normalizePersistedAuditTrafficSource(trafficSource)
        if (!normalized) {
          changed = true
          continue
        }
      }
      if (!output.write(`${line}\n`, 'utf8')) await once(output, 'drain')
    }
    await new Promise<void>((resolveOutput, rejectOutput) => {
      output.once('error', rejectOutput)
      output.end(() => resolveOutput())
    })
    if (changed) await rename(tempPath, filePath)
    else await unlink(tempPath)
    await drainPendingHotSearchAppends(filePath)
    return changed
  } catch (error) {
    try {
      await unlink(tempPath)
    } catch {
    }
    throw error
  } finally {
    reader.close()
    input.destroy()
    output.destroy()
  }
}

function appendHotSearchFileSync(filePath: string, contents: string): void {
  appendFileSync(activeHotSearchFileLocks.has(filePath) ? pendingHotSearchFilePath(filePath) : filePath, contents, 'utf8')
}

async function drainPendingHotSearchAppends(filePath: string): Promise<void> {
  await recoverPendingHotSearchFile(filePath, pendingHotSearchFilePath(filePath))
}

async function recoverPendingHotSearchFile(filePath: string, pendingPath: string): Promise<void> {
  try {
    const replayPath = `${pendingPath}.replay-${process.pid}-${Date.now()}`
    await rename(pendingPath, replayPath)
    await pipeline(
      createReadStream(replayPath),
      createWriteStream(filePath, { flags: 'a' })
    )
    await unlink(replayPath)
  } catch (error) {
    if (isMissingHotSearchDirectoryError(error) || (error && typeof error === 'object' && (error as { code?: unknown }).code === 'ENOENT')) return
    throw error
  }
}

function pendingHotSearchFilePath(filePath: string): string {
  return `${filePath}.pending-${process.pid}`
}

function pendingHotSearchMainFilePath(fileName: string): string | undefined {
  const marker = `${hotSearchFileSuffix}.pending-`
  const markerIndex = fileName.indexOf(marker)
  if (markerIndex < 0) return undefined
  const mainFileName = `${fileName.slice(0, markerIndex)}${hotSearchFileSuffix}`
  if (hotSearchBucketStartMsFromFileName(mainFileName) === undefined) return undefined
  return join(auditHotSearchRoot(), mainFileName)
}

async function withHotSearchFileLock<T>(filePath: string, action: () => Promise<T>): Promise<T> {
  const previous = hotSearchFileLocks.get(filePath) ?? Promise.resolve()
  let release!: () => void
  const current = new Promise<void>((resolveRelease) => { release = resolveRelease })
  const tail = previous.then(() => current)
  hotSearchFileLocks.set(filePath, tail)
  await previous
  activeHotSearchFileLocks.add(filePath)
  try {
    return await action()
  } finally {
    activeHotSearchFileLocks.delete(filePath)
    release()
    if (hotSearchFileLocks.get(filePath) === tail) hotSearchFileLocks.delete(filePath)
  }
}

function legacyHotSearchTrafficSource(text: string | undefined): string | undefined {
  const firstLine = text?.split('\n', 1)[0]?.trim()
  const candidate = firstLine?.split(/\s+/, 3)[1]
  return candidate && nonPersistedAuditTrafficSources.includes(candidate as typeof nonPersistedAuditTrafficSources[number])
    ? candidate
    : undefined
}

function buildHotSearchFileBatches(inputs: AuditLogInput[]): Map<string, string[]> {
  const batches = new Map<string, string[]>()
  for (const input of inputs) {
    const trafficSource = normalizePersistedAuditTrafficSource(input.trafficSource, input)
    if (!trafficSource) continue
    const createdAt = input.createdAt ?? input.endedAt
    const filePath = hotSearchFilePath(createdAt)
    const lines = buildHotSearchLines(input, createdAt, trafficSource)
    if (!lines.length) continue
    const existing = batches.get(filePath)
    if (existing) {
      existing.push(...lines)
    } else {
      batches.set(filePath, lines)
    }
  }
  return batches
}

function buildHotSearchLines(input: AuditLogInput, createdAt: string, trafficSource: string): string[] {
  const id = input.id
  if (!id) return []
  const includePayloadBodyText = shouldIncludePayloadBodyTextInHotSearch(input)
  const parts = [
    [
      input.traceId,
      input.trafficSource,
      input.method,
      input.path,
      input.queryString,
      input.model,
      input.upstreamModel,
      input.pricingModel,
      input.modelMappingApplied ? 'model_mapping_applied' : undefined,
      input.modelMappingSource,
      input.clientIp,
      input.userAgent,
      input.auditOutcome,
      input.finalStatusCode,
      input.errorPhase,
      input.errorCode,
      input.errorMessage,
      input.systemAccountId,
      input.apiKeyId,
      input.groupId,
      input.accountId,
      input.providerCode
    ].filter((value) => value !== undefined && value !== null).join(' ')
  ]

  for (const attempt of input.attempts) {
    parts.push([
      attempt.attemptIndex,
      attempt.accountId,
      attempt.groupId,
      attempt.providerCode,
      attempt.upstreamMethod,
      attempt.upstreamUrl,
      attempt.upstreamStatusCode,
      attempt.errorPhase,
      attempt.errorCode,
      attempt.errorMessage
    ].filter((value) => value !== undefined && value !== null).join(' '))
  }

  for (const payload of input.payloads) {
    parts.push([
      payload.partType,
      payload.contentType,
      payload.contentEncoding,
      payload.bodySha256,
      payload.rawBodySizeBytes,
      payload.captureStatus,
      payload.headers ? JSON.stringify(payload.headers) : '',
      includePayloadBodyText ? payloadBodySearchText(payload.body) : ''
    ].filter(Boolean).join(' '))
  }

  const text = parts.filter(Boolean).join('\n')
  return chunkText(text, maxHotSearchChunkChars).map((chunk, sequence) => `${JSON.stringify({
    auditLogId: id,
    createdAt,
    traceId: input.traceId,
    trafficSource,
    sequence,
    text: chunk
  } satisfies AuditHotSearchLine)}\n`)
}

function shouldIncludePayloadBodyTextInHotSearch(input: AuditLogInput): boolean {
  if (!input.success || input.auditOutcome !== 'success') {
    return true
  }
  return input.sampleReason.startsWith('success_sample_')
}

function payloadBodySearchText(value: AuditLogInput['payloads'][number]['body']): string {
  if (Buffer.isBuffer(value)) {
    return value.toString('utf8')
  }
  return typeof value === 'string' ? value : ''
}

function chunkText(text: string, size: number): string[] {
  if (!text) return []
  const chunks: string[] = []
  for (let index = 0; index < text.length; index += size) {
    chunks.push(text.slice(index, index + size))
  }
  return chunks
}

function hotSearchFilePath(timestamp: string): string {
  const bucket = hotSearchBucketStartMs(Date.parse(timestamp))
  return join(auditHotSearchRoot(), `${hotSearchFilePrefix}${new Date(bucket).toISOString().slice(0, 13).replace(/[-:T]/g, '')}${hotSearchFileSuffix}`)
}

function auditHotSearchRoot(): string {
  return resolve(dirname(runtimeConfig.datasetDatabasePath), 'audit', 'search-hot')
}

function hotSearchBucketStartMs(timeMs: number): number {
  const safeTimeMs = Number.isFinite(timeMs) ? timeMs : Date.now()
  return Math.floor(safeTimeMs / hotSearchBucketMs) * hotSearchBucketMs
}

function hotSearchBucketStartMsFromFileName(fileName: string): number | undefined {
  if (!fileName.startsWith(hotSearchFilePrefix) || !fileName.endsWith(hotSearchFileSuffix)) {
    return undefined
  }
  const value = fileName.slice(hotSearchFilePrefix.length, -hotSearchFileSuffix.length)
  if (!/^\d{10}$/.test(value)) return undefined
  const year = value.slice(0, 4)
  const month = value.slice(4, 6)
  const day = value.slice(6, 8)
  const hour = value.slice(8, 10)
  const time = Date.parse(`${year}-${month}-${day}T${hour}:00:00.000Z`)
  return Number.isFinite(time) ? time : undefined
}

function normalizeHotSearchKeywords(values: string[]): { keywords: string[]; shortKeywordCount: number } {
  const seen = new Set<string>()
  const keywords: string[] = []
  let shortKeywordCount = 0
  for (const value of values) {
    const keyword = value.trim()
    if (!keyword) continue
    const normalized = keyword.slice(0, maxHotSearchKeywordLength)
    if ([...normalized].length < minHotSearchKeywordLength) {
      shortKeywordCount += 1
      continue
    }
    const dedupeKey = normalized.toLowerCase()
    if (seen.has(dedupeKey)) continue
    seen.add(dedupeKey)
    keywords.push(normalized)
    if (keywords.length >= maxHotSearchKeywords) return { keywords, shortKeywordCount }
  }
  return { keywords, shortKeywordCount }
}

function normalizeHotSearchLimit(value: number | undefined): number {
  if (!Number.isFinite(value)) return defaultHotSearchLimit
  return Math.min(Math.max(Math.trunc(value ?? defaultHotSearchLimit), 1), maxHotSearchLimit)
}

function normalizeHotSearchTimeRange(options: Pick<AuditHotSearchOptions, 'startAt' | 'endAt'>): { startMs: number; endMs: number; startAt: string; endAt: string } {
  const nowMs = Date.now()
  let endMs = parseTimeMs(options.endAt) ?? nowMs
  if (endMs > nowMs) endMs = nowMs
  let startMs = parseTimeMs(options.startAt) ?? endMs - defaultHotSearchWindowMs
  const earliestMs = endMs - defaultHotSearchWindowMs
  if (startMs < earliestMs) startMs = earliestMs
  if (startMs > endMs) startMs = earliestMs
  return {
    startMs,
    endMs,
    startAt: new Date(startMs).toISOString(),
    endAt: new Date(endMs).toISOString()
  }
}

function parseTimeMs(value: string | undefined): number | undefined {
  if (!value) return undefined
  const time = Date.parse(value)
  return Number.isFinite(time) ? time : undefined
}

async function listHotSearchFiles(startMs: number, endMs: number): Promise<HotSearchFile[]> {
  const files: HotSearchFile[] = []
  let scanned = 0
  try {
    const directory = await opendir(auditHotSearchRoot())
    for await (const entry of directory) {
      scanned += 1
      if (scanned > maxSearchFileScanEntries) break
      if (scanned % searchFileScanYieldEvery === 0) {
        await new Promise((resolve) => setImmediate(resolve))
      }
      if (!entry.isFile()) continue
      const bucketStartMs = hotSearchBucketStartMsFromFileName(entry.name)
      if (bucketStartMs === undefined) continue
      const bucketEndMs = bucketStartMs + hotSearchBucketMs
      if (bucketEndMs < startMs || bucketStartMs > endMs) continue
      const path = join(auditHotSearchRoot(), entry.name)
      try {
        const stats = await stat(path)
        if (stats.isFile() && stats.size > 0) {
          files.push({
            path,
            fileName: entry.name,
            bucketStartMs,
            bucketEndMs,
            mtimeMs: Number(stats.mtimeMs)
          })
        }
      } catch {
      }
    }
  } catch (error) {
    if (isMissingHotSearchDirectoryError(error)) {
      return []
    }
    throw error
  }
  return files.sort((left, right) => right.bucketStartMs - left.bucketStartMs || right.mtimeMs - left.mtimeMs)
}

function isMissingHotSearchDirectoryError(error: unknown): boolean {
  return Boolean(error && typeof error === 'object' && (error as { code?: unknown }).code === 'ENOENT')
}

async function resolveRgExecutable(): Promise<string | undefined> {
  try {
    const ripgrep = await import('@vscode/ripgrep')
    await access(ripgrep.rgPath)
    return ripgrep.rgPath
  } catch {
    return undefined
  }
}

async function searchHotFilesWithRg(options: {
  files: HotSearchFile[]
  keywords: string[]
  limit: number
  rgExecutable: string
  startMs: number
  endMs: number
  startAt: string
  endAt: string
  startedAt: number
}): Promise<AuditHotSearchFileResult> {
  const normalizedKeywords = options.keywords.map((keyword) => keyword.toLowerCase())
  const matches: Array<{ id: string; createdAtMs: number }> = []
  const seenIds = new Set<string>()
  let parsedMatchEventCount = 0
  let stoppedReason: RgStopReason | undefined

  const result = await runRgSearch({
    executable: options.rgExecutable,
    patterns: options.keywords,
    files: options.files,
    shouldStop: () => parsedMatchEventCount >= maxRgParsedMatchEvents,
    onMatch: (event) => {
      parsedMatchEventCount += 1
      const line = event.data?.lines?.text?.replace(/\r?\n$/, '')
      if (!line || !lineMatchesAnyKeyword(line, normalizedKeywords)) return
      const item = parseHotSearchLine(line)
      if (!item.auditLogId || seenIds.has(item.auditLogId)) return
      const createdAtMs = parseTimeMs(item.createdAt)
      if (createdAtMs === undefined || createdAtMs < options.startMs || createdAtMs > options.endMs) return
      seenIds.add(item.auditLogId)
      matches.push({ id: item.auditLogId, createdAtMs })
    }
  })
  stoppedReason = result.stopReason

  matches.sort((left, right) => right.createdAtMs - left.createdAtMs || left.id.localeCompare(right.id))
  const truncated = matches.length > options.limit || Boolean(stoppedReason)
  const auditLogIds = matches.slice(0, options.limit).map((item) => item.id)
  return {
    available: true,
    elapsedMs: Math.round(performance.now() - options.startedAt),
    keywords: options.keywords,
    startAt: options.startAt,
    endAt: options.endAt,
    limit: options.limit,
    auditLogIds,
    truncated,
    scannedFileCount: options.files.length,
    message: [
      truncated ? `结果超过 ${options.limit} 条，已按最新优先截断显示` : undefined,
      stoppedReason === 'match_parse_limit'
        ? `rg 命中行超过安全解析上限 ${maxRgParsedMatchEvents}，已提前停止`
        : undefined,
      stoppedReason === 'timeout'
        ? `rg 搜索超过 ${Math.round(maxRgSearchMs / 1000)} 秒，已提前停止`
        : undefined,
      result.exitState === 'no-match' || matches.length === 0 ? '没有匹配的审计内容' : undefined
    ].filter(Boolean).join('；') || undefined
  }
}

function lineMatchesAnyKeyword(line: string, normalizedKeywords: string[]): boolean {
  if (line.length > maxHotSearchLineChars) return false
  const searchableLine = line.toLowerCase()
  return normalizedKeywords.some((keyword) => searchableLine.includes(keyword))
}

function parseHotSearchLine(value: string): AuditHotSearchLine {
  try {
    const parsed = JSON.parse(value) as unknown
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed)
      ? parsed as AuditHotSearchLine
      : {}
  } catch {
    return {}
  }
}

function baseRgArgs(patterns: string[]): string[] {
  const args = [
    '--json',
    '--fixed-strings',
    '--ignore-case',
    '--no-heading',
    '--color=never',
    '--max-columns',
    String(maxHotSearchLineChars)
  ]
  for (const pattern of patterns) {
    args.push('-e', pattern)
  }
  args.push('--')
  return args
}

function runRgSearch(options: {
  executable: string
  patterns: string[]
  files: HotSearchFile[]
  onMatch: (event: RgMatchEvent) => void
  shouldStop?: () => boolean
}): Promise<RgSearchResult> {
  return new Promise((resolve, reject) => {
    const args = [...baseRgArgs(options.patterns), ...options.files.map((file) => file.path)]
    const child = spawn(options.executable, args, { windowsHide: true })
    let stdoutPending = ''
    let droppingOversizedStdoutLine = false
    let stderrText = ''
    let matched = false
    let stoppedReason: RgStopReason | undefined
    let settled = false
    const timeout = setTimeout(() => stopChild('timeout'), maxRgSearchMs)
    timeout.unref()

    const cleanup = () => clearTimeout(timeout)
    const settleResolve = (result: RgSearchResult) => {
      if (settled) return
      settled = true
      cleanup()
      resolve(result)
    }
    const settleReject = (error: Error) => {
      if (settled) return
      settled = true
      cleanup()
      reject(error)
    }
    const stopChild = (reason: RgStopReason) => {
      if (stoppedReason) return
      stoppedReason = reason
      child.kill()
    }
    const handleMatchEvent = (event: RgMatchEvent) => {
      matched = true
      options.onMatch(event)
      if (options.shouldStop?.()) {
        stopChild('match_parse_limit')
      }
    }

    child.stdout.setEncoding('utf8')
    child.stdout.on('data', (chunk: string) => {
      if (stoppedReason) return
      let remaining = chunk
      while (remaining.length > 0) {
        if (stoppedReason) return
        const newlineIndex = remaining.indexOf('\n')
        const segment = newlineIndex >= 0 ? remaining.slice(0, newlineIndex) : remaining
        remaining = newlineIndex >= 0 ? remaining.slice(newlineIndex + 1) : ''

        if (!droppingOversizedStdoutLine) {
          if (stdoutPending.length + segment.length > maxRgJsonLineLength) {
            stdoutPending = ''
            droppingOversizedStdoutLine = true
          } else {
            stdoutPending += segment
          }
        }

        if (newlineIndex >= 0) {
          if (!droppingOversizedStdoutLine) {
            const event = parseRgJsonLine(stdoutPending)
            if (event?.type === 'match') {
              handleMatchEvent(event)
            }
          }
          stdoutPending = ''
          droppingOversizedStdoutLine = false
        }
      }
    })

    child.stderr.setEncoding('utf8')
    child.stderr.on('data', (chunk: string) => {
      if (stoppedReason) return
      stderrText = trimText(stderrText + chunk, maxRgStderrLength)
    })

    child.on('error', (error) => {
      if (stoppedReason) {
        settleResolve({ exitState: matched ? 'matched' : 'no-match', stopReason: stoppedReason })
        return
      }
      settleReject(rgExecutionError(error))
    })

    child.on('close', (code) => {
      if (stoppedReason) {
        settleResolve({ exitState: matched ? 'matched' : 'no-match', stopReason: stoppedReason })
        return
      }
      if (!droppingOversizedStdoutLine && stdoutPending.trim()) {
        const event = parseRgJsonLine(stdoutPending)
        if (event?.type === 'match') {
          handleMatchEvent(event)
        }
      }
      if (stoppedReason) {
        settleResolve({ exitState: matched ? 'matched' : 'no-match', stopReason: stoppedReason })
        return
      }
      if (code === 0) {
        settleResolve({ exitState: 'matched' })
        return
      }
      if (code === 1 && !matched) {
        settleResolve({ exitState: 'no-match' })
        return
      }
      settleReject(new Error(`rg 执行失败，审计内容搜索暂不可用。${stderrText ? `错误信息：${stderrText.trim()}` : ''}`))
    })
  })
}

function parseRgJsonLine(value: string): RgMatchEvent | undefined {
  const line = value.trim()
  if (!line) return undefined
  try {
    const event = JSON.parse(line) as unknown
    return event && typeof event === 'object' && !Array.isArray(event) ? event as RgMatchEvent : undefined
  } catch {
    return undefined
  }
}

function rgExecutionError(error: Error): Error {
  const code = (error as NodeJS.ErrnoException).code
  if (code === 'ENOENT') {
    return new Error('当前运行环境未找到 rg，审计内容搜索不可用。请确认部署时已成功安装后端生产依赖 @vscode/ripgrep。')
  }
  return new Error(`rg 启动失败，审计内容搜索暂不可用。${error.message}`)
}

function trimText(value: string, maxLength: number): string {
  if (value.length <= maxLength) return value
  return value.slice(value.length - maxLength)
}

function acquireHotSearchSlot(): boolean {
  if (activeHotSearches >= maxConcurrentHotSearches) {
    return false
  }
  activeHotSearches += 1
  return true
}

function releaseHotSearchSlot(): void {
  activeHotSearches = Math.max(0, activeHotSearches - 1)
}

function positiveInteger(value: number | undefined, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0
    ? Math.trunc(value)
    : fallback
}

function yieldToEventLoop(): Promise<void> {
  return new Promise((resolvePromise) => setImmediate(resolvePromise))
}

function unavailableHotSearchResult(
  base: Omit<AuditHotSearchFileResult, 'available' | 'auditLogIds' | 'truncated' | 'elapsedMs' | 'scannedFileCount'>,
  startedAt: number,
  message: string
): AuditHotSearchFileResult {
  return {
    ...base,
    available: false,
    elapsedMs: Math.round(performance.now() - startedAt),
    auditLogIds: [],
    truncated: false,
    scannedFileCount: 0,
    message
  }
}

import { spawn } from 'node:child_process'
import { existsSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

export interface RuntimeLogGrepOptions {
  keywords: string[]
  limit?: number
  startAt?: string
  endAt?: string
}

export interface RuntimeLogGrepItem {
  id: string
  file: string
  fileName: string
  lineNumber?: number
  time: string
  level: string
  traceId?: string
  event?: string
  message?: string
  errorMessage?: string
  rawJson: string
  line: string
}

export interface RuntimeLogGrepResult {
  available: boolean
  elapsedMs: number
  keywords: string[]
  startAt: string
  endAt: string
  defaultRangeDays: number
  maxRangeDays: number
  items: RuntimeLogGrepItem[]
  limit: number
  truncated: boolean
  scannedFileCount: number
  message?: string
}

export interface RuntimeLogGrepRuntime {
  earliestFileTime?: string
  defaultStartAt: string
  defaultEndAt: string
  defaultRangeDays: number
  maxRangeDays: number
  fileRetentionDays: number
}

interface LogFile {
  path: string
  fileName: string
  size: number
  birthtimeMs: number
  mtimeMs: number
  order: number
}

interface RuntimeLogGrepTimeRange {
  startMs: number
  endMs: number
  startAt: string
  endAt: string
  adjusted: boolean
}

interface RgMatchEvent {
  type?: string
  data?: {
    path?: { text?: string }
    lines?: { text?: string }
    line_number?: number
  }
}

type RgExitState = 'matched' | 'no-match'

type OrderedRuntimeLogGrepItem = RuntimeLogGrepItem & {
  fileOrder: number
  sortTimeMs: number
}

const maxKeywords = 10
const maxKeywordLength = 128
const defaultLimit = 100
const maxLimit = 100
const maxLineLength = 20_000
const maxRgStderrLength = 2_000
const maxRgCommandChars = 24_000
const dayMs = 24 * 60 * 60 * 1000
const defaultGrepRangeDays = 3
const maxGrepRangeDays = 7

export async function grepRuntimeLogFiles(options: RuntimeLogGrepOptions): Promise<RuntimeLogGrepResult> {
  const startedAt = performance.now()
  const keywords = normalizeGrepKeywords(options.keywords)
  const limit = normalizeLimit(options.limit)
  const files = runtimeConfig.log.fileEnabled ? listLogFiles() : []
  const timeRange = normalizeGrepTimeRange(options, files)
  const baseResult = (): Omit<RuntimeLogGrepResult, 'available' | 'items' | 'truncated' | 'elapsedMs' | 'scannedFileCount'> => ({
    keywords,
    startAt: timeRange.startAt,
    endAt: timeRange.endAt,
    defaultRangeDays: defaultGrepRangeDays,
    maxRangeDays: maxGrepRangeDays,
    limit
  })

  if (!keywords.length) {
    return {
      ...baseResult(),
      available: true,
      elapsedMs: Math.round(performance.now() - startedAt),
      items: [],
      truncated: false,
      scannedFileCount: 0,
      message: '请输入要搜索的关键字'
    }
  }

  if (!runtimeConfig.log.fileEnabled) {
    return unavailableResult(baseResult(), startedAt, '文件日志未启用，无法使用 grep 模式。')
  }

  if (!files.length) {
    return {
      ...baseResult(),
      available: true,
      elapsedMs: Math.round(performance.now() - startedAt),
      items: [],
      truncated: false,
      scannedFileCount: 0,
      message: '没有可搜索的日志文件'
    }
  }

  const searchableFiles = filterLogFilesByTimeRange(files, timeRange)
  if (!searchableFiles.length) {
    return {
      ...baseResult(),
      available: true,
      elapsedMs: Math.round(performance.now() - startedAt),
      items: [],
      truncated: false,
      scannedFileCount: 0,
      message: timeRange.adjusted
        ? `grep 文件时间范围已自动调整，单次最多 ${maxGrepRangeDays} 天；当前时间范围内没有可搜索的日志文件。`
        : '当前文件时间范围内没有可搜索的日志文件'
    }
  }

  const rgExecutable = await resolveRgExecutable()
  if (!rgExecutable) {
    return unavailableResult(
      baseResult(),
      startedAt,
      '当前运行环境未找到 rg，grep 模式不可用。请确认部署时已成功安装后端生产依赖 @vscode/ripgrep。'
    )
  }

  try {
    const result = await searchLogFilesWithRg({
      ...baseResult(),
      files: searchableFiles,
      rgExecutable,
      timeRange,
      rangeAdjustedMessage: timeRange.adjusted ? `grep 文件时间范围已自动调整，单次最多 ${maxGrepRangeDays} 天` : undefined,
      startedAt
    })
    return result
  } catch (error) {
    return unavailableResult(
      baseResult(),
      startedAt,
      error instanceof Error && error.message ? error.message : 'rg 执行失败，grep 模式暂不可用。'
    )
  }
}

export function getRuntimeLogGrepRuntime(): RuntimeLogGrepRuntime {
  const files = runtimeConfig.log.fileEnabled ? listLogFiles() : []
  const range = normalizeGrepTimeRange({}, files)
  const earliestFileMs = earliestLogFileMs(files)
  return {
    earliestFileTime: earliestFileMs === undefined ? undefined : new Date(earliestFileMs).toISOString(),
    defaultStartAt: range.startAt,
    defaultEndAt: range.endAt,
    defaultRangeDays: defaultGrepRangeDays,
    maxRangeDays: maxGrepRangeDays,
    fileRetentionDays: runtimeConfig.log.retentionDays
  }
}

function normalizeGrepKeywords(values: string[]): string[] {
  const seen = new Set<string>()
  const keywords: string[] = []
  for (const value of values) {
    for (const part of value.split(/[\s,;，；]+/)) {
      const keyword = part.trim()
      if (!keyword) continue
      const normalized = keyword.slice(0, maxKeywordLength)
      const dedupeKey = normalized.toLowerCase()
      if (seen.has(dedupeKey)) continue
      seen.add(dedupeKey)
      keywords.push(normalized)
      if (keywords.length >= maxKeywords) return keywords
    }
  }
  return keywords
}

function normalizeLimit(value: number | undefined): number {
  if (!Number.isFinite(value)) return defaultLimit
  return Math.min(Math.max(Math.trunc(value ?? defaultLimit), 1), maxLimit)
}

function normalizeGrepTimeRange(options: Pick<RuntimeLogGrepOptions, 'startAt' | 'endAt'>, files: LogFile[]): RuntimeLogGrepTimeRange {
  const nowMs = Date.now()
  const earliestFileMs = earliestLogFileMs(files)
  let adjusted = false
  let endMs = parseTimeMs(options.endAt) ?? nowMs
  if (endMs > nowMs) {
    endMs = nowMs
    adjusted = true
  }

  if (earliestFileMs !== undefined && endMs < earliestFileMs) {
    endMs = earliestFileMs
    adjusted = true
  }

  let startMs = parseTimeMs(options.startAt) ?? endMs - defaultGrepRangeDays * dayMs
  if (earliestFileMs !== undefined && startMs < earliestFileMs) {
    startMs = earliestFileMs
    adjusted = true
  }

  if (startMs > endMs) {
    startMs = Math.max(endMs - defaultGrepRangeDays * dayMs, earliestFileMs ?? Number.NEGATIVE_INFINITY)
    adjusted = true
  }

  if (endMs - startMs > maxGrepRangeDays * dayMs) {
    startMs = endMs - maxGrepRangeDays * dayMs
    adjusted = true
  }

  if (earliestFileMs !== undefined && startMs < earliestFileMs) {
    startMs = earliestFileMs
  }

  return {
    startMs,
    endMs,
    startAt: new Date(startMs).toISOString(),
    endAt: new Date(endMs).toISOString(),
    adjusted
  }
}

function parseTimeMs(value: string | undefined): number | undefined {
  if (!value) return undefined
  const time = Date.parse(value)
  return Number.isFinite(time) ? time : undefined
}

function listLogFiles(): LogFile[] {
  if (!existsSync(runtimeConfig.log.directory)) {
    return []
  }

  try {
    return readdirSync(runtimeConfig.log.directory)
      .filter((fileName) => fileName.endsWith('.log'))
      .map((fileName) => {
        const path = join(runtimeConfig.log.directory, fileName)
        const stats = statSync(path)
        return { path, fileName, stats }
      })
      .filter((file) => file.stats.isFile())
      .sort((left, right) => right.stats.mtimeMs - left.stats.mtimeMs)
      .map((file, order) => ({
        path: file.path,
        fileName: file.fileName,
        size: file.stats.size,
        birthtimeMs: file.stats.birthtimeMs,
        mtimeMs: file.stats.mtimeMs,
        order
      }))
  } catch {
    return []
  }
}

function earliestLogFileMs(files: LogFile[]): number | undefined {
  const values = files.map((file) => fileStartMs(file)).filter(Number.isFinite)
  return values.length ? Math.min(...values) : undefined
}

function fileStartMs(file: LogFile): number {
  const birthtimeMs = Number.isFinite(file.birthtimeMs) && file.birthtimeMs > 0 ? file.birthtimeMs : file.mtimeMs
  return Math.min(birthtimeMs, file.mtimeMs)
}

function filterLogFilesByTimeRange(files: LogFile[], timeRange: RuntimeLogGrepTimeRange): LogFile[] {
  return files.filter((file) => {
    if (file.size <= 0) return false
    return file.mtimeMs >= timeRange.startMs && fileStartMs(file) <= timeRange.endMs
  })
}

async function resolveRgExecutable(): Promise<string | undefined> {
  try {
    const ripgrep = await import('@vscode/ripgrep')
    return existsSync(ripgrep.rgPath) ? ripgrep.rgPath : undefined
  } catch {
    return undefined
  }
}

async function searchLogFilesWithRg(options: {
  files: LogFile[]
  keywords: string[]
  limit: number
  rgExecutable: string
  timeRange: RuntimeLogGrepTimeRange
  rangeAdjustedMessage?: string
  startedAt: number
}): Promise<RuntimeLogGrepResult> {
  const normalizedKeywords = options.keywords.map((keyword) => keyword.toLowerCase())
  const primaryKeyword = selectPrimaryKeyword(options.keywords)
  const filesByPath = new Map(options.files.map((file) => [file.path, file]))
  const filesByLowerPath = new Map(options.files.map((file) => [file.path.toLowerCase(), file]))
  const items: OrderedRuntimeLogGrepItem[] = []
  let matchedCount = 0
  let noMatchBatches = 0
  const batches = batchLogFilesForRg(options.files, primaryKeyword)

  for (const files of batches) {
    const exitState = await runRgSearch({
      executable: options.rgExecutable,
      pattern: primaryKeyword,
      files,
      onMatch: (event) => {
        const filePath = event.data?.path?.text
        const line = event.data?.lines?.text?.replace(/\r?\n$/, '')
        if (!filePath || line === undefined) return
        const file = filesByPath.get(filePath) ?? filesByLowerPath.get(filePath.toLowerCase())
        if (!file) return
        if (!lineMatchesKeywords(line, normalizedKeywords)) return
        if (isRuntimeLogSearchRequestLine(line)) return

        matchedCount += 1
        insertLatestGrepItem(items, buildGrepItem({
          file,
          line,
          lineNumber: event.data?.line_number,
          sequence: matchedCount
        }), options.limit)
      }
    })
    if (exitState === 'no-match') {
      noMatchBatches += 1
    }
  }

  const truncated = matchedCount > options.limit
  return {
    available: true,
    elapsedMs: Math.round(performance.now() - options.startedAt),
    keywords: options.keywords,
    startAt: options.timeRange.startAt,
    endAt: options.timeRange.endAt,
    defaultRangeDays: defaultGrepRangeDays,
    maxRangeDays: maxGrepRangeDays,
    items: items.sort(compareGrepItems).map(stripOrderFields),
    limit: options.limit,
    truncated,
    scannedFileCount: options.files.length,
    message: [
      options.rangeAdjustedMessage,
      truncated ? `结果超过 ${options.limit} 行，已按最新优先截断显示` : undefined,
      noMatchBatches === batches.length ? '没有匹配的日志行' : undefined
    ].filter(Boolean).join('；') || undefined
  }
}

function selectPrimaryKeyword(keywords: string[]): string {
  return [...keywords].sort((left, right) => right.length - left.length)[0] ?? ''
}

function batchLogFilesForRg(files: LogFile[], pattern: string): LogFile[][] {
  const batches: LogFile[][] = []
  let batch: LogFile[] = []
  let currentChars = baseRgArgs(pattern).reduce((total, value) => total + value.length + 3, 0)

  for (const file of files) {
    const nextChars = file.path.length + 3
    if (batch.length && currentChars + nextChars > maxRgCommandChars) {
      batches.push(batch)
      batch = []
      currentChars = baseRgArgs(pattern).reduce((total, value) => total + value.length + 3, 0)
    }
    batch.push(file)
    currentChars += nextChars
  }

  if (batch.length) {
    batches.push(batch)
  }
  return batches
}

function baseRgArgs(pattern: string): string[] {
  return [
    '--json',
    '--fixed-strings',
    '--ignore-case',
    '--no-heading',
    '--color=never',
    '--',
    pattern
  ]
}

function runRgSearch(options: {
  executable: string
  pattern: string
  files: LogFile[]
  onMatch: (event: RgMatchEvent) => void
}): Promise<RgExitState> {
  return new Promise((resolve, reject) => {
    const args = [...baseRgArgs(options.pattern), ...options.files.map((file) => file.path)]
    const child = spawn(options.executable, args, { windowsHide: true })
    let stdoutPending = ''
    let stderrText = ''
    let matched = false

    child.stdout.setEncoding('utf8')
    child.stdout.on('data', (chunk: string) => {
      stdoutPending += chunk
      let newlineIndex = stdoutPending.indexOf('\n')
      while (newlineIndex >= 0) {
        const line = stdoutPending.slice(0, newlineIndex)
        stdoutPending = stdoutPending.slice(newlineIndex + 1)
        const event = parseRgJsonLine(line)
        if (event?.type === 'match') {
          matched = true
          options.onMatch(event)
        }
        newlineIndex = stdoutPending.indexOf('\n')
      }
    })

    child.stderr.setEncoding('utf8')
    child.stderr.on('data', (chunk: string) => {
      stderrText = trimLine(stderrText + chunk, maxRgStderrLength)
    })

    child.on('error', (error) => {
      reject(rgExecutionError(error))
    })

    child.on('close', (code) => {
      if (stdoutPending.trim()) {
        const event = parseRgJsonLine(stdoutPending)
        if (event?.type === 'match') {
          matched = true
          options.onMatch(event)
        }
      }

      if (code === 0) {
        resolve('matched')
        return
      }
      if (code === 1 && !matched) {
        resolve('no-match')
        return
      }
      reject(new Error(`rg 执行失败，grep 模式暂不可用。${stderrText ? `错误信息：${stderrText.trim()}` : ''}`))
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
    return new Error('当前运行环境未找到 rg，grep 模式不可用。请确认部署时已成功安装后端生产依赖 @vscode/ripgrep。')
  }
  return new Error(`rg 启动失败，grep 模式暂不可用。${error.message}`)
}

function buildGrepItem(input: {
  file: LogFile
  line: string
  lineNumber?: number
  sequence: number
}): OrderedRuntimeLogGrepItem {
  const fields = runtimeLogFieldsFromLine(input.line)
  const parsedTime = parseRuntimeLogTimeMs(fields.time)
  return {
    id: `${input.file.path}:${input.lineNumber ?? input.sequence}:${input.sequence}`,
    file: input.file.path,
    fileName: input.file.fileName,
    lineNumber: input.lineNumber,
    fileOrder: input.file.order,
    sortTimeMs: Number.isFinite(parsedTime) ? parsedTime : input.file.mtimeMs,
    ...fields
  }
}

function insertLatestGrepItem(items: OrderedRuntimeLogGrepItem[], item: OrderedRuntimeLogGrepItem, limit: number): void {
  items.push(item)
  items.sort(compareGrepItems)
  if (items.length > limit) {
    items.length = limit
  }
}

function lineMatchesKeywords(line: string, normalizedKeywords: string[]): boolean {
  const searchableLine = line.toLowerCase()
  return normalizedKeywords.every((keyword) => searchableLine.includes(keyword))
}

function isRuntimeLogSearchRequestLine(line: string): boolean {
  try {
    const parsed = JSON.parse(line) as unknown
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return false
    const record = parsed as Record<string, unknown>
    const event = stringValue(record.event)
    if (event !== 'http_request_completed' && event !== 'http_request_closed') return false
    return isRuntimeLogSearchPath(stringValue(record.originalUrl)) || isRuntimeLogSearchPath(stringValue(record.path))
  } catch {
    return false
  }
}

function isRuntimeLogSearchPath(value: string | undefined): boolean {
  if (!value) return false
  const path = value.split('?', 1)[0]?.replace(/\/+$/, '')
  return path === '/api/runtime-logs' || Boolean(path?.startsWith('/api/runtime-logs/'))
}

function compareGrepItems(left: OrderedRuntimeLogGrepItem, right: OrderedRuntimeLogGrepItem): number {
  if (left.sortTimeMs !== right.sortTimeMs) {
    return right.sortTimeMs - left.sortTimeMs
  }
  if (left.fileOrder !== right.fileOrder) {
    return left.fileOrder - right.fileOrder
  }
  return (right.lineNumber ?? 0) - (left.lineNumber ?? 0)
}

function stripOrderFields(item: OrderedRuntimeLogGrepItem): RuntimeLogGrepItem {
  return {
    id: item.id,
    file: item.file,
    fileName: item.fileName,
    lineNumber: item.lineNumber,
    time: item.time,
    level: item.level,
    traceId: item.traceId,
    event: item.event,
    message: item.message,
    errorMessage: item.errorMessage,
    rawJson: item.rawJson,
    line: item.line
  }
}

function unavailableResult(
  base: Omit<RuntimeLogGrepResult, 'available' | 'items' | 'truncated' | 'elapsedMs' | 'scannedFileCount'>,
  startedAt: number,
  message: string
): RuntimeLogGrepResult {
  return {
    ...base,
    available: false,
    elapsedMs: Math.round(performance.now() - startedAt),
    items: [],
    truncated: false,
    scannedFileCount: 0,
    message
  }
}

function trimLine(value: string, length = maxLineLength): string {
  return value.length > length ? `${value.slice(0, length)}...` : value
}

function runtimeLogFieldsFromLine(line: string): Pick<RuntimeLogGrepItem, 'time' | 'level' | 'traceId' | 'event' | 'message' | 'errorMessage' | 'rawJson' | 'line'> {
  const rawJson = trimLine(line)
  try {
    const parsed = JSON.parse(line) as unknown
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      return fallbackRuntimeLogFields(rawJson)
    }
    const record = parsed as Record<string, unknown>
    return {
      time: runtimeLogTimeValue(record.time),
      level: normalizeLevel(record.level),
      traceId: stringValue(record.traceId),
      event: stringValue(record.event),
      message: stringValue(record.msg) ?? stringValue(record.message),
      errorMessage: stringValue(record.errorMessage) ?? errorMessageFromErr(record.err),
      rawJson,
      line: rawJson
    }
  } catch {
    return fallbackRuntimeLogFields(rawJson)
  }
}

function fallbackRuntimeLogFields(rawJson: string): Pick<RuntimeLogGrepItem, 'time' | 'level' | 'rawJson' | 'line'> {
  return {
    time: '',
    level: 'info',
    rawJson,
    line: rawJson
  }
}

function runtimeLogTimeValue(value: unknown): string {
  if (typeof value === 'string' && value.trim()) return value.trim()
  if (typeof value === 'number' && Number.isFinite(value)) return new Date(value).toISOString()
  return ''
}

function parseRuntimeLogTimeMs(value: string): number {
  if (!value) return Number.NaN
  const numeric = Number(value)
  if (Number.isFinite(numeric)) return numeric
  return Date.parse(value)
}

function normalizeLevel(value: unknown): string {
  if (typeof value === 'string' && value.trim()) {
    return value.trim().toLowerCase()
  }
  if (typeof value !== 'number') return 'info'
  if (value >= 60) return 'fatal'
  if (value >= 50) return 'error'
  if (value >= 40) return 'warn'
  if (value >= 30) return 'info'
  if (value >= 20) return 'debug'
  return 'trace'
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function errorMessageFromErr(value: unknown): string | undefined {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return undefined
  }
  return stringValue((value as Record<string, unknown>).message)
}

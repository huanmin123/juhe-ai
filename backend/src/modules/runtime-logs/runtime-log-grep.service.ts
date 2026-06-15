import { spawn } from 'node:child_process'
import { access, opendir, stat } from 'node:fs/promises'
import { join } from 'node:path'
import { setImmediate as yieldImmediate } from 'node:timers/promises'

import { runtimeConfig } from '../../config/runtime.js'
import {
  defaultGrepRangeDays,
  earliestLogFileMs,
  filterLogFilesByTimeRange,
  maxGrepRangeDays,
  minGrepKeywordLength,
  normalizeGrepKeywords,
  normalizeGrepLimit,
  normalizeGrepTimeRange,
  type RuntimeLogGrepTimeRange
} from './runtime-log-grep-normalizers.js'

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
  activeSearchCount: number
  maxConcurrentSearches: number
}

interface LogFile {
  path: string
  fileName: string
  size: number
  birthtimeMs: number
  mtimeMs: number
  order: number
}

interface LogFileListing {
  files: LogFile[]
  scannedEntryCount: number
  truncatedReason?: 'entry_limit' | 'deadline'
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
type RgStopReason = 'match_parse_limit' | 'timeout'

interface RgSearchResult {
  exitState: RgExitState
  stopReason?: RgStopReason
}

type OrderedRuntimeLogGrepItem = RuntimeLogGrepItem & {
  fileOrder: number
  sortTimeMs: number
}

const maxLineLength = 20_000
const maxRgJsonLineLength = maxLineLength + 8_000
const maxRgStderrLength = 2_000
const maxRgCommandChars = 24_000
const maxRgParsedMatchEvents = 2_000
const maxRgSearchMs = 15_000
const maxConcurrentGrepSearches = 1
const maxLogDirectoryScanEntries = 10_000
const maxLogDirectoryScanMs = 2_000
const logFileScanYieldEvery = 100
let activeGrepSearches = 0

export async function grepRuntimeLogFiles(options: RuntimeLogGrepOptions): Promise<RuntimeLogGrepResult> {
  const startedAt = performance.now()
  const keywordInput = normalizeGrepKeywords(options.keywords)
  const keywords = keywordInput.keywords
  const limit = normalizeGrepLimit(options.limit)
  let timeRange = normalizeGrepTimeRange(options, [])
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
      message: keywordInput.shortKeywordCount > 0
        ? `grep 关键字至少需要 ${minGrepKeywordLength} 个字符，请输入更具体的关键字。`
        : '请输入要搜索的关键字'
    }
  }

  if (!runtimeConfig.log.fileEnabled) {
    return unavailableResult(baseResult(), startedAt, '文件日志未启用，无法使用 grep 模式。')
  }

  if (!acquireGrepSearchSlot()) {
    return unavailableResult(baseResult(), startedAt, '已有 grep 搜索正在运行，请稍后重试。')
  }

  try {
    const fileListing = await listLogFiles()
    const files = fileListing.files
    timeRange = normalizeGrepTimeRange(options, files)

    if (!files.length) {
      return {
        ...baseResult(),
        available: true,
        elapsedMs: Math.round(performance.now() - startedAt),
        items: [],
        truncated: false,
        scannedFileCount: 0,
        message: [logFileListingWarning(fileListing), '没有可搜索的日志文件'].filter(Boolean).join('；')
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
        message: [
          logFileListingWarning(fileListing),
          timeRange.adjusted
            ? `grep 文件时间范围已自动调整，单次最多 ${maxGrepRangeDays} 天；当前时间范围内没有可搜索的日志文件。`
            : '当前文件时间范围内没有可搜索的日志文件'
        ].filter(Boolean).join('；')
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
        warnings: [
          logFileListingWarning(fileListing),
          timeRange.adjusted ? `grep 文件时间范围已自动调整，单次最多 ${maxGrepRangeDays} 天` : undefined,
          keywordInput.shortKeywordCount > 0 ? `已忽略少于 ${minGrepKeywordLength} 个字符的短关键字` : undefined
        ].filter((item): item is string => Boolean(item)),
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
  } finally {
    releaseGrepSearchSlot()
  }
}

export async function getRuntimeLogGrepRuntime(): Promise<RuntimeLogGrepRuntime> {
  const files = runtimeConfig.log.fileEnabled ? (await listLogFiles()).files : []
  const range = normalizeGrepTimeRange({}, files)
  const earliestFileMs = earliestLogFileMs(files)
  return {
    earliestFileTime: earliestFileMs === undefined ? undefined : new Date(earliestFileMs).toISOString(),
    defaultStartAt: range.startAt,
    defaultEndAt: range.endAt,
    defaultRangeDays: defaultGrepRangeDays,
    maxRangeDays: maxGrepRangeDays,
    fileRetentionDays: runtimeConfig.log.retentionDays,
    activeSearchCount: activeGrepSearches,
    maxConcurrentSearches: maxConcurrentGrepSearches
  }
}

function acquireGrepSearchSlot(): boolean {
  if (activeGrepSearches >= maxConcurrentGrepSearches) {
    return false
  }
  activeGrepSearches += 1
  return true
}

function releaseGrepSearchSlot(): void {
  activeGrepSearches = Math.max(0, activeGrepSearches - 1)
}

async function listLogFiles(): Promise<LogFileListing> {
  const maxFiles = Math.max(1, Math.trunc(runtimeConfig.log.maxFiles))
  const retainedFiles: Array<{ path: string; fileName: string; stats: Awaited<ReturnType<typeof stat>> }> = []
  let scannedEntryCount = 0
  let truncatedReason: LogFileListing['truncatedReason']
  const deadline = performance.now() + maxLogDirectoryScanMs
  try {
    const directory = await opendir(runtimeConfig.log.directory)
    for await (const entry of directory) {
      if (scannedEntryCount >= maxLogDirectoryScanEntries) {
        truncatedReason = 'entry_limit'
        break
      }
      if (performance.now() >= deadline) {
        truncatedReason = 'deadline'
        break
      }
      scannedEntryCount += 1
      if (scannedEntryCount % logFileScanYieldEvery === 0) {
        await yieldImmediate()
      }
      if (!entry.isFile() || !entry.name.endsWith('.log')) continue
      const path = join(runtimeConfig.log.directory, entry.name)
      try {
        const stats = await stat(path)
        if (stats.isFile()) {
          retainNewestLogFile(retainedFiles, { path, fileName: entry.name, stats }, maxFiles)
        }
      } catch {
      }
    }
  } catch {
    return { files: [], scannedEntryCount, truncatedReason }
  }
  return {
    files: retainedFiles.map((file, order) => ({
      path: file.path,
      fileName: file.fileName,
      size: Number(file.stats.size),
      birthtimeMs: Number(file.stats.birthtimeMs),
      mtimeMs: Number(file.stats.mtimeMs),
      order
    })),
    scannedEntryCount,
    truncatedReason
  }
}

function logFileListingWarning(listing: LogFileListing): string | undefined {
  if (!listing.truncatedReason) return undefined
  if (listing.truncatedReason === 'deadline') {
    return `日志目录扫描超过 ${Math.round(maxLogDirectoryScanMs / 1000)} 秒，已只使用扫描到的最新日志文件`
  }
  return `日志目录条目超过 ${maxLogDirectoryScanEntries} 个，已只使用扫描到的最新日志文件`
}

function retainNewestLogFile<T extends { stats: Awaited<ReturnType<typeof stat>> }>(files: T[], file: T, maxFiles: number): void {
  let insertIndex = files.length
  for (let index = 0; index < files.length; index += 1) {
    if (Number(file.stats.mtimeMs) > Number(files[index].stats.mtimeMs)) {
      insertIndex = index
      break
    }
  }
  if (insertIndex >= maxFiles) {
    return
  }
  files.splice(insertIndex, 0, file)
  if (files.length > maxFiles) {
    files.pop()
  }
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

async function searchLogFilesWithRg(options: {
  files: LogFile[]
  keywords: string[]
  limit: number
  rgExecutable: string
  timeRange: RuntimeLogGrepTimeRange
  warnings: string[]
  startedAt: number
}): Promise<RuntimeLogGrepResult> {
  const normalizedKeywords = options.keywords.map((keyword) => keyword.toLowerCase())
  const primaryKeyword = selectPrimaryKeyword(options.keywords)
  const filesByPath = new Map(options.files.map((file) => [file.path, file]))
  const filesByLowerPath = new Map(options.files.map((file) => [file.path.toLowerCase(), file]))
  const items: OrderedRuntimeLogGrepItem[] = []
  let matchedCount = 0
  let noMatchBatches = 0
  let stoppedReason: RgStopReason | undefined
  const batches = batchLogFilesForRg(options.files, primaryKeyword)

  for (const files of batches) {
    if (stoppedReason) {
      break
    }
    const searchResult = await runRgSearch({
      executable: options.rgExecutable,
      pattern: primaryKeyword,
      files,
      shouldStop: () => matchedCount >= maxRgParsedMatchEvents,
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
    stoppedReason = searchResult.stopReason
    if (searchResult.exitState === 'no-match') {
      noMatchBatches += 1
    }
  }

  const truncated = matchedCount > options.limit || Boolean(stoppedReason)
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
      ...options.warnings,
      truncated ? `结果超过 ${options.limit} 行，已按最新优先截断显示` : undefined,
      stoppedReason === 'match_parse_limit'
        ? `grep 命中行超过安全解析上限 ${maxRgParsedMatchEvents}，已提前停止以保护 DB service 事件循环`
        : undefined,
      stoppedReason === 'timeout'
        ? `grep 搜索超过 ${Math.round(maxRgSearchMs / 1000)} 秒，已提前停止以保护 DB service 事件循环`
        : undefined,
      !stoppedReason && noMatchBatches === batches.length ? '没有匹配的日志行' : undefined
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
    '--max-columns',
    String(maxLineLength),
    '--',
    pattern
  ]
}

function runRgSearch(options: {
  executable: string
  pattern: string
  files: LogFile[]
  onMatch: (event: RgMatchEvent) => void
  shouldStop?: () => boolean
}): Promise<RgSearchResult> {
  return new Promise((resolve, reject) => {
    const args = [...baseRgArgs(options.pattern), ...options.files.map((file) => file.path)]
    const child = spawn(options.executable, args, { windowsHide: true })
    let stdoutPending = ''
    let droppingOversizedStdoutLine = false
    let stderrText = ''
    let matched = false
    let stoppedReason: RgStopReason | undefined
    let settled = false
    const timeout = setTimeout(() => {
      stopChild('timeout')
    }, maxRgSearchMs)
    timeout.unref()

    const cleanup = () => {
      clearTimeout(timeout)
    }
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
      if (stoppedReason) {
        return
      }
      let remaining = chunk
      while (remaining.length > 0) {
        if (stoppedReason) {
          return
        }
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
      if (stoppedReason) {
        return
      }
      stderrText = trimLine(stderrText + chunk, maxRgStderrLength)
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
      settleReject(new Error(`rg 执行失败，grep 模式暂不可用。${stderrText ? `错误信息：${stderrText.trim()}` : ''}`))
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
  if (line.length > maxLineLength) {
    return false
  }
  const searchableLine = line.toLowerCase()
  return normalizedKeywords.every((keyword) => searchableLine.includes(keyword))
}

function isRuntimeLogSearchRequestLine(line: string): boolean {
  if (line.length > maxLineLength) {
    return isRuntimeLogSearchPathText(line)
  }

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
  return path === '/__aisys__/api/runtime-logs'
    || Boolean(path?.startsWith('/__aisys__/api/runtime-logs/'))
}

function isRuntimeLogSearchPathText(value: string): boolean {
  return value.includes('/__aisys__/api/runtime-logs')
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
  if (line.length > maxLineLength) {
    return fallbackRuntimeLogFields(rawJson)
  }

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

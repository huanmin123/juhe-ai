import { spawn } from 'node:child_process'
import { existsSync, readdirSync, statSync } from 'node:fs'
import { open } from 'node:fs/promises'
import { basename, join } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

export type RuntimeLogGrepMode = 'rg'

export interface RuntimeLogGrepOptions {
  keywords: string[]
  limit?: number
}

export interface RuntimeLogGrepItem {
  id: string
  file: string
  fileName: string
  lineNumber?: number
  lineNumberFromEnd: number
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
  mode?: RuntimeLogGrepMode
  elapsedMs: number
  keywords: string[]
  items: RuntimeLogGrepItem[]
  limit: number
  truncated: boolean
  scannedFileCount: number
  message?: string
  installSteps?: string[]
}

interface LogFile {
  path: string
  fileName: string
  size: number
  mtimeMs: number
  order: number
}

type OrderedRuntimeLogGrepItem = RuntimeLogGrepItem & {
  fileOrder: number
  fileMtimeMs: number
}

const maxKeywords = 8
const maxKeywordLength = 128
const defaultLimit = 100
const maxLimit = 100
const maxLineLength = 20_000
const reverseReadChunkBytes = 64 * 1024
const fileSearchConcurrency = 8

export async function grepRuntimeLogFiles(options: RuntimeLogGrepOptions): Promise<RuntimeLogGrepResult> {
  const startedAt = performance.now()
  const keywords = normalizeGrepKeywords(options.keywords)
  const limit = normalizeLimit(options.limit)
  const baseResult = (): Omit<RuntimeLogGrepResult, 'available' | 'items' | 'truncated' | 'elapsedMs' | 'scannedFileCount'> => ({
    keywords,
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
      message: '请输入 grep 关键字'
    }
  }

  if (!runtimeConfig.log.fileEnabled) {
    return unavailableResult(baseResult(), startedAt, '文件日志未启用，无法使用 grep 模式。')
  }

  if (!await ripgrepAvailable()) {
    return unavailableResult(
      baseResult(),
      startedAt,
      '当前环境未安装 ripgrep（rg），无法使用 grep 模式。',
      ripgrepInstallSteps()
    )
  }

  const files = listLogFiles()
  if (!files.length) {
    return {
      ...baseResult(),
      available: true,
      mode: 'rg',
      elapsedMs: Math.round(performance.now() - startedAt),
      items: [],
      truncated: false,
      scannedFileCount: 0,
      message: '没有可搜索的日志文件'
    }
  }

  return searchLogFilesFromEnd({
    ...baseResult(),
    files,
    startedAt
  })
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

function ripgrepAvailable(): Promise<boolean> {
  return commandAvailable('rg', ['--version'])
}

function commandAvailable(command: string, args: string[]): Promise<boolean> {
  return new Promise((resolve) => {
    const child = spawn(command, args, { stdio: 'ignore', windowsHide: true })
    const timer = setTimeout(() => {
      child.kill()
      resolve(false)
    }, 1500)

    child.once('error', () => {
      clearTimeout(timer)
      resolve(false)
    })

    child.once('exit', (code) => {
      clearTimeout(timer)
      resolve(code === 0)
    })
  })
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
        mtimeMs: file.stats.mtimeMs,
        order
      }))
  } catch {
    return []
  }
}

async function searchLogFilesFromEnd(options: {
  files: LogFile[]
  keywords: string[]
  limit: number
  startedAt: number
}): Promise<RuntimeLogGrepResult> {
  const items: OrderedRuntimeLogGrepItem[] = []
  const normalizedKeywords = options.keywords.map((keyword) => keyword.toLowerCase())
  let scannedFileCount = 0
  let truncated = false
  let lastMessage: string | undefined

  for (let index = 0; index < options.files.length && items.length < options.limit; index += fileSearchConcurrency) {
    const batch = options.files.slice(index, index + fileSearchConcurrency)
    const batchResults = await Promise.all(batch.map(async (file) => {
      try {
        return await searchLogFileFromEnd(file, normalizedKeywords, options.limit)
      } catch (error) {
        lastMessage = error instanceof Error ? error.message : String(error)
        return []
      }
    }))
    scannedFileCount += batch.length

    const batchItems = batchResults.flat().sort(compareGrepItems)
    const remaining = options.limit - items.length
    if (batchItems.length > remaining) {
      truncated = true
    }

    for (const item of batchItems) {
      if (items.length >= options.limit) break
      items.push(item)
    }

    if (items.length >= options.limit && index + batch.length < options.files.length) {
      truncated = true
    }
  }

  return {
    available: true,
    mode: 'rg',
    elapsedMs: Math.round(performance.now() - options.startedAt),
    keywords: options.keywords,
    items: items.map(stripOrderFields),
    limit: options.limit,
    truncated,
    scannedFileCount,
    message: truncated ? `结果超过 ${options.limit} 行，已按最新优先截断显示` : lastMessage
  }
}

async function searchLogFileFromEnd(file: LogFile, normalizedKeywords: string[], limit: number): Promise<OrderedRuntimeLogGrepItem[]> {
  if (file.size <= 0) return []

  const items: OrderedRuntimeLogGrepItem[] = []
  const handle = await open(file.path, 'r')
  let position = file.size
  let pending = Buffer.alloc(0)
  let lineNumberFromEnd = 0
  let skippedTrailingNewline = false

  try {
    while (position > 0 && items.length < limit) {
      const chunkSize = Math.min(reverseReadChunkBytes, position)
      position -= chunkSize
      const buffer = Buffer.allocUnsafe(chunkSize)
      const { bytesRead } = await handle.read(buffer, 0, chunkSize, position)
      if (bytesRead <= 0) break

      const chunk = buffer.subarray(0, bytesRead)
      const combined = pending.length ? Buffer.concat([chunk, pending]) : chunk
      let end = combined.length

      for (let index = combined.length - 1; index >= 0; index -= 1) {
        if (combined[index] !== 10) continue

        const lineBuffer = combined.subarray(index + 1, end)
        const isTrailingNewline = !skippedTrailingNewline
          && position + bytesRead === file.size
          && end === combined.length
          && lineBuffer.length === 0

        if (isTrailingNewline) {
          skippedTrailingNewline = true
        } else {
          lineNumberFromEnd += 1
          pushMatchedLine(items, file, lineNumberFromEnd, lineBuffer, normalizedKeywords)
        }

        end = index
        if (items.length >= limit) break
      }

      pending = combined.subarray(0, end)
    }

    if (pending.length > 0 && items.length < limit) {
      lineNumberFromEnd += 1
      pushMatchedLine(items, file, lineNumberFromEnd, pending, normalizedKeywords)
    }
  } finally {
    await handle.close()
  }

  return items
}

function pushMatchedLine(
  items: OrderedRuntimeLogGrepItem[],
  file: LogFile,
  lineNumberFromEnd: number,
  lineBuffer: Buffer,
  normalizedKeywords: string[]
): void {
  const line = lineBuffer.toString('utf8').replace(/\r$/, '')
  if (!line.trim()) return
  if (!lineMatchesKeywords(line, normalizedKeywords)) return
  if (isRuntimeLogSearchRequestLine(line)) return

  items.push({
    id: `${file.path}:tail-${lineNumberFromEnd}:${items.length}`,
    file: file.path,
    fileName: file.fileName,
    lineNumberFromEnd,
    fileOrder: file.order,
    fileMtimeMs: file.mtimeMs,
    ...runtimeLogFieldsFromLine(line)
  })
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
  const leftTime = Date.parse(left.time)
  const rightTime = Date.parse(right.time)
  if (Number.isFinite(leftTime) && Number.isFinite(rightTime) && leftTime !== rightTime) {
    return rightTime - leftTime
  }
  if (left.fileOrder !== right.fileOrder) {
    return left.fileOrder - right.fileOrder
  }
  return left.lineNumberFromEnd - right.lineNumberFromEnd
}

function stripOrderFields(item: OrderedRuntimeLogGrepItem): RuntimeLogGrepItem {
  return {
    id: item.id,
    file: item.file,
    fileName: item.fileName,
    lineNumber: item.lineNumber,
    lineNumberFromEnd: item.lineNumberFromEnd,
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
  message: string,
  installSteps?: string[]
): RuntimeLogGrepResult {
  return {
    ...base,
    available: false,
    elapsedMs: Math.round(performance.now() - startedAt),
    items: [],
    truncated: false,
    scannedFileCount: 0,
    message,
    installSteps
  }
}

function ripgrepInstallSteps(): string[] {
  if (process.platform === 'win32') {
    return [
      'winget install BurntSushi.ripgrep.MSVC',
      'scoop install ripgrep',
      'choco install ripgrep',
      '安装完成后重启后端服务，并确认 rg --version 可以执行。'
    ]
  }

  return [
    'macOS: brew install ripgrep',
    'Debian / Ubuntu: sudo apt install ripgrep',
    'Fedora / RHEL: sudo dnf install ripgrep',
    'Arch Linux: sudo pacman -S ripgrep',
    '安装完成后重启后端服务，并确认 rg --version 可以执行。'
  ]
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
      time: stringValue(record.time) ?? '',
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

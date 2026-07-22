export interface RuntimeLogGrepTimeRange {
  startMs: number
  endMs: number
  startAt: string
  endAt: string
  adjusted: boolean
}

interface RuntimeLogTimeFile {
  birthtimeMs: number
  mtimeMs: number
}

interface RuntimeLogSearchableFile extends RuntimeLogTimeFile {
  size: number
}

interface RuntimeLogGrepRangeOptions {
  startAt?: string
  endAt?: string
}

export const minGrepKeywordLength = 3
export const defaultGrepRangeDays = 3
export const maxGrepRangeDays = 7

const maxKeywords = 10
const maxKeywordLength = 128
const defaultLimit = 100
const maxLimit = 100
const dayMs = 24 * 60 * 60 * 1000

export function normalizeGrepKeywords(values: string[]): { keywords: string[]; shortKeywordCount: number } {
  const seen = new Set<string>()
  const keywords: string[] = []
  let shortKeywordCount = 0
  for (const value of values) {
    for (const part of value.split(/[\s,;，；]+/)) {
      const keyword = part.trim()
      if (!keyword) continue
      const normalized = keyword.slice(0, maxKeywordLength)
      if ([...normalized].length < minGrepKeywordLength) {
        shortKeywordCount += 1
        continue
      }
      const dedupeKey = normalized.toLowerCase()
      if (seen.has(dedupeKey)) continue
      seen.add(dedupeKey)
      keywords.push(normalized)
      if (keywords.length >= maxKeywords) return { keywords, shortKeywordCount }
    }
  }
  return { keywords, shortKeywordCount }
}

export function normalizeGrepLimit(value: number | undefined): number {
  if (!Number.isFinite(value)) return defaultLimit
  return Math.min(Math.max(Math.trunc(value ?? defaultLimit), 1), maxLimit)
}

export function normalizeGrepTimeRange(
  options: RuntimeLogGrepRangeOptions,
  files: RuntimeLogTimeFile[]
): RuntimeLogGrepTimeRange {
  const nowMs = Date.now()
  const earliestFileMs = earliestLogFileMs(files)
  const latestFileMs = latestLogFileMs(files)
  let adjusted = false
  const requestedEndMs = parseTimeMs(options.endAt)
  let endMs = requestedEndMs ?? nowMs
  if (endMs > nowMs) {
    endMs = nowMs
    adjusted = true
  }

  if (requestedEndMs === undefined && latestFileMs !== undefined && endMs < latestFileMs) {
    endMs = latestFileMs
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

export function earliestLogFileMs(files: RuntimeLogTimeFile[]): number | undefined {
  const values = files.map((file) => runtimeLogFileStartMs(file)).filter(Number.isFinite)
  return values.length ? Math.min(...values) : undefined
}

function latestLogFileMs(files: RuntimeLogTimeFile[]): number | undefined {
  const values = files.map((file) => file.mtimeMs).filter(Number.isFinite)
  return values.length ? Math.max(...values) : undefined
}

export function runtimeLogFileStartMs(file: RuntimeLogTimeFile): number {
  const birthtimeMs = Number.isFinite(file.birthtimeMs) && file.birthtimeMs > 0 ? file.birthtimeMs : file.mtimeMs
  return Math.min(birthtimeMs, file.mtimeMs)
}

export function filterLogFilesByTimeRange<TFile extends RuntimeLogSearchableFile>(
  files: TFile[],
  timeRange: RuntimeLogGrepTimeRange
): TFile[] {
  return files.filter((file) => {
    if (file.size <= 0) return false
    return file.mtimeMs >= timeRange.startMs && runtimeLogFileStartMs(file) <= timeRange.endMs
  })
}

function parseTimeMs(value: string | undefined): number | undefined {
  if (!value) return undefined
  const time = Date.parse(value)
  return Number.isFinite(time) ? time : undefined
}

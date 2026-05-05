import type { RuntimeLogGrepItem } from '@/types/domain'

export function levelText(value: string): string {
  return value.toLowerCase()
}

export function levelColor(value: string): string {
  const level = value.toLowerCase()
  if (level === 'fatal' || level === 'error') return 'red'
  if (level === 'warn') return 'orange'
  if (level === 'debug' || level === 'trace') return 'blue'
  return 'green'
}

export function prettyRawJson(rawJson: string): string {
  try {
    return JSON.stringify(JSON.parse(rawJson), null, 2)
  } catch {
    return rawJson
  }
}

export function splitGrepKeywords(value: string): string[] {
  const seen = new Set<string>()
  const keywords: string[] = []
  for (const part of value.split(/[\s,;，；]+/)) {
    const keyword = part.trim()
    if (!keyword) continue
    const key = keyword.toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    keywords.push(keyword)
  }
  return keywords
}

export function grepLinePositionText(record: RuntimeLogGrepItem): string {
  return record.lineNumber ? `第 ${record.lineNumber} 行` : `倒数第 ${record.lineNumberFromEnd} 行`
}

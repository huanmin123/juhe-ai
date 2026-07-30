export function responsesFailureStatusFromCapturedJson(responseBodyText: string | undefined): boolean {
  if (!responseBodyText) return false
  try {
    const parsed = JSON.parse(responseBodyText) as unknown
    return Boolean(parsed && typeof parsed === 'object' && !Array.isArray(parsed)
      && (parsed as Record<string, unknown>).status === 'failed')
  } catch {
    return rootStatusFromJsonPrefix(responseBodyText)
  }
}

function rootStatusFromJsonPrefix(text: string): boolean {
  let depth = 0
  let index = 0
  while (index < text.length) {
    const char = text[index]
    if (char === '"') {
      const stringStart = index
      index = readJsonString(text, index)
      if (depth === 1) {
        let cursor = index
        while (/\s/u.test(text[cursor] ?? '')) cursor += 1
        if (text[cursor] === ':') {
          cursor += 1
          while (/\s/u.test(text[cursor] ?? '')) cursor += 1
          if (text.slice(stringStart, index) === '"status"'
            && text.slice(cursor, cursor + 8) === '"failed"') {
            return true
          }
        }
      }
      continue
    }
    if (char === '{') depth += 1
    else if (char === '}') depth = Math.max(0, depth - 1)
    index += 1
  }
  return false
}

function readJsonString(text: string, start: number): number {
  let index = start + 1
  while (index < text.length) {
    if (text[index] === '\\') {
      index += 2
      continue
    }
    if (text[index] === '"') return index + 1
    index += 1
  }
  return text.length
}

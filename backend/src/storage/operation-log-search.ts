const operationLogSearchMinTermLength = 1
const operationLogSearchMaxTermLength = 128
const operationLogSearchMaxFieldChars = 256
const operationLogSearchMaxTermsPerLog = 1500

export function operationLogSearchTermFromKeyword(value?: string): string | undefined {
  const normalized = normalizeOperationLogSearchText(value)
  if (!normalized) return undefined
  if (normalized.length >= operationLogSearchMinTermLength && normalized.length <= operationLogSearchMaxTermLength) {
    return normalized
  }

  const compact = compactOperationLogSearchText(normalized)
  if (compact.length >= operationLogSearchMinTermLength && compact.length <= operationLogSearchMaxTermLength) {
    return compact
  }
  return undefined
}

export function buildOperationLogSearchTerms(summary: unknown): string[] {
  const terms = new Set<string>()
  collectOperationLogSearchTerms(terms, summary)
  return [...terms]
}

function collectOperationLogSearchTerms(terms: Set<string>, value: unknown): void {
  const normalized = normalizeOperationLogSearchText(value)
  if (!normalized) return

  const compact = compactOperationLogSearchText(normalized)
  addOperationLogSearchExactTerm(terms, normalized)
  if (compact !== normalized) {
    addOperationLogSearchExactTerm(terms, compact)
  }
  for (const part of normalized.split(' ')) {
    addOperationLogSearchExactTerm(terms, part)
  }
  if (terms.size >= operationLogSearchMaxTermsPerLog) return

  addOperationLogSearchSubstrings(terms, normalized)
  if (terms.size >= operationLogSearchMaxTermsPerLog) return

  for (const part of normalized.split(' ')) {
    addOperationLogSearchSubstrings(terms, part)
    if (terms.size >= operationLogSearchMaxTermsPerLog) return
  }

  if (compact !== normalized) {
    addOperationLogSearchSubstrings(terms, compact)
  }
}

function addOperationLogSearchExactTerm(terms: Set<string>, value: string): void {
  const term = value.trim()
  if (term.length >= operationLogSearchMinTermLength && term.length <= operationLogSearchMaxTermLength) {
    terms.add(term)
  }
}

function addOperationLogSearchSubstrings(terms: Set<string>, value: string): void {
  if (terms.size >= operationLogSearchMaxTermsPerLog) return
  const chars = [...value].slice(0, operationLogSearchMaxFieldChars)
  const maxLength = Math.min(operationLogSearchMaxTermLength, chars.length)
  for (let length = operationLogSearchMinTermLength; length <= maxLength; length += 1) {
    for (let start = 0; start + length <= chars.length; start += 1) {
      const term = chars.slice(start, start + length).join('').trim()
      if (term.length >= operationLogSearchMinTermLength) {
        terms.add(term)
        if (terms.size >= operationLogSearchMaxTermsPerLog) return
      }
    }
  }
}

function normalizeOperationLogSearchText(value: unknown): string {
  if (typeof value !== 'string') return ''
  return value
    .normalize('NFKC')
    .toLowerCase()
    .replace(/[^\p{L}\p{N}]+/gu, ' ')
    .replace(/\s+/g, ' ')
    .trim()
}

function compactOperationLogSearchText(value: string): string {
  return value.replace(/\s+/g, '')
}

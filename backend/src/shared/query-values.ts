export function firstQueryValue(value: unknown): unknown {
  return Array.isArray(value) ? value[0] : value
}

export function optionalQueryText(value: unknown): string | undefined {
  const text = firstQueryValue(value)
  return typeof text === 'string' && text.trim() ? text.trim() : undefined
}

export function integerQueryValue(value: unknown): number | undefined {
  const text = firstQueryValue(value)
  if (typeof text === 'string') {
    const trimmed = text.trim()
    if (!trimmed) return undefined
    const number = Number(trimmed)
    return Number.isInteger(number) ? number : undefined
  }
  return typeof text === 'number' && Number.isInteger(text) ? text : undefined
}

export function finiteNumberQueryValue(value: unknown): number | undefined {
  const text = firstQueryValue(value)
  if (typeof text === 'string') {
    const trimmed = text.trim()
    if (!trimmed) return undefined
    const number = Number(trimmed)
    return Number.isFinite(number) ? number : undefined
  }
  return typeof text === 'number' && Number.isFinite(text) ? text : undefined
}

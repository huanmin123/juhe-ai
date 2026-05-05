export function sqlPlaceholders(count: number): string {
  return Array.from({ length: Math.max(1, count) }, () => '?').join(',')
}

export function chunkValues<T>(values: T[], chunkSize: number): T[][] {
  const chunks: T[][] = []
  const size = Math.max(1, Math.trunc(chunkSize))
  for (let index = 0; index < values.length; index += size) {
    chunks.push(values.slice(index, index + size))
  }
  return chunks
}

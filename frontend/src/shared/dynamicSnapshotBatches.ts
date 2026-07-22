export interface DynamicSnapshotBatchResult<T> {
  ids: string[]
  value?: T
  error?: unknown
}

interface DynamicSnapshotBatchOptions {
  batchSize?: number
  concurrency?: number
}

export async function loadDynamicSnapshotsInBatches<T>(
  inputIds: string[],
  request: (ids: string[]) => Promise<T>,
  options: DynamicSnapshotBatchOptions = {}
): Promise<Array<DynamicSnapshotBatchResult<T>>> {
  const ids = uniqueSnapshotIds(inputIds)
  if (!ids.length) return []

  const batchSize = boundedPositiveInteger(options.batchSize, 100, 100)
  const workerLimit = boundedPositiveInteger(options.concurrency, 2, 2)
  const batches: string[][] = []
  for (let offset = 0; offset < ids.length; offset += batchSize) {
    batches.push(ids.slice(offset, offset + batchSize))
  }

  const results = new Array<DynamicSnapshotBatchResult<T>>(batches.length)
  let nextBatchIndex = 0
  const worker = async () => {
    while (nextBatchIndex < batches.length) {
      const batchIndex = nextBatchIndex
      nextBatchIndex += 1
      const batchIds = batches[batchIndex]
      try {
        results[batchIndex] = { ids: batchIds, value: await request(batchIds) }
      } catch (error) {
        results[batchIndex] = { ids: batchIds, error }
      }
    }
  }
  await Promise.all(Array.from({ length: Math.min(workerLimit, batches.length) }, worker))
  return results
}

function uniqueSnapshotIds(inputIds: string[]): string[] {
  const seen = new Set<string>()
  const ids: string[] = []
  for (const rawId of inputIds) {
    const id = rawId.trim()
    if (!id || seen.has(id)) continue
    seen.add(id)
    ids.push(id)
  }
  return ids
}

function boundedPositiveInteger(value: number | undefined, fallback: number, maximum: number): number {
  if (!Number.isFinite(value)) return fallback
  return Math.max(1, Math.min(maximum, Math.trunc(value ?? fallback)))
}

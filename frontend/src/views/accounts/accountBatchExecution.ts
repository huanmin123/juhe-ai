export const accountBatchConcurrency = 5

export const accountBatchTestChunkSize = 10

export async function runWithConcurrency<TItem, TResult>(
  items: TItem[],
  concurrency: number,
  task: (item: TItem) => Promise<TResult>
): Promise<Array<PromiseSettledResult<TResult>>> {
  const results: Array<PromiseSettledResult<TResult>> = new Array(items.length)
  let nextIndex = 0
  const workerCount = Math.min(Math.max(1, concurrency), items.length)
  async function runWorker(): Promise<void> {
    while (nextIndex < items.length) {
      const index = nextIndex
      nextIndex += 1
      try {
        results[index] = { status: 'fulfilled', value: await task(items[index]) }
      } catch (reason) {
        results[index] = { status: 'rejected', reason }
      }
    }
  }
  await Promise.all(Array.from({ length: workerCount }, runWorker))
  return results
}

export async function runInFixedBatches<TItem>(
  items: TItem[],
  batchSize: number,
  task: (item: TItem, index: number) => Promise<void>,
  signal: AbortSignal
): Promise<void> {
  const size = Math.max(1, Math.trunc(batchSize))
  for (let startIndex = 0; startIndex < items.length && !signal.aborted; startIndex += size) {
    const batch = items.slice(startIndex, startIndex + size)
    await Promise.all(batch.map((item, offset) => task(item, startIndex + offset)))
  }
}

import { chunkValues } from './query-utils.js'

export async function loadSharedCacheEntriesByBatches<T>(
  values: string[],
  load: (id: string) => Promise<T | undefined>
): Promise<Array<readonly [string, T | undefined]>> {
  const ids = [...new Set(values)].filter(Boolean)
  const result: Array<readonly [string, T | undefined]> = []
  for (const chunk of chunkValues(ids, 100)) {
    result.push(...await Promise.all(chunk.map(async (id) => [id, await load(id)] as const)))
  }
  return result
}

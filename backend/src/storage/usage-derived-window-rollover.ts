export const derivedWindowRolloverSeedPageSize = 256
export const derivedWindowRolloverSeedMaxPages = 8

interface DerivedWindowRolloverSeedRow {
  system_account_id: string
}

interface RunDerivedWindowRolloverSeedPagesOptions<T extends DerivedWindowRolloverSeedRow> {
  cursor: string
  loadPage: (cursor: string, pageSize: number) => Promise<T[]>
  seedPage: (rows: T[]) => Promise<void>
}

export interface DerivedWindowRolloverSeedProgress {
  nextCursor: string
  pageCount: number
  rowCount: number
  done: boolean
}

export async function runDerivedWindowRolloverSeedPages<T extends DerivedWindowRolloverSeedRow>(
  options: RunDerivedWindowRolloverSeedPagesOptions<T>
): Promise<DerivedWindowRolloverSeedProgress> {
  let nextCursor = options.cursor
  let rowCount = 0

  for (let pageCount = 1; pageCount <= derivedWindowRolloverSeedMaxPages; pageCount += 1) {
    const rows = await options.loadPage(nextCursor, derivedWindowRolloverSeedPageSize)
    if (rows.length > derivedWindowRolloverSeedPageSize) {
      throw new Error(`派生窗口 rollover seed 单页超过预算: ${rows.length}`)
    }
    if (rows.length > 0) {
      const lastSystemAccountId = rows.at(-1)?.system_account_id ?? ''
      if (!lastSystemAccountId || lastSystemAccountId <= nextCursor) {
        throw new Error('派生窗口 rollover seed keyset 游标未前进')
      }
      await options.seedPage(rows)
      nextCursor = lastSystemAccountId
      rowCount += rows.length
    }
    if (rows.length < derivedWindowRolloverSeedPageSize) {
      return { nextCursor: '__done__', pageCount, rowCount, done: true }
    }
  }

  return {
    nextCursor,
    pageCount: derivedWindowRolloverSeedMaxPages,
    rowCount,
    done: false
  }
}

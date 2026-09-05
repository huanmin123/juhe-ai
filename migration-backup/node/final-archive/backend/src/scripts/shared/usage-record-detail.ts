import assert from 'node:assert/strict'

import type { AccessScope } from '../../storage/access-scope.js'
import type { UsageRecordListItem, UsageRecordSummary } from '../../storage/repositories.js'

interface UsageRecordDetailReader {
  getUsageRecordDetail(id: string, access?: AccessScope): UsageRecordSummary | undefined
}

interface AsyncUsageRecordDetailReader {
  getUsageRecordDetailAsync(id: string, access?: AccessScope): Promise<UsageRecordSummary | undefined>
}

export function requireUsageRecordDetail(
  reader: UsageRecordDetailReader,
  item: Pick<UsageRecordListItem, 'id'>,
  access?: AccessScope
): UsageRecordSummary {
  const detail = reader.getUsageRecordDetail(item.id, access)
  assert(detail, `未找到使用记录详情：${item.id}`)
  return detail
}

export function requireUsageRecordDetails(
  reader: UsageRecordDetailReader,
  items: readonly Pick<UsageRecordListItem, 'id'>[],
  access?: AccessScope
): UsageRecordSummary[] {
  return items.map((item) => requireUsageRecordDetail(reader, item, access))
}

export async function requireUsageRecordDetailAsync(
  reader: AsyncUsageRecordDetailReader,
  item: Pick<UsageRecordListItem, 'id'>,
  access?: AccessScope
): Promise<UsageRecordSummary> {
  const detail = await reader.getUsageRecordDetailAsync(item.id, access)
  assert(detail, `未找到使用记录详情：${item.id}`)
  return detail
}

export async function requireUsageRecordDetailsAsync(
  reader: AsyncUsageRecordDetailReader,
  items: readonly Pick<UsageRecordListItem, 'id'>[],
  access?: AccessScope
): Promise<UsageRecordSummary[]> {
  return Promise.all(items.map((item) => requireUsageRecordDetailAsync(reader, item, access)))
}

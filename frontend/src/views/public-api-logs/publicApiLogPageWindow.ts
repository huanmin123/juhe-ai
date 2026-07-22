import type { PublicApiLogListItem } from '@/types/domain'

export function mergePublicApiLogListItems(
  current: PublicApiLogListItem[],
  incoming: PublicApiLogListItem[]
): PublicApiLogListItem[] {
  const seen = new Set<string>()
  const merged: PublicApiLogListItem[] = []
  for (const item of [...current, ...incoming]) {
    if (seen.has(item.id)) continue
    seen.add(item.id)
    merged.push(item)
  }
  return merged
}

import type { GroupOptionSummary } from '@/types/domain'
import {
  mergeSelectedSelectOptions,
  rememberSelectOption,
  selectLabelForValue,
  type SelectOption
} from './selectLabelCache'

export type { SelectOption } from './selectLabelCache'

export interface GroupSelection {
  id: string
  name: string
}

const groupCacheKey = 'groups'

export function rememberGroupLabels(groups: Array<Pick<GroupOptionSummary, 'id' | 'name'>>): void {
  for (const group of groups) {
    rememberGroupLabel(group.id, group.name)
  }
}

export function rememberGroupLabel(id: string | undefined, name: string | undefined): void {
  rememberSelectOption(groupCacheKey, id, name)
}

export function rememberGroupSelection(selection: GroupSelection | undefined): void {
  rememberGroupLabel(selection?.id, selection?.name)
}

export function rememberGroupSelections(selections: Array<GroupSelection | undefined>): void {
  for (const selection of selections) {
    rememberGroupSelection(selection)
  }
}

export function groupLabelForId(id: string | undefined): string | undefined {
  return selectLabelForValue(groupCacheKey, id)
}

export function groupSelectionForId(
  id: string | undefined,
  groups: Array<Pick<GroupOptionSummary, 'id' | 'name'>> = [],
  options: SelectOption[] = []
): GroupSelection | undefined {
  const normalizedId = id?.trim()
  if (!normalizedId) return undefined
  const group = groups.find((item) => item.id === normalizedId)
  if (group?.name?.trim()) return { id: normalizedId, name: group.name.trim() }
  const option = options.find((item) => item.value === normalizedId)
  if (option?.label?.trim()) return { id: normalizedId, name: option.label.trim() }
  const cached = groupLabelForId(normalizedId)
  return cached ? { id: normalizedId, name: cached } : undefined
}

export function displayGroupName(name: string | undefined, id: string | undefined, fallback = '已删除或未知'): string {
  if (name?.trim()) return name
  const cached = groupLabelForId(id)
  if (cached) return cached
  return id ? fallback : '-'
}

export function groupSelectOptionLabel(group: GroupOptionSummary): string {
  if (group.accessType !== 'authorized') return group.name
  return `${group.name}（来自 ${group.ownerSystemAccountName || '其他用户'} 授权）`
}

export function mergeSelectedGroupOptions(
  options: SelectOption[],
  selectedIds: Array<string | undefined>,
  selectedGroups: Array<GroupSelection | undefined> = []
): SelectOption[] {
  return mergeSelectedSelectOptions(
    groupCacheKey,
    options,
    selectedIds,
    selectedGroups.map((group) => group ? { label: group.name, value: group.id } : undefined)
  )
}

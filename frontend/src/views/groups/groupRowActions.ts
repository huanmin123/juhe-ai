import type { RowActionItem } from '@/components/rowActions'
import type { GroupSummary } from '@/types/domain'

export function isAuthorizedGroup(group: GroupSummary): boolean {
  return group.accessType === 'authorized'
}

export function canEditGroup(group: GroupSummary): boolean {
  return group.canEdit ?? (!group.isDefault && group.permissions?.canEdit !== false)
}

export function canDeleteGroup(group: GroupSummary): boolean {
  return group.canDelete ?? (!group.isDefault && group.permissions?.canDelete !== false)
}

export function canReturnAuthorizedGroup(group: GroupSummary): boolean {
  return group.canReturn ?? (isAuthorizedGroup(group) && group.permissions?.canReturnAuthorization === true)
}

export function groupRowActions(group: GroupSummary): RowActionItem[] {
  const actions: RowActionItem[] = []
  if (isAuthorizedGroup(group)) {
    if (canEditGroup(group)) {
      actions.push({ key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' })
    }
    if (canReturnAuthorizedGroup(group)) {
      actions.push(returnAuthorizedGroupAction(group))
    }
    return actions
  }
  if (canDeleteGroup(group)) {
    actions.push(deleteGroupAction(group))
  }
  return actions
}

export function groupMoreActions(group: GroupSummary): RowActionItem[] {
  if (isAuthorizedGroup(group)) return []
  const actions: RowActionItem[] = []
  if (canEditGroup(group)) {
    actions.push({ key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' })
  }
  return actions
}

function deleteGroupAction(group: GroupSummary): RowActionItem {
  return {
    key: 'delete',
    label: '删除',
    icon: 'delete',
    tone: 'danger',
    confirmTitle: `确认删除分组 ${group.name}？`,
    confirmOkText: '删除'
  }
}

function returnAuthorizedGroupAction(group: GroupSummary): RowActionItem {
  return {
    key: 'return-authorization',
    label: '归还',
    icon: 'revoke',
    tone: 'danger',
    confirmTitle: `确认归还授权分组「${group.name}」？归还后你将不再看到或使用它，不影响授权方原分组。`,
    confirmOkText: '归还'
  }
}

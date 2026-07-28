import type { RowActionItem } from '@/components/rowActions'
import type { GroupListItem, GroupSummary } from '@/types/domain'

type GroupRow = GroupListItem | GroupSummary

export function isAuthorizedGroup(group: GroupRow): boolean {
  return group.accessType === 'authorized'
}

export function canEditGroup(group: GroupRow): boolean {
  return group.canEdit ?? (!group.isDefault && ('permissions' in group ? group.permissions?.canEdit !== false : true))
}

export function canDeleteGroup(group: GroupRow): boolean {
  return group.canDelete ?? (!group.isDefault && ('permissions' in group ? group.permissions?.canDelete !== false : true))
}

export function canReturnAuthorizedGroup(group: GroupRow): boolean {
  return group.canReturn ?? (isAuthorizedGroup(group) && ('permissions' in group && group.permissions?.canReturnAuthorization === true))
}

export function groupRowActions(group: GroupRow): RowActionItem[] {
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

export function groupMoreActions(group: GroupRow): RowActionItem[] {
  if (isAuthorizedGroup(group)) return []
  const actions: RowActionItem[] = []
  if (canEditGroup(group)) {
    actions.push({ key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' })
  }
  return actions
}

function deleteGroupAction(group: GroupRow): RowActionItem {
  return {
    key: 'delete',
    label: '删除',
    icon: 'delete',
    tone: 'danger',
    confirmTitle: `确认删除分组 ${group.name}？`,
    confirmOkText: '删除'
  }
}

function returnAuthorizedGroupAction(group: GroupRow): RowActionItem {
  return {
    key: 'return-authorization',
    label: '归还',
    icon: 'revoke',
    tone: 'danger',
    confirmTitle: `确认归还授权分组「${group.name}」？归还后你将不再看到或使用它，不影响授权方原分组。`,
    confirmOkText: '归还'
  }
}

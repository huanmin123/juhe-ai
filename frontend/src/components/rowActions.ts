export type RowActionTone = 'default' | 'primary' | 'success' | 'warning' | 'info' | 'purple' | 'danger'

export type RowActionIcon =
  | 'bind'
  | 'copy'
  | 'delete'
  | 'detail'
  | 'disable'
  | 'edit'
  | 'enable'
  | 'fallback'
  | 'members'
  | 'migrate'
  | 'more'
  | 'password'
  | 'pause'
  | 'refresh'
  | 'reset'
  | 'restore'
  | 'resume'
  | 'revoke'
  | 'settings'
  | 'stop'
  | 'superPriority'
  | 'test'
  | 'view'

export interface RowActionItem {
  key: string
  label: string
  icon?: RowActionIcon
  tone?: RowActionTone
  danger?: boolean
  disabled?: boolean
  confirmTitle?: string
  confirmOkText?: string
  children?: RowActionItem[]
}

const rowActionButtonWidth = 28
const rowActionGap = 2
const rowActionColumnPadding = 16
const rowActionColumnMinWidth = 56

export function rowActionVisibleSlotCount(actions: readonly RowActionItem[] = [], moreActions: readonly RowActionItem[] = []): number {
  if (moreActions.length === 1 && !moreActions[0]?.children?.length) return actions.length + 1
  return actions.length + (moreActions.length ? 1 : 0)
}

export function rowActionColumnWidth(actionCount = 3): number {
  const count = Number.isFinite(actionCount) ? Math.max(1, Math.trunc(actionCount)) : 3
  return Math.max(rowActionColumnMinWidth, rowActionColumnPadding + count * rowActionButtonWidth + Math.max(0, count - 1) * rowActionGap)
}

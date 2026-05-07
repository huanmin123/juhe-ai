export type RowActionTone = 'default' | 'primary' | 'success' | 'warning' | 'info' | 'purple' | 'danger'

export type RowActionIcon =
  | 'bind'
  | 'copy'
  | 'delete'
  | 'detail'
  | 'disable'
  | 'edit'
  | 'enable'
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

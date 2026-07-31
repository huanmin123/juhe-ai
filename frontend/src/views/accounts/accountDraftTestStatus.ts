import type { AccountFormModel } from './accountFormTypes'

export function statusAfterDraftTestSuccess(status: AccountFormModel['status']): AccountFormModel['status'] {
  return status === 'disabled' ? 'disabled' : 'active'
}

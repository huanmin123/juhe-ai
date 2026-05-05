import type { Dayjs } from 'dayjs'

export interface AuthorizationCreateFormModel {
  resourceType: 'account' | 'group'
  resourceId: string
  granteeType: 'system_account' | 'team'
  granteeId: string
  remark: string
  expiresAt?: Dayjs
}

export interface AuthorizationExpireFormModel {
  expiresAt?: Dayjs
}

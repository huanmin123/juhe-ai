import type { Dayjs } from 'dayjs'
import type { RequestQuotaFormModel } from '../shared/requestQuotaForm'

export interface AuthorizationCreateFormModel {
  ownerSystemAccountId?: string
  resourceType: 'account' | 'group'
  resourceId: string
  granteeType: 'system_account' | 'team'
  granteeId: string
  remark: string
  expiresAt?: Dayjs
  quotaLimits: RequestQuotaFormModel
}

export interface AuthorizationExpireFormModel {
  expiresAt?: Dayjs
  quotaLimits: RequestQuotaFormModel
}

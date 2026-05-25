import type { Dayjs } from 'dayjs'
import type { AccountSelection } from '@/shared/accountLabelCache'
import type { GroupSelection } from '@/shared/groupLabelCache'
import type { RequestQuotaFormModel } from '../shared/requestQuotaForm'

export interface AuthorizationCreateFormModel {
  ownerSystemAccountId?: string
  resourceType: 'account' | 'group'
  resourceId: string
  resourceAccount?: AccountSelection
  resourceGroup?: GroupSelection
  granteeType: 'system_account' | 'team'
  granteeId: string
  targetGroupId: string
  targetGroup?: GroupSelection
  remark: string
  expiresAt?: Dayjs
  quotaLimits: RequestQuotaFormModel
}

export interface AuthorizationExpireFormModel {
  expiresAt?: Dayjs
  quotaLimits: RequestQuotaFormModel
}

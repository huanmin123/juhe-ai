import type { RequestQuotaLimits, ResourceAuthorizationListItem } from '@/types/domain'
import { createQuotaLimitForm, quotaLimitsPayload } from '../shared/requestQuotaForm'
import { formatServerDateTimeInput, parseStrictDatePickerValue } from './authorizationFormatters'
import type { AuthorizationCreateFormModel, AuthorizationExpireFormModel } from './authorizationFormTypes'

export type { AuthorizationCreateFormModel, AuthorizationExpireFormModel } from './authorizationFormTypes'

export interface AuthorizationCreateFormDefaults {
  ownerSystemAccountId?: string
  resourceType?: 'account' | 'group'
}

export function createAuthorizationCreateFormModel(defaults: AuthorizationCreateFormDefaults = {}): AuthorizationCreateFormModel {
  return {
    ownerSystemAccountId: defaults.ownerSystemAccountId,
    resourceType: defaults.resourceType ?? 'account',
    resourceId: '',
    resourceAccount: undefined,
    resourceGroup: undefined,
    granteeType: 'system_account',
    granteeId: '',
    targetGroupId: '',
    targetGroup: undefined,
    remark: '',
    expiresAt: undefined,
    quotaLimits: createQuotaLimitForm()
  }
}

export function resetAuthorizationCreateForm(form: AuthorizationCreateFormModel, defaults: AuthorizationCreateFormDefaults = {}): void {
  Object.assign(form, createAuthorizationCreateFormModel(defaults))
}

export function createAuthorizationExpireFormModel(): AuthorizationExpireFormModel {
  return {
    expiresAt: undefined,
    quotaLimits: createQuotaLimitForm()
  }
}

export function authorizationExpireFormFromSummary(item: Pick<ResourceAuthorizationListItem, 'expiresAt' | 'limits'>): AuthorizationExpireFormModel {
  return {
    expiresAt: parseStrictDatePickerValue(item.expiresAt, '授权过期时间'),
    quotaLimits: createQuotaLimitForm(item.limits)
  }
}

export function authorizationCreatePayload(form: AuthorizationCreateFormModel, includeTargetGroup: boolean) {
  return {
    resourceType: form.resourceType,
    resourceId: form.resourceId,
    granteeType: form.granteeType,
    granteeId: form.granteeId,
    targetGroupId: includeTargetGroup ? form.targetGroupId : undefined,
    remark: form.remark.trim() || undefined,
    expiresAt: formatServerDateTimeInput(form.expiresAt) ?? undefined,
    limits: quotaLimitsPayload(form.quotaLimits)
  }
}

export interface AuthorizationExpireBaseline {
  expiresAt: string | null
  limits: RequestQuotaLimits
}

export function authorizationExpireBaseline(item: Pick<ResourceAuthorizationListItem, 'expiresAt' | 'limits'>): AuthorizationExpireBaseline {
  return {
    expiresAt: item.expiresAt ?? null,
    limits: item.limits ?? {}
  }
}

export function authorizationExpirePayload(form: AuthorizationExpireFormModel, baseline: AuthorizationExpireBaseline) {
  const expiresAt = formatServerDateTimeInput(form.expiresAt)
  const limits = quotaLimitsPayload(form.quotaLimits)
  return {
    ...(expiresAt !== baseline.expiresAt ? { expiresAt } : {}),
    ...(JSON.stringify(limits) !== JSON.stringify(baseline.limits) ? { limits } : {})
  }
}

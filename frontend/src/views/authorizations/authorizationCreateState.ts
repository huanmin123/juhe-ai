import { mergeSelectedGroupOptions, type SelectOption } from '@/shared/groupLabelCache'
import type {
  AccountOptionSummary,
  GroupOptionSummary,
  SystemAccountPrincipalSummary,
  SystemTeamPrincipalSummary
} from '@/types/domain'
import type { AuthorizationCreateFormModel } from './authorizationFormModel'

type CreateResourceForm = Pick<AuthorizationCreateFormModel, 'resourceType' | 'resourceId' | 'resourceGroup'>

export function authorizationCreateExcludedGranteeIds(ownerSystemAccountId: string | undefined): string[] {
  return ownerSystemAccountId ? [ownerSystemAccountId] : []
}

export function authorizationCreateResourceOptions(options: {
  form: CreateResourceForm
  accounts: Array<Pick<AccountOptionSummary, 'id' | 'name'>>
  groups: Array<Pick<GroupOptionSummary, 'id' | 'name'>>
}): SelectOption[] {
  if (options.form.resourceType === 'account') {
    return options.accounts.map((account) => ({ label: account.name, value: account.id }))
  }
  return mergeSelectedGroupOptions(
    options.groups.map((group) => ({ label: group.name, value: group.id })),
    [options.form.resourceId],
    [options.form.resourceGroup]
  )
}

export function authorizationCreateResourceSelectDisabled(options: {
  isManagementView: boolean
  ownerSystemAccountId: string | undefined
}): boolean {
  return options.isManagementView && !options.ownerSystemAccountId
}

export function authorizationCreateResourcePlaceholder(options: {
  isManagementView: boolean
  ownerSystemAccountId: string | undefined
  resourceType: AuthorizationCreateFormModel['resourceType']
}): string {
  if (options.isManagementView && !options.ownerSystemAccountId) return '请先选择授权人'
  if (options.resourceType === 'account') return '输入 AI 账户名称搜索'
  return '输入分组名称搜索'
}

export function authorizationCreateHasGranteeOptions(options: {
  granteeType: AuthorizationCreateFormModel['granteeType']
  users: Array<Pick<SystemAccountPrincipalSummary, 'id' | 'status'>>
  teams: Array<Pick<SystemTeamPrincipalSummary, 'status'>>
  excludedGranteeIds: string[]
}): boolean {
  if (options.granteeType === 'system_account') {
    return options.users.some((user) => user.status === 'active' && !options.excludedGranteeIds.includes(user.id))
  }
  return options.teams.some((team) => team.status === 'active')
}

export function authorizationCreateTargetGroupDisabled(options: {
  resourceId: string
  granteeId: string
  selectedAccountProviderCode: string | undefined
}): boolean {
  return !options.resourceId || !options.granteeId || !options.selectedAccountProviderCode
}

export function authorizationCreateTargetGroupPlaceholder(options: {
  resourceId: string
  granteeId: string
}): string {
  if (!options.resourceId) return '请先选择 AI 账户'
  if (!options.granteeId) return '请先选择被授权用户'
  return '选择目标用户分组'
}

export function authorizationCreateTargetGroupTip(targetGroupCount: number): string {
  return targetGroupCount
    ? '默认选择目标用户的默认分组；授权创建后会直接把账户加入该分组。'
    : '目标用户暂无可选兼容分组，请先为目标用户准备分组。'
}

import type { ComputedRef, Ref } from 'vue'
import dayjs from 'dayjs'

import { api } from '@/api/client'
import { useSubmitAction } from '@/composables/useSubmitAction'
import { message } from '@/lib/antd'
import { serverDateTimeTimestamp } from '@/shared/formatters'
import type { AccountOptionSummary, GroupOptionSummary, ResourceAuthorizationSummary, SystemAccountPrincipalSummary, SystemTeamPrincipalSummary } from '@/types/domain'
import {
  extractApiErrorMessage,
  formatDateTime,
  parseStrictDatePickerValue
} from './authorizationFormatters'
import {
  authorizationCreatePayload,
  authorizationExpireFormFromSummary,
  authorizationExpirePayload,
  type AuthorizationCreateFormModel,
  type AuthorizationExpireFormModel
} from './authorizationFormModel'

interface UseAuthorizationActionsOptions {
  createAuthorizationScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  createExcludedGranteeIds: ComputedRef<string[]>
  createForm: AuthorizationCreateFormModel
  createModalOpen: Ref<boolean>
  createOwnedGroups: ComputedRef<GroupOptionSummary[]>
  createTargetGroupVisible: ComputedRef<boolean>
  createTeams: Ref<SystemTeamPrincipalSummary[]>
  createUsers: Ref<SystemAccountPrincipalSummary[]>
  expireAuthorization: Ref<ResourceAuthorizationSummary | undefined>
  expireForm: AuthorizationExpireFormModel
  expireModalOpen: Ref<boolean>
  isManagementView: ComputedRef<boolean>
  loadData: (options?: { quiet?: boolean }) => Promise<boolean>
  removeAuthorizationItems: (predicate: (item: ResourceAuthorizationSummary) => boolean) => number
  selectedCreateAccount: ComputedRef<AccountOptionSummary | undefined>
  updateAuthorizationItems: (
    predicate: (item: ResourceAuthorizationSummary) => boolean,
    updater: (item: ResourceAuthorizationSummary) => ResourceAuthorizationSummary
  ) => number
}

export function useAuthorizationActions(options: UseAuthorizationActionsOptions) {
  const {
    createAuthorizationScopeParams,
    createExcludedGranteeIds,
    createForm,
    createModalOpen,
    createOwnedGroups,
    createTargetGroupVisible,
    createTeams,
    createUsers,
    expireAuthorization,
    expireForm,
    expireModalOpen,
    isManagementView,
    loadData,
    removeAuthorizationItems,
    selectedCreateAccount,
    updateAuthorizationItems
  } = options
  const { submitAction, submittingRef } = useSubmitAction('authorizations')
  const authorizationCreating = submittingRef('authorizations.create')

  const createAuthorization = submitAction('authorizations.create', async () => {
    if (isManagementView.value && !createForm.ownerSystemAccountId) {
      message.warning('请先选择授权人')
      return
    }
    if (!createForm.resourceId) {
      message.warning(createForm.resourceType === 'account' ? '请选择要授权的 AI 账户' : '请选择要授权的分组')
      return
    }
    if (!createForm.granteeId) {
      message.warning(createForm.granteeType === 'system_account' ? '请选择被授权用户' : '请选择团队')
      return
    }
    if (createForm.granteeType === 'system_account' && !createUsers.value.some((user) => user.id === createForm.granteeId && user.status === 'active')) {
      message.warning('请选择启用中的系统账户')
      return
    }
    if (createForm.granteeType === 'system_account' && createExcludedGranteeIds.value.includes(createForm.granteeId)) {
      message.warning('不能授权给资源所有者自己')
      return
    }
    if (createForm.granteeType === 'team' && !createTeams.value.some((team) => team.id === createForm.granteeId && team.status === 'active')) {
      message.warning('请选择启用中的团队')
      return
    }
    const selectedResource = createForm.resourceType === 'account'
      ? selectedCreateAccount.value
      : createOwnedGroups.value.find((group) => group.id === createForm.resourceId)
    if (!selectedResource) {
      message.warning('只能授权自己拥有的资源')
      return
    }
    if (!validateAuthorizationExpiresAt(createForm.expiresAt, selectedCreateAccount.value?.accountExpiresAt)) {
      return
    }
    if (createTargetGroupVisible.value && !createForm.targetGroupId) {
      message.warning('请选择目标分组')
      return
    }
    try {
      const payload = authorizationCreatePayload(createForm, createTargetGroupVisible.value)
      if (isManagementView.value) {
        await api.authorizations.create(payload, createAuthorizationScopeParams.value)
      } else {
        await api.myAuthorizations.create(payload)
      }
      createModalOpen.value = false
      message.success(createForm.granteeType === 'team' ? '团队授权已创建' : '授权已创建')
      await loadData()
    } catch (error) {
      console.error(error)
      message.error(extractApiErrorMessage(error, '创建授权失败'))
    }
  })

  async function revokeManualSource(item: ResourceAuthorizationSummary) {
    await revokeWithMessage(item, '个人授权来源已回收', '回收个人授权失败')
  }

  async function revokeTeamSource(item: ResourceAuthorizationSummary) {
    await revokeWithMessage(item, '团队授权来源已回收', '回收团队授权失败')
  }

  async function revokeAuthorization(item: ResourceAuthorizationSummary) {
    await revokeWithMessage(
      item,
      item.granteeType === 'team' ? '团队授权已回收' : '授权已回收',
      item.granteeType === 'team' ? '回收团队授权失败' : '回收授权失败'
    )
  }

  async function revokeWithMessage(item: ResourceAuthorizationSummary, successMessage: string, errorMessage: string) {
    try {
      const updated = isManagementView.value
        ? await api.authorizations.revoke(item.id, authorizationOperationScopeParams(item))
        : await api.myAuthorizations.revoke(item.id)
      updateAuthorizationItems((authorization) => authorization.id === item.id, () => updated)
      message.success(successMessage)
      void loadData({ quiet: true })
    } catch (error) {
      console.error(error)
      message.error(extractApiErrorMessage(error, errorMessage))
    }
  }

  async function returnAuthorization(item: ResourceAuthorizationSummary) {
    try {
      await api.myAuthorizations.returnAuthorization(item.id)
      removeAuthorizationItems((authorization) => authorization.id === item.id)
      message.success('授权已归还')
      void loadData({ quiet: true })
    } catch (error) {
      console.error(error)
      message.error(extractApiErrorMessage(error, '归还授权失败'))
    }
  }

  function handleActionMenuClick(event: { key: string | number }, item: ResourceAuthorizationSummary) {
    const key = String(event.key)
    if (key === 'return') {
      void returnAuthorization(item)
      return
    }
    if (key === 'edit-expire') {
      openExpireModal(item)
      return
    }
    if (key === 'pause') {
      void updateAuthorizationStatus(item, 'paused')
      return
    }
    if (key === 'resume') {
      void updateAuthorizationStatus(item, 'active')
      return
    }
    if (key === 'revoke-authorization') {
      void revokeAuthorization(item)
      return
    }
    if (key === 'revoke-manual') {
      void revokeManualSource(item)
      return
    }
    if (key === 'revoke-team-grant') {
      void revokeAuthorization(item)
      return
    }
    if (key.startsWith('team:')) {
      const sourceTeamId = key.slice('team:'.length)
      if (sourceTeamId) {
        void revokeTeamSource(item)
      }
    }
  }

  async function updateAuthorizationStatus(item: ResourceAuthorizationSummary, status: 'active' | 'paused') {
    try {
      const payload: { status: 'active' | 'paused'; expiresAt?: string | null } = { status }
      if (status === 'active' && item.expiresAt) {
        const expiresAtTimestamp = serverDateTimeTimestamp(item.expiresAt)
        if (expiresAtTimestamp === undefined || expiresAtTimestamp <= Date.now()) {
          payload.expiresAt = null
        }
      }
      const updated = isManagementView.value
        ? await api.authorizations.update(item.id, payload, authorizationOperationScopeParams(item))
        : await api.myAuthorizations.update(item.id, payload)
      updateAuthorizationItems((authorization) => authorization.id === item.id, () => updated)
      message.success(status === 'active' ? '授权已恢复' : '授权已暂停')
      void loadData({ quiet: true })
    } catch (error) {
      console.error(error)
      message.error(extractApiErrorMessage(error, status === 'active' ? '恢复授权失败' : '暂停授权失败'))
    }
  }

  function openExpireModal(item: ResourceAuthorizationSummary) {
    let nextForm: AuthorizationExpireFormModel
    try {
      nextForm = authorizationExpireFormFromSummary(item)
    } catch (error) {
      message.error(extractApiErrorMessage(error, '授权数据结构异常，请清理后再编辑'))
      return
    }
    expireAuthorization.value = item
    Object.assign(expireForm, nextForm)
    expireModalOpen.value = true
  }

  async function confirmExpireChange() {
    const authorization = expireAuthorization.value
    if (!authorization) {
      expireModalOpen.value = false
      return
    }
    if (!validateAuthorizationExpiresAt(expireForm.expiresAt, authorization.resourceAccountExpiresAt)) {
      return
    }
    try {
      const payload = authorizationExpirePayload(expireForm)
      const updated = isManagementView.value
        ? await api.authorizations.updateExpire(authorization.id, payload, authorizationOperationScopeParams(authorization))
        : await api.myAuthorizations.updateExpire(authorization.id, payload)
      updateAuthorizationItems((item) => item.id === authorization.id, () => updated)
      expireModalOpen.value = false
      expireAuthorization.value = undefined
      message.success('授权配置已更新')
      void loadData({ quiet: true })
    } catch (error) {
      console.error(error)
      message.error(extractApiErrorMessage(error, '修改授权配置失败'))
    }
  }

  function validateAuthorizationExpiresAt(expiresAt: AuthorizationCreateFormModel['expiresAt'], accountExpiresAt?: string): boolean {
    if (!expiresAt) return true
    if (expiresAt.isBefore(dayjs())) {
      message.warning('授权到期时间不能早于当前时间')
      return false
    }
    if (!accountExpiresAt) return true
    let maxExpiresAt: NonNullable<AuthorizationCreateFormModel['expiresAt']>
    try {
      const parsed = parseStrictDatePickerValue(accountExpiresAt, '账户到期时间')
      if (!parsed) return true
      maxExpiresAt = parsed
    } catch (error) {
      message.error(extractApiErrorMessage(error, '账户到期时间数据异常，请清理后再配置授权'))
      return false
    }
    if (expiresAt.isAfter(maxExpiresAt)) {
      message.warning(`授权到期时间不能晚于账户到期时间：${formatDateTime(accountExpiresAt)}`)
      return false
    }
    return true
  }

  function authorizationOperationScopeParams(item: ResourceAuthorizationSummary) {
    if (!isManagementView.value || !item.resourceOwnerSystemAccountId) return undefined
    return { systemAccountId: item.resourceOwnerSystemAccountId }
  }

  return {
    authorizationCreating,
    confirmExpireChange,
    createAuthorization,
    handleActionMenuClick
  }
}

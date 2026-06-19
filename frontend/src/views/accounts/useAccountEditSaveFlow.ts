import { api } from '@/api/client'
import { useSubmitAction } from '@/composables/useSubmitAction'
import { message } from '@/lib/antd'
import type { AccountSummary, OpenAIAuthURLResult } from '@/types/domain'
import { ref, type ComputedRef, type Ref } from 'vue'

import type { AccountErrorPolicyRuleForm } from './accountErrorPolicyTypes'
import type { AccountFormModel } from './accountFormTypes'
import type { AccountResponseInspectionRuleForm } from './accountResponseInspectionPolicyTypes'
import { isAuthorizedAccount } from './accountFormatters'
import { buildOAuthCreatePayload } from './accountOAuthPayload'
import type { AccountScopeParams } from './accountOperationScope'
import {
  buildAccountSavePayload,
  buildAccountUpdatePayload,
  buildOAuthCreateCommonPayload,
  validateAccountSaveForm,
  type AccountSavePayload
} from './accountSavePayload'
import {
  normalizeFormTagNames,
  sameTagNames
} from './accountEditFormPayload'
import {
  invalidateAccountTagOptionsCache,
  resolveAccountTagOptionsScopeKey
} from './accountTagOptionsCache'
import {
  invalidateAccountDetailCache,
  resolveAccountDetailCacheKey
} from './accountDetailCache'

type ReadonlyValue<T> = {
  readonly value: T
}

interface UseAccountEditSaveFlowOptions {
  accountCreatePayloadWithActivationTest: (payload: AccountSavePayload) => AccountSavePayload & { status?: 'active'; activationTestTaskId?: string }
  accountErrorPolicyRules: Ref<AccountErrorPolicyRuleForm[]>
  accountResponseInspectionRules: Ref<AccountResponseInspectionRuleForm[]>
  accounts: ReadonlyValue<AccountSummary[]>
  clearSuccessfulDraftActivationTest: () => void
  createScopeParams: ComputedRef<AccountScopeParams>
  editingAccountDetail: Ref<AccountSummary | undefined>
  editingAccountScopeParams: () => AccountScopeParams
  editingAuthorizedAccount: ComputedRef<boolean>
  editingId: Ref<string | undefined>
  extractApiErrorMessage: (error: unknown, fallback: string) => string
  form: AccountFormModel
  isManagementView: ComputedRef<boolean>
  loadData: () => Promise<void>
  modalOpen: Ref<boolean>
}

export function useAccountEditSaveFlow(options: UseAccountEditSaveFlowOptions) {
  const { submitAction, submittingRef } = useSubmitAction('accounts')
  const saving = submittingRef('accounts.save')
  const authLoading = ref(false)
  const authResult = ref<OpenAIAuthURLResult>()

  const saveAccount = submitAction('accounts.save', async () => {
    if (options.editingAuthorizedAccount.value) {
      await saveAuthorizedAccountEdit()
      return
    }

    const validationMessage = validateAccountSaveForm({
      editingId: options.editingId.value,
      form: options.form,
      hasAuthSession: Boolean(authResult.value?.sessionId),
      errorPolicyRules: options.accountErrorPolicyRules.value,
      responseInspectionRules: options.accountResponseInspectionRules.value
    })
    if (validationMessage) {
      message.warning(validationMessage)
      return
    }

    const payload = buildAccountSavePayload({
      accounts: options.accounts.value,
      accountDetail: options.editingAccountDetail.value,
      editingId: options.editingId.value,
      form: options.form,
      errorPolicyRules: options.accountErrorPolicyRules.value,
      responseInspectionRules: options.accountResponseInspectionRules.value
    })

    try {
      if (options.editingId.value) {
        const updatePayload = buildAccountUpdatePayload(payload)
        if (options.isManagementView.value) {
          await api.accounts.update(options.editingId.value, updatePayload, options.editingAccountScopeParams())
        } else {
          await api.myAccounts.update(options.editingId.value, updatePayload)
        }
        invalidateAccountDetailOptions(options.editingId.value, options.editingAccountScopeParams())
        message.success('账户已更新')
      } else if (options.form.type === 'oauth') {
        await createOAuthAccountFromUnifiedForm()
        message.success('OAuth 账户已创建，需测试通过后参与调度')
      } else {
        const created = await createApiKeyAccount(options.accountCreatePayloadWithActivationTest(payload))
        message.success(created?.status === 'active' ? '账户已创建并启用' : '账户已创建，需测试通过后参与调度')
      }
      invalidateAccountTagOptions(options.editingId.value ? options.editingAccountScopeParams() : options.createScopeParams.value)
      options.clearSuccessfulDraftActivationTest()
      options.modalOpen.value = false
      await options.loadData()
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '保存账户失败'))
    }
  })

  async function generateOAuthUrl() {
    authLoading.value = true
    try {
      authResult.value = options.isManagementView.value ? await api.openaiOAuth.authUrl({}) : await api.myOpenaiOAuth.authUrl({})
      message.success('授权链接已生成')
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '生成授权链接失败'))
    } finally {
      authLoading.value = false
    }
  }

  async function saveAuthorizedAccountEdit(): Promise<void> {
    const account = options.editingAccountDetail.value
    if (!options.editingId.value || !account || !isAuthorizedAccount(account)) {
      message.warning('请选择要编辑的授权账户')
      return
    }
    if (!options.form.groupId) {
      message.warning('请选择加入分组')
      return
    }
    const priority = Number(options.form.priority)
    if (!Number.isFinite(priority) || priority < 0) {
      message.warning('优先级必须是大于等于 0 的整数')
      return
    }
    const nextPriority = Math.trunc(priority)
    const scopeParams = options.editingAccountScopeParams()
    try {
      if (options.form.groupId !== account.boundGroupId) {
        if (options.isManagementView.value) {
          await api.accounts.bindGroup(account.id, { groupId: options.form.groupId }, scopeParams)
        } else {
          await api.myAccounts.bindGroup(account.id, { groupId: options.form.groupId })
        }
      }
      if (nextPriority !== account.priority) {
        if (options.isManagementView.value) {
          await api.accounts.updateAuthorizedDispatch(account.id, { priority: nextPriority }, scopeParams)
        } else {
          await api.myAccounts.updateAuthorizedDispatch(account.id, { priority: nextPriority })
        }
      }
      if (!sameTagNames(options.form.tags, account.tags)) {
        const payload = { tags: normalizeFormTagNames(options.form.tags) }
        if (options.isManagementView.value) {
          await api.accounts.updateTags(account.id, payload, scopeParams)
        } else {
          await api.myAccounts.updateTags(account.id, payload)
        }
        invalidateAccountTagOptions(scopeParams)
      }
      invalidateAccountDetailOptions(account.id, scopeParams)
      message.success('授权账户已更新')
      options.modalOpen.value = false
      await options.loadData()
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '保存授权账户失败'))
    }
  }

  async function createOAuthAccountFromUnifiedForm() {
    const commonPayload = buildOAuthCreateCommonPayload({
      accounts: options.accounts.value,
      editingId: options.editingId.value,
      form: options.form,
      errorPolicyRules: options.accountErrorPolicyRules.value,
      responseInspectionRules: options.accountResponseInspectionRules.value
    })

    const payload = buildOAuthCreatePayload({
      commonPayload,
      form: options.form,
      sessionId: authResult.value?.sessionId
    })

    if (options.form.oauthMode === 'manual') {
      if (options.isManagementView.value) {
        await api.openaiOAuth.createFromCode(payload, options.createScopeParams.value)
      } else {
        await api.myOpenaiOAuth.createFromCode(payload)
      }
      return
    }

    if (options.isManagementView.value) {
      await api.openaiOAuth.createFromRefreshToken(payload, options.createScopeParams.value)
    } else {
      await api.myOpenaiOAuth.createFromRefreshToken(payload)
    }
  }

  async function createApiKeyAccount(payload: AccountSavePayload): Promise<AccountSummary> {
    return options.isManagementView.value
      ? api.accounts.create(payload, options.createScopeParams.value)
      : api.myAccounts.create(payload)
  }

  function invalidateAccountTagOptions(scopeParams: AccountScopeParams | undefined): void {
    invalidateAccountTagOptionsCache(resolveAccountTagOptionsScopeKey(options.isManagementView.value, scopeParams))
  }

  function invalidateAccountDetailOptions(accountId: string | undefined, scopeParams: AccountScopeParams | undefined): void {
    if (!accountId) return
    invalidateAccountDetailCache(resolveAccountDetailCacheKey(options.isManagementView.value, accountId, scopeParams))
  }

  return {
    authLoading,
    authResult,
    generateOAuthUrl,
    saveAccount,
    saving
  }
}

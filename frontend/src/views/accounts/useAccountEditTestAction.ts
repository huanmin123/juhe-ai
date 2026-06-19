import { toValue, type MaybeRefOrGetter } from 'vue'

import type { AccountDraftTestAccountPayload } from '@/api/client'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { rememberGroupLabel } from '@/shared/groupLabelCache'
import type { AccountSummary, ProviderDefinition, ProviderProtocolProfileDefinition } from '@/types/domain'
import type { AccountErrorPolicyRuleForm } from './accountErrorPolicyTypes'
import type { AccountFormModel } from './accountFormTypes'
import type { AccountResponseInspectionRuleForm } from './accountResponseInspectionPolicyTypes'
import { accountOperationScopeParams } from './accountOperationScope'
import {
  buildAccountDraftTestPayload,
  buildAccountDraftTestSummary,
  validateAccountDraftTestForm
} from './accountDraftTestPayload'

interface UseAccountEditTestActionOptions {
  accountDetail: MaybeRefOrGetter<AccountSummary | undefined>
  accountScopeParams: MaybeRefOrGetter<{ systemAccountId: string } | undefined>
  accounts: MaybeRefOrGetter<AccountSummary[]>
  authSessionId: MaybeRefOrGetter<string | undefined>
  createScopeParams: MaybeRefOrGetter<{ systemAccountId: string } | undefined>
  editingAuthorizedAccount: MaybeRefOrGetter<boolean>
  editingId: MaybeRefOrGetter<string | undefined>
  errorPolicyRules: MaybeRefOrGetter<AccountErrorPolicyRuleForm[]>
  form: AccountFormModel
  openDraftTestModal: (account: AccountSummary, draftPayload: AccountDraftTestAccountPayload) => void | Promise<void>
  openSavedDraftTestModal: (account: AccountSummary, draftPayload: AccountDraftTestAccountPayload) => void | Promise<void>
  openTestModal: (account: AccountSummary) => void | Promise<void>
  providers: MaybeRefOrGetter<ProviderDefinition[]>
  responseInspectionRules: MaybeRefOrGetter<AccountResponseInspectionRuleForm[]>
  selectedProtocolProfile: MaybeRefOrGetter<ProviderProtocolProfileDefinition | undefined>
}

export function useAccountEditTestAction(options: UseAccountEditTestActionOptions) {
  async function testAccountFromEditModal(): Promise<void> {
    const accountDetail = toValue(options.accountDetail)
    if (toValue(options.editingAuthorizedAccount)) {
      if (!accountDetail) {
        message.warning('请选择要测试的授权账户')
        return
      }
      if (options.form.groupId && options.form.groupId !== accountDetail.boundGroupId) {
        message.info('授权账户测试使用当前已保存的分组绑定，保存后新分组才会生效')
      }
      await options.openTestModal(accountDetail)
      return
    }

    const validationMessage = validateAccountDraftTestForm({
      accounts: toValue(options.accounts),
      accountDetail,
      editingId: toValue(options.editingId),
      form: options.form,
      hasAuthSession: Boolean(toValue(options.authSessionId)),
      errorPolicyRules: toValue(options.errorPolicyRules),
      responseInspectionRules: toValue(options.responseInspectionRules),
      providers: toValue(options.providers)
    })
    if (validationMessage) {
      message.warning(validationMessage)
      return
    }

    try {
      const draftPayload = buildAccountDraftTestPayload({
        accounts: toValue(options.accounts),
        accountDetail,
        editingId: toValue(options.editingId),
        form: options.form,
        errorPolicyRules: toValue(options.errorPolicyRules),
        responseInspectionRules: toValue(options.responseInspectionRules),
        providers: toValue(options.providers)
      })
      if (!draftPayload.groupId) {
        message.warning('请选择加入分组')
        return
      }
      if (options.form.group?.id === draftPayload.groupId && options.form.group.name) {
        rememberGroupLabel(options.form.group.id, options.form.group.name)
      }
      const draftAccount = buildAccountDraftTestSummary({
        accountDetail,
        draftPayload,
        clientCompatibility: options.form.clientCompatibility,
        protocolProfile: toValue(options.selectedProtocolProfile),
        scopeSystemAccountId: draftTestScopeSystemAccountId(accountDetail)
      })
      if (options.form.group?.id === draftPayload.groupId && options.form.group.name) {
        draftAccount.boundGroupName = options.form.group.name
      }
      if (toValue(options.editingId) && accountDetail) {
        await options.openSavedDraftTestModal(draftAccount, draftPayload)
      } else {
        await options.openDraftTestModal(draftAccount, draftPayload)
      }
    } catch (error) {
      console.error(error)
      message.error(extractApiErrorMessage(error, '生成账户测试草稿失败'))
    }
  }

  function draftTestScopeSystemAccountId(accountDetail: AccountSummary | undefined): string | undefined {
    if (accountDetail) {
      return accountOperationScopeParams(accountDetail, toValue(options.accountScopeParams))?.systemAccountId
    }
    return toValue(options.createScopeParams)?.systemAccountId ?? toValue(options.accountScopeParams)?.systemAccountId
  }

  return {
    testAccountFromEditModal
  }
}

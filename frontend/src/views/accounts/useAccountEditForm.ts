import { message } from '@/lib/antd'
import { computed, nextTick, reactive, ref, watch, type ComputedRef } from 'vue'

import { useProviderModelSelectOptions } from '@/composables/useProviderModelSelectOptions'
import { rememberGroupLabel, type GroupSelection } from '@/shared/groupLabelCache'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type {
  AccountSummary,
  AccountType,
  GroupOptionSummary,
  ProviderDefinition,
  SystemAccountPrincipalSummary
} from '@/types/domain'
import {
  defaultGroupForProvider as selectDefaultGroupForProvider,
  groupOptionsForProviderWithSelected,
  isManageableGroupForProvider,
  targetSystemAccountLabel as buildTargetSystemAccountLabel
} from './accountDerivedState'
import {
  loadAccountErrorPolicyRules
} from './accountErrorPolicyPayload'
import type { AccountErrorPolicyRuleForm } from './accountErrorPolicyTypes'
import {
  loadAccountResponseInspectionRules
} from './accountResponseInspectionPolicyPayload'
import type { AccountResponseInspectionRuleForm } from './accountResponseInspectionPolicyTypes'
import {
  accountEditAccountTypeTitle,
  accountEditModalTitle,
  accountEditProviderName,
  accountTypeChoiceValue,
  accountTypeChoicesForProvider
} from './accountEditFormDisplay'
import { defaultAccountForm } from './accountFormDefaults'
import { isAuthorizedAccount } from './accountFormatters'
import type { AccountFormModel } from './accountFormTypes'
import { FALLBACK_PROVIDERS } from './accountOptions'
import { canCreateOAuthAccount } from './accountProviderCapabilities'
import { authUrl } from './accountOAuthPayload'
import { loadAccountDetailCached } from './accountDetailCache'
import { accountOperationScopeParams, type AccountScopeParams } from './accountOperationScope'
import type { AccountSavePayload } from './accountSavePayload'
import {
  accountCreatePayloadWithActivationTest as applyActivationTestToCreatePayload
} from './accountEditFormPayload'
import {
  AccountEditFormLoadError,
  buildAccountCloneFormLoad,
  buildAccountEditFormLoad
} from './accountEditFormLoaders'
import type { SuccessfulDraftActivationTest } from './useAccountTestModal'
import { useAccountProviderModelOptions } from './useAccountProviderModelOptions'
import { useAccountEditTagOptions } from './useAccountEditTagOptions'
import { useAccountEditSaveFlow } from './useAccountEditSaveFlow'
import type { AccountGroupOptionsLoadOptions, AccountGroupOptionsScope } from './useAccountGroupOptions'

type ReadonlyValue<T> = {
  readonly value: T
}

interface UseAccountEditFormOptions {
  accountScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  accounts: ReadonlyValue<AccountSummary[]>
  extractApiErrorMessage: (error: unknown, fallback: string) => string
  groupIdForAccount: (accountId: string) => string | undefined
  groups: ReadonlyValue<GroupOptionSummary[]>
  isManagementView: ComputedRef<boolean>
  loadAccountOptions: (systemAccountId?: string, force?: boolean) => Promise<void>
  loadGroupOptions: (keyword?: string, force?: boolean, scopeOverride?: Partial<AccountGroupOptionsScope>, loadOptions?: AccountGroupOptionsLoadOptions) => Promise<void>
  loadData: () => Promise<void>
  focusCreatedAccount?: (account: AccountSummary) => void
  providers: ReadonlyValue<ProviderDefinition[]>
  systemAccountSelection?: ReadonlyValue<PrincipalSelection | undefined>
  systemAccounts: ReadonlyValue<SystemAccountPrincipalSummary[]>
  successfulDraftActivationTest?: { value: SuccessfulDraftActivationTest | undefined }
}

export function useAccountEditForm(options: UseAccountEditFormOptions) {
  const modalOpen = ref(false)
  const editingId = ref<string>()
  const editingAccountDetail = ref<AccountSummary>()
  const cloningSourceId = ref<string>()
  const creatingAccountScopeParams = ref<AccountScopeParams>()
  const editingScheduleFingerprint = ref<string>()
  const cloningScheduleFingerprint = ref<string>()
  const form = reactive<AccountFormModel>(defaultForm())
  const accountErrorPolicyRules = ref<AccountErrorPolicyRuleForm[]>(loadAccountErrorPolicyRules())
  const accountResponseInspectionRules = ref<AccountResponseInspectionRuleForm[]>(loadAccountResponseInspectionRules())
  let formOpenRequestToken = 0

  const createScopeParams = computed<AccountScopeParams>(() => creatingAccountScopeParams.value ?? options.accountScopeParams.value)
  const allProviderModelScopeParams = computed<AccountScopeParams>(() => editingId.value ? editingAccountScopeParams() : createScopeParams.value)
  const {
    loadProviderModelOptions,
    providerModelOptions,
    providerModelsLoading,
    resetProviderModelOptions
  } = useAccountProviderModelOptions({
    createScopeParams,
    currentProviderCode: () => form.providerCode,
    extractApiErrorMessage: options.extractApiErrorMessage,
    isManagementView: options.isManagementView
  })
  const {
    loading: allProviderModelsLoading,
    loadModelOptions: loadAllProviderModelOptions,
    selectOptions: mappingSourceModelOptions
  } = useProviderModelSelectOptions({
    scopeParams: allProviderModelScopeParams,
    onLoadError: (error) => {
      message.error(options.extractApiErrorMessage(error, '加载全局模型目录失败'))
    }
  })
  const strategyModelsLoading = computed(() => providerModelsLoading.value || allProviderModelsLoading.value)
  const {
    accountTagOptions,
    accountTagOptionsLoading,
    deleteAccountTag,
    deletingAccountTagId,
    loadAccountTagOptions
  } = useAccountEditTagOptions({
    accountTagOperationScopeParams,
    extractApiErrorMessage: options.extractApiErrorMessage,
    form,
    isManagementView: options.isManagementView
  })
  const targetSystemAccountLabel = computed(() => {
    if (!options.isManagementView.value) return undefined
    const systemAccountId = createScopeParams.value?.systemAccountId
    return buildTargetSystemAccountLabel(options.systemAccounts.value, systemAccountId, options.systemAccountSelection?.value)
  })

  const groupOptions = computed(() => groupOptionsForProviderWithSelected(options.groups.value, form.providerCode, [form.groupId], form.providerProtocolProfileId))
  const availableProviders = computed(() => options.providers.value.length ? options.providers.value : FALLBACK_PROVIDERS)
  const selectedProvider = computed(() => availableProviders.value.find((provider) => provider.code === form.providerCode))
  const selectedProtocolProfile = computed(() => selectedProvider.value
    ? selectedProvider.value.protocolProfiles.find((profile) => profile.id === form.providerProtocolProfileId)
      ?? selectedProvider.value.protocolProfiles.find((profile) => profile.id === selectedProvider.value?.defaultProtocolProfileId)
      ?? selectedProvider.value.protocolProfiles[0]
    : undefined)
  const accountTypeChoices = computed(() => accountTypeChoicesForProvider(selectedProvider.value, availableProviders.value))
  const selectedAccountTypeChoice = computed(() => accountTypeChoices.value.find((choice) => choice.type === form.type && choice.providerProtocolProfileId === form.providerProtocolProfileId))
  const hasAccountType = computed(() => Boolean(form.providerCode && form.providerProtocolProfileId && form.type))
  const isApiKeyForm = computed(() => hasAccountType.value && form.type === 'api_key')
  const isOAuthForm = computed(() => hasAccountType.value && form.type === 'oauth')
  const isOpenAIOAuthForm = computed(() => form.type === 'oauth' && canCreateOAuthAccount({
    provider: selectedProvider.value,
    profile: selectedProtocolProfile.value
  }))
  const editingAuthorizedAccount = computed(() => Boolean(editingId.value && editingAccountDetail.value && isAuthorizedAccount(editingAccountDetail.value)))
  const {
    authLoading,
    authResult,
    generateOAuthUrl,
    saveAccount,
    saving
  } = useAccountEditSaveFlow({
    accountCreatePayloadWithActivationTest,
    accountErrorPolicyRules,
    accountResponseInspectionRules,
    accounts: options.accounts,
    clearSuccessfulDraftActivationTest,
    createScopeParams,
    editingAccountDetail,
    editingAccountScopeParams,
    editingAuthorizedAccount,
    editingId,
    extractApiErrorMessage: options.extractApiErrorMessage,
    form,
    isManagementView: options.isManagementView,
    loadData: options.loadData,
    modalOpen,
    providers: availableProviders
  })
  const editingSystemAccountLabel = computed(() => {
    if (!options.isManagementView.value) return ''
    const account = editingAccountDetail.value ?? options.accounts.value.find((item) => item.id === editingId.value)
    const accountLabel = account?.systemAccountName?.trim()
    if (accountLabel) return accountLabel
    const systemAccountId = account?.systemAccountId
    if (!systemAccountId) return ''
    return buildTargetSystemAccountLabel(options.systemAccounts.value, systemAccountId)
  })
  const modalTitle = computed(() => accountEditModalTitle({
    cloningSourceId: cloningSourceId.value,
    editingAuthorizedAccount: editingAuthorizedAccount.value,
    editingId: editingId.value,
    editingSystemAccountLabel: editingSystemAccountLabel.value,
    providerCode: form.providerCode,
    providerProtocolProfileId: form.providerProtocolProfileId,
    providers: availableProviders.value,
    targetSystemAccountLabel: targetSystemAccountLabel.value,
    type: form.type,
    typeTitle: selectedAccountTypeChoice.value?.label
  }))
  const modalConfirmLoading = computed(() => saving.value)
  const modalOkButtonProps = computed(() => ({
    type: 'primary' as const,
    disabled: saving.value || !hasAccountType.value || (!editingId.value && isOAuthForm.value && !isOpenAIOAuthForm.value)
  }))
  const selectedAccountTypeTitle = computed(() => hasAccountType.value ? selectedAccountTypeChoice.value?.label ?? accountTypeTitle(form.providerCode, form.type) : '')

  function defaultForm(providerCode = '', type: AccountType = '', providerProtocolProfileId = ''): AccountFormModel {
    return defaultAccountForm(providerCode, type, options.providers.value, providerProtocolProfileId)
  }

  function resetForm(providerCode = '', type: AccountType = '') {
    cloningSourceId.value = undefined
    editingScheduleFingerprint.value = undefined
    cloningScheduleFingerprint.value = undefined
    clearSuccessfulDraftActivationTest()
    Object.assign(form, defaultForm(providerCode, type))
    resetProviderModelOptions()
    ensureDefaultGroupSelected(form.providerCode, form.providerProtocolProfileId)
    accountErrorPolicyRules.value = loadAccountErrorPolicyRules()
    accountResponseInspectionRules.value = loadAccountResponseInspectionRules()
    authResult.value = undefined
  }

  function accountTypeTitle(providerCode: string, type: AccountType) {
    return accountEditAccountTypeTitle(providerCode, type, availableProviders.value)
  }

  function providerName(providerCode?: string) {
    return accountEditProviderName(providerCode, availableProviders.value)
  }

  function defaultGroupForProvider(providerCode: string, providerProtocolProfileId?: string) {
    return selectDefaultGroupForProvider(options.groups.value, providerCode, providerProtocolProfileId)
  }

  function ensureDefaultGroupSelected(providerCode = form.providerCode, providerProtocolProfileId = form.providerProtocolProfileId) {
    if (!providerCode) {
      form.groupId = undefined
      form.group = undefined
      return
    }
    const currentGroup = options.groups.value.find((group) => group.id === form.groupId)
    if (currentGroup && isManageableGroupForProvider(currentGroup, providerCode, providerProtocolProfileId)) {
      form.group = { id: currentGroup.id, name: currentGroup.name }
      return
    }
    const nextGroup = defaultGroupForProvider(providerCode, providerProtocolProfileId)
    setFormGroup(nextGroup ? { id: nextGroup.id, name: nextGroup.name } : undefined)
  }

  function openCreate() {
    nextFormOpenRequestToken()
    if (options.isManagementView.value && !options.accountScopeParams.value?.systemAccountId) {
      message.warning('请先在右侧选择目标系统账户，再创建 AI 账户')
      return
    }
    editingId.value = undefined
    editingAccountDetail.value = undefined
    editingScheduleFingerprint.value = undefined
    cloningScheduleFingerprint.value = undefined
    creatingAccountScopeParams.value = undefined
    void options.loadAccountOptions(options.accountScopeParams.value?.systemAccountId)
    resetForm('', '')
    void options.loadGroupOptions('', false, {
      providerCode: form.providerCode,
      providerProtocolProfileId: form.providerProtocolProfileId,
      systemAccountId: options.accountScopeParams.value?.systemAccountId
    }, {
      useLocalWindow: false
    })
    void loadAccountTagOptions(options.accountScopeParams.value)
    void loadProviderModelOptions(form.providerCode)
    void loadAllProviderModelOptions()
    modalOpen.value = true
  }

  watch(
    () => options.groups.value,
    () => {
      if (modalOpen.value && !editingId.value && form.providerCode) {
        ensureDefaultGroupSelected()
      }
    }
  )

  function handleModalCancel() {
    modalOpen.value = false
    authResult.value = undefined
  }

  function selectProvider(providerCode: string) {
    if (editingId.value || form.providerCode === providerCode) return
    resetForm(providerCode, '')
    void loadProviderGroupOptions(providerCode, form.providerProtocolProfileId)
    void loadProviderModelOptions(providerCode)
    void loadAllProviderModelOptions()
  }

  function selectAccountType(type: AccountType) {
    const choice = accountTypeChoices.value.find((item) => item.value === accountTypeChoiceValue(form.providerProtocolProfileId, type))
      ?? accountTypeChoices.value.find((item) => item.type === type)
    if (!choice) return
    selectAccountTypeChoice(choice.value)
  }

  function selectAccountTypeChoice(choiceValue: string) {
    if (editingId.value) return
    const choice = accountTypeChoices.value.find((item) => item.value === choiceValue)
    if (!choice) return
    if (form.type === choice.type && form.providerProtocolProfileId === choice.providerProtocolProfileId) return
    cloningSourceId.value = undefined
    const providerCode = choice.providerCode || form.providerCode
    const providerProtocolProfileId = choice.providerProtocolProfileId
    const keepCurrentGroup = form.providerCode === providerCode && form.providerProtocolProfileId === providerProtocolProfileId
    Object.assign(form, {
      ...defaultForm(providerCode, choice.type, providerProtocolProfileId),
      groupId: keepCurrentGroup ? form.groupId : undefined,
      group: keepCurrentGroup ? form.group : undefined,
      proxyProfileId: form.proxyProfileId,
      notes: form.notes,
      supportedModels: form.supportedModels,
      modelMappings: form.modelMappings,
      tags: form.tags,
      concurrencyLimit: form.concurrencyLimit,
      priority: form.priority,
      accountExpiresAt: form.accountExpiresAt,
      availabilitySchedule: form.availabilitySchedule
    })
    void loadProviderGroupOptions(providerCode, providerProtocolProfileId)
    void loadProviderModelOptions(providerCode)
    void loadAllProviderModelOptions()
    ensureDefaultGroupSelected(providerCode, providerProtocolProfileId)
    authResult.value = undefined
  }

  async function openEdit(account: AccountSummary) {
    const requestToken = nextFormOpenRequestToken()
    const editScopeParams = accountOperationScopeParams(account, options.accountScopeParams.value)
    const sourceAccount = await loadAccountDetailForForm(account.id, editScopeParams, '加载账户详情失败')
    if (!sourceAccount || !isCurrentFormOpenRequest(requestToken)) return
    const defaults = defaultForm(sourceAccount.providerCode, sourceAccount.type, sourceAccount.providerProtocolProfileId)
    const selectedGroup = sourceAccount.boundGroupId
      ? groupSelectionForId(sourceAccount.boundGroupId, sourceAccount.boundGroupName)
      : undefined
    const fallbackGroupId = options.groupIdForAccount(sourceAccount.id)
    let formLoad: ReturnType<typeof buildAccountEditFormLoad>
    try {
      formLoad = buildAccountEditFormLoad({
        account: sourceAccount,
        credentials: sourceAccount.credentials,
        defaults,
        fallbackGroupId,
        selectedGroup
      })
    } catch (error) {
      reportAccountFormLoadError(error, '账户数据结构异常，请清理后再编辑')
      return
    }
    editingId.value = sourceAccount.id
    editingAccountDetail.value = sourceAccount
    cloningSourceId.value = undefined
    creatingAccountScopeParams.value = undefined
    void options.loadAccountOptions(editScopeParams?.systemAccountId)
    Object.assign(form, formLoad.patch)
    editingScheduleFingerprint.value = formLoad.scheduleFingerprint
    cloningScheduleFingerprint.value = undefined
    accountErrorPolicyRules.value = formLoad.errorPolicyRules
    accountResponseInspectionRules.value = formLoad.responseInspectionRules
    authResult.value = undefined
    modalOpen.value = true
    void options.loadGroupOptions('', false, {
      providerCode: sourceAccount.providerCode,
      providerProtocolProfileId: sourceAccount.providerProtocolProfileId,
      systemAccountId: editScopeParams?.systemAccountId,
      selectedIds: [form.groupId]
    }, {
      useLocalWindow: false
    })
    void loadAccountTagOptions(editScopeParams)
    void loadProviderModelOptions(sourceAccount.providerCode)
    void loadAllProviderModelOptions()
  }

  async function openClone(account: AccountSummary) {
    const requestToken = nextFormOpenRequestToken()
    const cloneScopeParams = accountOperationScopeParams(account, options.accountScopeParams.value)
    if (options.isManagementView.value && !cloneScopeParams?.systemAccountId) {
      message.warning('无法确定克隆目标系统账户，请先筛选目标系统账户后再克隆')
      return
    }
    const sourceAccount = await loadAccountDetailForForm(account.id, cloneScopeParams, '加载克隆账户配置失败')
    if (!sourceAccount || !isCurrentFormOpenRequest(requestToken)) return
    editingId.value = undefined
    editingAccountDetail.value = undefined
    editingScheduleFingerprint.value = undefined
    cloningSourceId.value = sourceAccount.id
    creatingAccountScopeParams.value = cloneScopeParams
    void options.loadAccountOptions(cloneScopeParams?.systemAccountId)
    const defaults = defaultForm(sourceAccount.providerCode, sourceAccount.type, sourceAccount.providerProtocolProfileId)
    const selectedGroup = sourceAccount.boundGroupId
      ? groupSelectionForId(sourceAccount.boundGroupId, sourceAccount.boundGroupName)
      : undefined
    const fallbackGroupId = options.groupIdForAccount(sourceAccount.id)
    let formLoad: ReturnType<typeof buildAccountCloneFormLoad>
    try {
      formLoad = buildAccountCloneFormLoad({
        account: sourceAccount,
        credentials: sourceAccount.credentials,
        defaults,
        fallbackGroupId,
        selectedGroup
      })
    } catch (error) {
      reportAccountFormLoadError(error, '克隆来源账户数据结构异常，请清理后再克隆')
      return
    }
    Object.assign(form, formLoad.patch)
    cloningScheduleFingerprint.value = formLoad.scheduleFingerprint
    accountErrorPolicyRules.value = formLoad.errorPolicyRules
    accountResponseInspectionRules.value = formLoad.responseInspectionRules
    authResult.value = undefined
    modalOpen.value = true
    void options.loadGroupOptions('', false, {
      providerCode: sourceAccount.providerCode,
      providerProtocolProfileId: sourceAccount.providerProtocolProfileId,
      systemAccountId: cloneScopeParams?.systemAccountId,
      selectedIds: [form.groupId]
    }, {
      useLocalWindow: false
    })
    void loadAccountTagOptions(cloneScopeParams)
    void loadProviderModelOptions(sourceAccount.providerCode)
    void loadAllProviderModelOptions()
  }

  async function loadProviderGroupOptions(providerCode: string, providerProtocolProfileId = form.providerProtocolProfileId): Promise<void> {
    await nextTick()
    await options.loadGroupOptions('', false, {
      providerCode,
      providerProtocolProfileId,
      systemAccountId: createScopeParams.value?.systemAccountId,
      selectedIds: [form.groupId]
    }, {
      useLocalWindow: false
    })
    syncFormGroupFromOptions()
    ensureDefaultGroupSelected(providerCode, providerProtocolProfileId)
  }

  async function loadAccountDetailForForm(accountId: string, scopeParams: AccountScopeParams | undefined, fallbackMessage: string): Promise<AccountSummary | undefined> {
    try {
      return await loadAccountDetailCached({
        accountId,
        isManagementView: options.isManagementView.value,
        scopeParams
      })
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, fallbackMessage))
      return undefined
    }
  }

  function nextFormOpenRequestToken(): number {
    formOpenRequestToken += 1
    return formOpenRequestToken
  }

  function isCurrentFormOpenRequest(requestToken: number): boolean {
    return requestToken === formOpenRequestToken
  }

  function openAuthUrl() {
    const url = authUrl(authResult.value?.authUrl)
    if (!url) return
    window.open(url, '_blank', 'noopener,noreferrer')
  }

  return {
    accountErrorPolicyRules,
    accountResponseInspectionRules,
    accountTagOptions,
    accountTagOptionsLoading,
    accountTypeChoices,
    authLoading,
    authResult,
    availableProviders,
    cloningSourceId,
    createScopeParams,
    editingId,
    editingAccountDetail,
    editingAuthorizedAccount,
    ensureDefaultGroupSelected,
    form,
    generateOAuthUrl,
    deleteAccountTag,
    deletingAccountTagId,
    groupOptions,
    handleModalCancel,
    hasAccountType,
    isApiKeyForm,
    isOAuthForm,
    isOpenAIOAuthForm,
    mappingSourceModelOptions,
    modalConfirmLoading,
    modalOkButtonProps,
    modalOpen,
    modalTitle,
    openAuthUrl,
    openClone,
    openCreate,
    openEdit,
    providerName,
    providerModelOptions,
    providerModelsLoading,
    saveAccount,
    selectAccountType,
    selectAccountTypeChoice,
    selectedAccountTypeTitle,
    selectedProtocolProfile,
    selectedProvider,
    selectProvider,
    strategyModelsLoading,
    targetSystemAccountLabel
  }

  function groupSelectionForId(id: string | undefined, name: string | undefined): GroupSelection | undefined {
    const normalizedId = id?.trim()
    if (!normalizedId) return undefined
    const optionGroup = options.groups.value.find((group) => group.id === normalizedId)
    const normalizedName = optionGroup?.name?.trim() || name?.trim()
    if (!normalizedName) return form.group?.id === normalizedId ? form.group : undefined
    rememberGroupLabel(normalizedId, normalizedName)
    return { id: normalizedId, name: normalizedName }
  }

  function reportAccountFormLoadError(error: unknown, fallbackMessage: string): void {
    if (error instanceof AccountEditFormLoadError) {
      if (error.log) {
        console.error(error.cause ?? error)
      }
      message.error(error.fallbackMessage
        ? options.extractApiErrorMessage(error.cause ?? error, error.fallbackMessage)
        : error.message
      )
      return
    }
    console.error(error)
    message.error(options.extractApiErrorMessage(error, fallbackMessage))
  }

  function setFormGroup(group: GroupSelection | undefined): void {
    form.groupId = group?.id
    form.group = group
  }

  function syncFormGroupFromOptions(): void {
    if (!form.groupId) {
      form.group = undefined
      return
    }
    const group = groupSelectionForId(form.groupId, form.group?.name)
    if (group) {
      form.group = group
    }
  }

  function editingAccountScopeParams(): AccountScopeParams {
    if (!editingId.value) return options.accountScopeParams.value
    const account = editingAccountDetail.value ?? options.accounts.value.find((item) => item.id === editingId.value)
    return account ? accountOperationScopeParams(account, options.accountScopeParams.value) : options.accountScopeParams.value
  }

  function accountTagOperationScopeParams(): AccountScopeParams {
    return editingId.value ? editingAccountScopeParams() : createScopeParams.value
  }

  function accountCreatePayloadWithActivationTest(payload: AccountSavePayload): AccountSavePayload & { status?: 'active'; activationTestTaskId?: string } {
    return applyActivationTestToCreatePayload(payload, options.successfulDraftActivationTest?.value, form.name.trim())
  }

  function clearSuccessfulDraftActivationTest(): void {
    if (options.successfulDraftActivationTest) {
      options.successfulDraftActivationTest.value = undefined
    }
  }
}

import { message } from '@/lib/antd'
import { computed, nextTick, reactive, ref, watch, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import { useSubmitAction } from '@/composables/useSubmitAction'
import { rememberGroupLabel, type GroupSelection } from '@/shared/groupLabelCache'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type {
  AccountSummary,
  AccountType,
  GroupOptionSummary,
  OpenAIAuthURLResult,
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
  accountTypeChoicesForProfile
} from './accountEditFormDisplay'
import { defaultAccountClientCompatibility, defaultAccountForm } from './accountFormDefaults'
import { isAuthorizedAccount } from './accountFormatters'
import type { AccountFormModel } from './accountFormTypes'
import { FALLBACK_PROVIDERS, GPT_VENDOR_CODE } from './accountOptions'
import { isOpenAIProtocolProfile } from '@/shared/providerProtocol'
import { authUrl, buildOAuthCreatePayload } from './accountOAuthPayload'
import { accountOperationScopeParams, type AccountScopeParams } from './accountOperationScope'
import { buildAccountSavePayload, buildAccountUpdatePayload, buildOAuthCreateCommonPayload, validateAccountSaveForm, type AccountSavePayload } from './accountSavePayload'
import {
  accountCreatePayloadWithActivationTest as applyActivationTestToCreatePayload,
  normalizeFormTagNames,
  sameTagNames
} from './accountEditFormPayload'
import {
  AccountEditFormLoadError,
  buildAccountCloneFormLoad,
  buildAccountEditFormLoad
} from './accountEditFormLoaders'
import type { SuccessfulDraftActivationTest } from './useAccountTestModal'
import { useAccountProviderModelOptions } from './useAccountProviderModelOptions'
import { useAccountEditTagOptions } from './useAccountEditTagOptions'

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
  loadGroupOptions: (keyword?: string, force?: boolean, scopeOverride?: { providerCode?: string; systemAccountId?: string; selectedIds?: Array<string | undefined> }) => Promise<void>
  loadData: () => Promise<void>
  focusCreatedAccount?: (account: AccountSummary) => void
  providers: ReadonlyValue<ProviderDefinition[]>
  systemAccountSelection?: ReadonlyValue<PrincipalSelection | undefined>
  systemAccounts: ReadonlyValue<SystemAccountPrincipalSummary[]>
  successfulDraftActivationTest?: { value: SuccessfulDraftActivationTest | undefined }
}

export function useAccountEditForm(options: UseAccountEditFormOptions) {
  const { submitAction, submittingRef } = useSubmitAction('accounts')
  const saving = submittingRef('accounts.save')
  const authLoading = ref(false)
  const modalOpen = ref(false)
  const authResult = ref<OpenAIAuthURLResult>()
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
  const {
    loadProviderModelOptions,
    mappingTargetModelOptions,
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
  const accountTypeChoices = computed(() => accountTypeChoicesForProfile(
    selectedProtocolProfile.value,
    selectedProvider.value?.code ?? form.providerCode,
    availableProviders.value
  ))
  const hasAccountType = computed(() => Boolean(form.providerCode && form.providerProtocolProfileId && form.type))
  const isApiKeyForm = computed(() => hasAccountType.value && form.type === 'api_key')
  const isOAuthForm = computed(() => hasAccountType.value && form.type === 'oauth')
  const isOpenAIOAuthForm = computed(() => form.providerCode === GPT_VENDOR_CODE && form.type === 'oauth' && isOpenAIProtocolProfile(selectedProtocolProfile.value))
  const editingAuthorizedAccount = computed(() => Boolean(editingId.value && editingAccountDetail.value && isAuthorizedAccount(editingAccountDetail.value)))
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
    type: form.type
  }))
  const modalConfirmLoading = computed(() => saving.value)
  const modalOkButtonProps = computed(() => ({
    type: 'primary' as const,
    disabled: saving.value || !hasAccountType.value || (!editingId.value && isOAuthForm.value && !isOpenAIOAuthForm.value)
  }))
  const selectedAccountTypeTitle = computed(() => hasAccountType.value ? accountTypeTitle(form.providerCode, form.type) : '')

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
    void options.loadGroupOptions('', true, {
      providerCode: form.providerCode,
      systemAccountId: options.accountScopeParams.value?.systemAccountId
    })
    void loadAccountTagOptions(options.accountScopeParams.value, true)
    void loadProviderModelOptions(form.providerCode)
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
    void loadProviderGroupOptions(providerCode)
    void loadProviderModelOptions(providerCode)
  }

  function selectAccountType(type: AccountType) {
    if (editingId.value || form.type === type) return
    cloningSourceId.value = undefined
    const providerCode = form.providerCode
    const providerProtocolProfileId = form.providerProtocolProfileId
    Object.assign(form, {
      ...defaultForm(providerCode, type, providerProtocolProfileId),
      groupId: form.groupId,
      group: form.group,
      proxyProfileId: form.proxyProfileId,
      notes: form.notes,
      clientCompatibility: defaultAccountClientCompatibility(providerCode),
      supportedModels: form.supportedModels,
      modelMappings: form.modelMappings,
      tags: form.tags,
      concurrencyLimit: form.concurrencyLimit,
      priority: form.priority,
      accountExpiresAt: form.accountExpiresAt,
      availabilitySchedule: form.availabilitySchedule
    })
    void loadProviderGroupOptions(providerCode)
    void loadProviderModelOptions(providerCode)
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
    void options.loadGroupOptions('', true, {
      providerCode: sourceAccount.providerCode,
      systemAccountId: editScopeParams?.systemAccountId,
      selectedIds: [form.groupId]
    })
    void loadAccountTagOptions(editScopeParams, true)
    void loadProviderModelOptions(sourceAccount.providerCode)
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
    void options.loadGroupOptions('', true, {
      providerCode: sourceAccount.providerCode,
      systemAccountId: cloneScopeParams?.systemAccountId,
      selectedIds: [form.groupId]
    })
    void loadAccountTagOptions(cloneScopeParams, true)
    void loadProviderModelOptions(sourceAccount.providerCode)
  }

  async function loadProviderGroupOptions(providerCode: string): Promise<void> {
    await nextTick()
    await options.loadGroupOptions('', true)
    syncFormGroupFromOptions()
    ensureDefaultGroupSelected(providerCode)
  }

  async function loadAccountDetailForForm(accountId: string, scopeParams: AccountScopeParams | undefined, fallbackMessage: string): Promise<AccountSummary | undefined> {
    try {
      return options.isManagementView.value
        ? await api.accounts.detail(accountId, scopeParams)
        : await api.myAccounts.detail(accountId)
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

  const saveAccount = submitAction('accounts.save', async () => {
    if (editingAuthorizedAccount.value) {
      await saveAuthorizedAccountEdit()
      return
    }

    const validationMessage = validateAccountSaveForm({
      editingId: editingId.value,
      form,
      hasAuthSession: Boolean(authResult.value?.sessionId),
      errorPolicyRules: accountErrorPolicyRules.value,
      responseInspectionRules: accountResponseInspectionRules.value
    })
    if (validationMessage) {
      message.warning(validationMessage)
      return
    }

    const payload = buildAccountSavePayload({
      accounts: options.accounts.value,
      accountDetail: editingAccountDetail.value,
      editingId: editingId.value,
      form,
      errorPolicyRules: accountErrorPolicyRules.value,
      responseInspectionRules: accountResponseInspectionRules.value
    })

    try {
      if (editingId.value) {
        const updatePayload = buildAccountUpdatePayload(payload)
        if (options.isManagementView.value) {
          await api.accounts.update(editingId.value, updatePayload, editingAccountScopeParams())
        } else {
          await api.myAccounts.update(editingId.value, updatePayload)
        }
        message.success('账户已更新')
      } else if (form.type === 'oauth') {
        await createOAuthAccountFromUnifiedForm()
        message.success('OAuth 账户已创建，需测试通过后参与调度')
      } else {
        const created = await createApiKeyAccount(accountCreatePayloadWithActivationTest(payload))
        message.success(created?.status === 'active' ? '账户已创建并启用' : '账户已创建，需测试通过后参与调度')
      }
      clearSuccessfulDraftActivationTest()
      modalOpen.value = false
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
    const account = editingAccountDetail.value
    if (!editingId.value || !account || !isAuthorizedAccount(account)) {
      message.warning('请选择要编辑的授权账户')
      return
    }
    if (!form.groupId) {
      message.warning('请选择加入分组')
      return
    }
    const priority = Number(form.priority)
    if (!Number.isFinite(priority) || priority < 0) {
      message.warning('优先级必须是大于等于 0 的整数')
      return
    }
    const nextPriority = Math.trunc(priority)
    const scopeParams = editingAccountScopeParams()
    try {
      if (form.groupId !== account.boundGroupId) {
        if (options.isManagementView.value) {
          await api.accounts.bindGroup(account.id, { groupId: form.groupId }, scopeParams)
        } else {
          await api.myAccounts.bindGroup(account.id, { groupId: form.groupId })
        }
      }
      if (nextPriority !== account.priority) {
        if (options.isManagementView.value) {
          await api.accounts.updateAuthorizedDispatch(account.id, { priority: nextPriority }, scopeParams)
        } else {
          await api.myAccounts.updateAuthorizedDispatch(account.id, { priority: nextPriority })
        }
      }
      if (!sameTagNames(form.tags, account.tags)) {
        const payload = { tags: normalizeFormTagNames(form.tags) }
        if (options.isManagementView.value) {
          await api.accounts.updateTags(account.id, payload, scopeParams)
        } else {
          await api.myAccounts.updateTags(account.id, payload)
        }
      }
      message.success('授权账户已更新')
      modalOpen.value = false
      await options.loadData()
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '保存授权账户失败'))
    }
  }

  function openAuthUrl() {
    const url = authUrl(authResult.value?.authUrl)
    if (!url) return
    window.open(url, '_blank', 'noopener,noreferrer')
  }

  async function createOAuthAccountFromUnifiedForm() {
    const commonPayload = buildOAuthCreateCommonPayload({
      accounts: options.accounts.value,
      editingId: editingId.value,
      form,
      errorPolicyRules: accountErrorPolicyRules.value,
      responseInspectionRules: accountResponseInspectionRules.value
    })

    const payload = buildOAuthCreatePayload({
      commonPayload,
      form,
      sessionId: authResult.value?.sessionId
    })

    if (form.oauthMode === 'manual') {
      if (options.isManagementView.value) {
        await api.openaiOAuth.createFromCode(payload, createScopeParams.value)
      } else {
        await api.myOpenaiOAuth.createFromCode(payload)
      }
      return
    }

    if (options.isManagementView.value) {
      await api.openaiOAuth.createFromRefreshToken(payload, createScopeParams.value)
    } else {
      await api.myOpenaiOAuth.createFromRefreshToken(payload)
    }
  }

  async function createApiKeyAccount(payload: AccountSavePayload): Promise<AccountSummary> {
    return options.isManagementView.value
      ? api.accounts.create(payload, createScopeParams.value)
      : api.myAccounts.create(payload)
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
    mappingTargetModelOptions,
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
    selectedAccountTypeTitle,
    selectedProtocolProfile,
    selectedProvider,
    selectProvider,
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

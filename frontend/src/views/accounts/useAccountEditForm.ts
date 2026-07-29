import { message } from '@/lib/antd'
import { computed, onBeforeUnmount, reactive, ref, watch, type ComputedRef } from 'vue'

import { api, type AccountDraftTestAccountPayload } from '@/api/client'
import { useProviderModelSelectOptions } from '@/composables/useProviderModelSelectOptions'
import { getCachedUserReferenceData } from '@/composables/useUserReferenceData'
import { rememberGroupLabel, type GroupSelection } from '@/shared/groupLabelCache'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type {
  AccountApiKeyRuntimeDetail,
  AccountApiKeyRuntimeResponse,
  AccountAdvancedDetail,
  AccountEditBasicDetail,
  AccountListItem,
  AccountMutationResult,
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
import { accountHealthCheckEndpointModeOptions, defaultAccountHealthCheckEndpointMode } from './accountHealthCheckEndpointMode'
import {
  createSavedAccountApiKeyRuntimeSnapshot,
  visibleSavedAccountApiKeyRuntimeDetails,
  type SavedAccountApiKeyRuntimeSnapshot
} from './accountApiKeyRuntimeDisplay'
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
import { accountProviderProtocolKind, canCreateOAuthAccount, supportsOAuthAccountType } from './accountProviderCapabilities'
import { authUrl } from './accountOAuthPayload'
import { accountOperationScopeParams, type AccountScopeParams } from './accountOperationScope'
import { normalizedAccountApiKeys } from './accountCredentials'
import { buildAccountDraftTestPayload } from './accountDraftTestPayload'
import {
  draftApiKeyTestRuntimeDetailsForPayload,
  type DraftApiKeyTestSnapshot
} from './accountDraftApiKeyTestRuntime'
import {
  AccountEditFormLoadError,
  buildAccountBasicEditFormLoad,
  buildAccountCloneFormLoad,
  buildAccountEditFormLoad
} from './accountEditFormLoaders'
import {
  buildAccountBasicEditSnapshot,
  type AccountBasicEditSnapshot
} from './accountEditPatch'
import { buildAccountSavePayload, type AccountSavePayload } from './accountSavePayload'
import { useAccountProviderModelOptions } from './useAccountProviderModelOptions'
import { providerModelsForProtocolProfile } from './accountEditFormPayload'
import { useAccountEditTagOptions } from './useAccountEditTagOptions'
import { useAccountEditSaveFlow } from './useAccountEditSaveFlow'
import type { AccountGroupOptionsLoadOptions, AccountGroupOptionsScope } from './useAccountGroupOptions'

type ReadonlyValue<T> = {
  readonly value: T
}

interface UseAccountEditFormOptions {
  accountScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  accounts: ReadonlyValue<AccountListItem[]>
  extractApiErrorMessage: (error: unknown, fallback: string) => string
  groupIdForAccount: (accountId: string) => string | undefined
  groups: ReadonlyValue<GroupOptionSummary[]>
  isManagementView: ComputedRef<boolean>
  ensureProviderDefinition: (providerCode: string, systemAccountId?: string, force?: boolean) => Promise<ProviderDefinition | undefined>
  loadGroupOptions: (keyword?: string, force?: boolean, scopeOverride?: Partial<AccountGroupOptionsScope>, loadOptions?: AccountGroupOptionsLoadOptions) => Promise<void>
  loadData: () => Promise<void>
  refreshAccountMutationRows: (mutation: AccountMutationResult) => Promise<void>
  focusCreatedAccount?: (account: AccountSummary) => void
  providerDefinitions: ReadonlyValue<ProviderDefinition[]>
  providers: ReadonlyValue<ProviderDefinition[]>
  draftApiKeyTestSnapshot?: { value: DraftApiKeyTestSnapshot | undefined }
  systemAccountSelection?: ReadonlyValue<PrincipalSelection | undefined>
  systemAccounts: ReadonlyValue<SystemAccountPrincipalSummary[]>
}

export function useAccountEditForm(options: UseAccountEditFormOptions) {
  const modalOpen = ref(false)
  const accountEditDetailLoading = ref(false)
  const accountAdvancedDetailLoading = ref(false)
  const accountAdvancedDetailLoaded = ref(false)
  const editingId = ref<string>()
  const editingAccountDetail = ref<AccountEditBasicDetail>()
  const editingAccountAdvancedDetail = ref<AccountAdvancedDetail>()
  const savedApiKeyRuntimeSnapshot = ref<SavedAccountApiKeyRuntimeSnapshot>()
  const accountApiKeyRuntimeLoading = ref(false)
  const editingBasicBaseline = ref<AccountBasicEditSnapshot>()
  const editingAdvancedBaseline = ref<AccountSavePayload>()
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
    providerModelOptions: loadedProviderModelOptions,
    providerModelsLoading,
    resetProviderModelOptions
  } = useAccountProviderModelOptions({
    currentProviderCode: () => form.providerCode,
    extractApiErrorMessage: options.extractApiErrorMessage,
    isManagementView: options.isManagementView,
    modelScopeParams: allProviderModelScopeParams
  })
  const {
    loading: allProviderModelsLoading,
    loadModelOptions: loadAllProviderModelOptions,
    resetModelOptions: resetAllProviderModelOptions,
    selectOptions: mappingSourceModelOptions
  } = useProviderModelSelectOptions({
    protocol: 'openai',
    scopeParams: allProviderModelScopeParams,
    onLoadError: (error) => {
      message.error(options.extractApiErrorMessage(error, '加载 OpenAI 协议模型池失败'))
    }
  })
  const {
    loading: anthropicProviderModelsLoading,
    loadModelOptions: loadAnthropicProviderModelOptions,
    resetModelOptions: resetAnthropicProviderModelOptions,
    selectOptions: mappingAnthropicSourceModelOptions
  } = useProviderModelSelectOptions({
    protocol: 'anthropic',
    scopeParams: allProviderModelScopeParams,
    onLoadError: (error) => {
      message.error(options.extractApiErrorMessage(error, '加载 Anthropic 协议模型池失败'))
    }
  })
  const {
    loading: geminiProviderModelsLoading,
    loadModelOptions: loadGeminiProviderModelOptions,
    resetModelOptions: resetGeminiProviderModelOptions,
    selectOptions: mappingGeminiSourceModelOptions
  } = useProviderModelSelectOptions({
    protocol: 'gemini',
    scopeParams: allProviderModelScopeParams,
    onLoadError: (error) => {
      message.error(options.extractApiErrorMessage(error, '加载 Gemini 协议模型池失败'))
    }
  })
  const strategyModelsLoading = computed(() => providerModelsLoading.value || allProviderModelsLoading.value || anthropicProviderModelsLoading.value || geminiProviderModelsLoading.value)
  const {
    accountTagOptions,
    accountTagOptionsLoading,
    deleteAccountTag,
    deletingAccountTagId,
    loadAccountTagOptions,
    resetAccountTagOptions
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

  const groupOptions = computed(() => groupOptionsForProviderWithSelected(options.groups.value, form.providerCode, [form.groupId]))
  const availableProviders = computed(mergedProviderDefinitions)
  const selectedProvider = computed(() => availableProviders.value.find((provider) => provider.code === form.providerCode))
  const selectedProtocolProfile = computed(() => selectedProvider.value
    ? selectedProvider.value.protocolProfiles.find((profile) => profile.id === form.providerProtocolProfileId)
      ?? selectedProvider.value.protocolProfiles.find((profile) => profile.id === selectedProvider.value?.defaultProtocolProfileId)
      ?? selectedProvider.value.protocolProfiles[0]
    : undefined)
  const providerModelOptions = computed(() => providerModelsForProtocolProfile(
    loadedProviderModelOptions.value,
    selectedProtocolProfile.value,
    form.type
  ))
  const mappingCurrentProviderSourceModelOptions = computed(() => loadedProviderModelOptions.value)
  const accountTypeChoices = computed(() => accountTypeChoicesForProvider(selectedProvider.value, availableProviders.value))
  const selectedAccountTypeChoice = computed(() => accountTypeChoices.value.find((choice) => choice.type === form.type && choice.providerProtocolProfileId === form.providerProtocolProfileId))
  const hasAccountType = computed(() => Boolean(form.providerCode && form.providerProtocolProfileId && form.type))
  const isApiKeyForm = computed(() => hasAccountType.value && form.type === 'api_key')
  const isOAuthForm = computed(() => hasAccountType.value && form.type === 'oauth')
  const isTokenCredentialForm = computed(() => hasAccountType.value && ['oauth', 'google_oauth'].includes(form.type))
  const isSupportedOAuthForm = computed(() => form.type === 'oauth' && supportsOAuthAccountType({
    provider: selectedProvider.value,
    profile: selectedProtocolProfile.value
  }))
  const isOpenAIOAuthForm = computed(() => form.type === 'oauth' && canCreateOAuthAccount({
    provider: selectedProvider.value,
    profile: selectedProtocolProfile.value
  }))
  const isAnthropicOAuthForm = computed(() => isSupportedOAuthForm.value
    && !isOpenAIOAuthForm.value
    && accountProviderProtocolKind(selectedProtocolProfile.value) === 'anthropic_v1')
  const editingAuthorizedAccount = computed(() => {
    const account = options.accounts.value.find((item) => item.id === editingId.value)
    return Boolean(editingId.value && account && isAuthorizedAccount(account))
  })
  const {
    authLoading,
    authResult,
    generateOAuthUrl,
    saveAccount,
    saving
  } = useAccountEditSaveFlow({
    accountAdvancedDetailLoaded,
    accountErrorPolicyRules,
    accountResponseInspectionRules,
    accounts: options.accounts,
    createScopeParams,
    editingAccountDetail,
    editingAccountAdvancedDetail,
    editingAccountScopeParams,
    editingAuthorizedAccount,
    editingAdvancedBaseline,
    editingBasicBaseline,
    editingId,
    extractApiErrorMessage: options.extractApiErrorMessage,
    form,
    isManagementView: options.isManagementView,
    loadData: options.loadData,
    refreshAccountMutationRows: options.refreshAccountMutationRows,
    mappingAnthropicSourceModelOptions,
    mappingCurrentProviderSourceModelOptions,
    mappingGeminiSourceModelOptions,
    mappingSourceModelOptions,
    providerModelOptions,
    modalOpen,
    providers: availableProviders
  })
  const editingSystemAccountLabel = computed(() => {
    if (!options.isManagementView.value) return ''
    const listAccount = options.accounts.value.find((item) => item.id === editingId.value)
    const account = listAccount ?? editingAccountDetail.value
    const accountLabel = listAccount?.systemAccountName?.trim()
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
    disabled: accountEditDetailLoading.value || accountAdvancedDetailLoading.value || saving.value || !hasAccountType.value || (!editingId.value && isOAuthForm.value && !isSupportedOAuthForm.value)
  }))
  const selectedAccountTypeTitle = computed(() => hasAccountType.value ? selectedAccountTypeChoice.value?.label ?? accountTypeTitle(form.providerCode, form.type) : '')
  const apiKeyTestDetails = computed<AccountApiKeyRuntimeDetail[] | undefined>(() => {
    if (!modalOpen.value || accountEditDetailLoading.value || !isApiKeyForm.value || editingAuthorizedAccount.value) return undefined
    return draftApiKeyTestRuntimeDetailsForPayload(options.draftApiKeyTestSnapshot?.value, currentDraftTestPayload())
  })
  const accountApiKeyRuntimeDetails = computed<AccountApiKeyRuntimeDetail[] | undefined>(() => (
    visibleSavedAccountApiKeyRuntimeDetails(savedApiKeyRuntimeSnapshot.value, form.apiKeys)
  ))

  function defaultForm(providerCode = '', type: AccountType = '', providerProtocolProfileId = ''): AccountFormModel {
    return defaultAccountForm(providerCode, type, mergedProviderDefinitions(), providerProtocolProfileId)
  }

  function mergedProviderDefinitions(): ProviderDefinition[] {
    return mergeAccountProviderDefinitions(
      options.providers.value.length ? options.providers.value : FALLBACK_PROVIDERS,
      options.providerDefinitions.value
    )
  }

  function resetForm(providerCode = '', type: AccountType = '') {
    accountEditDetailLoading.value = false
    accountAdvancedDetailLoading.value = false
    accountAdvancedDetailLoaded.value = false
    cloningSourceId.value = undefined
    editingScheduleFingerprint.value = undefined
    cloningScheduleFingerprint.value = undefined
    savedApiKeyRuntimeSnapshot.value = undefined
    accountApiKeyRuntimeLoading.value = false
    editingBasicBaseline.value = undefined
    editingAdvancedBaseline.value = undefined
    editingAccountAdvancedDetail.value = undefined
    clearDraftApiKeyTestSnapshot()
    Object.assign(form, defaultForm(providerCode, type))
    resetDeferredAccountOptionState()
    ensureDefaultGroupSelected(form.providerCode)
    accountErrorPolicyRules.value = loadAccountErrorPolicyRules()
    accountResponseInspectionRules.value = loadAccountResponseInspectionRules()
    authResult.value = undefined
  }

  function accountTypeTitle(providerCode: string, type: AccountType) {
    return accountEditAccountTypeTitle(providerCode, type, availableProviders.value)
  }

  function providerName(providerCode?: string) {
    const listName = options.accounts.value.find((account) => account.providerCode === providerCode)?.providerName
    if (listName) return listName
    return accountEditProviderName(providerCode, availableProviders.value)
  }

  function defaultGroupForProvider(providerCode: string) {
    return selectDefaultGroupForProvider(options.groups.value, providerCode)
  }

  function ensureDefaultGroupSelected(providerCode = form.providerCode) {
    if (!providerCode) {
      form.groupId = undefined
      form.group = undefined
      return
    }
    const currentGroup = options.groups.value.find((group) => group.id === form.groupId)
    if (currentGroup && isManageableGroupForProvider(currentGroup, providerCode)) {
      form.group = { id: currentGroup.id, name: currentGroup.name }
      return
    }
    const nextGroup = defaultGroupForProvider(providerCode)
    setFormGroup(nextGroup ? { id: nextGroup.id, name: nextGroup.name } : undefined)
  }

  async function openCreate() {
    const requestToken = nextFormOpenRequestToken()
    if (options.isManagementView.value && !options.accountScopeParams.value?.systemAccountId) {
      message.warning('请先在右侧选择目标系统账户，再创建 AI 账户')
      return
    }
    editingId.value = undefined
    editingAccountDetail.value = undefined
    editingAccountAdvancedDetail.value = undefined
    editingScheduleFingerprint.value = undefined
    cloningScheduleFingerprint.value = undefined
    creatingAccountScopeParams.value = undefined
    if (!isCurrentFormOpenRequest(requestToken)) return
    resetForm('', '')
    applyCachedDefaultGroup()
    accountAdvancedDetailLoaded.value = true
    accountAdvancedDetailLoading.value = false
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

  watch(
    [
      () => [...form.supportedEndpointModes],
      () => form.providerCode,
      () => form.providerProtocolProfileId
    ],
    () => {
      const available = accountHealthCheckEndpointModeOptions(form.supportedEndpointModes)
      if (available.some((option) => option.value === form.healthCheckEndpointMode)) return
      form.healthCheckEndpointMode = defaultAccountHealthCheckEndpointMode(
        form.providerCode,
        form.providerProtocolProfileId,
        form.supportedEndpointModes
      )
    },
    { immediate: true }
  )

  watch(
    [
      () => [...form.supportedModels],
      () => form.providerCode,
      () => form.providerProtocolProfileId
    ],
    () => {
      const supportedModels = [...new Set(form.supportedModels.map((model) => model.trim()).filter(Boolean))]
      const current = form.healthCheckModel.trim()
      if (!supportedModels.includes(current)) form.healthCheckModel = supportedModels[0] ?? ''
    },
    { immediate: true }
  )

  function handleModalCancel() {
    nextFormOpenRequestToken()
    accountEditDetailLoading.value = false
    accountAdvancedDetailLoading.value = false
    accountAdvancedDetailLoaded.value = false
    modalOpen.value = false
    authResult.value = undefined
    clearDraftApiKeyTestSnapshot()
    savedApiKeyRuntimeSnapshot.value = undefined
    accountApiKeyRuntimeLoading.value = false
    editingBasicBaseline.value = undefined
    editingAdvancedBaseline.value = undefined
    editingAccountAdvancedDetail.value = undefined
    resetDeferredAccountOptionState()
  }

  function selectProvider(providerCode: string) {
    if (editingId.value || form.providerCode === providerCode) return
    const requestToken = formOpenRequestToken
    const systemAccountId = createScopeParams.value?.systemAccountId
    resetForm(providerCode, '')
    const initialDefaults = providerDefaultState(form)
    applyCachedDefaultGroup(providerCode)
    void applyProviderDefinitionDefaultsAfterLoad({
      ensureDefinition: () => options.ensureProviderDefinition(providerCode, systemAccountId),
      form,
      initialDefaults,
      isCurrent: () => isCurrentFormOpenRequest(requestToken) && modalOpen.value && form.providerCode === providerCode,
      resolvedDefaults: () => defaultForm(providerCode, '')
    }).catch((error) => {
      if (!isCurrentFormOpenRequest(requestToken) || !modalOpen.value || form.providerCode !== providerCode) return
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '加载供应商账户类型失败'))
    })
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
    const keepCurrentGroup = form.providerCode === providerCode
    const defaults = defaultForm(providerCode, choice.type, providerProtocolProfileId)
    Object.assign(form, {
      ...defaults,
      groupId: keepCurrentGroup ? form.groupId : undefined,
      group: keepCurrentGroup ? form.group : undefined,
      proxyProfileId: form.proxyProfileId,
      notes: form.notes,
      supportedModels: form.supportedModels.length ? form.supportedModels : defaults.supportedModels,
      serviceTierOverride: form.serviceTierOverride,
      reasoningEffortOverride: form.reasoningEffortOverride,
      modelMappings: form.modelMappings,
      tags: form.tags,
      concurrencyLimit: form.concurrencyLimit,
      priority: form.priority,
      privilege: form.privilege,
      status: form.status,
      accountExpiresAt: form.accountExpiresAt,
      availabilitySchedule: form.availabilitySchedule
    })
    applyCachedDefaultGroup(providerCode)
    authResult.value = undefined
  }

  function applyCachedDefaultGroup(providerCode = form.providerCode): void {
    if (!providerCode || form.groupId) return
    const referenceParams = {
      viewScope: options.isManagementView.value ? 'admin' : 'self',
      systemAccountId: createScopeParams.value?.systemAccountId
    } as const
    const referenceData = getCachedUserReferenceData(referenceParams)
    const defaultGroup = referenceData?.providerDefaults
      .find((item) => item.providerCode === providerCode)
      ?.defaultGroup
    if (!defaultGroup) {
      ensureDefaultGroupSelected(providerCode)
      return
    }
    setFormGroup({ id: defaultGroup.id, name: defaultGroup.name })
    rememberGroupLabel(defaultGroup.id, defaultGroup.name)
  }

  async function openEdit(account: AccountListItem) {
    const requestToken = nextFormOpenRequestToken()
    const editScopeParams = accountOperationScopeParams(account, options.accountScopeParams.value)
    editingId.value = account.id
    editingAccountDetail.value = undefined
    editingAccountAdvancedDetail.value = undefined
    accountAdvancedDetailLoaded.value = false
    accountAdvancedDetailLoading.value = false
    editingScheduleFingerprint.value = undefined
    cloningSourceId.value = undefined
    cloningScheduleFingerprint.value = undefined
    creatingAccountScopeParams.value = undefined
    accountEditDetailLoading.value = false
    authResult.value = undefined
    clearDraftApiKeyTestSnapshot()
    savedApiKeyRuntimeSnapshot.value = undefined
    accountApiKeyRuntimeLoading.value = false
    editingBasicBaseline.value = undefined
    editingAdvancedBaseline.value = undefined
    resetDeferredAccountOptionState()

    if (isAuthorizedAccount(account)) {
      accountEditDetailLoading.value = true
      modalOpen.value = true
      const advancedDetail = await loadAccountDetailForForm(account.id, editScopeParams, '加载账户详情失败')
      if (!isCurrentFormOpenRequest(requestToken)) return
      if (!advancedDetail || advancedDetail.accessType !== 'authorized') {
        accountEditDetailLoading.value = false
        modalOpen.value = false
        editingId.value = undefined
        editingAccountDetail.value = undefined
        editingAccountAdvancedDetail.value = undefined
        return
      }
      const sourceAccount = authorizedAccountBasicDetail(account, advancedDetail)
      if (!applyLoadedAccountDetailToEditForm(
        sourceAccount,
        advancedDetail,
        editScopeParams,
        '账户数据结构异常，请清理后再编辑'
      )) {
        accountEditDetailLoading.value = false
        modalOpen.value = false
        editingId.value = undefined
        editingAccountDetail.value = undefined
        editingAccountAdvancedDetail.value = undefined
        return
      }
      accountAdvancedDetailLoaded.value = true
      accountEditDetailLoading.value = false
      modalOpen.value = true
      return
    }

    accountEditDetailLoading.value = true
    modalOpen.value = true
    const basicDetailRequest = loadAccountDetailForForm(account.id, editScopeParams, '加载账户基础配置失败', 'edit-basic')
    const sourceAccount = await basicDetailRequest
    if (!isCurrentFormOpenRequest(requestToken)) return
    if (!sourceAccount) {
      accountEditDetailLoading.value = false
      modalOpen.value = false
      editingId.value = undefined
      editingAccountDetail.value = undefined
      editingAccountAdvancedDetail.value = undefined
      return
    }
    if (!applyLoadedAccountDetailToEditForm(
      sourceAccount,
      undefined,
      editScopeParams,
      '账户基础配置结构异常，请清理后再编辑'
    )) {
      accountEditDetailLoading.value = false
      modalOpen.value = false
      editingId.value = undefined
      editingAccountDetail.value = undefined
      editingAccountAdvancedDetail.value = undefined
      return
    }
    accountAdvancedDetailLoaded.value = false
    accountEditDetailLoading.value = false
  }

  async function ensureAccountEditDetailLoaded(): Promise<boolean> {
    if (!editingId.value) return true
    const requestToken = formOpenRequestToken
    while (accountEditDetailLoading.value && isCurrentFormOpenRequest(requestToken)) {
      await sleep(50)
    }
    return isCurrentFormOpenRequest(requestToken) && Boolean(editingAccountDetail.value)
  }

  async function loadAdvancedAccountDetail(): Promise<boolean> {
    if (!editingId.value) return true
    if (editingAuthorizedAccount.value) return accountAdvancedDetailLoaded.value
    if (accountAdvancedDetailLoaded.value) return true
    if (accountAdvancedDetailLoading.value) {
      const requestToken = formOpenRequestToken
      while (accountAdvancedDetailLoading.value && isCurrentFormOpenRequest(requestToken)) {
        await sleep(50)
      }
      return isCurrentFormOpenRequest(requestToken) && accountAdvancedDetailLoaded.value
    }
    const requestToken = formOpenRequestToken
    const scopeParams = editingAccountScopeParams()
    accountAdvancedDetailLoading.value = true
    const advancedDetail = await loadAccountDetailForForm(editingId.value, scopeParams, '加载账户高级配置失败')
    if (!isCurrentFormOpenRequest(requestToken)) return false
    const sourceAccount = editingAccountDetail.value
    if (!advancedDetail || !sourceAccount || advancedDetail.accessType !== 'owner') {
      accountAdvancedDetailLoading.value = false
      return false
    }
    if (applyLoadedAccountDetailToEditForm(
      sourceAccount,
      advancedDetail,
      scopeParams,
      '账户高级配置结构异常，请清理后再编辑',
      true
    )) {
      accountAdvancedDetailLoaded.value = true
    }
    accountAdvancedDetailLoading.value = false
    return accountAdvancedDetailLoaded.value
  }

  async function loadCurrentProviderModelOptions(keyword = ''): Promise<void> {
    await loadProviderModelOptions(form.providerCode, {
      keyword,
      selectedIds: [
        ...form.supportedModels,
        ...form.modelMappings.map((mapping) => mapping.sourceModel)
      ]
    })
  }

  async function loadMappingSourceModelOptions(protocol: 'openai' | 'anthropic' | 'gemini', keyword = ''): Promise<void> {
    const selectedIds = form.modelMappings
      .filter((mapping) => sourceProtocol(mapping.sourceEndpointFamily) === protocol)
      .map((mapping) => mapping.sourceModel.trim())
      .filter(Boolean)
    const request = {
      ...(keyword.trim() ? { keyword: keyword.trim() } : {}),
      selectedIds
    }
    if (protocol === 'anthropic') {
      await loadAnthropicProviderModelOptions(request)
      return
    }
    if (protocol === 'gemini') {
      await loadGeminiProviderModelOptions(request)
      return
    }
    await loadAllProviderModelOptions(request)
  }

  function sourceProtocol(endpointFamily: AccountFormModel['modelMappings'][number]['sourceEndpointFamily']): 'openai' | 'anthropic' | 'gemini' {
    if (endpointFamily === 'messages') return 'anthropic'
    if (endpointFamily === 'generate_content' || endpointFamily === 'stream_generate_content') return 'gemini'
    return 'openai'
  }

  function applyLoadedAccountDetailToEditForm(
    sourceAccount: AccountEditBasicDetail,
    advancedDetail: AccountAdvancedDetail | undefined,
    _editScopeParams: AccountScopeParams | undefined,
    errorMessage: string,
    preserveBasicFields = false
  ): boolean {
    const preserveTypedApiKeys = form.type === 'api_key' && normalizedAccountApiKeys(form).length > 0
    const preserveTypedOAuthTokens = (form.type === 'oauth' || form.type === 'google_oauth')
      && Boolean(form.accessToken.trim() || form.refreshToken.trim() || form.ssoTokens.trim())
    const preservedBasic = preserveBasicFields
      ? {
          name: form.name,
          groupId: form.groupId,
          group: form.group,
          concurrencyLimit: form.concurrencyLimit,
          priority: form.priority,
          privilege: form.privilege,
          status: form.status,
          tags: [...form.tags],
          notes: form.notes,
          baseUrl: form.baseUrl,
          supportedEndpointModes: [...form.supportedEndpointModes],
          supportedModels: [...form.supportedModels],
          healthCheckModel: form.healthCheckModel,
          healthCheckEndpointMode: form.healthCheckEndpointMode,
          ...(preserveTypedApiKeys
            ? {
                apiKey: form.apiKey,
                apiKeys: [...form.apiKeys],
                apiKeyStrategy: form.apiKeyStrategy,
                apiKeyWeights: [...form.apiKeyWeights]
              }
            : {}),
          ...(preserveTypedOAuthTokens
            ? {
                accessToken: form.accessToken,
                refreshToken: form.refreshToken,
                ssoTokens: form.ssoTokens,
                googleClientId: form.googleClientId,
                googleClientSecret: form.googleClientSecret,
                googleQuotaProjectId: form.googleQuotaProjectId,
                oauthType: form.oauthType,
                tierId: form.tierId,
                projectId: form.projectId
              }
            : {})
        }
      : undefined
    const defaults = defaultForm(sourceAccount.providerCode, sourceAccount.type, sourceAccount.providerProtocolProfileId)
    const selectedGroup = sourceAccount.boundGroupId
      ? groupSelectionForId(sourceAccount.boundGroupId, sourceAccount.boundGroupName)
      : undefined
    const fallbackGroupId = options.groupIdForAccount(sourceAccount.id)
    const commonLoadInput = {
      credentials: {
        ...sourceAccount.credentials,
        ...(advancedDetail?.credentials ?? {})
      },
      defaults,
      fallbackGroupId,
      selectedGroup,
      allowMissingBaseUrl: advancedDetail?.accessType === 'authorized'
    }
    let formPatch: AccountFormModel
    let advancedLoad: ReturnType<typeof buildAccountEditFormLoad> | undefined
    try {
      if (!advancedDetail) {
        formPatch = buildAccountBasicEditFormLoad({
          ...commonLoadInput,
          account: sourceAccount
        }).patch
      } else {
        advancedLoad = buildAccountEditFormLoad({
          ...commonLoadInput,
          account: sourceAccount,
          advanced: advancedDetail
        })
        formPatch = advancedLoad.patch
      }
    } catch (error) {
      reportAccountFormLoadError(error, errorMessage)
      return false
    }
    const basicBaseline = advancedDetail
      ? undefined
      : buildAccountBasicEditSnapshot(formPatch, sourceAccount.credentials)
    const advancedBaseline = advancedLoad
      ? buildAccountSavePayload({
          accounts: options.accounts.value,
          accountDetail: sourceAccount,
          editingId: sourceAccount.id,
          form: advancedLoad.patch,
          errorPolicyRules: advancedLoad.errorPolicyRules,
          responseInspectionRules: advancedLoad.responseInspectionRules
        })
      : undefined
    editingId.value = sourceAccount.id
    editingAccountDetail.value = sourceAccount
    editingAccountAdvancedDetail.value = advancedDetail
    cloningSourceId.value = undefined
    creatingAccountScopeParams.value = undefined
    Object.assign(form, formPatch)
    if (preservedBasic) {
      Object.assign(form, preservedBasic)
    }
    if (basicBaseline) editingBasicBaseline.value = basicBaseline
    editingAdvancedBaseline.value = advancedDetail?.accessType === 'owner' ? advancedBaseline : undefined
    editingScheduleFingerprint.value = advancedLoad?.scheduleFingerprint
    cloningScheduleFingerprint.value = undefined
    accountErrorPolicyRules.value = advancedLoad?.errorPolicyRules ?? loadAccountErrorPolicyRules()
    accountResponseInspectionRules.value = advancedLoad?.responseInspectionRules ?? loadAccountResponseInspectionRules()
    authResult.value = undefined
    return true
  }

  async function openClone(account: AccountListItem) {
    const requestToken = nextFormOpenRequestToken()
    savedApiKeyRuntimeSnapshot.value = undefined
    accountApiKeyRuntimeLoading.value = false
    editingBasicBaseline.value = undefined
    editingAdvancedBaseline.value = undefined
    editingAccountAdvancedDetail.value = undefined
    resetDeferredAccountOptionState()
    const cloneScopeParams = accountOperationScopeParams(account, options.accountScopeParams.value)
    if (options.isManagementView.value && !cloneScopeParams?.systemAccountId) {
      message.warning('无法确定克隆目标系统账户，请先筛选目标系统账户后再克隆')
      return
    }
    let sourceAccount
    try {
      sourceAccount = options.isManagementView.value
        ? await api.accounts.cloneContext(account.id, cloneScopeParams)
        : await api.myAccounts.cloneContext(account.id)
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '加载克隆账户配置失败'))
      return
    }
    if (!isCurrentFormOpenRequest(requestToken)) return
    accountEditDetailLoading.value = false
    editingId.value = undefined
    editingAccountDetail.value = undefined
    editingAccountAdvancedDetail.value = undefined
    editingScheduleFingerprint.value = undefined
    cloningSourceId.value = sourceAccount.id
    creatingAccountScopeParams.value = cloneScopeParams
    const defaults = defaultForm(sourceAccount.providerCode, sourceAccount.type, sourceAccount.providerProtocolProfileId)
    const selectedGroup = sourceAccount.boundGroupId
      ? groupSelectionForId(sourceAccount.boundGroupId, sourceAccount.boundGroupName)
      : undefined
    let formLoad: ReturnType<typeof buildAccountCloneFormLoad>
    try {
      formLoad = buildAccountCloneFormLoad({
        account: sourceAccount,
        defaults,
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
  }

  async function loadAccountDetailForForm(
    accountId: string,
    scopeParams: AccountScopeParams | undefined,
    fallbackMessage: string,
    level: 'edit-basic'
  ): Promise<AccountEditBasicDetail | undefined>
  async function loadAccountDetailForForm(
    accountId: string,
    scopeParams: AccountScopeParams | undefined,
    fallbackMessage: string,
    level?: 'advanced'
  ): Promise<AccountAdvancedDetail | undefined>
  async function loadAccountDetailForForm(
    accountId: string,
    scopeParams: AccountScopeParams | undefined,
    fallbackMessage: string,
    level: AccountDetailLevel = 'advanced'
  ): Promise<AccountEditBasicDetail | AccountAdvancedDetail | undefined> {
    try {
      if (options.isManagementView.value) {
        return level === 'edit-basic'
          ? await api.accounts.editBasicDetail(accountId, scopeParams)
          : await api.accounts.advancedDetail(accountId, scopeParams)
      }
      return level === 'edit-basic'
        ? await api.myAccounts.editBasicDetail(accountId)
        : await api.myAccounts.advancedDetail(accountId)
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, fallbackMessage))
      return undefined
    }
  }

  async function fetchAccountApiKeyRuntimeForEdit(
    accountId: string,
    scopeParams: AccountScopeParams | undefined
  ): Promise<AccountApiKeyRuntimeResponse | undefined> {
    try {
      return options.isManagementView.value
        ? await api.accounts.apiKeyRuntime(accountId, scopeParams)
        : await api.myAccounts.apiKeyRuntime(accountId)
    } catch (error) {
      console.error(error)
      return undefined
    }
  }

  async function loadAccountApiKeyRuntimeDetails(): Promise<void> {
    const account = editingAccountDetail.value
    const apiKeys = normalizedAccountApiKeys(form)
    if (!account || form.type !== 'api_key' || apiKeys.length < 2 || accountApiKeyRuntimeLoading.value) return
    if (visibleSavedAccountApiKeyRuntimeDetails(savedApiKeyRuntimeSnapshot.value, apiKeys)) return
    const configRevision = account.configRevision
    if (typeof configRevision !== 'number' || !Number.isInteger(configRevision) || configRevision < 1) {
      message.error('账户配置版本缺失或无效，请关闭弹窗并刷新列表后重试')
      return
    }
    const requestToken = formOpenRequestToken
    accountApiKeyRuntimeLoading.value = true
    try {
      const response = await fetchAccountApiKeyRuntimeForEdit(account.id, editingAccountScopeParams())
      if (!response || !isCurrentFormOpenRequest(requestToken) || editingAccountDetail.value?.id !== account.id) return
      savedApiKeyRuntimeSnapshot.value = createSavedAccountApiKeyRuntimeSnapshot({
        accountId: account.id,
        configRevision,
        apiKeys,
        response
      })
    } finally {
      if (isCurrentFormOpenRequest(requestToken)) accountApiKeyRuntimeLoading.value = false
    }
  }

  function handleAccountTagOptionsDropdown(open: boolean): void {
    if (open) void loadAccountTagOptions(accountTagOperationScopeParams())
  }

  function nextFormOpenRequestToken(): number {
    formOpenRequestToken += 1
    return formOpenRequestToken
  }

  function isCurrentFormOpenRequest(requestToken: number): boolean {
    return requestToken === formOpenRequestToken
  }

  function resetDeferredAccountOptionState(): void {
    resetProviderModelOptions()
    resetAllProviderModelOptions()
    resetAnthropicProviderModelOptions()
    resetGeminiProviderModelOptions()
    resetAccountTagOptions()
  }

  function openAuthUrl() {
    const url = authUrl(authResult.value?.authUrl)
    if (!url) return
    window.open(url, '_blank', 'noopener,noreferrer')
  }

  onBeforeUnmount(() => {
    nextFormOpenRequestToken()
    resetDeferredAccountOptionState()
  })

  return {
    accountAdvancedDetailLoaded,
    accountAdvancedDetailLoading,
    accountEditDetailLoading,
    accountErrorPolicyRules,
    accountResponseInspectionRules,
    accountApiKeyRuntimeDetails,
    accountApiKeyRuntimeLoading,
    apiKeyTestDetails,
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
    editingAccountAdvancedDetail,
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
    isAnthropicOAuthForm,
    isOAuthForm,
    isOpenAIOAuthForm,
    isSupportedOAuthForm,
    isTokenCredentialForm,
    mappingAnthropicSourceModelOptions,
    mappingCurrentProviderSourceModelOptions,
    mappingGeminiSourceModelOptions,
    mappingSourceModelOptions,
    modalConfirmLoading,
    modalOkButtonProps,
    modalOpen,
    modalTitle,
    openAuthUrl,
    openClone,
    openCreate,
    openEdit,
    ensureAccountEditDetailLoaded,
    loadAdvancedAccountDetail,
    loadAccountApiKeyRuntimeDetails,
    loadCurrentProviderModelOptions,
    loadMappingSourceModelOptions,
    handleAccountTagOptionsDropdown,
    providerName,
    providerModelOptions,
    providerModelsLoading,
    currentDraftTestPayload,
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

  function editingAccountScopeParams(): AccountScopeParams {
    if (!editingId.value) return options.accountScopeParams.value
    const listAccount = options.accounts.value.find((item) => item.id === editingId.value)
    if (listAccount) return accountOperationScopeParams(listAccount, options.accountScopeParams.value)
    const detail = editingAccountDetail.value
    const systemAccountId = detail?.systemAccountId
      ?? detail?.ownerSystemAccountId
      ?? options.accountScopeParams.value?.systemAccountId
    return systemAccountId ? { systemAccountId } : undefined
  }

  function accountTagOperationScopeParams(): AccountScopeParams {
    return editingId.value ? editingAccountScopeParams() : createScopeParams.value
  }

  function clearDraftApiKeyTestSnapshot(): void {
    if (options.draftApiKeyTestSnapshot) {
      options.draftApiKeyTestSnapshot.value = undefined
    }
  }

  function currentDraftTestPayload(): AccountDraftTestAccountPayload | undefined {
    try {
      return buildAccountDraftTestPayload({
        accounts: options.accounts.value,
        accountDetail: editingAccountDetail.value,
        editingId: editingId.value,
        form,
        errorPolicyRules: accountErrorPolicyRules.value,
        responseInspectionRules: accountResponseInspectionRules.value,
        mappingAnthropicSourceModelOptions: mappingAnthropicSourceModelOptions.value,
        mappingCurrentProviderSourceModelOptions: mappingCurrentProviderSourceModelOptions.value,
        mappingGeminiSourceModelOptions: mappingGeminiSourceModelOptions.value,
        mappingSourceModelOptions: mappingSourceModelOptions.value,
        mappingUpstreamModelOptions: providerModelOptions.value,
        providers: availableProviders.value
      })
    } catch {
      return undefined
    }
  }
}

function mergeAccountProviderDefinitions(
  providerOptions: ProviderDefinition[],
  loadedDefinitions: ProviderDefinition[]
): ProviderDefinition[] {
  const definitionsByCode = new Map<string, ProviderDefinition>()
  for (const provider of [...FALLBACK_PROVIDERS, ...loadedDefinitions]) {
    definitionsByCode.set(provider.code, provider)
  }
  return providerOptions.map((provider) => {
    const definition = definitionsByCode.get(provider.code)
    if (!definition) return provider
    return {
      ...definition,
      id: provider.id,
      code: provider.code,
      name: provider.name,
      enabled: provider.enabled
    }
  })
}

type ProviderDefaultState = Pick<
  AccountFormModel,
  | 'providerProtocolProfileId'
  | 'type'
  | 'baseUrl'
  | 'clientCompatibility'
  | 'supportedEndpointModes'
  | 'healthCheckEndpointMode'
  | 'oauthMode'
>

interface ApplyProviderDefinitionDefaultsInput {
  ensureDefinition: () => Promise<ProviderDefinition | undefined>
  form: ProviderDefaultState
  initialDefaults: ProviderDefaultState
  isCurrent: () => boolean
  resolvedDefaults: () => ProviderDefaultState
}

export async function applyProviderDefinitionDefaultsAfterLoad(
  input: ApplyProviderDefinitionDefaultsInput
): Promise<void> {
  const definition = await input.ensureDefinition()
  if (!definition || !input.isCurrent() || !providerDefaultsEqual(input.form, input.initialDefaults)) return
  Object.assign(input.form, providerDefaultState(input.resolvedDefaults()))
}

function providerDefaultState(form: ProviderDefaultState): ProviderDefaultState {
  return {
    providerProtocolProfileId: form.providerProtocolProfileId,
    type: form.type,
    baseUrl: form.baseUrl,
    clientCompatibility: form.clientCompatibility,
    supportedEndpointModes: [...form.supportedEndpointModes],
    healthCheckEndpointMode: form.healthCheckEndpointMode,
    oauthMode: form.oauthMode
  }
}

function providerDefaultsEqual(left: ProviderDefaultState, right: ProviderDefaultState): boolean {
  return left.providerProtocolProfileId === right.providerProtocolProfileId
    && left.type === right.type
    && left.baseUrl === right.baseUrl
    && left.clientCompatibility === right.clientCompatibility
    && left.healthCheckEndpointMode === right.healthCheckEndpointMode
    && left.oauthMode === right.oauthMode
    && left.supportedEndpointModes.length === right.supportedEndpointModes.length
    && left.supportedEndpointModes.every((mode, index) => mode === right.supportedEndpointModes[index])
}

function authorizedAccountBasicDetail(
  account: AccountListItem,
  advanced: AccountAdvancedDetail
): AccountEditBasicDetail {
  const supportedModels = [...new Set([
    account.healthCheckModel,
    ...advanced.modelMappings.map((mapping) => mapping.upstreamModel)
  ].map((model) => model.trim()).filter(Boolean))]
  return {
    id: account.id,
    configRevision: advanced.configRevision,
    systemAccountId: account.systemAccountId,
    ownerSystemAccountId: account.ownerSystemAccountId ?? account.systemAccountId ?? '',
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId ?? '',
    protocolCode: account.protocolCode ?? '',
    protocolVersion: account.protocolVersion ?? '',
    name: account.name,
    notes: account.notes,
    type: account.type,
    credentials: {},
    status: account.status,
    concurrencyLimit: account.concurrencyLimit,
    priority: account.priority,
    superPriorityEnabled: account.superPriorityEnabled,
    fallbackEnabled: account.fallbackEnabled,
    clientCompatibility: account.clientCompatibility,
    supportedModels,
    tags: [...(account.tags ?? [])],
    healthCheckModel: account.healthCheckModel,
    healthCheckEndpointMode: account.healthCheckEndpointMode,
    boundGroupId: account.boundGroupId,
    boundGroupName: account.boundGroupName
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, ms))
}
type AccountDetailLevel = 'edit-basic' | 'advanced'

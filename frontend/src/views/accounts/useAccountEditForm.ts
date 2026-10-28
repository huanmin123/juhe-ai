import { message } from '@/lib/antd'
import { computed, nextTick, onBeforeUnmount, reactive, ref, watch, type ComputedRef } from 'vue'

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
  groupOptionsWithSelected,
  resolveAccountDefaultGroupSelection,
  targetSystemAccountLabel as buildTargetSystemAccountLabel
} from './accountDerivedState'
import {
  loadAccountErrorPolicyRules
} from './accountErrorPolicyPayload'
import {
  systemInheritedErrorPolicyRulesPreview,
  type AccountErrorPolicyInheritedRule,
  type AccountErrorPolicyRuleForm
} from './accountErrorPolicyTypes'
import {
  loadAccountResponseInspectionRules
} from './accountResponseInspectionPolicyPayload'
import { loadAccountQuotaRecoveryPolicy } from './accountQuotaRecoveryPolicyTypes'
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
import { DOMAIN_MODEL_MODES, capabilityDomainIsMedia, defaultHealthCheckEndpointModeForCapabilityDomain, preferredMediaDomainProviderCode, type AccountCapabilityDomain } from './accountCapabilityDomains'
import { cachedMediaDomainModelAvailability } from './mediaDomainModelAvailability'
import { useAccountEditTagOptions } from './useAccountEditTagOptions'
import { useAccountEditSaveFlow } from './useAccountEditSaveFlow'
import type { AccountGroupOptionsLoadOptions, AccountGroupOptionsScope } from './useAccountGroupOptions'

type ReadonlyValue<T> = {
  readonly value: T
}

export interface UseAccountEditFormOptions {
  accountScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  accounts: ReadonlyValue<AccountListItem[]>
  /** 当前路由能力域（v8-D 媒体域新建默认供应商收敛依据；四域路由共用本组合式）。 */
  capabilityDomain: ComputedRef<AccountCapabilityDomain>
  extractApiErrorMessage: (error: unknown, fallback: string) => string
  groupIdForAccount: (accountId: string) => string | undefined
  groups: ReadonlyValue<GroupOptionSummary[]>
  isManagementView: ComputedRef<boolean>
  ensureProviderDefinition: (providerCode: string, systemAccountId?: string, force?: boolean) => Promise<ProviderDefinition | undefined>
  loadGroupOptions: (keyword?: string, force?: boolean, scopeOverride?: Partial<AccountGroupOptionsScope>, loadOptions?: AccountGroupOptionsLoadOptions) => Promise<void>
  loadData: () => Promise<void>
  refreshAccountMutationRows: (mutation: AccountMutationResult) => Promise<void>
  focusCreatedAccount?: (account: AccountSummary) => void
  /** 创建被「未选系统账户」guard 拦截时触发（页面可借此打开筛选抽屉引导选择）。 */
  onCreateGuardSystemAccount?: () => void
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
  const inheritedErrorPolicyRules = ref<AccountErrorPolicyInheritedRule[]>(systemInheritedErrorPolicyRulesPreview())
  const accountResponseInspectionRules = ref<AccountResponseInspectionRuleForm[]>(loadAccountResponseInspectionRules())
  let formOpenRequestToken = 0

  const createScopeParams = computed<AccountScopeParams>(() => creatingAccountScopeParams.value ?? options.accountScopeParams.value)
  const allProviderModelScopeParams = computed<AccountScopeParams>(() => editingId.value ? editingAccountScopeParams() : createScopeParams.value)
  const {
    loadProviderModelOptions,
    providerModelOptions: loadedProviderModelOptions,
    providerModelsLoading,
    providerModelOptionsSettled,
    providerModelOptionsTruncated,
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
    modelOptionsTruncated: allProviderModelOptionsTruncated,
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
    modelOptionsTruncated: anthropicProviderModelOptionsTruncated,
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
    modelOptionsTruncated: geminiProviderModelOptionsTruncated,
    resetModelOptions: resetGeminiProviderModelOptions,
    selectOptions: mappingGeminiSourceModelOptions
  } = useProviderModelSelectOptions({
    protocol: 'gemini',
    scopeParams: allProviderModelScopeParams,
    onLoadError: (error) => {
      message.error(options.extractApiErrorMessage(error, '加载 Gemini 协议模型池失败'))
    }
  })
  // T23：hybrid 账户的映射源模型下拉按协议取三实例候选；三实例是同一目录
  // （同 scope、同供应商窗口）的协议切片，服务端截断判定一致，取或即可。
  const mappingSourceModelOptionsTruncated = computed(() => (
    allProviderModelOptionsTruncated.value
    || anthropicProviderModelOptionsTruncated.value
    || geminiProviderModelOptionsTruncated.value
  ))
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

  const groupOptions = computed(() => groupOptionsWithSelected(options.groups.value, [form.groupId]))
  const availableProviders = computed(mergedProviderDefinitions)
  const selectedProvider = computed(() => availableProviders.value.find((provider) => provider.code === form.providerCode))
  const selectedProtocolProfile = computed(() => selectedProvider.value
    ? selectedProvider.value.protocolProfiles.find((profile) => profile.id === form.providerProtocolProfileId)
      ?? selectedProvider.value.protocolProfiles.find((profile) => profile.id === selectedProvider.value?.defaultProtocolProfileId)
      ?? selectedProvider.value.protocolProfiles[0]
    : undefined)
  // v8-C：目录 mode 服务端已按账户能力域过滤。对话域再叠协议层过滤
  // （providerModelsForProtocolProfile，gpt images 分支已随对话域纯粹化删除）；
  // 媒体域旁路聊天档案协议过滤——媒体行的协议 token（video/audio_speech/images）
  // 与对话档案 endpointFamilies 不相交，经其过滤会把域内模型清空，媒体域的协议
  // 正确性由 AccountMediaForm 的域 token 过滤承担。
  const providerModelOptions = computed(() => capabilityDomainIsMedia(form.capabilityDomain)
    ? loadedProviderModelOptions.value
    : providerModelsForProtocolProfile(
      loadedProviderModelOptions.value,
      selectedProtocolProfile.value
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
    lastValidationFailure,
    saveAccount,
    saving
  } = useAccountEditSaveFlow({
    accountAdvancedDetailLoaded,
    accountErrorPolicyRules,
    accountResponseInspectionRules,
    accounts: options.accounts,
    createScopeParams,
    cloningSourceId,
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
    providers: availableProviders,
    reopenCreatedAccountForEdit
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
    inheritedErrorPolicyRules.value = systemInheritedErrorPolicyRulesPreview()
    accountResponseInspectionRules.value = loadAccountResponseInspectionRules()
    form.quotaRecoveryPolicy = loadAccountQuotaRecoveryPolicy(undefined)
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

  function ensureDefaultGroupSelected(providerCode = form.providerCode) {
    if (!providerCode) {
      form.groupId = undefined
      form.group = undefined
      return
    }
    setFormGroup(resolveAccountDefaultGroupSelection({
      groups: options.groups.value,
      providerCode,
      selectedGroupId: form.groupId,
      cachedDefaultGroup: cachedDefaultGroupForProvider(providerCode)
    }))
  }

  async function openCreate() {
    const requestToken = nextFormOpenRequestToken()
    if (options.isManagementView.value && !options.accountScopeParams.value?.systemAccountId) {
      message.warning('请先选择目标系统账户，再创建 AI 账户')
      options.onCreateGuardSystemAccount?.()
      return
    }
    editingId.value = undefined
    editingAccountDetail.value = undefined
    editingAccountAdvancedDetail.value = undefined
    editingScheduleFingerprint.value = undefined
    cloningScheduleFingerprint.value = undefined
    creatingAccountScopeParams.value = undefined
    if (!isCurrentFormOpenRequest(requestToken)) return
    resetForm(mediaDomainDefaultProviderCode(), '')
    applyCachedDefaultGroup()
    accountAdvancedDetailLoaded.value = true
    accountAdvancedDetailLoading.value = false
    modalOpen.value = true
  }

  // v8-D 媒体域新建默认供应商收敛：预取就绪且既有默认供应商在当前域无在售
  // 目录模型时，返回域内首个「启用且有货」供应商（候选序同列表页供应商清单），
  // 由 resetForm 一次性落值。预取未就绪或交集为空时返回空串——表单回退既有
  // 全局默认（GPT 优先），不做打开后异步改选，避免默认值跳变与用户已输入被
  // 重置；对话域行为零变化。
  function mediaDomainDefaultProviderCode(): string {
    const capabilityDomain = options.capabilityDomain.value
    if (!capabilityDomainIsMedia(capabilityDomain)) return ''
    const availability = cachedMediaDomainModelAvailability({
      capabilityDomain,
      isManagementView: options.isManagementView.value,
      systemAccountId: createScopeParams.value?.systemAccountId
    })
    if (!availability) return ''
    return preferredMediaDomainProviderCode({
      capabilityDomain,
      providers: mergedProviderDefinitions(),
      providerCodesWithDomainModels: availability
    }) ?? ''
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
      // 音频/视频域账户无后台探针，health_check_endpoint_mode 仅为记录值：失效时
      // 兜底域常量（镜像 DefaultHealthCheckEndpointModeForDomain），不得落 chat 族
      // 默认——qwen 长音频声明集没有任何检查形态词表值，落 chat_json 会被保存校验
      // 拒绝（R5）。
      if (form.capabilityDomain === 'audio' || form.capabilityDomain === 'video') {
        form.healthCheckEndpointMode = defaultHealthCheckEndpointModeForCapabilityDomain({
          capabilityDomain: form.capabilityDomain
        })
        return
      }
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
    applyCachedDefaultGroup(providerCode)
    // 域默认 watch（如媒体表单）与 resetForm 落在同一 flush 周期内：快照必须等
    // flush 落定后再拍，否则 watch 改写过的字段会让回填等值判定误判为「用户已
    // 改动」而整体跳过（B3）。快照与加载都在 microtask 内完成，对外仍是同步入口。
    void nextTick().then(async () => {
      if (!isCurrentFormOpenRequest(requestToken) || !modalOpen.value || form.providerCode !== providerCode) return
      const initialDefaults = providerDefaultState(form)
      await applyProviderDefinitionDefaultsAfterLoad({
        ensureDefinition: () => options.ensureProviderDefinition(providerCode, systemAccountId),
        form,
        initialDefaults,
        isCurrent: () => isCurrentFormOpenRequest(requestToken) && modalOpen.value && form.providerCode === providerCode,
        resolvedDefaults: () => defaultForm(providerCode, '')
      })
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
    const defaultGroup = cachedDefaultGroupForProvider(providerCode)
    if (defaultGroup) {
      setFormGroup(defaultGroup)
      return
    }
    ensureDefaultGroupSelected(providerCode)
  }

  function cachedDefaultGroupForProvider(providerCode: string): GroupSelection | undefined {
    const referenceParams = {
      viewScope: options.isManagementView.value ? 'admin' : 'self',
      systemAccountId: createScopeParams.value?.systemAccountId
    } as const
    const referenceData = getCachedUserReferenceData(referenceParams)
    const defaultGroup = referenceData?.providerDefaults
      .find((item) => item.providerCode === providerCode)
      ?.defaultGroup
    if (!defaultGroup) return undefined
    rememberGroupLabel(defaultGroup.id, defaultGroup.name)
    return { id: defaultGroup.id, name: defaultGroup.name }
  }

  async function openEdit(account: AccountListItem) {
    await openEditAtPath(account.id, accountOperationScopeParams(account, options.accountScopeParams.value), account)
  }

  /**
   * openEdit 的状态装配主体（列表编辑与「创建成功转编辑」共用）：清创建/克隆残留、
   * 置 editingId 与加载态，拉取 edit-basic 基础详情并回填表单。modalOpen 的开合由
   * 调用方语义决定：openEdit 从关闭态打开，转编辑路径保持已开的弹窗不重置；
   * basic 详情加载失败时沿用 openEdit 既有行为复位并关闭弹窗。
   * listAccount 仅列表编辑路径传入：授权账户走只读授权分支；转编辑场景新账户
   * 尚无列表行且必为 owner 账户，传 undefined 直接走普通编辑分支。
   */
  async function openEditAtPath(
    accountId: string,
    scopeParams: AccountScopeParams,
    listAccount: AccountListItem | undefined
  ): Promise<void> {
    const requestToken = nextFormOpenRequestToken()
    const editScopeParams = scopeParams
    editingId.value = accountId
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

    if (listAccount && isAuthorizedAccount(listAccount)) {
      accountEditDetailLoading.value = true
      modalOpen.value = true
      const advancedDetail = await loadAccountDetailForForm(accountId, editScopeParams, '加载账户详情失败')
      if (!isCurrentFormOpenRequest(requestToken)) return
      if (!advancedDetail || advancedDetail.accessType !== 'authorized') {
        accountEditDetailLoading.value = false
        modalOpen.value = false
        editingId.value = undefined
        editingAccountDetail.value = undefined
        editingAccountAdvancedDetail.value = undefined
        return
      }
      const sourceAccount = authorizedAccountBasicDetail(listAccount, advancedDetail)
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
      void loadAccountApiKeyRuntimeDetails()
      return
    }

    accountEditDetailLoading.value = true
    modalOpen.value = true
    const basicDetailRequest = loadAccountDetailForForm(accountId, editScopeParams, '加载账户基础配置失败', 'edit-basic')
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
    void loadAccountApiKeyRuntimeDetails()
  }

  // 方案 B（创建成功转编辑）：托管 OAuth 单账户创建成功后弹窗不关，直接转入该账户
  // 编辑态（后端 CreateOAuthAccount 已派生支持模型、检查模型与连接）。创建场景没有
  // 列表行，编辑 scope 用创建时的 createScopeParams 桥接——新账户就建在该
  // systemAccount 下；openEditAtPath 会清掉 creatingAccountScopeParams，必须先取快照。
  async function reopenCreatedAccountForEdit(accountId: string): Promise<void> {
    await openEditAtPath(accountId, createScopeParams.value, undefined)
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
      // v8-C：模型候选按账户能力域过滤目录 mode（对话账户选不出非对话模型）。
      modes: [...DOMAIN_MODEL_MODES[form.capabilityDomain]],
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
          statusSelectionExplicit: form.statusSelectionExplicit,
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
    inheritedErrorPolicyRules.value = advancedLoad?.inheritedErrorPolicyRules ?? []
    accountResponseInspectionRules.value = advancedLoad?.responseInspectionRules ?? loadAccountResponseInspectionRules()
    form.quotaRecoveryPolicy = advancedLoad?.patch.quotaRecoveryPolicy ?? loadAccountQuotaRecoveryPolicy(undefined)
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
    const sourceGroupId = sourceAccount.boundGroupId
      ?? options.groupIdForAccount(account.id)
      ?? account.boundGroupId
    const selectedGroup = groupSelectionForId(
      sourceGroupId,
      sourceAccount.boundGroupName ?? account.boundGroupName
    )
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
    inheritedErrorPolicyRules.value = formLoad.inheritedErrorPolicyRules
    accountResponseInspectionRules.value = formLoad.responseInspectionRules
    form.quotaRecoveryPolicy = formLoad.patch.quotaRecoveryPolicy ?? loadAccountQuotaRecoveryPolicy(undefined)
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
    return options.isManagementView.value
      ? await api.accounts.apiKeyRuntime(accountId, scopeParams)
      : await api.myAccounts.apiKeyRuntime(accountId)
  }

  async function loadAccountApiKeyRuntimeDetails(force = false): Promise<void> {
    const account = editingAccountDetail.value
    const apiKeys = normalizedAccountApiKeys(form)
    if (!account || form.type !== 'api_key' || apiKeys.length < 2 || accountApiKeyRuntimeLoading.value) return
    if (!force && visibleSavedAccountApiKeyRuntimeDetails(savedApiKeyRuntimeSnapshot.value, apiKeys)) return
    const configRevision = account.configRevision
    if (typeof configRevision !== 'number' || !Number.isInteger(configRevision) || configRevision < 1) {
      message.error('账户配置版本缺失或无效，请关闭弹窗并刷新列表后重试')
      return
    }
    const requestToken = formOpenRequestToken
    const failureMessage = force ? '刷新多 Key 运行状态失败，请稍后重试' : '加载多 Key 运行状态失败，请稍后重试'
    if (force) savedApiKeyRuntimeSnapshot.value = undefined
    accountApiKeyRuntimeLoading.value = true
    try {
      const response = await fetchAccountApiKeyRuntimeForEdit(account.id, editingAccountScopeParams())
      if (!response) {
        if (isCurrentFormOpenRequest(requestToken)) message.error(failureMessage)
        return
      }
      if (!isCurrentFormOpenRequest(requestToken) || editingAccountDetail.value?.id !== account.id) return
      savedApiKeyRuntimeSnapshot.value = createSavedAccountApiKeyRuntimeSnapshot({
        accountId: account.id,
        configRevision,
        apiKeys,
        response
      })
    } catch (error) {
      console.error(error)
      if (isCurrentFormOpenRequest(requestToken)) message.error(failureMessage)
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
    inheritedErrorPolicyRules,
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
    allProviderModelScopeParams,
    lastValidationFailure,
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
    reopenCreatedAccountForEdit,
    ensureAccountEditDetailLoaded,
    loadAdvancedAccountDetail,
    loadAccountApiKeyRuntimeDetails,
    loadCurrentProviderModelOptions,
    loadMappingSourceModelOptions,
    handleAccountTagOptionsDropdown,
    mappingSourceModelOptionsTruncated,
    providerName,
    providerModelOptions,
    providerModelsLoading,
    providerModelOptionsSettled,
    providerModelOptionsTruncated,
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

/**
 * 供应商读面随协议档案表退役后（供应商与上游协议面重构设计 §9.11），
 * `/providers/{code}` 只返回 vendors 身份字段：`protocolProfiles`、
 * `accountTypes`、`defaultProtocolProfileId` 恒为空。前端账户表单尚未迁移到
 * 连接预设契约，仍以内置定义驱动账户类型与凭证区渲染，因此服务端详情为空时
 * 必须保留内置定义的协议形状；服务端返回完整档案（旧库或迁移完成后）仍以
 * 服务端为准。
 */
export function mergeProviderDefinition(
  fallback: ProviderDefinition | undefined,
  loaded: ProviderDefinition | undefined
): ProviderDefinition | undefined {
  if (!fallback) return loaded
  if (!loaded) return fallback
  if (loaded.protocolProfiles.length > 0) return loaded
  return {
    ...fallback,
    ...loaded,
    defaultProtocolProfileId: fallback.defaultProtocolProfileId,
    protocolCode: fallback.protocolCode,
    protocolVersion: fallback.protocolVersion,
    baseUrl: fallback.baseUrl,
    defaultHealthCheckModel: loaded.defaultHealthCheckModel || fallback.defaultHealthCheckModel,
    defaultSupportedModels: loaded.defaultSupportedModels.length ? loaded.defaultSupportedModels : fallback.defaultSupportedModels,
    accountTypes: fallback.accountTypes,
    capabilities: fallback.capabilities,
    protocolProfiles: fallback.protocolProfiles
  }
}

export function mergeAccountProviderDefinitions(
  providerOptions: ProviderDefinition[],
  loadedDefinitions: ProviderDefinition[]
): ProviderDefinition[] {
  const fallbackByCode = new Map<string, ProviderDefinition>(
    FALLBACK_PROVIDERS.map((provider) => [provider.code, provider])
  )
  const loadedByCode = new Map<string, ProviderDefinition>(
    loadedDefinitions.map((provider) => [provider.code, provider])
  )
  const base = providerOptions.length ? providerOptions : FALLBACK_PROVIDERS
  const merged: ProviderDefinition[] = []
  const seen = new Set<string>()
  for (const provider of base) {
    seen.add(provider.code)
    const definition = mergeProviderDefinition(fallbackByCode.get(provider.code), loadedByCode.get(provider.code))
    if (!definition) {
      merged.push(provider)
      continue
    }
    merged.push({
      ...definition,
      id: provider.id,
      code: provider.code,
      name: provider.name,
      enabled: provider.enabled
    })
  }
  // 懒加载得到的完整定义可能覆盖基座之外的供应商码（媒体族不在 options 轻量
  // 清单与回退清单里）：不追加会让标题、类型默认值与矩阵过滤永远缺这批供应商。
  for (const provider of loadedDefinitions) {
    if (!provider.code || seen.has(provider.code)) continue
    seen.add(provider.code)
    merged.push(mergeProviderDefinition(fallbackByCode.get(provider.code), provider) ?? provider)
  }
  return merged
}

export type ProviderDefaultState = Pick<
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
  let definition = await input.ensureDefinition()
  // isCurrent 成立但 definition 缺失：ensureProviderDefinition 的全局请求代次被
  // 并发切换顶掉时以 undefined 结束在途请求而非抛错。静默放弃会让首次选择停留
  // 在空表单（B3 第二入口）——重试一次，仍失败则上抛由调用方提示用户。
  if (!definition && input.isCurrent()) {
    definition = await input.ensureDefinition()
    if (!definition && input.isCurrent()) {
      throw new Error('供应商定义加载失败，请重试')
    }
  }
  if (!definition || !input.isCurrent()) return
  const resolved = providerDefaultState(input.resolvedDefaults())
  // 字段级 pristine 判定（B3 根修）：逐字段与快照比较，只回填未被触碰的字段，
  // 替代旧的全量等值闸——同 flush 周期内域默认 watch 会改写个别字段，全量等值
  // 必然失败并静默跳过回填，权威默认值整体不落。
  const pristine = providerPristineFields(input.form, input.initialDefaults)
  if (input.form.type === '' || pristine.has('type')) {
    // type==='' 是重置占位而非用户选择，恒可回填。
    input.form.type = resolved.type
  }
  if (pristine.has('providerProtocolProfileId')) input.form.providerProtocolProfileId = resolved.providerProtocolProfileId
  if (pristine.has('baseUrl')) input.form.baseUrl = resolved.baseUrl
  if (pristine.has('clientCompatibility')) input.form.clientCompatibility = resolved.clientCompatibility
  // 同 supportedEndpointModes：媒体供应商解析出的默认集为空时，健康检查形态
  // 已由域默认 watch 落位（video_create / audio_speech 等），不得用空默认集
  // 推导出的 chat 形态覆盖。
  if (pristine.has('healthCheckEndpointMode') && resolved.supportedEndpointModes.length > 0) {
    input.form.healthCheckEndpointMode = resolved.healthCheckEndpointMode
  }
  if (pristine.has('oauthMode')) input.form.oauthMode = resolved.oauthMode
  // 媒体供应商的协议档案默认集为空（媒体 modes 全部 opt-in，由域默认 watch
  // 按账户能力域落位）；此时空数组不是权威默认值，回填会清掉 watch 已选的
  // 视频/音频组。仅在解析出非空默认集时覆盖。
  if (pristine.has('supportedEndpointModes') && resolved.supportedEndpointModes.length > 0) {
    input.form.supportedEndpointModes = [...resolved.supportedEndpointModes]
  }
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

function providerPristineFields(
  form: ProviderDefaultState,
  initialDefaults: ProviderDefaultState
): Set<keyof ProviderDefaultState> {
  const pristine = new Set<keyof ProviderDefaultState>()
  if (form.providerProtocolProfileId === initialDefaults.providerProtocolProfileId) pristine.add('providerProtocolProfileId')
  if (form.type === initialDefaults.type) pristine.add('type')
  if (form.baseUrl === initialDefaults.baseUrl) pristine.add('baseUrl')
  if (form.clientCompatibility === initialDefaults.clientCompatibility) pristine.add('clientCompatibility')
  if (form.healthCheckEndpointMode === initialDefaults.healthCheckEndpointMode) pristine.add('healthCheckEndpointMode')
  if (form.oauthMode === initialDefaults.oauthMode) pristine.add('oauthMode')
  if (form.supportedEndpointModes.length === initialDefaults.supportedEndpointModes.length
    && form.supportedEndpointModes.every((mode, index) => mode === initialDefaults.supportedEndpointModes[index])) {
    pristine.add('supportedEndpointModes')
  }
  return pristine
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

import { message } from '@/lib/antd'
import { computed, nextTick, reactive, ref, watch, type ComputedRef } from 'vue'

import { api, type AccountDraftTestPayload } from '@/api/client'
import { useSubmitAction } from '@/composables/useSubmitAction'
import { rememberGroupLabel, type GroupSelection } from '@/shared/groupLabelCache'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import { providerDisplayName } from '@/shared/providerDisplay'
import type {
  AccountSummary,
  AccountType,
  GroupOptionSummary,
  OpenAIAuthURLResult,
  ProviderDefinition,
  ProviderModelPricing,
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
  loadAccountStreamInterceptRules
} from './accountStreamInterceptPolicyPayload'
import type { AccountStreamInterceptRuleForm } from './accountStreamInterceptPolicyTypes'
import { defaultAccountForm } from './accountFormDefaults'
import { accountAvailabilityScheduleFormFingerprint, createAccountAvailabilityScheduleForm } from './accountAvailabilitySchedule'
import {
  accountTypeDescription,
  accountTypeText,
  accountTypeTitle as buildAccountTypeTitle,
  asString,
  isAuthorizedAccount,
  parseStrictDatePickerValue
} from './accountFormatters'
import type { AccountFormModel } from './accountFormTypes'
import { FALLBACK_PROVIDERS, GPT_VENDOR_CODE } from './accountOptions'
import { isOpenAIProtocolProfile } from '@/shared/providerProtocol'
import { authUrl, buildOAuthCreatePayload } from './accountOAuthPayload'
import { accountOperationScopeParams, type AccountScopeParams } from './accountOperationScope'
import { buildAccountSavePayload, buildAccountUpdatePayload, buildOAuthCreateCommonPayload, validateAccountSaveForm, type AccountSavePayload } from './accountSavePayload'
import type { SuccessfulDraftActivationTest } from './useAccountTestModal'

type ReadonlyValue<T> = {
  readonly value: T
}

interface SelectOption {
  label: string
  value: string
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
  const accountStreamInterceptRules = ref<AccountStreamInterceptRuleForm[]>(loadAccountStreamInterceptRules())
  const providerModelOptions = ref<SelectOption[]>([])
  const mappingTargetModelOptions = ref<SelectOption[]>([])
  const providerModelsLoading = ref(false)
  const providerModelOptionsCache = new Map<string, SelectOption[]>()
  const mappingTargetModelOptionsCache = new Map<string, SelectOption[]>()
  let formOpenRequestToken = 0

  const createScopeParams = computed<AccountScopeParams>(() => creatingAccountScopeParams.value ?? options.accountScopeParams.value)
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
  const accountTypeChoices = computed(() => (selectedProtocolProfile.value?.accountTypes ?? []).map((type) => ({
    value: type,
    label: accountTypeTitle(selectedProvider.value?.code ?? form.providerCode, type),
    description: accountTypeDescription(selectedProvider.value?.code ?? form.providerCode, type),
    tag: accountTypeText(type)
  })).sort((left, right) => accountTypeSortWeight(left.value) - accountTypeSortWeight(right.value)))
  const hasAccountType = computed(() => Boolean(form.providerCode && form.providerProtocolProfileId && form.type))
  const isApiKeyForm = computed(() => hasAccountType.value && form.type === 'api_key')
  const isOAuthForm = computed(() => hasAccountType.value && form.type === 'oauth')
  const isOpenAIOAuthForm = computed(() => form.providerCode === GPT_VENDOR_CODE && form.type === 'oauth' && isOpenAIProtocolProfile(selectedProtocolProfile.value))
  const editingAuthorizedAccount = computed(() => Boolean(editingId.value && editingAccountDetail.value && isAuthorizedAccount(editingAccountDetail.value)))
  const modalTitle = computed(() => {
    if (editingAuthorizedAccount.value) return '编辑授权账户'
    if (editingId.value) return '编辑账户'
    if (cloningSourceId.value) return '克隆账户'
    if (!form.providerCode) return '添加账户'
    if (!form.providerProtocolProfileId) return `添加 ${providerName(form.providerCode)} 账户`
    if (!form.type) return `添加 ${providerName(form.providerCode)} 账户`
    return `添加 ${accountTypeTitle(form.providerCode, form.type)} 账户`
  })
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
    providerModelOptions.value = []
    mappingTargetModelOptions.value = []
    providerModelsLoading.value = false
    ensureDefaultGroupSelected(form.providerCode, form.providerProtocolProfileId)
    accountErrorPolicyRules.value = loadAccountErrorPolicyRules()
    accountStreamInterceptRules.value = loadAccountStreamInterceptRules()
    authResult.value = undefined
  }

  function accountTypeTitle(providerCode: string, type: AccountType) {
    return buildAccountTypeTitle(providerName(providerCode), type)
  }

  function accountTypeSortWeight(type: AccountType): number {
    if (type === 'api_key') return 0
    if (type === 'oauth') return 1
    return 2
  }

  function providerName(providerCode?: string) {
    return providerDisplayName(providerCode, availableProviders.value)
  }

  function providerModelsToOptions(models: ProviderModelPricing[]): SelectOption[] {
    return models.map((item) => ({
      label: item.visibility === 'mapping_target_only' ? `${item.model}（仅映射）` : item.model,
      value: item.model
    }))
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
      clientCompatibility: providerCode === GPT_VENDOR_CODE && type === 'oauth' ? 'codex_responses' : form.clientCompatibility,
      openAIResponsesUpstreamMode: type === 'oauth' ? 'passthrough' : form.openAIResponsesUpstreamMode,
      supportedModels: form.supportedModels,
      modelMappings: form.modelMappings,
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
    const baseUrl = credentialBaseUrlForForm(sourceAccount.credentials, '账户详情凭据')
    const errorPolicyRules = loadCredentialErrorPolicyRules(sourceAccount.credentials, '账户详情错误处理策略')
    const streamInterceptRules = loadCredentialStreamInterceptRules(sourceAccount.credentials, '账户详情流式拦截策略')
    if (!baseUrl || !errorPolicyRules || !streamInterceptRules) return
    const selectedGroup = sourceAccount.boundGroupId
      ? groupSelectionForId(sourceAccount.boundGroupId, sourceAccount.boundGroupName)
      : undefined
    let accountExpiresAt: AccountFormModel['accountExpiresAt']
    let availabilitySchedule: AccountFormModel['availabilitySchedule']
    try {
      accountExpiresAt = parseStrictDatePickerValue(sourceAccount.accountExpiresAt, '账户过期时间')
      availabilitySchedule = createAccountAvailabilityScheduleForm(accountAvailabilityScheduleForForm(sourceAccount))
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '账户数据结构异常，请清理后再编辑'))
      return
    }
    editingId.value = sourceAccount.id
    editingAccountDetail.value = sourceAccount
    cloningSourceId.value = undefined
    creatingAccountScopeParams.value = undefined
    void options.loadAccountOptions(editScopeParams?.systemAccountId)
    Object.assign(form, defaults, {
      providerCode: sourceAccount.providerCode,
      providerProtocolProfileId: sourceAccount.providerProtocolProfileId ?? defaults.providerProtocolProfileId,
      name: sourceAccount.name,
      type: sourceAccount.type,
      concurrencyLimit: sourceAccount.concurrencyLimit,
      priority: sourceAccount.priority,
      clientCompatibility: sourceAccount.providerCode === GPT_VENDOR_CODE && sourceAccount.type === 'oauth' ? 'codex_responses' : sourceAccount.clientCompatibility ?? 'openai_standard',
      openAIResponsesUpstreamMode: sourceAccount.type === 'oauth' ? 'passthrough' : sourceAccount.openAIResponsesUpstreamMode ?? 'passthrough',
      proxyProfileId: sourceAccount.proxyProfileId,
      accountExpiresAt,
      groupId: selectedGroup?.id ?? options.groupIdForAccount(sourceAccount.id),
      group: selectedGroup,
      apiKey: asString(sourceAccount.credentials.api_key) ?? '',
      baseUrl,
      accessToken: asString(sourceAccount.credentials.access_token) ?? '',
      refreshToken: asString(sourceAccount.credentials.refresh_token) ?? '',
      supportedModels: [...(sourceAccount.supportedModels ?? [])],
      modelMappings: cloneAccountModelMappings(sourceAccount.modelMappings),
      availabilitySchedule,
      notes: sourceAccount.notes ?? ''
    })
    editingScheduleFingerprint.value = accountAvailabilityScheduleFormFingerprint(form.availabilitySchedule)
    cloningScheduleFingerprint.value = undefined
    accountErrorPolicyRules.value = errorPolicyRules
    accountStreamInterceptRules.value = streamInterceptRules
    authResult.value = undefined
    modalOpen.value = true
    void options.loadGroupOptions('', true, {
      providerCode: sourceAccount.providerCode,
      systemAccountId: editScopeParams?.systemAccountId,
      selectedIds: [form.groupId]
    })
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
    if (!fillCloneForm(sourceAccount, sourceAccount.credentials)) return
    modalOpen.value = true
    void options.loadGroupOptions('', true, {
      providerCode: sourceAccount.providerCode,
      systemAccountId: cloneScopeParams?.systemAccountId,
      selectedIds: [form.groupId]
    })
    void loadProviderModelOptions(sourceAccount.providerCode)
  }

  async function loadProviderModelOptions(providerCode: string): Promise<void> {
    const code = providerCode.trim()
    providerModelOptions.value = []
    mappingTargetModelOptions.value = []
    if (!code) return
    const cacheKey = providerModelCacheKey(code)
    const cachedPublic = providerModelOptionsCache.get(cacheKey)
    const cachedMappingTargets = mappingTargetModelOptionsCache.get(cacheKey)
    if (cachedPublic && cachedMappingTargets) {
      providerModelOptions.value = cachedPublic
      mappingTargetModelOptions.value = cachedMappingTargets
      providerModelsLoading.value = false
      return
    }
    providerModelsLoading.value = true
    try {
      const [models, mappingTargetModels] = await Promise.all([
        cachedPublic ? Promise.resolve(undefined) : api.providers.models(code),
        cachedMappingTargets ? Promise.resolve(undefined) : api.providers.models(code, { includeMappingTargets: true })
      ])
      const modelOptions = cachedPublic ?? providerModelsToOptions(models ?? [])
      const mappingTargetOptions = cachedMappingTargets ?? providerModelsToOptions(mappingTargetModels ?? [])
      providerModelOptionsCache.set(cacheKey, modelOptions)
      mappingTargetModelOptionsCache.set(cacheKey, mappingTargetOptions)
      if (form.providerCode === code) {
        providerModelOptions.value = modelOptions
        mappingTargetModelOptions.value = mappingTargetOptions
      }
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '加载供应商模型失败'))
    } finally {
      if (form.providerCode === code) {
        providerModelsLoading.value = false
      }
    }
  }

  function providerModelCacheKey(providerCode: string): string {
    return `${providerCode}:${createScopeParams.value?.systemAccountId ?? 'self'}:${options.isManagementView.value ? 'management' : 'self'}`
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
      streamInterceptRules: accountStreamInterceptRules.value
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
      streamInterceptRules: accountStreamInterceptRules.value
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
        const createPayload = accountCreatePayloadWithActivationTest(payload)
        let created: AccountSummary
        if (options.isManagementView.value) {
          created = await api.accounts.create(createPayload, createScopeParams.value)
        } else {
          created = await api.myAccounts.create(createPayload)
        }
        message.success(created.status === 'active' ? '账户已创建并启用' : '账户已创建，需测试通过后参与调度')
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
      streamInterceptRules: accountStreamInterceptRules.value
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

  return {
    accountErrorPolicyRules,
    accountStreamInterceptRules,
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

  function accountAvailabilityScheduleForForm(account: AccountSummary) {
    return isAuthorizedAccount(account)
      ? account.authorizationInstanceSourceAccountAvailabilitySchedule
      : account.availabilitySchedule
  }

  function credentialBaseUrlForForm(credentials: Record<string, unknown>, label: string): string | undefined {
    const baseUrl = asString(credentials.base_url)
    if (!baseUrl) {
      message.error(`${label}缺少 Base URL，请先修正账户凭据`)
      return undefined
    }
    return baseUrl
  }

  function loadCredentialStreamInterceptRules(credentials: Record<string, unknown>, label: string): AccountStreamInterceptRuleForm[] | undefined {
    try {
      return loadAccountStreamInterceptRules(credentials)
    } catch (error) {
      console.error(error)
      message.error(`${label}配置异常，请先修正已保存的账户凭据`)
      return undefined
    }
  }

  function loadCredentialErrorPolicyRules(credentials: Record<string, unknown>, label: string): AccountErrorPolicyRuleForm[] | undefined {
    try {
      return loadAccountErrorPolicyRules(credentials)
    } catch (error) {
      console.error(error)
      message.error(`${label}配置异常，请先修正已保存的账户凭据`)
      return undefined
    }
  }

  function fillCloneForm(account: AccountSummary, credentials: Record<string, unknown>): boolean {
    const errorPolicyRules = loadCredentialErrorPolicyRules(credentials, '克隆来源错误处理策略')
    if (!errorPolicyRules) return false
    const streamInterceptRules = loadCredentialStreamInterceptRules(credentials, '克隆来源流式拦截策略')
    if (!streamInterceptRules) return false
    const baseUrl = credentialBaseUrlForForm(credentials, '克隆来源凭据')
    if (!baseUrl) return false
    const selectedGroup = account.boundGroupId
      ? groupSelectionForId(account.boundGroupId, account.boundGroupName)
      : undefined
    const defaults = defaultForm(account.providerCode, account.type, account.providerProtocolProfileId)
    let accountExpiresAt: AccountFormModel['accountExpiresAt']
    let availabilitySchedule: AccountFormModel['availabilitySchedule']
    try {
      accountExpiresAt = parseStrictDatePickerValue(account.accountExpiresAt, '账户过期时间')
      availabilitySchedule = createAccountAvailabilityScheduleForm(account.availabilitySchedule)
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '克隆来源账户数据结构异常，请清理后再克隆'))
      return false
    }
    Object.assign(form, defaults, {
      providerCode: account.providerCode,
      providerProtocolProfileId: account.providerProtocolProfileId ?? defaults.providerProtocolProfileId,
      name: cloneAccountName(account.name),
      type: account.type,
      concurrencyLimit: account.concurrencyLimit,
      priority: account.priority,
      clientCompatibility: account.providerCode === GPT_VENDOR_CODE && account.type === 'oauth' ? 'codex_responses' : account.clientCompatibility ?? 'openai_standard',
      openAIResponsesUpstreamMode: account.type === 'oauth' ? 'passthrough' : account.openAIResponsesUpstreamMode ?? 'passthrough',
      proxyProfileId: account.proxyProfileId,
      accountExpiresAt,
      groupId: selectedGroup?.id ?? options.groupIdForAccount(account.id),
      group: selectedGroup,
      apiKey: '',
      baseUrl,
      accessToken: '',
      refreshToken: '',
      callbackUrl: '',
      oauthMode: 'manual',
      supportedModels: [...(account.supportedModels ?? [])],
      modelMappings: cloneAccountModelMappings(account.modelMappings),
      availabilitySchedule,
      notes: account.notes ?? ''
    })
    cloningScheduleFingerprint.value = accountAvailabilityScheduleFormFingerprint(form.availabilitySchedule)
    accountErrorPolicyRules.value = errorPolicyRules
    accountStreamInterceptRules.value = streamInterceptRules
    authResult.value = undefined
    return true
  }

  function cloneAccountName(name: string): string {
    const trimmed = name.trim()
    return trimmed ? `${trimmed} - 克隆` : ''
  }

  function cloneAccountModelMappings(value: AccountSummary['modelMappings']): AccountFormModel['modelMappings'] {
    return (value ?? []).map((item) => ({ ...item }))
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

  function accountCreatePayloadWithActivationTest(payload: AccountSavePayload): AccountSavePayload & { status?: 'active'; activationTestTaskId?: string } {
    const activationTest = options.successfulDraftActivationTest?.value
    if (!activationTest || !isActivationTestForPayload(activationTest, payload)) {
      return payload
    }
    return {
      ...payload,
      status: 'active',
      activationTestTaskId: activationTest.taskId
    }
  }

  function isActivationTestForPayload(activationTest: SuccessfulDraftActivationTest, payload: AccountSavePayload): boolean {
    return stablePayloadFingerprint(activationTest.account) === stablePayloadFingerprint(accountDraftPayloadFromSavePayload(payload))
  }

  function accountDraftPayloadFromSavePayload(payload: AccountSavePayload): AccountDraftTestPayload['account'] {
    return {
      providerCode: payload.providerCode,
      providerProtocolProfileId: payload.providerProtocolProfileId,
      name: payload.name ?? form.name.trim(),
      type: payload.type,
      credentials: payload.credentials,
      concurrencyLimit: payload.concurrencyLimit,
      priority: payload.priority,
      clientCompatibility: payload.clientCompatibility,
      openAIResponsesUpstreamMode: payload.openAIResponsesUpstreamMode,
      supportedModels: payload.supportedModels,
      modelMappings: payload.modelMappings,
      proxyProfileId: payload.proxyProfileId,
      groupId: payload.groupId ?? '',
      accountExpiresAt: payload.accountExpiresAt,
      availabilitySchedule: payload.availabilitySchedule as AccountDraftTestPayload['account']['availabilitySchedule'],
      notes: payload.notes
    }
  }

  function stablePayloadFingerprint(value: unknown): string {
    return JSON.stringify(stablePayloadValue(value))
  }

  function stablePayloadValue(value: unknown): unknown {
    if (Array.isArray(value)) return value.map(stablePayloadValue)
    if (!value || typeof value !== 'object') return value
    const output: Record<string, unknown> = {}
    for (const key of Object.keys(value).sort()) {
      const item = (value as Record<string, unknown>)[key]
      if (item !== undefined) {
        output[key] = stablePayloadValue(item)
      }
    }
    return output
  }

  function clearSuccessfulDraftActivationTest(): void {
    if (options.successfulDraftActivationTest) {
      options.successfulDraftActivationTest.value = undefined
    }
  }
}

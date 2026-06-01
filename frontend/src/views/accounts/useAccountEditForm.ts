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
  ProviderModelPricing,
  SystemAccountPrincipalSummary
} from '@/types/domain'
import {
  defaultGroupForProvider as selectDefaultGroupForProvider,
  groupOptionsForProviderWithSelected,
  isManageableGroupForProvider,
  providerNameByCodeMap,
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
  parseDatePickerValue
} from './accountFormatters'
import type { AccountFormModel } from './accountFormTypes'
import { FALLBACK_PROVIDER } from './accountOptions'
import { authUrl, buildOAuthCreatePayload } from './accountOAuthPayload'
import { accountOperationScopeParams, type AccountScopeParams } from './accountOperationScope'
import { buildAccountSavePayload, buildAccountUpdatePayload, buildOAuthCreateCommonPayload, validateAccountSaveForm } from './accountSavePayload'

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
  const providerModelsLoading = ref(false)
  const providerModelOptionsCache = new Map<string, SelectOption[]>()

  const createScopeParams = computed<AccountScopeParams>(() => creatingAccountScopeParams.value ?? options.accountScopeParams.value)
  const targetSystemAccountLabel = computed(() => {
    if (!options.isManagementView.value) return undefined
    const systemAccountId = createScopeParams.value?.systemAccountId
    return buildTargetSystemAccountLabel(options.systemAccounts.value, systemAccountId, options.systemAccountSelection?.value)
  })

  const groupOptions = computed(() => groupOptionsForProviderWithSelected(options.groups.value, form.providerCode, [form.groupId]))
  const availableProviders = computed(() => options.providers.value.length ? options.providers.value : [FALLBACK_PROVIDER])
  const providerNameByCode = computed(() => providerNameByCodeMap(availableProviders.value))
  const selectedProvider = computed(() => availableProviders.value.find((provider) => provider.code === form.providerCode))
  const accountTypeChoices = computed(() => (selectedProvider.value?.accountTypes ?? []).map((type) => ({
    value: type,
    label: accountTypeTitle(selectedProvider.value?.code ?? form.providerCode, type),
    description: accountTypeDescription(selectedProvider.value?.code ?? form.providerCode, type),
    tag: accountTypeText(type)
  })))
  const hasAccountType = computed(() => Boolean(form.providerCode && form.type))
  const isApiKeyForm = computed(() => hasAccountType.value && form.type === 'api_key')
  const isOAuthForm = computed(() => hasAccountType.value && form.type === 'oauth')
  const isOpenAIOAuthForm = computed(() => form.providerCode === 'openai' && form.type === 'oauth')
  const editingAuthorizedAccount = computed(() => Boolean(editingId.value && editingAccountDetail.value && isAuthorizedAccount(editingAccountDetail.value)))
  const modalTitle = computed(() => {
    if (editingAuthorizedAccount.value) return '编辑授权账户'
    if (editingId.value) return '编辑账户'
    if (cloningSourceId.value) return '克隆账户'
    if (!form.providerCode) return '添加账户'
    if (!form.type) return `添加 ${providerName(form.providerCode)} 账户`
    return `添加 ${accountTypeTitle(form.providerCode, form.type)} 账户`
  })
  const modalConfirmLoading = computed(() => saving.value)
  const modalOkButtonProps = computed(() => ({
    type: 'primary' as const,
    disabled: saving.value || !hasAccountType.value || (!editingId.value && isOAuthForm.value && !isOpenAIOAuthForm.value)
  }))
  const selectedAccountTypeTitle = computed(() => hasAccountType.value ? accountTypeTitle(form.providerCode, form.type) : '')

  function defaultForm(providerCode = '', type: AccountType = ''): AccountFormModel {
    return defaultAccountForm(providerCode, type, options.providers.value)
  }

  function resetForm(providerCode = '', type: AccountType = '') {
    cloningSourceId.value = undefined
    editingScheduleFingerprint.value = undefined
    cloningScheduleFingerprint.value = undefined
    Object.assign(form, defaultForm(providerCode, type))
    providerModelOptions.value = []
    providerModelsLoading.value = false
    ensureDefaultGroupSelected(providerCode)
    accountErrorPolicyRules.value = loadAccountErrorPolicyRules()
    accountStreamInterceptRules.value = loadAccountStreamInterceptRules()
    authResult.value = undefined
  }

  function accountTypeTitle(providerCode: string, type: AccountType) {
    return buildAccountTypeTitle(providerName(providerCode), type)
  }

  function providerName(providerCode?: string) {
    if (!providerCode) return '未知供应商'
    return providerNameByCode.value.get(providerCode) ?? providerCode
  }

  function providerModelsToOptions(models: ProviderModelPricing[]): SelectOption[] {
    return models.map((item) => ({ label: item.model, value: item.model }))
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

  function openCreate() {
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
    void options.loadGroupOptions('', true, { systemAccountId: options.accountScopeParams.value?.systemAccountId })
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
    Object.assign(form, {
      ...defaultForm(providerCode, type),
      groupId: form.groupId,
      group: form.group,
      proxyProfileId: form.proxyProfileId,
      notes: form.notes,
      supportedModels: form.supportedModels,
      concurrencyLimit: form.concurrencyLimit,
      priority: form.priority,
      accountExpiresAt: form.accountExpiresAt,
      availabilitySchedule: form.availabilitySchedule
    })
    void loadProviderGroupOptions(providerCode)
    void loadProviderModelOptions(providerCode)
    ensureDefaultGroupSelected(providerCode)
    authResult.value = undefined
  }

  function openEdit(account: AccountSummary) {
    const editScopeParams = accountOperationScopeParams(account, options.accountScopeParams.value)
    const defaults = defaultForm(account.providerCode, account.type)
    const baseUrl = isAuthorizedAccount(account)
      ? defaults.baseUrl
      : credentialBaseUrlForForm(account.credentials, '账户凭据')
    const policyRules = loadCredentialPolicyRules(account.credentials, '账户策略')
    if (!baseUrl || !policyRules) return
    editingId.value = account.id
    editingAccountDetail.value = account
    cloningSourceId.value = undefined
    creatingAccountScopeParams.value = undefined
    void options.loadAccountOptions(editScopeParams?.systemAccountId)
    const selectedGroup = account.boundGroupId
      ? groupSelectionForId(account.boundGroupId, account.boundGroupName)
      : undefined
    Object.assign(form, defaults, {
      providerCode: account.providerCode,
      name: account.name,
      type: account.type,
      concurrencyLimit: account.concurrencyLimit,
      priority: account.priority,
      proxyProfileId: account.proxyProfileId,
      accountExpiresAt: parseDatePickerValue(account.accountExpiresAt),
      groupId: selectedGroup?.id ?? options.groupIdForAccount(account.id),
      group: selectedGroup,
      apiKey: isAuthorizedAccount(account) ? '' : asString(account.credentials.api_key),
      baseUrl,
      accessToken: isAuthorizedAccount(account) ? '' : asString(account.credentials.access_token),
      refreshToken: isAuthorizedAccount(account) ? '' : asString(account.credentials.refresh_token),
      supportedModels: [...(account.supportedModels ?? [])],
      availabilitySchedule: createAccountAvailabilityScheduleForm(account.availabilitySchedule),
      notes: account.notes ?? ''
    })
    editingScheduleFingerprint.value = accountAvailabilityScheduleFormFingerprint(form.availabilitySchedule)
    cloningScheduleFingerprint.value = undefined
    accountErrorPolicyRules.value = policyRules.error
    accountStreamInterceptRules.value = policyRules.stream
    authResult.value = undefined
    modalOpen.value = true
    void options.loadGroupOptions('', true, {
      providerCode: account.providerCode,
      systemAccountId: editScopeParams?.systemAccountId,
      selectedIds: [form.groupId]
    })
    void loadProviderModelOptions(account.providerCode)
    if (!isAuthorizedAccount(account)) {
      void loadEditingAccountDetail(account.id, editScopeParams)
    }
  }

  function openClone(account: AccountSummary) {
    const cloneScopeParams = accountOperationScopeParams(account, options.accountScopeParams.value)
    if (options.isManagementView.value && !cloneScopeParams?.systemAccountId) {
      message.warning('无法确定克隆目标系统账户，请先筛选目标系统账户后再克隆')
      return
    }
    editingId.value = undefined
    editingAccountDetail.value = undefined
    editingScheduleFingerprint.value = undefined
    cloningSourceId.value = account.id
    creatingAccountScopeParams.value = cloneScopeParams
    void options.loadAccountOptions(cloneScopeParams?.systemAccountId)
    if (!fillCloneForm(account, account.credentials)) return
    modalOpen.value = true
    void options.loadGroupOptions('', true, {
      providerCode: account.providerCode,
      systemAccountId: cloneScopeParams?.systemAccountId,
      selectedIds: [form.groupId]
    })
    void loadProviderModelOptions(account.providerCode)
    void loadCloneAccountDetail(account.id, cloneScopeParams)
  }

  async function loadProviderModelOptions(providerCode: string): Promise<void> {
    const code = providerCode.trim()
    providerModelOptions.value = []
    if (!code) return
    const cached = providerModelOptionsCache.get(code)
    if (cached) {
      providerModelOptions.value = cached
      providerModelsLoading.value = false
      return
    }
    providerModelsLoading.value = true
    try {
      const models = await api.providers.models(code)
      const modelOptions = providerModelsToOptions(models)
      providerModelOptionsCache.set(code, modelOptions)
      if (form.providerCode === code) {
        providerModelOptions.value = modelOptions
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

  async function loadProviderGroupOptions(providerCode: string): Promise<void> {
    await nextTick()
    await options.loadGroupOptions('', true)
    syncFormGroupFromOptions()
    ensureDefaultGroupSelected(providerCode)
  }

  async function loadEditingAccountDetail(accountId: string, scopeParams?: AccountScopeParams): Promise<void> {
    try {
      const detail = options.isManagementView.value
        ? await api.accounts.detail(accountId, scopeParams)
        : await api.myAccounts.detail(accountId)
      if (editingId.value !== accountId) return
      editingAccountDetail.value = detail
      const baseUrl = credentialBaseUrlForForm(detail.credentials, '账户详情凭据')
      const policyRules = loadCredentialPolicyRules(detail.credentials, '账户详情策略')
      if (!baseUrl || !policyRules) return
      const nextDetailPatch: Partial<AccountFormModel> = {
        apiKey: asString(detail.credentials.api_key),
        baseUrl,
        accessToken: asString(detail.credentials.access_token),
        refreshToken: asString(detail.credentials.refresh_token),
        supportedModels: [...(detail.supportedModels ?? [])]
      }
      if (editingScheduleFingerprint.value === accountAvailabilityScheduleFormFingerprint(form.availabilitySchedule)) {
        nextDetailPatch.availabilitySchedule = createAccountAvailabilityScheduleForm(detail.availabilitySchedule)
      }
      Object.assign(form, nextDetailPatch)
      editingScheduleFingerprint.value = accountAvailabilityScheduleFormFingerprint(form.availabilitySchedule)
      if (detail.boundGroupId || detail.boundGroupName) {
        setFormGroup(groupSelectionForId(detail.boundGroupId, detail.boundGroupName))
      }
      accountErrorPolicyRules.value = policyRules.error
      accountStreamInterceptRules.value = policyRules.stream
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '加载账户详情失败'))
    }
  }

  async function loadCloneAccountDetail(accountId: string, scopeParams?: AccountScopeParams): Promise<void> {
    try {
      const detail = options.isManagementView.value
        ? await api.accounts.detail(accountId, scopeParams)
        : await api.myAccounts.detail(accountId)
      if (cloningSourceId.value !== accountId || editingId.value) return
      applyCloneCredentialDetails(detail, detail.credentials)
      void options.loadGroupOptions('', true, {
        providerCode: detail.providerCode,
        systemAccountId: creatingAccountScopeParams.value?.systemAccountId,
        selectedIds: [form.groupId]
      })
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '加载克隆账户配置失败'))
    }
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
        message.success('OAuth 账户已创建')
      } else {
        if (options.isManagementView.value) {
          await api.accounts.create(payload, createScopeParams.value)
        } else {
          await api.myAccounts.create(payload)
        }
        message.success('账户已创建')
      }
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

  function credentialBaseUrlForForm(credentials: Record<string, unknown>, label: string): string | undefined {
    const baseUrl = asString(credentials.base_url)
    if (!baseUrl) {
      message.error(`${label}缺少 Base URL，请先修正账户凭据`)
      return undefined
    }
    return baseUrl
  }

  function loadCredentialPolicyRules(credentials: Record<string, unknown>, label: string): { error: AccountErrorPolicyRuleForm[]; stream: AccountStreamInterceptRuleForm[] } | undefined {
    try {
      return {
        error: loadAccountErrorPolicyRules(credentials),
        stream: loadAccountStreamInterceptRules(credentials)
      }
    } catch (error) {
      console.error(error)
      message.error(`${label}配置异常，请先修正已保存的账户凭据`)
      return undefined
    }
  }

  function fillCloneForm(account: AccountSummary, credentials: Record<string, unknown>): boolean {
    const policyRules = loadCredentialPolicyRules(credentials, '克隆来源策略')
    if (!policyRules) return false
    const selectedGroup = account.boundGroupId
      ? groupSelectionForId(account.boundGroupId, account.boundGroupName)
      : undefined
    const defaults = defaultForm(account.providerCode, account.type)
    Object.assign(form, defaults, {
      providerCode: account.providerCode,
      name: cloneAccountName(account.name),
      type: account.type,
      concurrencyLimit: account.concurrencyLimit,
      priority: account.priority,
      proxyProfileId: account.proxyProfileId,
      accountExpiresAt: parseDatePickerValue(account.accountExpiresAt),
      groupId: selectedGroup?.id ?? options.groupIdForAccount(account.id),
      group: selectedGroup,
      apiKey: '',
      baseUrl: asString(credentials.base_url),
      accessToken: '',
      refreshToken: '',
      callbackUrl: '',
      oauthMode: 'manual',
      supportedModels: [...(account.supportedModels ?? [])],
      availabilitySchedule: createAccountAvailabilityScheduleForm(account.availabilitySchedule),
      notes: account.notes ?? ''
    })
    cloningScheduleFingerprint.value = accountAvailabilityScheduleFormFingerprint(form.availabilitySchedule)
    accountErrorPolicyRules.value = policyRules.error
    accountStreamInterceptRules.value = policyRules.stream
    authResult.value = undefined
    return true
  }

  function applyCloneCredentialDetails(account: AccountSummary, credentials: Record<string, unknown>): void {
    const defaults = defaultForm(account.providerCode, account.type)
    const baseUrl = asString(credentials.base_url)
    if (baseUrl && (!form.baseUrl || form.baseUrl === defaults.baseUrl)) {
      form.baseUrl = baseUrl
    }
    if (account.boundGroupId && (!form.groupId || form.groupId === account.boundGroupId)) {
      setFormGroup(groupSelectionForId(account.boundGroupId, account.boundGroupName))
    }
    if (cloningScheduleFingerprint.value === accountAvailabilityScheduleFormFingerprint(form.availabilitySchedule)) {
      form.availabilitySchedule = createAccountAvailabilityScheduleForm(account.availabilitySchedule)
      cloningScheduleFingerprint.value = accountAvailabilityScheduleFormFingerprint(form.availabilitySchedule)
    }
    if (accountErrorPolicyRules.value.length === 0) {
      const policyRules = loadCredentialPolicyRules(credentials, '克隆来源策略')
      if (policyRules) accountErrorPolicyRules.value = policyRules.error
    }
    if (accountStreamInterceptRules.value.length === 0) {
      const policyRules = loadCredentialPolicyRules(credentials, '克隆来源策略')
      if (policyRules) accountStreamInterceptRules.value = policyRules.stream
    }
  }

  function cloneAccountName(name: string): string {
    const trimmed = name.trim()
    return trimmed ? `${trimmed} - 克隆` : ''
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
}

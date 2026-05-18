import { message } from '@/lib/antd'
import { computed, reactive, ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import { useSubmitAction } from '@/composables/useSubmitAction'
import type {
  AccountSummary,
  AccountType,
  AccountGroupOptionSummary,
  OpenAIAuthURLResult,
  ProviderDefinition,
  SystemAccountPrincipalSummary
} from '@/types/domain'
import {
  defaultGroupForProvider as selectDefaultGroupForProvider,
  groupOptionsForProvider,
  isManageableGroupForProvider,
  providerNameByCodeMap,
  targetSystemAccountLabel as buildTargetSystemAccountLabel
} from './accountDerivedState'
import {
  loadAccountErrorPolicyRules,
  type AccountErrorPolicyRuleForm
} from './accountErrorPolicy'
import { defaultAccountForm } from './accountFormDefaults'
import {
  accountTypeDescription,
  accountTypeText,
  accountTypeTitle as buildAccountTypeTitle,
  asString,
  parseDatePickerValue
} from './accountFormatters'
import type { AccountFormModel } from './accountFormTypes'
import { FALLBACK_PROVIDER } from './accountOptions'
import { authUrl, buildOAuthCreatePayload } from './accountOAuthPayload'
import { buildAccountSavePayload, buildOAuthCreateCommonPayload, validateAccountSaveForm } from './accountSavePayload'

type ReadonlyValue<T> = {
  readonly value: T
}

interface UseAccountEditFormOptions {
  accountScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  accounts: ReadonlyValue<AccountSummary[]>
  extractApiErrorMessage: (error: unknown, fallback: string) => string
  groupIdForAccount: (accountId: string) => string | undefined
  groups: ReadonlyValue<AccountGroupOptionSummary[]>
  isManagementView: ComputedRef<boolean>
  loadData: () => Promise<void>
  providers: ReadonlyValue<ProviderDefinition[]>
  systemAccounts: ReadonlyValue<SystemAccountPrincipalSummary[]>
}

export function useAccountEditForm(options: UseAccountEditFormOptions) {
  const { submitAction, submittingRef } = useSubmitAction('accounts')
  const saving = submittingRef('accounts.save')
  const authLoading = ref(false)
  const modalOpen = ref(false)
  const authResult = ref<OpenAIAuthURLResult>()
  const editingId = ref<string>()
  const form = reactive<AccountFormModel>(defaultForm())
  const accountErrorPolicyRules = ref<AccountErrorPolicyRuleForm[]>(loadAccountErrorPolicyRules())

  const targetSystemAccountLabel = computed(() => {
    if (!options.isManagementView.value) return undefined
    const systemAccountId = options.accountScopeParams.value?.systemAccountId
    return buildTargetSystemAccountLabel(options.systemAccounts.value, systemAccountId)
  })

  const groupOptions = computed(() => groupOptionsForProvider(options.groups.value, form.providerCode))
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
  const modalTitle = computed(() => {
    if (editingId.value) return '编辑账户'
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
    Object.assign(form, defaultForm(providerCode, type))
    ensureDefaultGroupSelected(providerCode)
    accountErrorPolicyRules.value = loadAccountErrorPolicyRules()
    authResult.value = undefined
  }

  function accountTypeTitle(providerCode: string, type: AccountType) {
    return buildAccountTypeTitle(providerName(providerCode), type)
  }

  function providerName(providerCode?: string) {
    if (!providerCode) return '未知供应商'
    return providerNameByCode.value.get(providerCode) ?? providerCode
  }

  function defaultGroupForProvider(providerCode: string) {
    return selectDefaultGroupForProvider(options.groups.value, providerCode)
  }

  function ensureDefaultGroupSelected(providerCode = form.providerCode) {
    if (!providerCode) {
      form.groupId = undefined
      return
    }
    const currentGroup = options.groups.value.find((group) => group.id === form.groupId)
    if (currentGroup && isManageableGroupForProvider(currentGroup, providerCode)) {
      return
    }
    form.groupId = defaultGroupForProvider(providerCode)?.id
  }

  function openCreate() {
    if (options.isManagementView.value && !options.accountScopeParams.value?.systemAccountId) {
      message.warning('请先在右侧选择目标系统账户，再创建 AI 账户')
      return
    }
    editingId.value = undefined
    resetForm('', '')
    modalOpen.value = true
  }

  function handleModalCancel() {
    authResult.value = undefined
  }

  function selectProvider(providerCode: string) {
    if (editingId.value || form.providerCode === providerCode) return
    resetForm(providerCode, '')
  }

  function selectAccountType(type: AccountType) {
    if (editingId.value || form.type === type) return
    const providerCode = form.providerCode
    Object.assign(form, {
      ...defaultForm(providerCode, type),
      groupId: form.groupId,
      proxyProfileId: form.proxyProfileId,
      notes: form.notes,
      concurrencyLimit: form.concurrencyLimit,
      priority: form.priority,
      accountExpiresAt: form.accountExpiresAt
    })
    ensureDefaultGroupSelected(providerCode)
    authResult.value = undefined
  }

  function openEdit(account: AccountSummary) {
    editingId.value = account.id
    Object.assign(form, defaultForm(account.providerCode, account.type), {
      providerCode: account.providerCode,
      name: account.name,
      type: account.type,
      concurrencyLimit: account.concurrencyLimit,
      priority: account.priority,
      proxyProfileId: account.proxyProfileId,
      accountExpiresAt: parseDatePickerValue(account.accountExpiresAt),
      groupId: options.groupIdForAccount(account.id),
      apiKey: asString(account.credentials.api_key),
      baseUrl: asString(account.credentials.base_url) || 'https://api.openai.com/v1',
      accessToken: asString(account.credentials.access_token),
      refreshToken: asString(account.credentials.refresh_token),
      notes: account.notes ?? ''
    })
    accountErrorPolicyRules.value = loadAccountErrorPolicyRules(account.credentials)
    authResult.value = undefined
    modalOpen.value = true
  }

  const saveAccount = submitAction('accounts.save', async () => {
    const validationMessage = validateAccountSaveForm({
      editingId: editingId.value,
      form,
      hasAuthSession: Boolean(authResult.value?.sessionId),
      errorPolicyRules: accountErrorPolicyRules.value
    })
    if (validationMessage) {
      message.warning(validationMessage)
      return
    }

    const payload = buildAccountSavePayload({
      accounts: options.accounts.value,
      editingId: editingId.value,
      form,
      errorPolicyRules: accountErrorPolicyRules.value
    })

    try {
      if (editingId.value) {
        if (options.isManagementView.value) {
          await api.accounts.update(editingId.value, payload, options.accountScopeParams.value)
        } else {
          await api.myAccounts.update(editingId.value, payload)
        }
        message.success('账户已更新')
      } else if (form.type === 'oauth') {
        await createOAuthAccountFromUnifiedForm()
        message.success('OAuth 账户已创建')
      } else {
        if (options.isManagementView.value) {
          await api.accounts.create(payload, options.accountScopeParams.value)
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
      errorPolicyRules: accountErrorPolicyRules.value
    })

    const payload = buildOAuthCreatePayload({
      commonPayload,
      form,
      sessionId: authResult.value?.sessionId
    })

    if (form.oauthMode === 'manual') {
      if (options.isManagementView.value) {
        await api.openaiOAuth.createFromCode(payload, options.accountScopeParams.value)
      } else {
        await api.myOpenaiOAuth.createFromCode(payload)
      }
      return
    }

    if (options.isManagementView.value) {
      await api.openaiOAuth.createFromRefreshToken(payload, options.accountScopeParams.value)
    } else {
      await api.myOpenaiOAuth.createFromRefreshToken(payload)
    }
  }

  return {
    accountErrorPolicyRules,
    accountTypeChoices,
    authLoading,
    authResult,
    availableProviders,
    editingId,
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
    openCreate,
    openEdit,
    providerName,
    saveAccount,
    selectAccountType,
    selectedAccountTypeTitle,
    selectedProvider,
    selectProvider,
    targetSystemAccountLabel
  }
}

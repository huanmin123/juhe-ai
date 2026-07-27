import { api } from '@/api/client'
import { useSubmitAction } from '@/composables/useSubmitAction'
import { message } from '@/lib/antd'
import type { AccountSummary, OpenAIAuthURLResult, ProviderDefinition } from '@/types/domain'
import { ref, type ComputedRef, type Ref } from 'vue'

import type { AccountErrorPolicyRuleForm } from './accountErrorPolicyTypes'
import type { AccountFormModel } from './accountFormTypes'
import type { AccountResponseInspectionRuleForm } from './accountResponseInspectionPolicyTypes'
import { isAuthorizedAccount } from './accountFormatters'
import { buildOAuthCreatePayload } from './accountOAuthPayload'
import type { AccountScopeParams } from './accountOperationScope'
import {
  ACCOUNT_API_KEY_BATCH_CREATE_LIMIT,
  buildAccountSavePayload,
  buildAccountUpdatePayload,
  buildOAuthCreateCommonPayload,
  resolveFormProviderProfile,
  validateAccountSaveForm,
  type AccountSavePayload
} from './accountSavePayload'
import { validateOpenAICompatibleBaseUrl } from './accountBaseUrlValidation'
import { accountBalanceWillAutoDisable, buildAccountBalancePayload } from './accountBalanceQuery'
import {
  normalizedAccountApiKeys,
  normalizedAccountApiKeyWeights
} from './accountCredentials'
import { canCreateOAuthAccount } from './accountProviderCapabilities'
import {
  normalizeFormTagNames,
  sameTagNames,
  type AccountModelSelectOption
} from './accountEditFormPayload'

type ReadonlyValue<T> = {
  readonly value: T
}

interface UseAccountEditSaveFlowOptions {
  accountAdvancedDetailLoaded: ReadonlyValue<boolean>
  accountErrorPolicyRules: Ref<AccountErrorPolicyRuleForm[]>
  accountResponseInspectionRules: Ref<AccountResponseInspectionRuleForm[]>
  accounts: ReadonlyValue<AccountSummary[]>
  createScopeParams: ComputedRef<AccountScopeParams>
  editingAccountDetail: Ref<AccountSummary | undefined>
  editingAccountScopeParams: () => AccountScopeParams
  editingAuthorizedAccount: ComputedRef<boolean>
  editingId: Ref<string | undefined>
  extractApiErrorMessage: (error: unknown, fallback: string) => string
  form: AccountFormModel
  isManagementView: ComputedRef<boolean>
  loadData: () => Promise<void>
  mappingAnthropicSourceModelOptions: ReadonlyValue<AccountModelSelectOption[]>
  mappingCurrentProviderSourceModelOptions: ReadonlyValue<AccountModelSelectOption[]>
  mappingGeminiSourceModelOptions: ReadonlyValue<AccountModelSelectOption[]>
  mappingSourceModelOptions: ReadonlyValue<AccountModelSelectOption[]>
  providerModelOptions: ReadonlyValue<AccountModelSelectOption[]>
  modalOpen: Ref<boolean>
  providers: ReadonlyValue<ProviderDefinition[]>
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

    if (options.editingId.value && !options.accountAdvancedDetailLoaded.value) {
      await saveBasicAccountEdit()
      return
    }

    const validationMessage = validateAccountSaveForm({
      editingId: options.editingId.value,
      form: options.form,
      hasAuthSession: Boolean(authResult.value?.sessionId),
      errorPolicyRules: options.accountErrorPolicyRules.value,
      responseInspectionRules: options.accountResponseInspectionRules.value,
      providers: options.providers.value,
      mappingAnthropicSourceModelOptions: options.mappingAnthropicSourceModelOptions.value,
      mappingCurrentProviderSourceModelOptions: options.mappingCurrentProviderSourceModelOptions.value,
      mappingGeminiSourceModelOptions: options.mappingGeminiSourceModelOptions.value,
      mappingSourceModelOptions: options.mappingSourceModelOptions.value,
      mappingUpstreamModelOptions: options.providerModelOptions.value
    })
    if (validationMessage) {
      message.warning(validationMessage)
      return
    }

    const balanceAutoDisabled = accountBalanceWillAutoDisable(options.form)
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
        const updatePayload = buildAccountUpdatePayload(payload, options.editingAccountDetail.value?.status)
        if (options.isManagementView.value) {
          await api.accounts.update(options.editingId.value, updatePayload, options.editingAccountScopeParams())
        } else {
          await api.myAccounts.update(options.editingId.value, updatePayload)
        }
        message.success(balanceAutoDisabled ? '账户已更新，已因多 Key 自动关闭余额查询' : '账户已更新')
      } else if (options.form.type === 'oauth') {
        const created = usesManagedOAuthCreateFlow(options.form, options.providers.value)
          ? await createOAuthAccountFromUnifiedForm()
          : await createApiKeyAccount(payload)
        message.success(created?.status === 'active' ? 'OAuth 账户已创建并启用' : 'OAuth 账户已创建，等待后台检查')
      } else {
        const created = await createApiKeyAccount(payload)
        message.success(balanceAutoDisabled
          ? '账户已创建，已因多 Key 自动关闭余额查询'
          : created?.status === 'active' ? '账户已创建并启用' : '账户已创建，等待后台检查')
      }
      options.modalOpen.value = false
      await options.loadData()
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '保存账户失败'))
    }
  })

  async function generateOAuthUrl() {
    if (!usesManagedOAuthCreateFlow(options.form, options.providers.value)) {
      message.warning('当前供应商 OAuth 使用直接录入 Access Token，不提供站内授权链接')
      return
    }
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
      }
      message.success('授权账户已更新')
      options.modalOpen.value = false
      await options.loadData()
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '保存授权账户失败'))
    }
  }

  async function saveBasicAccountEdit(): Promise<void> {
    if (!options.editingId.value) return
    if (!options.form.name.trim()) {
      message.warning('请填写账户名称')
      return
    }
    if (!options.form.groupId) {
      message.warning('请选择加入分组')
      return
    }
    const concurrencyLimit = Number(options.form.concurrencyLimit)
    if (!Number.isFinite(concurrencyLimit) || concurrencyLimit < 1) {
      message.warning('并发上限必须是大于 0 的整数')
      return
    }
    const priority = Number(options.form.priority)
    if (!Number.isFinite(priority) || priority < 0) {
      message.warning('优先级必须是大于等于 0 的整数')
      return
    }
    const credentialValidationMessage = validateBasicEditCredentialFields(options.form)
    if (credentialValidationMessage) {
      message.warning(credentialValidationMessage)
      return
    }
    const tagValidationMessage = validateBasicEditTags(options.form.tags)
    if (tagValidationMessage) {
      message.warning(tagValidationMessage)
      return
    }
    const supportedModels = normalizeBasicEditSupportedModels(options.form.supportedModels)
    if (!supportedModels.length) {
      message.warning('请选择支持模型')
      return
    }
    const healthCheckModel = options.form.healthCheckModel.trim()
    if (healthCheckModel && !supportedModels.includes(healthCheckModel)) {
      message.warning('检查模型必须从账户支持模型中选择')
      return
    }
    const balanceAutoDisabled = accountBalanceWillAutoDisable(options.form)
    const payload: AccountBasicEditPayload = {
      name: options.form.name.trim(),
      concurrencyLimit: Math.trunc(concurrencyLimit),
      priority: Math.trunc(priority),
      ...(options.form.status === 'active' || options.form.status === 'disabled'
        ? { status: options.form.status }
        : {}),
      superPriorityEnabled: options.form.privilege === 'super_priority',
      fallbackEnabled: options.form.privilege === 'fallback',
      groupId: options.form.groupId,
      tags: normalizeFormTagNames(options.form.tags),
      notes: options.form.notes,
      supportedModels,
      ...(healthCheckModel ? { healthCheckModel } : {}),
      healthCheckEndpointMode: options.form.healthCheckEndpointMode,
      credentials: buildBasicEditCredentialsPatch(options.form, options.editingAccountDetail.value?.credentials),
      ...buildAccountBalancePayload(options.form)
    }
    try {
      if (options.isManagementView.value) {
        await api.accounts.update(options.editingId.value, payload, options.editingAccountScopeParams())
      } else {
        await api.myAccounts.update(options.editingId.value, payload)
      }
      message.success(balanceAutoDisabled ? '账户基础信息已更新，已因多 Key 自动关闭余额查询' : '账户基础信息已更新')
      options.modalOpen.value = false
      await options.loadData()
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '保存账户失败'))
    }
  }

  async function createOAuthAccountFromUnifiedForm(): Promise<AccountSummary> {
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
        return await api.openaiOAuth.createFromCode(payload, options.createScopeParams.value)
      } else {
        return await api.myOpenaiOAuth.createFromCode(payload)
      }
    }

    if (options.isManagementView.value) {
      return await api.openaiOAuth.createFromRefreshToken(payload, options.createScopeParams.value)
    } else {
      return await api.myOpenaiOAuth.createFromRefreshToken(payload)
    }
  }

  async function createApiKeyAccount(payload: AccountSavePayload): Promise<AccountSummary> {
    return options.isManagementView.value
      ? api.accounts.create(payload, options.createScopeParams.value)
      : api.myAccounts.create(payload)
  }

  return {
    authLoading,
    authResult,
    generateOAuthUrl,
    saveAccount,
    saving
  }
}

type AccountBasicEditPayload = {
  name: string
  concurrencyLimit: number
  priority: number
  status?: 'active' | 'disabled'
  superPriorityEnabled?: boolean
  fallbackEnabled?: boolean
  groupId: string
  tags: string[]
  notes: string
  supportedModels: string[]
  healthCheckModel?: string
  healthCheckEndpointMode: AccountFormModel['healthCheckEndpointMode']
  credentials: Record<string, unknown>
  balanceQueryEnabled?: boolean
  balanceQueryConfig?: Record<string, unknown>
}

export function validateBasicEditCredentialFields(form: AccountFormModel): string | undefined {
  if (form.type === 'api_key') {
    if (!form.baseUrl.trim()) return '请填写 Base URL'
    const baseUrlValidation = validateOpenAICompatibleBaseUrl(form.baseUrl)
    if (baseUrlValidation) return baseUrlValidation
    const apiKeyCount = normalizedAccountApiKeys(form).length
    if (apiKeyCount > ACCOUNT_API_KEY_BATCH_CREATE_LIMIT) return `单个账户最多配置 ${ACCOUNT_API_KEY_BATCH_CREATE_LIMIT} 个 API Key`
  }
  if (form.type === 'oauth') {
    if (!form.baseUrl.trim()) return '请填写 Base URL'
  }
  if (form.type === 'google_oauth') {
    if (!form.baseUrl.trim()) return '请填写 Base URL'
  }
  return undefined
}

function validateBasicEditTags(value: string[]): string | undefined {
  const normalized = normalizeFormTagNames(value)
  if (normalized.length > 24) return '单个账户最多配置 24 个标签'
  if (normalized.some((item) => item.length > 40)) return '账户标签不能超过 40 个字符'
  return undefined
}

function normalizeBasicEditSupportedModels(value: string[]): string[] {
  const output: string[] = []
  const seen = new Set<string>()
  for (const item of value ?? []) {
    const model = item.trim()
    if (!model || seen.has(model)) continue
    seen.add(model)
    output.push(model)
  }
  return output
}

function buildBasicEditCredentialsPatch(form: AccountFormModel, currentCredentials: Record<string, unknown> = {}): Record<string, unknown> {
  const credentials: Record<string, unknown> = {
    supported_endpoint_modes: [...form.supportedEndpointModes]
  }
  if (form.type === 'api_key') {
    credentials.base_url = form.baseUrl.trim()
    const apiKeys = normalizedAccountApiKeys(form)
    if (apiKeys.length > 0) {
      credentials.api_key = apiKeys[0]
    }
    if (apiKeys.length > 1) {
      credentials.api_keys = apiKeys
      credentials.api_key_strategy = form.apiKeyStrategy === 'weighted_round_robin'
        ? 'weighted_round_robin'
        : 'round_robin'
      if (credentials.api_key_strategy === 'weighted_round_robin') {
        credentials.api_key_weights = normalizedAccountApiKeyWeights(form, apiKeys.length)
      }
    }
  } else if (form.type === 'oauth') {
    credentials.base_url = form.baseUrl.trim() || credentialText(currentCredentials.base_url) || ''
    if (form.accessToken.trim() || credentialText(currentCredentials.access_token)) {
      credentials.access_token = form.accessToken.trim() || credentialText(currentCredentials.access_token)
    }
    if (form.refreshToken.trim() || credentialText(currentCredentials.refresh_token)) {
      credentials.refresh_token = form.refreshToken.trim() || credentialText(currentCredentials.refresh_token)
    }
  } else if (form.type === 'google_oauth') {
    if (form.accessToken.trim() || credentialText(currentCredentials.access_token)) {
      credentials.access_token = form.accessToken.trim() || credentialText(currentCredentials.access_token)
    }
    if (form.refreshToken.trim() || credentialText(currentCredentials.refresh_token)) {
      credentials.refresh_token = form.refreshToken.trim() || credentialText(currentCredentials.refresh_token)
    }
    if (form.googleClientId.trim() || credentialText(currentCredentials.client_id)) {
      credentials.client_id = form.googleClientId.trim() || credentialText(currentCredentials.client_id)
    }
    if (form.googleClientSecret.trim() || credentialText(currentCredentials.client_secret)) {
      credentials.client_secret = form.googleClientSecret.trim() || credentialText(currentCredentials.client_secret)
    }
    if (form.googleQuotaProjectId.trim() || credentialText(currentCredentials.quota_project_id)) {
      credentials.quota_project_id = form.googleQuotaProjectId.trim() || credentialText(currentCredentials.quota_project_id)
    }
    credentials.base_url = form.baseUrl.trim() || credentialText(currentCredentials.base_url) || ''
  }
  return credentials
}

function usesManagedOAuthCreateFlow(form: AccountFormModel, providers: readonly ProviderDefinition[]): boolean {
  return canCreateOAuthAccount(
    providers.length
      ? resolveFormProviderProfile(form, [...providers])
      : resolveFormProviderProfile(form)
  )
}

function credentialText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

import { api } from '@/api/client'
import { useSubmitAction } from '@/composables/useSubmitAction'
import { message } from '@/lib/antd'
import type {
  AccountAdvancedDetail,
  AccountCreateResult,
  AccountEditBasicDetail,
  AccountListItem,
  AccountSummary,
  OAuthAuthURLResult,
  ProviderDefinition
} from '@/types/domain'
import { ref, watch, type ComputedRef, type Ref } from 'vue'

import type { AccountErrorPolicyRuleForm } from './accountErrorPolicyTypes'
import type { AccountFormModel } from './accountFormTypes'
import type { AccountResponseInspectionRuleForm } from './accountResponseInspectionPolicyTypes'
import { isAuthorizedAccount } from './accountFormatters'
import { buildOAuthCreatePayload, openAIOAuthClientPayload } from './accountOAuthPayload'
import type { AccountScopeParams } from './accountOperationScope'
import {
  ACCOUNT_API_KEY_BATCH_CREATE_LIMIT,
  buildAccountSavePayload,
  buildOAuthCreateCommonPayload,
  resolveFormProviderProfile,
  validateAccountSaveForm,
  type AccountSavePayload
} from './accountSavePayload'
import { validateOpenAICompatibleBaseUrl } from './accountBaseUrlValidation'
import { accountBalanceWillAutoDisable } from './accountBalanceQuery'
import {
  normalizedAccountApiKeys
} from './accountCredentials'
import { managedOAuthProviderKind, type ManagedOAuthProviderKind } from './accountProviderCapabilities'
import { normalizeGrokSsoTokens } from './grokSsoTokens'
import {
  normalizeFormTagNames,
  sameTagNames,
  type AccountModelSelectOption
} from './accountEditFormPayload'
import {
  buildAccountAdvancedUpdatePatch,
  buildAccountBasicEditSnapshot,
  buildAccountBasicUpdatePatch,
  type AccountBasicEditSnapshot
} from './accountEditPatch'

type ReadonlyValue<T> = {
  readonly value: T
}

interface UseAccountEditSaveFlowOptions {
  accountAdvancedDetailLoaded: ReadonlyValue<boolean>
  accountErrorPolicyRules: Ref<AccountErrorPolicyRuleForm[]>
  accountResponseInspectionRules: Ref<AccountResponseInspectionRuleForm[]>
  accounts: ReadonlyValue<AccountListItem[]>
  createScopeParams: ComputedRef<AccountScopeParams>
  editingAccountDetail: Ref<AccountEditBasicDetail | undefined>
  editingAccountAdvancedDetail: Ref<AccountAdvancedDetail | undefined>
  editingAdvancedBaseline: ReadonlyValue<AccountSavePayload | undefined>
  editingBasicBaseline: ReadonlyValue<AccountBasicEditSnapshot | undefined>
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
  const authResult = ref<OAuthAuthURLResult>()

  watch(() => options.form.oauthType, () => {
    authResult.value = undefined
  })

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

    const advancedDetail = options.editingId.value
      ? options.editingAccountAdvancedDetail.value
      : undefined
    if (options.editingId.value && !advancedDetail) {
      message.error('账户高级配置详情缺失，请关闭弹窗并刷新列表后重试')
      return
    }
    const configRevision = advancedDetail
      ? requiredAccountConfigRevision(advancedDetail)
      : undefined
    if (advancedDetail && configRevision === undefined) return

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
        if (configRevision === undefined) return
        const baseline = options.editingAdvancedBaseline.value
        if (!baseline) {
          message.error('账户高级配置基线缺失，请关闭弹窗后重试')
          return
        }
        const updatePayload = buildAccountAdvancedUpdatePatch(
          payload,
          baseline,
          configRevision
        )
        if (!updatePayload) {
          finishUnchangedEdit()
          return
        }
        if (options.isManagementView.value) {
          await api.accounts.update(options.editingId.value, updatePayload, options.editingAccountScopeParams())
        } else {
          await api.myAccounts.update(options.editingId.value, updatePayload)
        }
        message.success(balanceAutoDisabled ? '账户已更新，已因多 Key 自动关闭余额查询' : '账户已更新')
      } else if (options.form.type === 'oauth' || options.form.type === 'google_oauth') {
        if (options.form.oauthMode === 'sso_cookie') {
          const importComplete = await importGrokSsoAccounts()
          if (!importComplete) {
            await options.loadData()
            return
          }
        } else {
          const created = usesManagedOAuthCreateFlow(options.form, options.providers.value) && options.form.oauthMode !== 'access_token'
            ? await createOAuthAccountFromUnifiedForm()
            : await createApiKeyAccount(payload)
          message.success(created?.status === 'active' ? 'OAuth 账户已创建并启用' : 'OAuth 账户已创建，等待后台检查')
        }
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
    const providerKind = resolveManagedOAuthProvider(options.form, options.providers.value)
    if (providerKind === 'gemini'
      && options.form.oauthType === 'ai_studio'
      && (!options.form.googleClientId.trim() || !options.form.googleClientSecret.trim())) {
      message.warning('请先填写 Google OAuth Client ID 和 Client Secret')
      return
    }
    authLoading.value = true
    try {
      authResult.value = await requestManagedOAuthAuthUrl(options.form, options.providers.value, options.isManagementView.value)
      message.success('授权链接已生成')
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '生成授权链接失败'))
    } finally {
      authLoading.value = false
    }
  }

  async function saveAuthorizedAccountEdit(): Promise<void> {
    const account = options.accounts.value.find((item) => item.id === options.editingId.value)
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
    const expectedConfigRevision = account.configRevision
    if (!Number.isInteger(expectedConfigRevision) || Number(expectedConfigRevision) < 1) {
      message.error('账户配置版本缺失，请刷新列表后重试')
      return
    }
    const payload: Record<string, unknown> & { expectedConfigRevision: number } = {
      expectedConfigRevision: Number(expectedConfigRevision)
    }
    if (options.form.groupId !== account.boundGroupId) payload.groupId = options.form.groupId
    if (nextPriority !== account.priority) payload.priority = nextPriority
    if (!sameTagNames(options.form.tags, account.tags)) {
      payload.tags = normalizeFormTagNames(options.form.tags)
    }
    if (Object.keys(payload).length === 1) {
      finishUnchangedEdit()
      return
    }
    try {
      if (options.isManagementView.value) {
        await api.accounts.update(account.id, payload, scopeParams)
      } else {
        await api.myAccounts.update(account.id, payload)
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
    const baseline = options.editingBasicBaseline.value
    if (!baseline) {
      message.error('账户基础配置基线缺失，请关闭弹窗后重试')
      return
    }
    const accountDetail = accountBasicDetail(options.editingAccountDetail.value)
    if (!accountDetail) {
      message.error('账户基础配置详情缺失，请关闭弹窗并刷新列表后重试')
      return
    }
    const configRevision = requiredAccountConfigRevision(accountDetail)
    if (configRevision === undefined) return
    const current = buildAccountBasicEditSnapshot(
      options.form,
      accountDetail.credentials
    )
    const payload = buildAccountBasicUpdatePatch(
      current,
      baseline,
      configRevision
    )
    if (!payload) {
      finishUnchangedEdit()
      return
    }
    try {
      if (options.isManagementView.value) {
        await api.accounts.update(options.editingId.value, payload, options.editingAccountScopeParams())
      } else {
        await api.myAccounts.update(options.editingId.value, payload)
      }
      message.success('账户基础信息已更新')
      options.modalOpen.value = false
      await options.loadData()
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '保存账户失败'))
    }
  }

  function finishUnchangedEdit(): void {
    message.info('未检测到账户修改')
    options.modalOpen.value = false
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
      return await createManagedOAuthAccountFromCode(options.form, options.providers.value, payload, options.createScopeParams.value, options.isManagementView.value)
    }
    if (options.form.oauthMode === 'refresh_token') {
      return await createManagedOAuthAccountFromRefreshToken(options.form, options.providers.value, payload, options.createScopeParams.value, options.isManagementView.value)
    }

    throw new Error('当前授权方式不应走托管 OAuth 创建流程')
  }

  async function createApiKeyAccount(payload: AccountSavePayload): Promise<AccountCreateResult> {
    return options.isManagementView.value
      ? api.accounts.create(payload, options.createScopeParams.value)
      : api.myAccounts.create(payload)
  }

  async function importGrokSsoAccounts(): Promise<boolean> {
    if (resolveManagedOAuthProvider(options.form, options.providers.value) !== 'grok') {
      throw new Error('SSO Cookie 导入仅支持 Grok OAuth')
    }
    const ssoTokens = normalizeGrokSsoTokens(options.form.ssoTokens)
    const commonPayload = buildOAuthCreateCommonPayload({
      accounts: options.accounts.value,
      editingId: options.editingId.value,
      form: options.form,
      errorPolicyRules: options.accountErrorPolicyRules.value,
      responseInspectionRules: options.accountResponseInspectionRules.value
    })
    const result = options.isManagementView.value
      ? await api.grokOAuth.ssoToOAuth({ ...commonPayload, ssoTokens }, options.createScopeParams.value)
      : await api.myGrokOAuth.ssoToOAuth({ ...commonPayload, ssoTokens })
    if (!result.failed.length) {
      message.success(`Grok SSO 导入完成：成功 ${result.created.length} 个`)
      return true
    }
    const failedTokens = result.failed
      .map((item) => ssoTokens[item.index - 1])
      .filter((token): token is string => Boolean(token))
    options.form.ssoTokens = failedTokens.join('\n')
    const detail = result.failed.slice(0, 3).map((item) => `第 ${item.index} 项：${item.error}`).join('；')
    message.warning(`Grok SSO 导入完成：成功 ${result.created.length} 个，失败 ${result.failed.length} 个。${detail}`)
    return false
  }

  return {
    authLoading,
    authResult,
    generateOAuthUrl,
    saveAccount,
    saving
  }
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

function accountBasicDetail(account: AccountEditBasicDetail | undefined): AccountEditBasicDetail | undefined {
  return account
}

function requiredAccountConfigRevision(account: { configRevision?: number }): number | undefined {
  const configRevision = account.configRevision
  if (typeof configRevision === 'number' && Number.isInteger(configRevision) && configRevision >= 1) {
    return configRevision
  }
  message.error('账户配置版本缺失或无效，请关闭弹窗并刷新列表后重试')
  return undefined
}

function usesManagedOAuthCreateFlow(form: AccountFormModel, providers: readonly ProviderDefinition[]): boolean {
  return resolveManagedOAuthProvider(form, providers) !== undefined
}

function resolveManagedOAuthProvider(
  form: AccountFormModel,
  providers: readonly ProviderDefinition[]
): ManagedOAuthProviderKind | undefined {
  const providerProfile = providers.length
    ? resolveFormProviderProfile(form, [...providers])
    : resolveFormProviderProfile(form)
  return managedOAuthProviderKind(providerProfile)
}

async function requestManagedOAuthAuthUrl(
  form: AccountFormModel,
  providers: readonly ProviderDefinition[],
  isManagementView: boolean
): Promise<OAuthAuthURLResult> {
  const providerKind = resolveManagedOAuthProvider(form, providers)
  if (providerKind === 'anthropic') return isManagementView ? api.anthropicOAuth.authUrl({}) : api.myAnthropicOAuth.authUrl({})
  if (providerKind === 'gemini') {
    const payload = geminiOAuthClientPayload(form)
    return isManagementView ? api.geminiOAuth.authUrl(payload) : api.myGeminiOAuth.authUrl(payload)
  }
  if (providerKind === 'grok') return isManagementView ? api.grokOAuth.authUrl({}) : api.myGrokOAuth.authUrl({})
  return isManagementView ? api.openaiOAuth.authUrl({}) : api.myOpenaiOAuth.authUrl({})
}

async function createManagedOAuthAccountFromCode(
  form: AccountFormModel,
  providers: readonly ProviderDefinition[],
  payload: Record<string, unknown>,
  scopeParams: AccountScopeParams,
  isManagementView: boolean
): Promise<AccountSummary> {
  const providerKind = resolveManagedOAuthProvider(form, providers)
  if (providerKind === 'anthropic') {
    return isManagementView
      ? api.anthropicOAuth.createFromCode(payload, scopeParams)
      : api.myAnthropicOAuth.createFromCode(payload)
  }
  if (providerKind === 'gemini') {
    const geminiPayload = { ...payload, ...geminiOAuthClientPayload(form) }
    return isManagementView
      ? api.geminiOAuth.createFromCode(geminiPayload, scopeParams)
      : api.myGeminiOAuth.createFromCode(geminiPayload)
  }
  if (providerKind === 'grok') {
    return isManagementView
      ? api.grokOAuth.createFromCode(payload, scopeParams)
      : api.myGrokOAuth.createFromCode(payload)
  }
  return isManagementView
    ? api.openaiOAuth.createFromCode(payload, scopeParams)
    : api.myOpenaiOAuth.createFromCode(payload)
}

async function createManagedOAuthAccountFromRefreshToken(
  form: AccountFormModel,
  providers: readonly ProviderDefinition[],
  payload: Record<string, unknown>,
  scopeParams: AccountScopeParams,
  isManagementView: boolean
): Promise<AccountSummary> {
  const providerKind = resolveManagedOAuthProvider(form, providers)
  if (providerKind === 'anthropic') {
    return isManagementView
      ? api.anthropicOAuth.createFromRefreshToken(payload, scopeParams)
      : api.myAnthropicOAuth.createFromRefreshToken(payload)
  }
  if (providerKind === 'gemini') {
    const geminiPayload = { ...payload, ...geminiOAuthClientPayload(form) }
    return isManagementView
      ? api.geminiOAuth.createFromRefreshToken(geminiPayload, scopeParams)
      : api.myGeminiOAuth.createFromRefreshToken(geminiPayload)
  }
  if (providerKind === 'grok') {
    return isManagementView
      ? api.grokOAuth.createFromRefreshToken(payload, scopeParams)
      : api.myGrokOAuth.createFromRefreshToken(payload)
  }
  const openAIPayload = { ...payload, ...openAIOAuthClientPayload(form) }
  return isManagementView
    ? api.openaiOAuth.createFromRefreshToken(openAIPayload, scopeParams)
    : api.myOpenaiOAuth.createFromRefreshToken(openAIPayload)
}

function geminiOAuthClientPayload(form: AccountFormModel): Record<string, unknown> {
  return {
    oauthType: form.oauthType,
    tierId: form.tierId.trim(),
    ...(form.projectId.trim() ? { projectId: form.projectId.trim() } : {}),
    ...(form.oauthType === 'ai_studio' ? {
      clientId: form.googleClientId.trim(),
      clientSecret: form.googleClientSecret.trim()
    } : {}),
    ...(form.googleQuotaProjectId.trim() ? { quotaProjectId: form.googleQuotaProjectId.trim() } : {}),
    ...(form.baseUrl.trim() ? { baseUrl: form.baseUrl.trim() } : {})
  }
}

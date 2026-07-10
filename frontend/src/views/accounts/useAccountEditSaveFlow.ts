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
import { accountDefaultTestModelSaveQueue } from './accountDefaultTestModelSaveQueue'
import {
  ACCOUNT_API_KEY_BATCH_CREATE_LIMIT,
  buildAccountSavePayload,
  buildAccountUpdatePayload,
  buildOAuthCreateCommonPayload,
  validateAccountSaveForm,
  type AccountSavePayload
} from './accountSavePayload'
import { validateOpenAICompatibleBaseUrl } from './accountBaseUrlValidation'
import {
  normalizedAccountApiKeys,
  normalizedAccountApiKeyWeights
} from './accountCredentials'
import {
  normalizeFormTagNames,
  sameTagNames,
  type AccountModelSelectOption
} from './accountEditFormPayload'
import {
  invalidateAccountTagOptionsCache,
  resolveAccountTagOptionsScopeKey
} from './accountTagOptionsCache'
import { invalidateAccountDetailForAccount } from './accountDetailCache'

type ReadonlyValue<T> = {
  readonly value: T
}

interface UseAccountEditSaveFlowOptions {
  accountCreatePayloadWithActivationTest: (payload: AccountSavePayload) => AccountSavePayload & { status?: 'active'; activationTestTaskId?: string }
  accountUpdateDefaultTestModel: (payload: AccountSavePayload) => string | undefined
  accountUpdateActivationTestTaskId: (payload: AccountSavePayload) => string | undefined
  accountAdvancedDetailLoaded: ReadonlyValue<boolean>
  accountErrorPolicyRules: Ref<AccountErrorPolicyRuleForm[]>
  accountResponseInspectionRules: Ref<AccountResponseInspectionRuleForm[]>
  accounts: ReadonlyValue<AccountSummary[]>
  clearSuccessfulDraftActivationTest: () => void
  createScopeParams: ComputedRef<AccountScopeParams>
  editingAccountDetail: Ref<AccountSummary | undefined>
  editingAccountScopeParams: () => AccountScopeParams
  editingAuthorizedAccount: ComputedRef<boolean>
  editingId: Ref<string | undefined>
  extractApiErrorMessage: (error: unknown, fallback: string) => string
  form: AccountFormModel
  isApiKeyRuntimeChanged: () => boolean
  isManagementView: ComputedRef<boolean>
  loadData: () => Promise<void>
  mappingAnthropicSourceModelOptions: ReadonlyValue<AccountModelSelectOption[]>
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
      if (options.isApiKeyRuntimeChanged()) {
        message.warning('更换 API Key 或 Base URL 后请先点击测试，测试通过后再保存账户')
        return
      }
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
      mappingGeminiSourceModelOptions: options.mappingGeminiSourceModelOptions.value,
      mappingSourceModelOptions: options.mappingSourceModelOptions.value,
      mappingUpstreamModelOptions: options.providerModelOptions.value
    })
    if (validationMessage) {
      message.warning(validationMessage)
      return
    }

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
        await accountDefaultTestModelSaveQueue.whenIdle(options.editingId.value)
        const updatePayload = buildAccountUpdatePayload(payload)
        const activationTestTaskId = options.accountUpdateActivationTestTaskId(payload)
        const defaultTestModel = options.accountUpdateDefaultTestModel(payload)
        if (activationTestTaskId) {
          updatePayload.activationTestTaskId = activationTestTaskId
        }
        if (defaultTestModel) {
          updatePayload.defaultTestModel = defaultTestModel
        }
        if (options.isApiKeyRuntimeChanged()) {
          if (!activationTestTaskId) {
            message.warning('更换 API Key 或 Base URL 后请先测试通过，再保存账户')
            return
          }
        }
        if (options.isManagementView.value) {
          await api.accounts.update(options.editingId.value, updatePayload, options.editingAccountScopeParams())
        } else {
          await api.myAccounts.update(options.editingId.value, updatePayload)
        }
        invalidateAccountDetailOptions(options.editingId.value, options.editingAccountScopeParams())
        message.success('账户已更新')
      } else if (options.form.type === 'oauth') {
        const created = await createOAuthAccountFromUnifiedForm(options.accountCreatePayloadWithActivationTest(payload))
        message.success(created?.status === 'active' ? 'OAuth 账户已创建并启用' : 'OAuth 账户已创建，需测试通过后参与调度')
      } else {
        const created = await createApiKeyAccount(options.accountCreatePayloadWithActivationTest(payload))
        message.success(created?.status === 'active' ? '账户已创建并启用' : '账户已创建，需测试通过后参与调度')
      }
      invalidateAccountTagOptions(options.editingId.value ? options.editingAccountScopeParams() : options.createScopeParams.value)
      options.clearSuccessfulDraftActivationTest()
      options.modalOpen.value = false
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
        invalidateAccountTagOptions(scopeParams)
      }
      invalidateAccountDetailOptions(account.id, scopeParams)
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
    const payload: AccountBasicEditPayload = {
      name: options.form.name.trim(),
      concurrencyLimit: Math.trunc(concurrencyLimit),
      priority: Math.trunc(priority),
      groupId: options.form.groupId,
      tags: normalizeFormTagNames(options.form.tags),
      notes: options.form.notes,
      supportedModels,
      credentials: buildBasicEditCredentialsPatch(options.form)
    }
    try {
      if (options.isManagementView.value) {
        await api.accounts.update(options.editingId.value, payload, options.editingAccountScopeParams())
      } else {
        await api.myAccounts.update(options.editingId.value, payload)
      }
      invalidateAccountDetailOptions(options.editingId.value, options.editingAccountScopeParams())
      invalidateAccountTagOptions(options.editingAccountScopeParams())
      message.success('账户基础信息已更新')
      options.modalOpen.value = false
      await options.loadData()
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '保存账户失败'))
    }
  }

  async function createOAuthAccountFromUnifiedForm(activationPayload: AccountSavePayload & { status?: 'active'; activationTestTaskId?: string }): Promise<AccountSummary> {
    const commonPayload = buildOAuthCreateCommonPayload({
      accounts: options.accounts.value,
      editingId: options.editingId.value,
      form: options.form,
      errorPolicyRules: options.accountErrorPolicyRules.value,
      responseInspectionRules: options.accountResponseInspectionRules.value
    })
    if (activationPayload.defaultTestModel) {
      commonPayload.defaultTestModel = activationPayload.defaultTestModel
    }
    if (options.form.oauthMode === 'refresh_token' && activationPayload.activationTestTaskId) {
      commonPayload.status = 'active'
      commonPayload.activationTestTaskId = activationPayload.activationTestTaskId
    }

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

  function invalidateAccountTagOptions(scopeParams: AccountScopeParams | undefined): void {
    invalidateAccountTagOptionsCache(resolveAccountTagOptionsScopeKey(options.isManagementView.value, scopeParams))
  }

  function invalidateAccountDetailOptions(accountId: string | undefined, scopeParams: AccountScopeParams | undefined): void {
    if (!accountId) return
    invalidateAccountDetailForAccount({
      accountId,
      isManagementView: options.isManagementView.value,
      scopeParams
    })
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
  groupId: string
  tags: string[]
  notes: string
  supportedModels: string[]
  credentials: Record<string, unknown>
}

function validateBasicEditCredentialFields(form: AccountFormModel): string | undefined {
  if (form.type === 'api_key') {
    if (!form.baseUrl.trim()) return '请填写 Base URL'
    const baseUrlValidation = validateOpenAICompatibleBaseUrl(form.baseUrl)
    if (baseUrlValidation) return baseUrlValidation
    const apiKeyCount = normalizedAccountApiKeys(form).length
    if (apiKeyCount > ACCOUNT_API_KEY_BATCH_CREATE_LIMIT) return `单个账户最多配置 ${ACCOUNT_API_KEY_BATCH_CREATE_LIMIT} 个 API Key`
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

function buildBasicEditCredentialsPatch(form: AccountFormModel): Record<string, unknown> {
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
    if (form.accessToken.trim()) credentials.access_token = form.accessToken.trim()
    if (form.refreshToken.trim()) credentials.refresh_token = form.refreshToken.trim()
  }
  return credentials
}

import { message } from '@/lib/antd'
import { reactive, ref, watch, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import type { AccountListItem, AccountOAuthReauthorizationContext, OAuthAuthURLResult, OAuthCredentialRotationPayload, OAuthCredentialRotationResult } from '@/types/domain'
import type { AccountOAuthAuthorizeForm } from './accountFormTypes'
import { authUrl, buildReauthorizePayload, openAIOAuthClientPayload, validateReauthorizeForm } from './accountOAuthPayload'
import { isOAuthConfigRevisionConflict, requiredOAuthConfigRevision } from './accountOAuthRevision'
import { accountOperationScopeParams, type AccountScopeParams } from './accountOperationScope'
import { managedOAuthProviderKind, type ManagedOAuthProviderKind } from './accountProviderCapabilities'
import { canManageOAuthAccount } from './accountRules'
import { inferGeminiOAuthType } from './geminiOAuthType'

interface UseAccountReauthorizeOptions {
  accountScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  extractApiErrorMessage: (error: unknown, fallback: string) => string
  isManagementView: ComputedRef<boolean>
  loadData: () => Promise<void>
  updateLoadedAccountRevision: (accountId: string, configRevision: number) => boolean
}

export function useAccountReauthorize(options: UseAccountReauthorizeOptions) {
  const reauthorizeModalOpen = ref(false)
  const reauthorizeAuthLoading = ref(false)
  const reauthorizeSaving = ref(false)
  const reauthorizeAuthResult = ref<OAuthAuthURLResult>()
  const reauthorizingAccount = ref<AccountListItem>()
  const reauthorizingScopeParams = ref<AccountScopeParams>()
  let reauthorizeLoadToken = 0
  const reauthorizeForm = reactive<AccountOAuthAuthorizeForm>({
    oauthMode: 'manual',
    ssoTokens: '',
    callbackUrl: '',
    refreshToken: '',
    accessToken: '',
    googleClientId: '',
    googleClientSecret: '',
    googleQuotaProjectId: '',
    oauthType: 'code_assist',
    tierId: 'gcp_standard',
    projectId: '',
    baseUrl: ''
  })

  watch(() => reauthorizeForm.oauthType, () => {
    reauthorizeAuthResult.value = undefined
  })

  async function openReauthorizeModal(account: AccountListItem): Promise<void> {
    if (!canManageOAuthAccount(account)) {
      message.warning('只有支持 OAuth 管理的自有账户可以重新授权')
      return
    }
    const requestToken = ++reauthorizeLoadToken
    const scopeParams = accountOperationScopeParams(account, options.accountScopeParams.value)
    const providerKind = managedOAuthProviderKind({ profile: account })
    let context: AccountOAuthReauthorizationContext | undefined
    if (providerKind === 'gemini') {
      const hide = message.loading(`${account.name}: 正在加载授权配置...`, 0)
      try {
        context = options.isManagementView.value
          ? await api.accounts.oauthReauthorizationContext(account.id, scopeParams)
          : await api.myAccounts.oauthReauthorizationContext(account.id)
      } catch (error) {
        console.error(error)
        message.error(options.extractApiErrorMessage(error, `${account.name}: 加载授权配置失败`))
        return
      } finally {
        hide()
      }
    }
    if (requestToken !== reauthorizeLoadToken) return
    reauthorizingAccount.value = context
      ? { ...account, configRevision: context.configRevision }
      : account
    reauthorizingScopeParams.value = scopeParams
    reauthorizeForm.oauthMode = 'manual'
    reauthorizeForm.callbackUrl = ''
    reauthorizeForm.refreshToken = ''
    reauthorizeForm.accessToken = ''
    reauthorizeForm.googleClientId = credentialText(context?.clientId)
    reauthorizeForm.googleClientSecret = credentialText(context?.clientSecret)
    reauthorizeForm.googleQuotaProjectId = credentialText(context?.quotaProjectId)
    reauthorizeForm.oauthType = inferGeminiOAuthType({ oauth_type: context?.oauthType })
    reauthorizeForm.tierId = credentialText(context?.tierId) || defaultGeminiTierId(reauthorizeForm.oauthType)
    reauthorizeForm.projectId = credentialText(context?.projectId)
    reauthorizeForm.baseUrl = credentialText(context?.baseUrl)
    reauthorizeAuthResult.value = undefined
    reauthorizeModalOpen.value = true
  }

  function closeReauthorizeModal() {
    reauthorizeLoadToken += 1
    reauthorizingAccount.value = undefined
    reauthorizingScopeParams.value = undefined
    reauthorizeAuthResult.value = undefined
  }

  async function generateReauthorizeOAuthUrl() {
    reauthorizeAuthLoading.value = true
    try {
      const account = reauthorizingAccount.value
      if (!account) return
      const providerKind = managedOAuthProviderKind({ profile: account })
      if (providerKind === 'gemini'
        && reauthorizeForm.oauthType === 'ai_studio'
        && (!reauthorizeForm.googleClientId.trim() || !reauthorizeForm.googleClientSecret.trim())) {
        message.warning('请先填写 Google OAuth Client ID 和 Client Secret')
        return
      }
      reauthorizeAuthResult.value = await requestReauthorizeAuthUrl(providerKind, reauthorizeForm, options.isManagementView.value)
      message.success('授权链接已生成')
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '生成授权链接失败'))
    } finally {
      reauthorizeAuthLoading.value = false
    }
  }

  function openReauthorizeAuthUrl() {
    const url = authUrl(reauthorizeAuthResult.value?.authUrl)
    if (!url) return
    window.open(url, '_blank', 'noopener,noreferrer')
  }

  async function saveReauthorize() {
    const account = reauthorizingAccount.value
    if (!account || reauthorizeSaving.value) return
    const validationMessage = validateReauthorizeForm(reauthorizeForm, Boolean(reauthorizeAuthResult.value?.sessionId))
    if (validationMessage) {
      message.warning(validationMessage)
      return
    }
    const providerKind = managedOAuthProviderKind({ profile: account })
    if (!providerKind) {
      message.warning('当前供应商不支持托管 OAuth 重新授权')
      return
    }
    if (providerKind === 'gemini'
      && reauthorizeForm.oauthType === 'ai_studio'
      && (!reauthorizeForm.googleClientId.trim() || !reauthorizeForm.googleClientSecret.trim())) {
      message.warning('Gemini 官方 OAuth 需要 Client ID 和 Client Secret')
      return
    }
    if (reauthorizeForm.oauthMode === 'access_token') {
      message.warning('重新授权只支持浏览器回调 URL 或 Refresh Token；如仅更换 Access Token，请直接编辑账户保存')
      return
    }
    const expectedConfigRevision = requiredOAuthConfigRevision(account)
    if (expectedConfigRevision === undefined) {
      message.warning('账户配置版本缺失，请刷新列表后重试')
      return
    }

    reauthorizeSaving.value = true
    try {
      const basePayload = buildReauthorizePayload({
        form: reauthorizeForm,
        sessionId: reauthorizeAuthResult.value?.sessionId
      })
      const payload: OAuthCredentialRotationPayload = providerKind === 'gemini'
        ? { ...basePayload, ...geminiOAuthMetadataPayload(reauthorizeForm), expectedConfigRevision }
        : providerKind === 'openai' && reauthorizeForm.oauthMode === 'refresh_token'
          ? { ...basePayload, ...openAIOAuthClientPayload(reauthorizeForm), expectedConfigRevision }
          : { ...basePayload, expectedConfigRevision }
      const updated = reauthorizeForm.oauthMode === 'manual'
        ? await reauthorizeFromCode(providerKind, account, payload, options, reauthorizingScopeParams.value)
        : await reauthorizeFromRefreshToken(providerKind, account, payload, options, reauthorizingScopeParams.value)
      options.updateLoadedAccountRevision(account.id, updated.configRevision)
      message.success(`${account.name}: 重新授权成功`)
      reauthorizeModalOpen.value = false
      reauthorizingAccount.value = undefined
      reauthorizingScopeParams.value = undefined
      reauthorizeAuthResult.value = undefined
    } catch (error) {
      console.error(error)
      if (isOAuthConfigRevisionConflict(error)) {
        await options.loadData().catch((loadError) => console.error(loadError))
      }
      message.error(options.extractApiErrorMessage(error, `${account.name}: 重新授权失败`))
    } finally {
      reauthorizeSaving.value = false
    }
  }

  return {
    closeReauthorizeModal,
    generateReauthorizeOAuthUrl,
    openReauthorizeAuthUrl,
    openReauthorizeModal,
    reauthorizeAuthLoading,
    reauthorizeAuthResult,
    reauthorizeForm,
    reauthorizeModalOpen,
    reauthorizeSaving,
    reauthorizingAccount,
    saveReauthorize
  }
}

function credentialText(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function defaultGeminiTierId(oauthType: AccountOAuthAuthorizeForm['oauthType']): string {
  if (oauthType === 'code_assist') return 'gcp_standard'
  if (oauthType === 'google_one') return 'google_one_free'
  return 'aistudio_free'
}

function geminiOAuthMetadataPayload(form: AccountOAuthAuthorizeForm): Record<string, unknown> {
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

async function requestReauthorizeAuthUrl(
  providerKind: ManagedOAuthProviderKind | undefined,
  form: AccountOAuthAuthorizeForm,
  isManagementView: boolean
): Promise<OAuthAuthURLResult> {
  if (providerKind === 'anthropic') return isManagementView ? api.anthropicOAuth.authUrl({}) : api.myAnthropicOAuth.authUrl({})
  if (providerKind === 'gemini') {
    const payload = geminiOAuthMetadataPayload(form)
    return isManagementView ? api.geminiOAuth.authUrl(payload) : api.myGeminiOAuth.authUrl(payload)
  }
  if (providerKind === 'grok') return isManagementView ? api.grokOAuth.authUrl({}) : api.myGrokOAuth.authUrl({})
  return isManagementView ? api.openaiOAuth.authUrl({}) : api.myOpenaiOAuth.authUrl({})
}

async function reauthorizeFromCode(
  providerKind: ManagedOAuthProviderKind,
  account: Pick<AccountListItem, 'id'>,
  payload: OAuthCredentialRotationPayload,
  options: UseAccountReauthorizeOptions,
  scopeParams: AccountScopeParams | undefined
): Promise<OAuthCredentialRotationResult> {
  if (!options.isManagementView.value) {
    if (providerKind === 'anthropic') return await api.myAnthropicOAuth.reauthorizeFromCode(account.id, payload)
    if (providerKind === 'gemini') return await api.myGeminiOAuth.reauthorizeFromCode(account.id, payload)
    if (providerKind === 'grok') return await api.myGrokOAuth.reauthorizeFromCode(account.id, payload)
    return await api.myOpenaiOAuth.reauthorizeFromCode(account.id, payload)
  }
  if (providerKind === 'anthropic') return await api.anthropicOAuth.reauthorizeFromCode(account.id, payload, scopeParams)
  if (providerKind === 'gemini') return await api.geminiOAuth.reauthorizeFromCode(account.id, payload, scopeParams)
  if (providerKind === 'grok') return await api.grokOAuth.reauthorizeFromCode(account.id, payload, scopeParams)
  return await api.openaiOAuth.reauthorizeFromCode(account.id, payload, scopeParams)
}

async function reauthorizeFromRefreshToken(
  providerKind: ManagedOAuthProviderKind,
  account: Pick<AccountListItem, 'id'>,
  payload: OAuthCredentialRotationPayload,
  options: UseAccountReauthorizeOptions,
  scopeParams: AccountScopeParams | undefined
): Promise<OAuthCredentialRotationResult> {
  if (!options.isManagementView.value) {
    if (providerKind === 'anthropic') return await api.myAnthropicOAuth.reauthorizeFromRefreshToken(account.id, payload)
    if (providerKind === 'gemini') return await api.myGeminiOAuth.reauthorizeFromRefreshToken(account.id, payload)
    if (providerKind === 'grok') return await api.myGrokOAuth.reauthorizeFromRefreshToken(account.id, payload)
    return await api.myOpenaiOAuth.reauthorizeFromRefreshToken(account.id, payload)
  }
  if (providerKind === 'anthropic') return await api.anthropicOAuth.reauthorizeFromRefreshToken(account.id, payload, scopeParams)
  if (providerKind === 'gemini') return await api.geminiOAuth.reauthorizeFromRefreshToken(account.id, payload, scopeParams)
  if (providerKind === 'grok') return await api.grokOAuth.reauthorizeFromRefreshToken(account.id, payload, scopeParams)
  return await api.openaiOAuth.reauthorizeFromRefreshToken(account.id, payload, scopeParams)
}

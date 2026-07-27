import { message } from '@/lib/antd'
import { reactive, ref, watch, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import type { AccountSummary, OAuthAuthURLResult } from '@/types/domain'
import type { AccountOAuthAuthorizeForm } from './accountFormTypes'
import { authUrl, buildReauthorizePayload, validateReauthorizeForm } from './accountOAuthPayload'
import { accountOperationScopeParams } from './accountOperationScope'
import { managedOAuthProviderKind, type ManagedOAuthProviderKind } from './accountProviderCapabilities'
import { canManageOAuthAccount } from './accountRules'

interface UseAccountReauthorizeOptions {
  accountScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  extractApiErrorMessage: (error: unknown, fallback: string) => string
  isManagementView: ComputedRef<boolean>
  loadData: () => Promise<void>
}

export function useAccountReauthorize(options: UseAccountReauthorizeOptions) {
  const reauthorizeModalOpen = ref(false)
  const reauthorizeAuthLoading = ref(false)
  const reauthorizeSaving = ref(false)
  const reauthorizeAuthResult = ref<OAuthAuthURLResult>()
  const reauthorizingAccount = ref<AccountSummary>()
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

  function openReauthorizeModal(account: AccountSummary) {
    if (!canManageOAuthAccount(account)) {
      message.warning('只有支持 OAuth 管理的自有账户可以重新授权')
      return
    }
    reauthorizingAccount.value = account
    reauthorizeForm.oauthMode = 'manual'
    reauthorizeForm.callbackUrl = ''
    reauthorizeForm.refreshToken = ''
    reauthorizeForm.accessToken = ''
    reauthorizeForm.googleClientId = credentialText(account.credentials?.client_id)
    reauthorizeForm.googleClientSecret = credentialText(account.credentials?.client_secret)
    reauthorizeForm.googleQuotaProjectId = credentialText(account.credentials?.quota_project_id)
    reauthorizeForm.oauthType = googleOAuthType(account.credentials?.oauth_type)
    reauthorizeForm.tierId = credentialText(account.credentials?.tier_id) || defaultGeminiTierId(reauthorizeForm.oauthType)
    reauthorizeForm.projectId = credentialText(account.credentials?.project_id)
    reauthorizeForm.baseUrl = credentialText(account.credentials?.base_url)
    reauthorizeAuthResult.value = undefined
    reauthorizeModalOpen.value = true
  }

  function closeReauthorizeModal() {
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
      && reauthorizeForm.oauthMode === 'manual'
      && (!reauthorizeForm.googleClientId.trim() || !reauthorizeForm.googleClientSecret.trim())) {
      message.warning('Gemini 官方 OAuth 需要 Client ID 和 Client Secret')
      return
    }
    if (reauthorizeForm.oauthMode === 'access_token') {
      message.warning('重新授权只支持浏览器回调 URL 或 Refresh Token；如仅更换 Access Token，请直接编辑账户保存')
      return
    }

    reauthorizeSaving.value = true
    try {
      const basePayload = buildReauthorizePayload({
        form: reauthorizeForm,
        sessionId: reauthorizeAuthResult.value?.sessionId
      })
      const payload = providerKind === 'gemini'
        ? { ...basePayload, ...geminiOAuthMetadataPayload(reauthorizeForm) }
        : basePayload
      if (reauthorizeForm.oauthMode === 'manual') {
        await reauthorizeFromCode(providerKind, account, payload, options)
      } else {
        await reauthorizeFromRefreshToken(providerKind, account, payload, options)
      }
      message.success(`${account.name}: 重新授权成功`)
      reauthorizeModalOpen.value = false
      reauthorizingAccount.value = undefined
      reauthorizeAuthResult.value = undefined
      await options.loadData()
    } catch (error) {
      console.error(error)
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

function googleOAuthType(value: unknown): AccountOAuthAuthorizeForm['oauthType'] {
  if (value === 'code_assist' || value === 'google_one' || value === 'ai_studio') return value
  return 'code_assist'
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
  account: AccountSummary,
  payload: Record<string, unknown>,
  options: UseAccountReauthorizeOptions
): Promise<void> {
  if (!options.isManagementView.value) {
    if (providerKind === 'anthropic') await api.myAnthropicOAuth.reauthorizeFromCode(account.id, payload)
    else if (providerKind === 'gemini') await api.myGeminiOAuth.reauthorizeFromCode(account.id, payload)
    else if (providerKind === 'grok') await api.myGrokOAuth.reauthorizeFromCode(account.id, payload)
    else await api.myOpenaiOAuth.reauthorizeFromCode(account.id, payload)
    return
  }
  const scopeParams = accountOperationScopeParams(account, options.accountScopeParams.value)
  if (providerKind === 'anthropic') await api.anthropicOAuth.reauthorizeFromCode(account.id, payload, scopeParams)
  else if (providerKind === 'gemini') await api.geminiOAuth.reauthorizeFromCode(account.id, payload, scopeParams)
  else if (providerKind === 'grok') await api.grokOAuth.reauthorizeFromCode(account.id, payload, scopeParams)
  else await api.openaiOAuth.reauthorizeFromCode(account.id, payload, scopeParams)
}

async function reauthorizeFromRefreshToken(
  providerKind: ManagedOAuthProviderKind,
  account: AccountSummary,
  payload: Record<string, unknown>,
  options: UseAccountReauthorizeOptions
): Promise<void> {
  if (!options.isManagementView.value) {
    if (providerKind === 'anthropic') await api.myAnthropicOAuth.reauthorizeFromRefreshToken(account.id, payload)
    else if (providerKind === 'gemini') await api.myGeminiOAuth.reauthorizeFromRefreshToken(account.id, payload)
    else if (providerKind === 'grok') await api.myGrokOAuth.reauthorizeFromRefreshToken(account.id, payload)
    else await api.myOpenaiOAuth.reauthorizeFromRefreshToken(account.id, payload)
    return
  }
  const scopeParams = accountOperationScopeParams(account, options.accountScopeParams.value)
  if (providerKind === 'anthropic') await api.anthropicOAuth.reauthorizeFromRefreshToken(account.id, payload, scopeParams)
  else if (providerKind === 'gemini') await api.geminiOAuth.reauthorizeFromRefreshToken(account.id, payload, scopeParams)
  else if (providerKind === 'grok') await api.grokOAuth.reauthorizeFromRefreshToken(account.id, payload, scopeParams)
  else await api.openaiOAuth.reauthorizeFromRefreshToken(account.id, payload, scopeParams)
}

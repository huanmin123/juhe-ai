import { message } from '@/lib/antd'
import { reactive, ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import { isAnthropicProtocolProfile } from '@/shared/providerProtocol'
import type { AccountSummary, OAuthAuthURLResult } from '@/types/domain'
import type { AccountOAuthAuthorizeForm } from './accountFormTypes'
import { authUrl, buildReauthorizePayload, validateReauthorizeForm } from './accountOAuthPayload'
import { accountOperationScopeParams } from './accountOperationScope'
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
    callbackUrl: '',
    refreshToken: '',
    accessToken: ''
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
      reauthorizeAuthResult.value = isAnthropicProtocolProfile(account)
        ? (options.isManagementView.value ? await api.anthropicOAuth.authUrl({}) : await api.myAnthropicOAuth.authUrl({}))
        : (options.isManagementView.value ? await api.openaiOAuth.authUrl({}) : await api.myOpenaiOAuth.authUrl({}))
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
    if (reauthorizeForm.oauthMode === 'access_token') {
      message.warning('重新授权只支持浏览器回调 URL 或 Refresh Token；如仅更换 Access Token，请直接编辑账户保存')
      return
    }

    reauthorizeSaving.value = true
    try {
      const payload = buildReauthorizePayload({
        form: reauthorizeForm,
        sessionId: reauthorizeAuthResult.value?.sessionId
      })
      const isAnthropicOAuth = isAnthropicProtocolProfile(account)
      if (reauthorizeForm.oauthMode === 'manual') {
        if (options.isManagementView.value) {
          await (isAnthropicOAuth
            ? api.anthropicOAuth.reauthorizeFromCode(account.id, payload, accountOperationScopeParams(account, options.accountScopeParams.value))
            : api.openaiOAuth.reauthorizeFromCode(account.id, payload, accountOperationScopeParams(account, options.accountScopeParams.value)))
        } else {
          await (isAnthropicOAuth
            ? api.myAnthropicOAuth.reauthorizeFromCode(account.id, payload)
            : api.myOpenaiOAuth.reauthorizeFromCode(account.id, payload))
        }
      } else {
        if (options.isManagementView.value) {
          await (isAnthropicOAuth
            ? api.anthropicOAuth.reauthorizeFromRefreshToken(account.id, payload, accountOperationScopeParams(account, options.accountScopeParams.value))
            : api.openaiOAuth.reauthorizeFromRefreshToken(account.id, payload, accountOperationScopeParams(account, options.accountScopeParams.value)))
        } else {
          await (isAnthropicOAuth
            ? api.myAnthropicOAuth.reauthorizeFromRefreshToken(account.id, payload)
            : api.myOpenaiOAuth.reauthorizeFromRefreshToken(account.id, payload))
        }
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

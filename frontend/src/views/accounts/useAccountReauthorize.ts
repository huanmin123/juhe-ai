import { message } from '@/lib/antd'
import { reactive, ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import type { AccountSummary, OpenAIAuthURLResult } from '@/types/domain'
import type { AccountOAuthAuthorizeForm } from './accountFormTypes'
import { authUrl, buildReauthorizePayload, validateReauthorizeForm } from './accountOAuthPayload'
import { canManageOpenAIOAuth } from './accountRules'

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
  const reauthorizeAuthResult = ref<OpenAIAuthURLResult>()
  const reauthorizingAccount = ref<AccountSummary>()
  const reauthorizeForm = reactive<AccountOAuthAuthorizeForm>({
    oauthMode: 'manual',
    callbackUrl: '',
    refreshToken: ''
  })

  function openReauthorizeModal(account: AccountSummary) {
    if (!canManageOpenAIOAuth(account)) {
      message.warning('只有自有 OpenAI OAuth 账户可以重新授权')
      return
    }
    reauthorizingAccount.value = account
    reauthorizeForm.oauthMode = 'manual'
    reauthorizeForm.callbackUrl = ''
    reauthorizeForm.refreshToken = ''
    reauthorizeAuthResult.value = undefined
    reauthorizeModalOpen.value = true
  }

  function closeReauthorizeModal() {
    reauthorizeAuthResult.value = undefined
  }

  async function generateReauthorizeOAuthUrl() {
    reauthorizeAuthLoading.value = true
    try {
      reauthorizeAuthResult.value = options.isManagementView.value
        ? await api.openaiOAuth.authUrl({})
        : await api.myOpenaiOAuth.authUrl({})
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

    reauthorizeSaving.value = true
    try {
      const payload = buildReauthorizePayload({
        form: reauthorizeForm,
        sessionId: reauthorizeAuthResult.value?.sessionId
      })
      if (reauthorizeForm.oauthMode === 'manual') {
        if (options.isManagementView.value) {
          await api.openaiOAuth.reauthorizeFromCode(account.id, payload, options.accountScopeParams.value)
        } else {
          await api.myOpenaiOAuth.reauthorizeFromCode(account.id, payload)
        }
      } else {
        if (options.isManagementView.value) {
          await api.openaiOAuth.reauthorizeFromRefreshToken(account.id, payload, options.accountScopeParams.value)
        } else {
          await api.myOpenaiOAuth.reauthorizeFromRefreshToken(account.id, payload)
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

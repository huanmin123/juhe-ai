import axios from 'axios'
import { message } from '@/lib/antd'
import { computed, onBeforeUnmount, onDeactivated, reactive, ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import type { AccountSummary, AccountTestResult, ProviderModelPricing } from '@/types/domain'
import {
  accountTestErrorMessage,
  accountTestSuccessMessage,
  buildAccountTestPayload,
  failedAccountTestResult,
  nextTestModel,
  stoppedAccountTestMessage,
  type AccountTestForm
} from './accountTestFlow'
import { buildTestModelOptions, preferredTestModelForAccount } from './accountDerivedState'
import { isAuthorizedAccount } from './accountFormatters'
import { accountOperationScopeParams } from './accountOperationScope'
import { authorizedAccountUnavailableText, canTestAccount } from './accountRules'

interface UseAccountTestModalOptions {
  accountScopeParams: ComputedRef<{ systemAccountId: string } | undefined>
  isManagementView: ComputedRef<boolean>
  loadData: () => Promise<void>
}

export function useAccountTestModal(options: UseAccountTestModalOptions) {
  const testModalOpen = ref(false)
  const testRunning = ref(false)
  const testModelsLoading = ref(false)
  const testingAccount = ref<AccountSummary>()
  const testResult = ref<AccountTestResult>()
  const providerModels = ref<ProviderModelPricing[]>([])
  const testForm = reactive<AccountTestForm>({ model: 'gpt-5.5', clientCompatibility: 'account_default' })
  const testModelOptions = computed(() => buildTestModelOptions(providerModels.value, testingAccount.value))
  const defaultTestModel = computed(() => testModelOptions.value[0]?.value || 'gpt-5.5')

  let accountTestAbortController: AbortController | undefined

  async function loadTestModels() {
    if (!options.isManagementView.value || providerModels.value.length || testModelsLoading.value) return
    testModelsLoading.value = true
    try {
      providerModels.value = await api.providers.models('openai')
      testForm.model = nextTestModel(testForm.model, testModelOptions.value, defaultTestModel.value)
    } catch (error) {
      console.error(error)
      message.warning('测试模型列表加载失败，已使用默认模型')
    } finally {
      testModelsLoading.value = false
    }
  }

  async function openTestModal(account: AccountSummary) {
    if (!canTestAccount(account)) {
      if (isAuthorizedAccount(account) && !account.boundGroupId) {
        message.warning('请先把授权账户绑定到你的分组')
      } else if (isAuthorizedAccount(account)) {
        message.warning(authorizedAccountUnavailableText(account) ?? '当前授权账户不能测试')
      } else {
        message.warning('当前账户不能测试')
      }
      return
    }
    testingAccount.value = account
    testResult.value = undefined
    testForm.model = preferredTestModelForAccount(account, testForm.model, defaultTestModel.value)
    testForm.clientCompatibility = 'account_default'
    testModalOpen.value = true
    void loadTestModels()
  }

  async function runAccountTest() {
    if (!testingAccount.value || testRunning.value) return
    testResult.value = undefined
    testRunning.value = true
    const controller = new AbortController()
    accountTestAbortController = controller
    const startedAt = Date.now()
    const account = testingAccount.value
    try {
      const payload = buildAccountSpecificTestPayload(account)
      const result = options.isManagementView.value
        ? await api.accounts.test(account.id, payload, accountOperationScopeParams(account, options.accountScopeParams.value), { signal: controller.signal })
        : await api.myAccounts.test(account.id, payload, { signal: controller.signal })
      testResult.value = result
      if (result.success) {
        message.success(accountTestSuccessMessage(account, result))
      } else {
        message.error(accountTestErrorMessage(account, result))
      }
      await options.loadData()
    } catch (error) {
      if (axios.isCancel(error) || (error instanceof DOMException && error.name === 'AbortError')) {
        message.info(stoppedAccountTestMessage(account))
        return
      }
      console.error(error)
      testResult.value = failedAccountTestResult({
        account,
        error,
        model: testForm.model,
        clientCompatibility: testForm.clientCompatibility,
        startedAt
      })
      message.error(`${account.name}: 测试失败`)
    } finally {
      testRunning.value = false
      if (accountTestAbortController === controller) {
        accountTestAbortController = undefined
      }
    }
  }

  function stopAccountTest() {
    if (!testRunning.value) return
    accountTestAbortController?.abort()
  }

  function closeTestModal() {
    if (testRunning.value) {
      stopAccountTest()
    }
    testModalOpen.value = false
  }

  async function testAccountSilently(account: AccountSummary) {
    if (!canTestAccount(account)) return undefined
    try {
      const payload = buildAccountSpecificTestPayload(account, 'account_default')
      return options.isManagementView.value
        ? await api.accounts.test(account.id, payload, accountOperationScopeParams(account, options.accountScopeParams.value))
        : await api.myAccounts.test(account.id, payload)
    } catch (error) {
      console.error(error)
      return undefined
    }
  }

  onDeactivated(stopAccountTest)
  onBeforeUnmount(stopAccountTest)

  function buildAccountSpecificTestPayload(account: AccountSummary, clientCompatibility = testForm.clientCompatibility) {
    return buildAccountTestPayload({
      ...testForm,
      clientCompatibility,
      model: preferredTestModelForAccount(account, testForm.model, defaultTestModel.value)
    })
  }

  return {
    closeTestModal,
    openTestModal,
    runAccountTest,
    stopAccountTest,
    testAccountSilently,
    testForm,
    testModalOpen,
    testModelOptions,
    testModelsLoading,
    testResult,
    testRunning,
    testingAccount
  }
}

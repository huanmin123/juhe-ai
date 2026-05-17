<template>
  <a-card class="page-card accounts-page-card responsive-page-card">
    <AccountFilterToolbar
      :active-filter-count="activeAdvancedFilterCount"
      :filters="filters"
      :is-management-view="isManagementView"
      :refresh-loading="loading"
      :schedulable-options="schedulableOptions"
      :status-options="statusOptions"
      :system-accounts="systemAccounts"
      :type-options="typeOptions"
      @create="openCreate"
      @refresh="refreshData"
      @reset="resetFilters"
      @search="applyFilters"
      @system-account-change="handleSystemAccountFilterChange"
      @update:keyword="filters.keyword = $event"
      @update:schedulable="filters.schedulable = $event"
      @update:status="filters.status = $event"
      @update:system-account-id="filters.systemAccountId = $event"
      @update:type="filters.type = $event"
    />

    <AccountBatchToolbar
      :selected-count="selectedAccounts.length"
      @clear="clearSelection"
      @disable="batchSetStatus('disabled')"
      @enable="batchSetStatus('active')"
      @test="batchTestSelected"
    />

    <AccountList
      :accounts="filteredAccounts"
      :authorized-tooltip="authorizedAccountTooltip"
      :can-delete="canDeleteAccount"
      :can-edit="canEditAccount"
      :columns="columns"
      :group-name="groupNameForAccount"
      :is-management-view="isManagementView"
      :is-selected="isAccountSelected"
      :loading="loading"
      :loading-more="mobileLoadingMore"
      :menu-items="accountMenuItems"
      :mobile-accounts="mobileVisibleAccounts"
      :mobile-has-more="mobileHasMoreAccounts"
      :pagination="accountTablePagination"
      :provider-name="providerName"
      :proxy="proxyById"
      :refreshing="mobileRefreshing"
      :row-selection="rowSelection"
      :table-scroll-x="tableScrollX"
      :table-scroll-y="tableScrollY"
      @bind-group="openBindGroup"
      @change="handleAccountTableChangeAndLoad"
      @delete="removeAccount"
      @edit="openEdit"
      @menu-click="handleAccountMenuClick"
      @mobile-load-more="loadMoreMobileAccounts"
      @mobile-refresh="refreshMobileAccounts"
      @sort-change="handleAccountSortChange"
      @test="openTestModal"
      @toggle-selection="toggleAccountSelection"
    />

    <AccountTestModal
      v-model:open="testModalOpen"
      v-model:model="testForm.model"
      :account="testingAccount"
      :model-options="testModelOptions"
      :models-loading="testModelsLoading"
      :prompt="testForm.prompt"
      :result="testResult"
      :running="testRunning"
      @close="closeTestModal"
      @copy-result="copyText"
      @run="runAccountTest"
      @stop="stopAccountTest"
    />

    <AccountEditModal
      v-model:open="modalOpen"
      v-model:error-policy-rules="accountErrorPolicyRules"
      :account-type-choices="accountTypeChoices"
      :auth-loading="authLoading"
      :auth-result="authResult"
      :base-url-placeholder="selectedProvider?.baseUrl || 'https://api.openai.com/v1'"
      :confirm-loading="modalConfirmLoading"
      :credential-title="selectedAccountTypeTitle"
      :editing="Boolean(editingId)"
      :form="form"
      :group-options="groupOptions"
      :has-account-type="hasAccountType"
      :is-api-key-form="isApiKeyForm"
      :is-management-view="isManagementView"
      :is-o-auth-form="isOAuthForm"
      :is-open-a-i-o-auth-form="isOpenAIOAuthForm"
      :ok-button-props="modalOkButtonProps"
      :providers="availableProviders"
      :proxy-options="proxyOptions"
      :selected-provider="selectedProvider"
      :status-options="statusEditOptions"
      :title="modalTitle"
      :target-system-account-label="targetSystemAccountLabel"
      @cancel="handleModalCancel"
      @copy-auth-url="copyText"
      @generate-auth-url="generateOAuthUrl"
      @ok="saveAccount"
      @open-auth-url="openAuthUrl"
      @select-provider="selectProvider"
      @select-type="selectAccountType"
    />

    <AccountBindGroupModal
      v-model:open="bindGroupModalOpen"
      v-model:group-id="bindGroupForm.groupId"
      :account="bindingAccount"
      :group-options="bindGroupOptions"
      :saving="bindGroupSaving"
      :tip="bindGroupTip"
      @save="saveBindGroup"
    />

    <AccountTrafficMigrationModal
      v-model:open="trafficMigrationModalOpen"
      v-model:source-status="trafficMigrationForm.sourceStatus"
      v-model:target-account-id="trafficMigrationForm.targetAccountId"
      :saving="trafficMigrationSaving"
      :source-account="trafficMigrationSourceAccount"
      :target-options="trafficMigrationTargetOptions"
      @save="saveTrafficMigration"
    />

    <AccountReauthorizeModal
      v-model:open="reauthorizeModalOpen"
      :account="reauthorizingAccount"
      :auth-loading="reauthorizeAuthLoading"
      :auth-result="reauthorizeAuthResult"
      :form="reauthorizeForm"
      :saving="reauthorizeSaving"
      @cancel="closeReauthorizeModal"
      @copy-auth-url="copyText"
      @generate-auth-url="generateReauthorizeOAuthUrl"
      @open-auth-url="openReauthorizeAuthUrl"
      @save="saveReauthorize"
    />
  </a-card>
</template>

<script setup lang="ts">
import axios from 'axios'
import { message } from '@/lib/antd'
import { computed, onBeforeUnmount, onDeactivated, onMounted, reactive, ref } from 'vue'

import { api } from '@/api/client'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { useSubmitAction } from '@/composables/useSubmitAction'
import type { AccountStatus, AccountSummary, AccountTestResult, AccountType, OpenAIAuthURLResult, ProviderModelPricing } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import AccountBatchToolbar from './AccountBatchToolbar.vue'
import AccountBindGroupModal from './AccountBindGroupModal.vue'
import AccountEditModal from './AccountEditModal.vue'
import AccountFilterToolbar from './AccountFilterToolbar.vue'
import AccountList from './AccountList.vue'
import AccountReauthorizeModal from './AccountReauthorizeModal.vue'
import AccountTestModal from './AccountTestModal.vue'
import AccountTrafficMigrationModal from './AccountTrafficMigrationModal.vue'
import {
  loadAccountErrorPolicyRules,
  type AccountErrorPolicyRuleForm
} from './accountErrorPolicy'
import {
  accountByIdMap,
  buildProxyOptions,
  buildTestModelOptions,
  defaultGroupForProvider as selectDefaultGroupForProvider,
  groupIdByAccountIdMap,
  groupNameByAccountIdMap,
  groupOptionsForProvider,
  isManageableGroupForProvider,
  providerNameByCodeMap,
  proxyByIdMap,
  targetSystemAccountLabel as buildTargetSystemAccountLabel
} from './accountDerivedState'
import { defaultAccountForm } from './accountFormDefaults'
import {
  accountTypeDescription,
  accountTypeText,
  accountTypeTitle as buildAccountTypeTitle,
  asString,
  isTemporaryAccountStatus,
  parseDatePickerValue,
} from './accountFormatters'
import type { AccountFormModel } from './accountFormTypes'
import {
  FALLBACK_PROVIDER,
  schedulableOptions,
  statusOptions,
  typeOptions
} from './accountOptions'
import { authUrl, buildOAuthCreatePayload } from './accountOAuthPayload'
import {
  accountSelectionColumnWidth,
  accountTableScrollX,
  accountTableScrollY,
  buildAccountTableColumns,
  accountColumnSortOrder as resolveAccountColumnSortOrder,
} from './accountTableColumns'
import {
  accountMenuItems,
  authorizedAccountTooltip,
  canBatchManageAccount,
  canDeleteAccount,
  canEditAccount,
  canTestAccount
} from './accountRules'
import { buildAccountSavePayload, buildOAuthCreateCommonPayload, validateAccountSaveForm } from './accountSavePayload'
import {
  accountTestErrorMessage,
  accountTestSuccessMessage,
  buildAccountTestPayload,
  failedAccountTestResult,
  nextTestModel,
  stoppedAccountTestMessage
} from './accountTestFlow'
import { useAccountBindGroup } from './useAccountBindGroup'
import { useAccountBatchActions } from './useAccountBatchActions'
import { useAccountListData } from './useAccountListData'
import { useAccountMenuActions } from './useAccountMenuActions'
import { useAccountReauthorize } from './useAccountReauthorize'
import { useAccountTrafficMigration } from './useAccountTrafficMigration'

const { submitAction, submittingRef } = useSubmitAction('accounts')
const saving = submittingRef('accounts.save')
const authLoading = ref(false)
const testModalOpen = ref(false)
const testRunning = ref(false)
const testModelsLoading = ref(false)
const modalOpen = ref(false)
const authResult = ref<OpenAIAuthURLResult>()
const editingId = ref<string>()
const testingAccount = ref<AccountSummary>()
const testResult = ref<AccountTestResult>()
const selectedAccountIds = ref<string[]>([])
let accountTestAbortController: AbortController | undefined
const providerModels = ref<ProviderModelPricing[]>([])
const testForm = reactive({ model: 'gpt-5.5', prompt: 'hi' })
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const {
  loading,
  accounts,
  providers,
  proxies,
  groups,
  systemAccounts,
  filters,
  accountSorts,
  accountScopeParams,
  filteredAccounts,
  activeAdvancedFilterCount,
  mobileHasMoreAccounts,
  mobileLoadingMore,
  mobileRefreshing,
  mobileVisibleAccounts,
  accountTablePagination,
  loadMoreMobileAccounts,
  refreshMobileAccounts,
  loadData: loadAccountListData,
  refreshData,
  applyFilters,
  handleAccountTableChangeAndLoad,
  handleAccountSortChange,
  handleSystemAccountFilterChange: handleAccountListSystemAccountFilterChange,
  resetFilters: resetAccountListFilters
} = useAccountListData({
  isManagementView,
  scopedSystemAccountId,
  onLoaded: handleAccountListLoaded
})

function handleAccountListLoaded(selectableAccountIds: Set<string>) {
  selectedAccountIds.value = selectedAccountIds.value.filter((id) => selectableAccountIds.has(id))
  if (modalOpen.value && !editingId.value) {
    ensureDefaultGroupSelected()
  }
}

function loadData(options?: { append?: boolean; quiet?: boolean; forceOptions?: boolean }) {
  return loadAccountListData(options)
}

const form = reactive<AccountFormModel>(defaultForm())
const accountErrorPolicyRules = ref<AccountErrorPolicyRuleForm[]>(loadAccountErrorPolicyRules())

const currentEditingAccount = computed(() => editingId.value ? accounts.value.find((account) => account.id === editingId.value) : undefined)

const statusEditOptions = computed(() => {
  const options = statusOptions.filter((item) => item.value !== 'all')
  if (currentEditingAccount.value?.status === 'error') {
    return options.filter((item) => item.value === 'error')
  }
  if (currentEditingAccount.value && isTemporaryAccountStatus(currentEditingAccount.value)) {
    return options.filter((item) => item.value !== 'active')
  }
  return options
})

const columns = computed(() => buildAccountTableColumns(isManagementView.value, (field) => resolveAccountColumnSortOrder(accountSorts.value, field)))
const tableScrollX = computed(() => accountTableScrollX(isManagementView.value))
const tableScrollY = computed(accountTableScrollY)

const targetSystemAccountLabel = computed(() => {
  if (!isManagementView.value) return undefined
  const systemAccountId = accountScopeParams.value?.systemAccountId
  return buildTargetSystemAccountLabel(systemAccounts.value, systemAccountId)
})

const testModelOptions = computed(() => buildTestModelOptions(providerModels.value))
const defaultTestModel = computed(() => testModelOptions.value[0]?.value || 'gpt-5.5')

const accountById = computed(() => accountByIdMap(accounts.value))
const selectedAccountIdSet = computed(() => new Set(selectedAccountIds.value))
const selectedAccounts = computed(() => accounts.value.filter((account) => selectedAccountIdSet.value.has(account.id)))
const {
  bindGroupForm,
  bindGroupModalOpen,
  bindGroupOptions,
  bindGroupSaving,
  bindGroupTip,
  bindingAccount,
  openBindGroup,
  saveBindGroup
} = useAccountBindGroup({
  accountScopeParams,
  extractApiErrorMessage,
  groupIdForAccount,
  groups,
  isManagementView,
  loadData
})
const {
  openTrafficMigration,
  saveTrafficMigration,
  trafficMigrationForm,
  trafficMigrationModalOpen,
  trafficMigrationSaving,
  trafficMigrationSourceAccount,
  trafficMigrationTargetOptions
} = useAccountTrafficMigration({
  accountScopeParams,
  accounts,
  extractApiErrorMessage,
  groupIdForAccount,
  groupNameForAccount,
  isManagementView,
  loadData
})
const {
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
} = useAccountReauthorize({
  accountScopeParams,
  extractApiErrorMessage,
  isManagementView,
  loadData
})
const {
  batchSetStatus,
  batchTestSelected
} = useAccountBatchActions({
  accountScopeParams,
  clearSelection,
  isManagementView,
  loadData,
  selectedAccounts,
  testAccountSilently
})
const {
  handleAccountMenuClick
} = useAccountMenuActions({
  accountScopeParams,
  extractApiErrorMessage,
  isManagementView,
  loadData,
  openReauthorizeModal,
  openTestModal,
  openTrafficMigration
})
const providerNameByCode = computed(() => providerNameByCodeMap(availableProviders.value))
const proxyByIdMapRef = computed(() => proxyByIdMap(proxies.value))
const groupIdByAccountId = computed(() => groupIdByAccountIdMap(groups.value))
const groupNameByAccountId = computed(() => groupNameByAccountIdMap(accounts.value, groups.value))

const rowSelection = computed(() => ({
  columnWidth: accountSelectionColumnWidth,
  selectedRowKeys: selectedAccountIds.value,
  onChange: (selectedRowKeys: Array<string | number>) => {
    selectedAccountIds.value = selectedRowKeys.map((key) => String(key))
  },
  getCheckboxProps: (account: AccountSummary) => ({ disabled: !canBatchManageAccount(account) })
}))

function isAccountSelected(accountId: string): boolean {
  return selectedAccountIdSet.value.has(accountId)
}

function toggleAccountSelection(account: AccountSummary) {
  if (!canBatchManageAccount(account)) return
  selectedAccountIds.value = isAccountSelected(account.id)
    ? selectedAccountIds.value.filter((id) => id !== account.id)
    : [...selectedAccountIds.value, account.id]
}

const proxyOptions = computed(() => buildProxyOptions(proxies.value))
const proxyById = (proxyProfileId?: string) => proxyProfileId ? proxyByIdMapRef.value.get(proxyProfileId) : undefined
const groupOptions = computed(() => groupOptionsForProvider(groups.value, form.providerCode))
const availableProviders = computed(() => providers.value.length ? providers.value : [FALLBACK_PROVIDER])
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
  return defaultAccountForm(providerCode, type, providers.value)
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

function groupIdForAccount(accountId: string) {
  return accountById.value.get(accountId)?.boundGroupId ?? groupIdByAccountId.value.get(accountId)
}

function groupNameForAccount(accountId: string) {
  return groupNameByAccountId.value.get(accountId)
}

function defaultGroupForProvider(providerCode: string) {
  return selectDefaultGroupForProvider(groups.value, providerCode)
}

function ensureDefaultGroupSelected(providerCode = form.providerCode) {
  if (!providerCode) {
    form.groupId = undefined
    return
  }
  const currentGroup = groups.value.find((group) => group.id === form.groupId)
  if (currentGroup && isManageableGroupForProvider(currentGroup, providerCode)) {
    return
  }
  form.groupId = defaultGroupForProvider(providerCode)?.id
}

async function copyText(value: string) {
  if (!value) return
  await navigator.clipboard.writeText(value)
  message.success('已复制')
}

function resetFilters() {
  selectedAccountIds.value = []
  resetAccountListFilters()
}

function extractApiErrorMessage(error: unknown, fallback: string): string {
  if (axios.isAxiosError<{ message?: string }>(error)) {
    return error.response?.data?.message ?? fallback
  }
  return error instanceof Error ? error.message : fallback
}

function handleSystemAccountFilterChange() {
  selectedAccountIds.value = []
  handleAccountListSystemAccountFilterChange()
}

function clearSelection() {
  selectedAccountIds.value = []
}

function openCreate() {
  if (isManagementView.value && !accountScopeParams.value?.systemAccountId) {
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
    status: account.status,
    concurrencyLimit: account.concurrencyLimit,
    priority: account.priority,
    proxyProfileId: account.proxyProfileId,
    accountExpiresAt: parseDatePickerValue(account.accountExpiresAt),
    groupId: groupIdForAccount(account.id),
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
    accounts: accounts.value,
    editingId: editingId.value,
    form,
    errorPolicyRules: accountErrorPolicyRules.value
  })

  try {
    if (editingId.value) {
      if (isManagementView.value) {
        await api.accounts.update(editingId.value, payload, accountScopeParams.value)
      } else {
        await api.myAccounts.update(editingId.value, payload)
      }
      message.success('账户已更新')
    } else if (form.type === 'oauth') {
      await createOAuthAccountFromUnifiedForm()
      message.success('OAuth 账户已创建')
    } else {
      if (isManagementView.value) {
        await api.accounts.create(payload, accountScopeParams.value)
      } else {
        await api.myAccounts.create(payload)
      }
      message.success('账户已创建')
    }
    modalOpen.value = false
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存账户失败'))
  } finally {
  }
})

async function generateOAuthUrl() {
  authLoading.value = true
  try {
    authResult.value = isManagementView.value ? await api.openaiOAuth.authUrl({}) : await api.myOpenaiOAuth.authUrl({})
    message.success('授权链接已生成')
  } catch (error) {
    console.error(error)
    message.error('生成授权链接失败')
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
    accounts: accounts.value,
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
    if (isManagementView.value) {
      await api.openaiOAuth.createFromCode(payload, accountScopeParams.value)
    } else {
      await api.myOpenaiOAuth.createFromCode(payload)
    }
    return
  }

  if (isManagementView.value) {
    await api.openaiOAuth.createFromRefreshToken(payload, accountScopeParams.value)
  } else {
    await api.myOpenaiOAuth.createFromRefreshToken(payload)
  }
}

async function loadTestModels() {
  if (!isManagementView.value || providerModels.value.length || testModelsLoading.value) return
  testModelsLoading.value = true
  try {
    providerModels.value = await api.providers.models('openai')
    testForm.model = nextTestModel(testForm.model, providerModels.value, defaultTestModel.value)
  } catch (error) {
    console.error(error)
    message.warning('测试模型列表加载失败，已使用默认模型')
  } finally {
    testModelsLoading.value = false
  }
}

async function openTestModal(account: AccountSummary) {
  if (!canTestAccount(account)) {
    message.warning(account.status === 'disabled' ? '停用账户不能测试，请先手动启用账户' : '当前账户不能测试')
    return
  }
  testingAccount.value = account
  testResult.value = undefined
  testForm.model = testForm.model || defaultTestModel.value
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
    const payload = buildAccountTestPayload(testForm)
    const result = isManagementView.value
      ? await api.accounts.test(account.id, payload, accountScopeParams.value, { signal: controller.signal })
      : await api.myAccounts.test(account.id, payload, { signal: controller.signal })
    testResult.value = result
    if (result.success) {
      message.success(accountTestSuccessMessage(account, result))
    } else {
      message.error(accountTestErrorMessage(account, result))
    }
    await loadData()
  } catch (error) {
    if (axios.isCancel(error) || (error instanceof DOMException && error.name === 'AbortError')) {
      message.info(stoppedAccountTestMessage(account))
      return
    }
    console.error(error)
    testResult.value = failedAccountTestResult({ account, error, model: testForm.model, startedAt })
    message.error(`${account.name}: 测试失败`)
  } finally {
    testRunning.value = false
    if (accountTestAbortController === controller) {
      accountTestAbortController = undefined
    }
  }
}

function closeTestModal() {
  if (testRunning.value) {
    stopAccountTest()
  }
  testModalOpen.value = false
}

function stopAccountTest() {
  if (!testRunning.value) return
  accountTestAbortController?.abort()
}

async function testAccountSilently(account: AccountSummary) {
  if (!canTestAccount(account)) return undefined
  try {
    const payload = buildAccountTestPayload(testForm)
    return isManagementView.value
      ? await api.accounts.test(account.id, payload, accountScopeParams.value)
      : await api.myAccounts.test(account.id, payload)
  } catch (error) {
    console.error(error)
    return undefined
  }
}

async function removeAccount(id: string) {
  try {
    if (isManagementView.value) {
      await api.accounts.delete(id, accountScopeParams.value)
    } else {
      await api.myAccounts.delete(id)
    }
    message.success('账户已删除')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('删除账户失败')
  }
}

onMounted(loadData)

onDeactivated(stopAccountTest)

onBeforeUnmount(stopAccountTest)
</script>

<style scoped>
.accounts-page-card {
  border: 1px solid #e8edf5;
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.04);
}

.credential-cell {
  display: inline-block;
  max-width: 240px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: bottom;
}

.secret-cell {
  width: 100%;
}

.secret-input {
  width: calc(100% - 64px);
  font-family: Consolas, 'Courier New', monospace;
}

.form-help {
  margin-top: 4px;
  color: #64748b;
  font-size: 12px;
}

.notes-cell {
  display: inline-block;
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: bottom;
}

.account-form {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.form-section {
  padding: 16px;
  border: 1px solid #e8edf5;
  border-radius: 16px;
  background: #fff;
}

.form-section-head {
  margin-bottom: 12px;
}

.form-section-head h4 {
  margin: 0;
  color: #0f172a;
  font-size: 16px;
}

.form-section-head p {
  margin: 4px 0 0;
  color: #64748b;
  font-size: 12px;
}

.form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 16px;
}

.form-alert {
  border-radius: 12px;
}

@media (max-width: 992px) {
  .form-grid {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 900px) {
  .form-grid {
    grid-template-columns: 1fr;
  }
}
</style>

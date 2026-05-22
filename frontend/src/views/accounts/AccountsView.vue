<template>
  <a-card class="page-card accounts-page-card responsive-page-card">
    <AccountFilterToolbar
      :active-filter-count="activeAdvancedFilterCount"
      :filters="filters"
      :group-filter-disabled="isManagementView && !accountScopeParams?.systemAccountId"
      :group-options="filterGroupOptions"
      :group-options-loading="filterGroupOptionsLoading"
      :is-management-view="isManagementView"
      :refresh-loading="loading"
      :status-options="statusOptions"
      :system-accounts="systemAccounts"
      :system-accounts-loading="systemAccountOptionsLoading"
      :type-options="typeOptions"
      @create="openCreate"
      @group-dropdown="handleFilterGroupOptionsDropdown"
      @group-search="handleFilterGroupOptionsSearch"
      @import="openImportModal"
      @refresh="refreshData"
      @reset="resetFilters"
      @search="applyFilters"
      @system-account-change="handleSystemAccountFilterChange"
      @system-account-dropdown="handleSystemAccountOptionsDropdown"
      @system-account-search="handleSystemAccountOptionsSearch"
      @update:group-id="filters.groupId = $event"
      @update:keyword="filters.keyword = $event"
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

    <AccountImportModal
      v-if="importModalOpen"
      v-model:open="importModalOpen"
      :scope-params="accountScopeParams"
      :target-system-account-label="targetSystemAccountLabel"
      @imported="handleImportCompleted"
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
      @bind-group="handleOpenBindGroup"
      @change="handleAccountTableChange"
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
      v-if="testModalOpen"
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
      v-if="modalOpen"
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
      :group-options-loading="groupOptionsLoading"
      :has-account-type="hasAccountType"
      :is-api-key-form="isApiKeyForm"
      :is-management-view="isManagementView"
      :is-o-auth-form="isOAuthForm"
      :is-open-a-i-o-auth-form="isOpenAIOAuthForm"
      :model-options="providerModelOptions"
      :models-loading="providerModelsLoading"
      :ok-button-props="modalOkButtonProps"
      :providers="availableProviders"
      :proxy-options="proxyOptions"
      :selected-provider="selectedProvider"
      :title="modalTitle"
      :target-system-account-label="targetSystemAccountLabel"
      @cancel="handleModalCancel"
      @copy-auth-url="copyText"
      @generate-auth-url="generateOAuthUrl"
      @group-options-dropdown="handleGroupOptionsDropdown"
      @group-options-search="handleGroupOptionsSearch"
      @ok="saveAccount"
      @open-auth-url="openAuthUrl"
      @select-provider="selectProvider"
      @select-type="selectAccountType"
    />

    <AccountBindGroupModal
      v-if="bindGroupModalOpen"
      v-model:open="bindGroupModalOpen"
      v-model:group-id="bindGroupForm.groupId"
      v-model:dispatch-weight="bindGroupForm.dispatchWeight"
      v-model:soft-concurrency-limit="bindGroupForm.softConcurrencyLimit"
      :account="bindingAccount"
      :group-options="bindGroupOptions"
      :group-options-loading="groupOptionsLoading"
      :saving="bindGroupSaving"
      :soft-concurrency-visible="bindGroupSoftConcurrencyVisible"
      :tip="bindGroupTip"
      @group-options-dropdown="handleGroupOptionsDropdown"
      @group-options-search="handleGroupOptionsSearch"
      @save="saveBindGroup"
    />

    <AccountTrafficMigrationModal
      v-if="trafficMigrationModalOpen"
      v-model:open="trafficMigrationModalOpen"
      v-model:source-status="trafficMigrationForm.sourceStatus"
      v-model:target-account-id="trafficMigrationForm.targetAccountId"
      :saving="trafficMigrationSaving"
      :source-account="trafficMigrationSourceAccount"
      :target-options="trafficMigrationTargetOptions"
      @save="saveTrafficMigration"
    />

    <AccountReauthorizeModal
      v-if="reauthorizeModalOpen"
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
import { message } from '@/lib/antd'
import { computed, defineAsyncComponent, onMounted, ref, watch } from 'vue'

import { api } from '@/api/client'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import type { AccountSummary } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import AccountBatchToolbar from './AccountBatchToolbar.vue'
import AccountFilterToolbar from './AccountFilterToolbar.vue'
import AccountList from './AccountList.vue'
import {
  accountByIdMap,
  buildProxyOptions,
  proxyByIdMap
} from './accountDerivedState'
import {
  statusOptions,
  typeOptions
} from './accountOptions'
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
  canEditAccount
} from './accountRules'
import { useAccountBindGroup } from './useAccountBindGroup'
import { useAccountBatchActions } from './useAccountBatchActions'
import { useAccountEditForm } from './useAccountEditForm'
import { useAccountGroupOptions } from './useAccountGroupOptions'
import { useAccountListData } from './useAccountListData'
import { useAccountMenuActions } from './useAccountMenuActions'
import { useAccountReauthorize } from './useAccountReauthorize'
import { useAccountTestModal } from './useAccountTestModal'
import { useAccountTrafficMigration } from './useAccountTrafficMigration'

const AccountBindGroupModal = defineAsyncComponent(() => import('./AccountBindGroupModal.vue'))
const AccountEditModal = defineAsyncComponent(() => import('./AccountEditModal.vue'))
const AccountImportModal = defineAsyncComponent(() => import('./AccountImportModal.vue'))
const AccountReauthorizeModal = defineAsyncComponent(() => import('./AccountReauthorizeModal.vue'))
const AccountTestModal = defineAsyncComponent(() => import('./AccountTestModal.vue'))
const AccountTrafficMigrationModal = defineAsyncComponent(() => import('./AccountTrafficMigrationModal.vue'))

const selectedAccountIds = ref<string[]>([])
const importModalOpen = ref(false)
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const {
  loading,
  accounts,
  providers,
  proxies,
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
  systemAccountOptionsLoading,
  handleSystemAccountOptionsDropdown,
  handleSystemAccountOptionsSearch,
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
const {
  groups: filterGroupOptions,
  handleDropdown: handleFilterGroupOptionsDropdown,
  handleSearch: handleFilterGroupOptionsSearch,
  loading: filterGroupOptionsLoading,
  resetSearch: resetFilterGroupOptionsSearch
} = useAccountGroupOptions({
  allowAllProviders: true,
  errorMessage: '加载筛选分组选项失败',
  isManagementView: () => isManagementView.value,
  limit: 100,
  scope: () => ({
    systemAccountId: isManagementView.value ? accountScopeParams.value?.systemAccountId : undefined,
    selectedIds: [filters.groupId]
  })
})

function handleAccountListLoaded(selectableAccountIds: Set<string>) {
  selectedAccountIds.value = selectedAccountIds.value.filter((id) => selectableAccountIds.has(id))
  if (modalOpen.value && !editingId.value) {
    ensureDefaultGroupSelected()
  }
}

async function loadData(options?: { append?: boolean; quiet?: boolean; forceOptions?: boolean }) {
  await loadAccountListData(options)
}

const columns = computed(() => buildAccountTableColumns(isManagementView.value, (field) => resolveAccountColumnSortOrder(accountSorts.value, field)))
const tableScrollX = computed(() => accountTableScrollX(isManagementView.value))
const tableScrollY = computed(accountTableScrollY)

function handleAccountTableChange(...args: unknown[]): void {
  if (tableChangeAction(args[3]) === 'sort') return
  void handleAccountTableChangeAndLoad(args[0])
}

function tableChangeAction(value: unknown): string | undefined {
  return value && typeof value === 'object' && typeof (value as { action?: unknown }).action === 'string'
    ? (value as { action: string }).action
    : undefined
}

const accountById = computed(() => accountByIdMap(accounts.value))
const selectedAccountIdSet = computed(() => new Set(selectedAccountIds.value))
const selectedAccounts = computed(() => accounts.value.filter((account) => selectedAccountIdSet.value.has(account.id)))
const groupOptionProviderCode = ref('')
const selectedGroupIds = ref<Array<string | undefined>>([])
const {
  groups,
  handleDropdown: handleGroupOptionsDropdown,
  handleSearch: handleGroupOptionsSearch,
  load: loadGroupOptions,
  loading: groupOptionsLoading,
  resetSearch: resetGroupOptionsSearch
} = useAccountGroupOptions({
  isManagementView: () => isManagementView.value,
  scope: () => ({
    providerCode: groupOptionProviderCode.value,
    systemAccountId: isManagementView.value ? accountScopeParams.value?.systemAccountId : undefined,
    selectedIds: selectedGroupIds.value
  })
})
const {
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
  providerModelOptions,
  providerModelsLoading,
  saveAccount,
  selectAccountType,
  selectedAccountTypeTitle,
  selectedProvider,
  selectProvider,
  targetSystemAccountLabel
} = useAccountEditForm({
  accountScopeParams,
  accounts,
  extractApiErrorMessage,
  groupIdForAccount,
  groups,
  isManagementView,
  loadGroupOptions,
  loadData,
  providers,
  systemAccounts
})
const {
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
} = useAccountTestModal({
  accountScopeParams,
  isManagementView,
  loadData
})
const {
  bindGroupForm,
  bindGroupModalOpen,
  bindGroupOptions,
  bindGroupSaving,
  bindGroupSoftConcurrencyVisible,
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
  loadGroupOptions,
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
watch(
  [() => form.providerCode, () => form.groupId, () => bindGroupModalOpen.value, () => bindingAccount.value?.providerCode, () => bindingAccount.value?.id, () => bindGroupForm.groupId],
  () => {
    groupOptionProviderCode.value = bindGroupModalOpen.value ? bindingAccount.value?.providerCode ?? '' : form.providerCode
    selectedGroupIds.value = [form.groupId, bindGroupForm.groupId]
  },
  { immediate: true }
)
watch(groupOptionProviderCode, () => {
  resetGroupOptionsSearch()
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
const proxyByIdMapRef = computed(() => proxyByIdMap(proxies.value))

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

function groupIdForAccount(accountId: string) {
  return accountById.value.get(accountId)?.boundGroupId
}

function groupNameForAccount(accountId: string) {
  return accountById.value.get(accountId)?.boundGroupName
}

function handleOpenBindGroup(account: AccountSummary): void {
  void openBindGroup(account)
}

async function copyText(value: string) {
  await copyTextToClipboard(value)
}

function resetFilters() {
  selectedAccountIds.value = []
  resetGroupOptionsSearch()
  resetFilterGroupOptionsSearch()
  resetAccountListFilters()
}

function handleSystemAccountFilterChange() {
  selectedAccountIds.value = []
  filters.groupId = ''
  resetGroupOptionsSearch()
  resetFilterGroupOptionsSearch()
  handleAccountListSystemAccountFilterChange()
}

function clearSelection() {
  selectedAccountIds.value = []
}

function openImportModal() {
  if (isManagementView.value && !accountScopeParams.value?.systemAccountId) {
    message.warning('请先在右侧选择目标系统账户，再导入 AI 账户')
    return
  }
  importModalOpen.value = true
}

async function handleImportCompleted() {
  selectedAccountIds.value = []
  await loadData({ forceOptions: true })
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
    message.error(extractApiErrorMessage(error, '删除账户失败'))
  }
}

onMounted(loadData)
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

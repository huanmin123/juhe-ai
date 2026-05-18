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
import { message } from '@/lib/antd'
import { computed, onMounted, ref } from 'vue'

import { api } from '@/api/client'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { AccountSummary } from '@/types/domain'
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
  accountByIdMap,
  buildProxyOptions,
  groupIdByAccountIdMap,
  groupNameByAccountIdMap,
  proxyByIdMap
} from './accountDerivedState'
import {
  schedulableOptions,
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
import { useAccountListData } from './useAccountListData'
import { useAccountMenuActions } from './useAccountMenuActions'
import { useAccountReauthorize } from './useAccountReauthorize'
import { useAccountTestModal } from './useAccountTestModal'
import { useAccountTrafficMigration } from './useAccountTrafficMigration'

const selectedAccountIds = ref<string[]>([])
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

const columns = computed(() => buildAccountTableColumns(isManagementView.value, (field) => resolveAccountColumnSortOrder(accountSorts.value, field)))
const tableScrollX = computed(() => accountTableScrollX(isManagementView.value))
const tableScrollY = computed(accountTableScrollY)

const accountById = computed(() => accountByIdMap(accounts.value))
const selectedAccountIdSet = computed(() => new Set(selectedAccountIds.value))
const selectedAccounts = computed(() => accounts.value.filter((account) => selectedAccountIdSet.value.has(account.id)))
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

function groupIdForAccount(accountId: string) {
  return accountById.value.get(accountId)?.boundGroupId ?? groupIdByAccountId.value.get(accountId)
}

function groupNameForAccount(accountId: string) {
  return groupNameByAccountId.value.get(accountId)
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

function handleSystemAccountFilterChange() {
  selectedAccountIds.value = []
  handleAccountListSystemAccountFilterChange()
}

function clearSelection() {
  selectedAccountIds.value = []
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

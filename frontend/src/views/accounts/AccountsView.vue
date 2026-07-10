<template>
  <a-card class="page-card accounts-page-card responsive-page-card">
    <AccountFilterToolbar
      :active-filter-count="activeAdvancedFilterCount"
      :export-loading="exportLoading"
      :filters="filters"
      :group-filter-disabled="false"
      :group-options="filterGroupOptions"
      :group-options-loading="filterGroupOptionsLoading"
      :is-management-view="isManagementView"
      :providers="availableProviders"
      :refresh-loading="loading"
      :selected-count="selectedAccounts.length"
      :status-options="statusOptions"
      :system-accounts="systemAccounts"
      :system-accounts-loading="systemAccountOptionsLoading"
      :tag-filter-disabled="tagFilterDisabled"
      :tag-options="filterAccountTagOptions"
      :tag-options-loading="filterAccountTagOptionsLoading"
      @create="openCreate"
      @export="exportAccounts"
      @group-dropdown="handleFilterGroupOptionsDropdown"
      @group-search="handleFilterGroupOptionsSearch"
      @import="openImportModal"
      @refresh="refreshData"
      @reset="resetFilters"
      @search="applyFilters"
      @system-account-change="handleSystemAccountFilterChange"
      @system-account-dropdown="handleSystemAccountOptionsDropdown"
      @system-account-search="handleSystemAccountOptionsSearch"
      @tag-dropdown="handleFilterAccountTagDropdown"
      @update:group-id="filters.groupId = $event"
      @update:group-selection="filters.group = $event"
      @update:keyword="filters.keyword = $event"
      @update:provider-code="handleProviderFilterChange"
      @update:status="filters.status = $event"
      @update:system-account-id="filters.systemAccountId = $event"
      @update:system-account-selection="filters.systemAccount = $event"
      @update:tag-ids="filters.tagIds = $event"
      @update:type="handleAccountTypeFilterChange"
    >
      <template #actions>
        <TableColumnManager
          :columns="rawColumns"
          :settings="columnSettings"
          :required-keys="['name']"
          @reset="resetColumnSettings"
          @update:settings="updateColumnSettings"
        />
      </template>
    </AccountFilterToolbar>

    <AccountBatchToolbar
      :deletable-count="selectedDeletableAccountCount"
      :selected-count="selectedAccounts.length"
      @clear="clearSelection"
      @delete="openBatchDeleteConfirm"
      @disable="batchSetStatus('disabled')"
      @enable="batchSetStatus('active')"
      @restore="batchRestoreSelected"
      @test="batchTestSelected"
    />

    <AccountBatchDeleteConfirmModal
      v-model:open="batchDeleteConfirmOpen"
      :accounts="batchDeleteTargets"
      :is-management-view="isManagementView"
      :loading="batchDeleteConfirmLoading"
      :provider-name="providerName"
      @cancel="closeBatchDeleteConfirm"
      @ok="confirmBatchDelete"
    />

    <AccountImportModal
      v-if="importModalOpen"
      v-model:open="importModalOpen"
      :is-management-view="isManagementView"
      :scope-params="accountScopeParams"
      :target-system-account-label="targetSystemAccountLabel"
      @imported="handleImportCompleted"
    />

    <AccountList
      :accounts="filteredAccounts"
      :can-clone="canCloneAccount"
      :can-delete="canDeleteAccount"
      :can-edit="canEditAccount"
      :can-select="canSelectAccountForBatch"
      :columns="managedColumns"
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
      @change="handleAccountTableChange"
      @clone="openClone"
      @delete="removeAccount"
      @edit="openEdit"
      @menu-click="handleAccountMenuClick"
      @mobile-load-more="loadMoreMobileAccounts"
      @mobile-refresh="refreshMobileAccounts"
      @return-authorization="returnAuthorizationAccount"
      @sort-change="handleAccountSortChange"
      @test="openTestModal"
      @toggle-selection="toggleAccountSelection"
    />

    <AccountTestModal
      v-if="testModalOpen"
      v-model:open="testModalOpen"
      :model="testForm.model"
      :account="testingAccount"
      :accounts="batchTestingAccounts"
      :active-task="activeSingleTestTask"
      :batch-items="batchTestItems"
      :draft-account="draftTestingAccountPayload"
      :mode="testMode"
      :model-options="testModelOptions"
      :models-loading="testModelsLoading"
      :provider-name="providerName"
      :result="testResult"
      :running="testRunning"
      v-model:test-endpoint-mode="testForm.testEndpointMode"
      @close="closeTestModal"
      @copy-result="copyText"
      @run="runAccountTest"
      @stop="stopAccountTest"
      @update:model="updateAccountTestModel"
    />

    <AccountEditModal
      v-model:open="modalOpen"
      v-model:error-policy-rules="accountErrorPolicyRules"
      v-model:response-inspection-rules="accountResponseInspectionRules"
      :account-type-choices="accountTypeChoices"
      :api-key-test-details="apiKeyTestDetails"
      :authorized-editing="editingAuthorizedAccount"
      :auth-loading="authLoading"
      :auth-result="authResult"
      :base-url-placeholder="accountBaseUrlPlaceholder"
      :confirm-loading="modalConfirmLoading"
      :credential-title="selectedAccountTypeTitle"
      :editing="Boolean(editingId)"
      :account-detail="editingAccountDetail"
      :advanced-loaded="accountAdvancedDetailLoaded"
      :advanced-loading="accountAdvancedDetailLoading"
      :form="form"
      :group-options="groupOptions"
      :group-options-loading="groupOptionsLoading"
      :tag-options="accountTagOptions"
      :tag-options-loading="accountTagOptionsLoading"
      :deleting-tag-id="deletingAccountTagId"
      :has-account-type="hasAccountType"
      :is-api-key-form="isApiKeyForm"
      :is-management-view="isManagementView"
      :is-o-auth-form="isOAuthForm"
      :is-open-a-i-o-auth-form="isOpenAIOAuthForm"
      :loading="accountEditDetailLoading"
      :mapping-anthropic-source-model-options="mappingAnthropicSourceModelOptions"
      :mapping-gemini-source-model-options="mappingGeminiSourceModelOptions"
      :mapping-source-model-options="mappingSourceModelOptions"
      :model-options="providerModelOptions"
      :models-loading="strategyModelsLoading"
      :ok-button-props="modalOkButtonProps"
      :providers="availableProviders"
      :proxy-options="proxyOptions"
      :selected-protocol-profile="selectedProtocolProfile"
      :selected-provider="selectedProvider"
      :test-button-disabled="accountEditTestButtonDisabled"
      :test-loading="accountEditTestLoading"
      :title="modalTitle"
      @cancel="handleModalCancel"
      @copy-auth-url="copyText"
      @delete-tag="deleteAccountTag"
      @generate-auth-url="generateOAuthUrl"
      @group-options-dropdown="handleGroupOptionsDropdown"
      @group-options-search="handleGroupOptionsSearch"
      @advanced-open="loadAdvancedAccountDetail"
      @ok="saveAccount"
      @open-auth-url="openAuthUrl"
      @select-provider="selectProvider"
      @test="testAccountFromEditModal"
      @select-type-choice="selectAccountTypeChoice"
    />

    <AccountTrafficMigrationModal
      v-if="trafficMigrationModalOpen"
      v-model:open="trafficMigrationModalOpen"
      v-model:source-status="trafficMigrationForm.sourceStatus"
      v-model:target-account-id="trafficMigrationForm.targetAccountId"
      v-model:target-account="trafficMigrationForm.targetAccount"
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

import TableColumnManager from '@/components/TableColumnManager.vue'
import { useTableColumnSettings } from '@/components/tableColumnSettings'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import { groupLabelForId } from '@/shared/groupLabelCache'
import { isHybridProviderCode } from '@/shared/providerProtocol'
import type { AccountTagSummary } from '@/types/domain'
import AccountBatchDeleteConfirmModal from './AccountBatchDeleteConfirmModal.vue'
import AccountBatchToolbar from './AccountBatchToolbar.vue'
import AccountEditModal from './AccountEditModal.vue'
import AccountFilterToolbar from './AccountFilterToolbar.vue'
import AccountList from './AccountList.vue'
import {
  accountByIdMap,
  buildProxyOptions,
  proxyByIdMap
} from './accountDerivedState'
import {
  statusOptions
} from './accountOptions'
import {
  accountDisplayName
} from './accountBasicFormatters'
import {
  accountTableScrollX,
  accountTableScrollY,
  buildAccountTableColumns,
  accountColumnSortOrder as resolveAccountColumnSortOrder,
} from './accountTableColumns'
import {
  accountMenuItems,
  canCloneAccount,
  canDeleteAccount,
  canEditAccount,
  canSelectAccountForBatch
} from './accountRules'
import { useAccountBatchActions } from './useAccountBatchActions'
import { useAccountEditForm } from './useAccountEditForm'
import { useAccountEditTestAction } from './useAccountEditTestAction'
import { useAccountExportActions } from './useAccountExportActions'
import { useAccountFilterTagOptions } from './useAccountFilterTagOptions'
import { useAccountFilterInteractions } from './useAccountFilterInteractions'
import { useAccountGroupOptions } from './useAccountGroupOptions'
import { useAccountEditGroupOptions } from './useAccountEditGroupOptions'
import { useAccountListData } from './useAccountListData'
import { useAccountMenuActions } from './useAccountMenuActions'
import { accountOperationSystemAccountId } from './accountOperationScope'
import { useAccountReauthorize } from './useAccountReauthorize'
import { useAccountRemovalActions } from './useAccountRemovalActions'
import { useAccountSelectionActions } from './useAccountSelectionActions'
import { useAccountTestModal, type SuccessfulDraftActivationTest } from './useAccountTestModal'
import type { DraftApiKeyTestSnapshot } from './accountDraftApiKeyTestRuntime'
import { useAccountTrafficMigration } from './useAccountTrafficMigration'

const AccountImportModal = defineAsyncComponent(() => import('./AccountImportModal.vue'))
const AccountReauthorizeModal = defineAsyncComponent(() => import('./AccountReauthorizeModal.vue'))
const AccountTestModal = defineAsyncComponent(() => import('./AccountTestModal.vue'))
const AccountTrafficMigrationModal = defineAsyncComponent(() => import('./AccountTrafficMigrationModal.vue'))

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
  loadAccountOptions: loadAccountAuxiliaryOptions,
  loadData: loadAccountListData,
  refreshData,
  applyFilters,
  handleAccountTableChangeAndLoad,
  handleAccountSortChange,
  handleSystemAccountFilterChange: handleAccountListSystemAccountFilterChange,
  applyAccountDefaultTestModel,
  removeLoadedAccount,
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
  allowGlobalManagement: true,
  errorMessage: '加载筛选分组选项失败',
  isManagementView: () => isManagementView.value,
  limit: 50,
  onMissingSelectedIds: (ids) => {
    const groupId = filters.groupId.trim()
    if (!groupId || !ids.includes(groupId)) return false
    filters.groupId = ''
    filters.group = undefined
    applyFilters()
    return true
  },
  scope: () => ({
    providerCode: filters.providerCode !== 'all' ? filters.providerCode : undefined,
    systemAccountId: isManagementView.value ? accountScopeParams.value?.systemAccountId : undefined,
    selectedIds: [filters.groupId]
  })
})
const {
  disabled: tagFilterDisabled,
  handleDropdown: handleFilterAccountTagDropdown,
  load: loadFilterAccountTagOptions,
  loading: filterAccountTagOptionsLoading,
  options: filterAccountTagOptions,
  reset: resetFilterAccountTagOptions
} = useAccountFilterTagOptions({
  accountScopeParams,
  isManagementView
})

function handleAccountListLoaded(selectableAccountIds: Set<string>) {
  pruneSelection(selectableAccountIds)
  rememberAccountGroupLabels(accounts.value)
  syncFilterGroupSelection()
  if (modalOpen.value && !editingId.value) {
    ensureDefaultGroupSelected()
  }
}

async function loadData(options?: { append?: boolean; quiet?: boolean; forceOptions?: boolean; shouldApply?: () => boolean }) {
  await loadAccountListData(options)
}

const rawColumns = computed(() => buildAccountTableColumns(
  isManagementView.value,
  (field) => resolveAccountColumnSortOrder(accountSorts.value, field)
))
const columnStorageKey = computed(() => (isManagementView.value ? 'accounts:management' : 'accounts:self'))
const {
  managedColumns,
  columnSettings,
  updateColumnSettings,
  resetColumnSettings
} = useTableColumnSettings(columnStorageKey, rawColumns, {
  requiredKeys: ['name'],
  minVisible: 1
})
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
const {
  batchDeleteConfirmLoading,
  batchDeleteConfirmOpen,
  batchDeleteTargets,
  clearSelectedAccountIds,
  clearSelection,
  closeBatchDeleteConfirm,
  confirmBatchDeleteWith,
  isAccountSelected,
  openBatchDeleteConfirm,
  pruneSelection,
  rowSelection,
  selectedAccounts,
  selectedDeletableAccountCount,
  toggleAccountSelection
} = useAccountSelectionActions({
  accounts
})
const {
  exportAccounts,
  exportLoading
} = useAccountExportActions({
  accountScopeParams,
  accountSorts,
  filters,
  isManagementView,
  selectedAccounts,
  systemAccounts
})
const successfulDraftActivationTest = ref<SuccessfulDraftActivationTest>()
const successfulSavedDraftUpdateTest = ref<SuccessfulDraftActivationTest>()
const draftApiKeyTestSnapshot = ref<DraftApiKeyTestSnapshot>()
const {
  groups,
  handleDropdown: handleGroupOptionsDropdown,
  handleSearch: handleGroupOptionsSearch,
  load: loadGroupOptions,
  loading: groupOptionsLoading,
  resetSearch: resetGroupOptionsSearch,
  setEditGroupOptionScope
} = useAccountEditGroupOptions({
  accountScopeParams,
  isManagementView
})
const {
  accountErrorPolicyRules,
  accountResponseInspectionRules,
  accountAdvancedDetailLoaded,
  accountAdvancedDetailLoading,
  accountEditDetailLoading,
  apiKeyTestDetails,
  accountTagOptions,
  accountTagOptionsLoading,
  accountTypeChoices,
  authLoading,
  authResult,
  availableProviders,
  createScopeParams,
  editingAccountDetail,
  editingId,
  editingAuthorizedAccount,
  ensureDefaultGroupSelected,
  form,
  generateOAuthUrl,
  deleteAccountTag,
  deletingAccountTagId,
  groupOptions,
  handleModalCancel,
  hasAccountType,
  isApiKeyForm,
  isOAuthForm,
  isOpenAIOAuthForm,
  mappingAnthropicSourceModelOptions,
  mappingGeminiSourceModelOptions,
  mappingSourceModelOptions,
  modalConfirmLoading,
  modalOkButtonProps,
  modalOpen,
  modalTitle,
  openAuthUrl,
  openClone,
  openCreate,
  openEdit,
  ensureAccountEditDetailLoaded,
  loadAdvancedAccountDetail,
  providerName,
  providerModelOptions,
  strategyModelsLoading,
  saveAccount,
  selectAccountTypeChoice,
  selectedAccountTypeTitle,
  selectedProtocolProfile,
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
  loadAccountOptions: loadAccountAuxiliaryOptions,
  loadGroupOptions,
  loadData,
  providers,
  draftApiKeyTestSnapshot,
  systemAccountSelection: computed(() => filters.systemAccount),
  systemAccounts,
  successfulDraftActivationTest,
  successfulSavedDraftUpdateTest
})
watch(
  [
    () => form.providerCode,
    () => form.providerProtocolProfileId,
    () => form.groupId,
    () => createScopeParams.value?.systemAccountId,
    () => editingId.value,
    () => accountScopeParams.value?.systemAccountId
  ],
  () => {
    const activeAccount = editingId.value
      ? accountById.value.get(editingId.value)
      : undefined
    setEditGroupOptionScope({
      providerCode: form.providerCode,
      systemAccountId: isManagementView.value
        ? accountOperationSystemAccountId(activeAccount, createScopeParams.value) ?? ''
        : undefined,
      selectedIds: [form.groupId]
    })
  },
  { immediate: true }
)
const {
  handleAccountTypeFilterChange,
  handleProviderFilterChange,
  handleSystemAccountFilterChange,
  rememberAccountGroupLabels,
  resetFilters,
  syncFilterGroupSelection
} = useAccountFilterInteractions({
  accounts,
  applyFilters,
  availableProviders,
  clearSelection,
  filterGroupOptions,
  filters,
  handleAccountListSystemAccountFilterChange,
  resetAccountListFilters,
  resetFilterAccountTagOptions,
  resetFilterGroupOptionsSearch,
  resetGroupOptionsSearch
})
const {
  activeSingleTestTask,
  batchTestItems,
  batchTestingAccounts,
  closeTestModal,
  openBatchTestModal,
  openDraftTestModal,
  openSavedDraftTestModal,
  openTestModal,
  runAccountTest,
  stopAccountTest,
  testForm,
  testModalOpen,
  testMode,
  testModelOptions,
  testModelsLoading,
  testResult,
  testRunning,
  draftTestingAccountPayload,
  testingAccount,
  updateAccountTestModel
} = useAccountTestModal({
  accountScopeParams,
  applyAccountDefaultTestModel,
  clearSelection,
  isManagementView,
  loadData,
  providers: availableProviders,
  draftApiKeyTestSnapshot,
  successfulDraftActivationTest,
  successfulSavedDraftUpdateTest
})
const {
  accountEditTestPreparing,
  testAccountFromEditModal
} = useAccountEditTestAction({
  accountAdvancedDetailLoaded,
  accountDetail: editingAccountDetail,
  accountScopeParams,
  accounts,
  authSessionId: computed(() => authResult.value?.sessionId),
  createScopeParams,
  editingAuthorizedAccount,
  editingId,
  ensureAccountEditDetailLoaded,
  ensureAdvancedAccountDetailLoaded: loadAdvancedAccountDetail,
  errorPolicyRules: accountErrorPolicyRules,
  form,
  mappingAnthropicSourceModelOptions,
  mappingGeminiSourceModelOptions,
  mappingSourceModelOptions,
  mappingUpstreamModelOptions: providerModelOptions,
  openDraftTestModal,
  openSavedDraftTestModal,
  openTestModal,
  providers: availableProviders,
  responseInspectionRules: accountResponseInspectionRules,
  selectedProtocolProfile
})
const accountEditTestLoading = computed(() => accountEditTestPreparing.value || testRunning.value)
const accountEditTestButtonDisabled = computed(() => accountEditTestLoading.value)
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
  batchRestoreSelected,
  batchSetStatus,
  batchTestSelected
} = useAccountBatchActions({
  accountScopeParams,
  clearSelection,
  isManagementView,
  loadData,
  openBatchTestModal,
  selectedAccounts
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
const {
  batchDeleteSelected,
  removeAccount,
  returnAuthorizationAccount
} = useAccountRemovalActions({
  accountById,
  accounts,
  accountScopeParams,
  clearSelection,
  extractApiErrorMessage,
  isManagementView,
  loadData,
  pruneSelection,
  removeLoadedAccount
})
const proxyByIdMapRef = computed(() => proxyByIdMap(proxies.value))

const proxyOptions = computed(() => buildProxyOptions(proxies.value))
const proxyById = (proxyProfileId?: string) => proxyProfileId ? proxyByIdMapRef.value.get(proxyProfileId) : undefined
const accountBaseUrlPlaceholder = computed(() => (
  isHybridProviderCode(form.providerCode)
    ? '填写真实上游 Base URL'
    : selectedProtocolProfile.value?.baseUrl || selectedProvider.value?.baseUrl || 'https://api.openai.com/v1'
))

function groupIdForAccount(accountId: string) {
  return accountById.value.get(accountId)?.boundGroupId
}

function groupNameForAccount(accountId: string) {
  const account = accountById.value.get(accountId)
  if (!account) return undefined
  return account.boundGroupName ?? groupLabelForId(account.boundGroupId)
}

async function copyText(value: string) {
  await copyTextToClipboard(value)
}

async function confirmBatchDelete() {
  await confirmBatchDeleteWith(batchDeleteSelected)
}

function openImportModal() {
  if (isManagementView.value && !accountScopeParams.value?.systemAccountId) {
    message.warning('请先在右侧选择目标系统账户，再导入 AI 账户')
    return
  }
  importModalOpen.value = true
}

async function handleImportCompleted() {
  clearSelectedAccountIds()
  await loadData({ forceOptions: true })
}

onMounted(() => {
  void loadData()
  void loadAccountAuxiliaryOptions(accountScopeParams.value?.systemAccountId).catch((error) => {
    console.error(error)
  })
  void loadFilterAccountTagOptions().catch((error) => {
    console.error(error)
  })
})
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

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
      @provider-dropdown="handleProviderFilterDropdown"
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
      :edit-disabled="batchEditDisabled"
      :edit-disabled-reason="batchEditDisabledReason"
      :selected-count="selectedAccounts.length"
      @clear="clearSelection"
      @delete="openBatchDeleteConfirm"
      @disable="openBatchDisableConfirm"
      @edit="openBatchEdit"
      @enable="batchSetStatus('active')"
      @restore="batchRestoreSelected"
    />

    <AccountBatchEditModal
      v-if="batchEditOpen"
      v-model:open="batchEditOpen"
      :accounts="selectedAccounts"
      :is-management-view="isManagementView"
      :providers="availableProviders"
      :proxy-options="proxyOptions"
      :proxy-options-loading="proxyOptionsLoading"
      @proxy-options-dropdown="handleProxyOptionsDropdown"
      @proxy-options-search="handleProxyOptionsSearch"
      :scope-params="accountScopeParams"
      :tags="accountTagOptions"
      @saved="handleBatchEditSaved"
    />

    <AccountBatchDisableConfirmModal
      v-model:open="batchDisableConfirmOpen"
      :accounts="batchDisableTargets"
      :loading="batchDisableConfirmLoading"
      @cancel="batchDisableConfirmOpen = false"
      @ok="confirmBatchDisable"
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
      :editing-priority-account-id="editingPriorityAccountId"
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
      :save-priority="saveAccountPriority"
      :table-scroll-x="tableScrollX"
      :table-scroll-y="tableScrollY"
      :balance-refreshing-ids="balanceRefreshingIds"
      @change="handleAccountTableChange"
      @cancel-priority-edit="closePriorityEditor"
      @clone="openClone"
      @delete="removeAccount"
      @edit="openEdit"
      @menu-click="handleAccountMenuClick"
      @mobile-load-more="loadMoreMobileAccounts"
      @mobile-refresh="refreshMobileAccounts"
      @return-authorization="returnAuthorizationAccount"
      @refresh-balance="refreshAccountBalance"
      @sort-change="handleAccountSortChange"
      @start-priority-edit="startPriorityEditor"
      @test="openTestModal"
      @toggle-selection="toggleAccountSelection"
    />

    <AccountTestModal
      v-if="testModalOpen"
      v-model:open="testModalOpen"
      :model="testForm.model"
      :account="testingAccount"
      :active-task="activeSingleTestTask"
      :endpoint-modes-error="testEndpointModesError"
      :endpoint-modes-loading="testEndpointModesLoading"
      :model-options="testModelOptions"
      :model-readonly="testModelReadonly"
      :models-error="testModelsError"
      :models-loading="testModelsLoading"
      :provider-name="providerName"
      :result="testResult"
      :running="testRunning"
      :test-endpoint-modes="testEndpointModes"
      v-model:test-endpoint-mode="testForm.testEndpointMode"
      @close="closeTestModal"
      @copy-result="copyText"
      @load-endpoint-mode-options="loadAccountTestEndpointModeOptions"
      @load-model-options="loadAccountTestModelOptions"
      @search-model-options="loadAccountTestModelOptions(true, $event)"
      @run="runAccountTest"
      @stop="stopAccountTest"
      @update:model="updateAccountTestModel"
    />

    <AccountEditModal
      v-model:open="modalOpen"
      v-model:error-policy-rules="accountErrorPolicyRules"
      v-model:response-inspection-rules="accountResponseInspectionRules"
      :account-type-choices="accountTypeChoices"
      :api-key-runtime-details="accountApiKeyRuntimeDetails"
      :api-key-runtime-loading="accountApiKeyRuntimeLoading"
      :api-key-test-details="apiKeyTestDetails"
      :authorized-editing="editingAuthorizedAccount"
      :auth-loading="authLoading"
      :auth-result="authResult"
      :base-url-placeholder="accountBaseUrlPlaceholder"
      :balance-query-can-run="form.balanceQueryEnabled"
      :balance-query-loading="balanceQueryTesting"
      :confirm-loading="modalConfirmLoading"
      :credential-title="selectedAccountTypeTitle"
      :editing="Boolean(editingId)"
      :account-detail="editingAccountDetail"
      :account-advanced-detail="editingAccountAdvancedDetail"
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
      :is-anthropic-o-auth-form="isAnthropicOAuthForm"
      :is-o-auth-form="isOAuthForm"
      :is-token-credential-form="isTokenCredentialForm"
      :is-open-a-i-o-auth-form="isOpenAIOAuthForm"
      :loading="accountEditDetailLoading"
      :mapping-anthropic-source-model-options="mappingAnthropicSourceModelOptions"
      :mapping-current-provider-source-model-options="mappingCurrentProviderSourceModelOptions"
      :mapping-gemini-source-model-options="mappingGeminiSourceModelOptions"
      :mapping-source-model-options="mappingSourceModelOptions"
      :model-options="providerModelOptions"
      :models-loading="strategyModelsLoading"
      :model-syncing="modelCatalogSyncing"
      :ok-button-props="modalOkButtonProps"
      :providers="availableProviders"
      :proxy-options="proxyOptions"
      :proxy-options-loading="proxyOptionsLoading"
      @proxy-options-dropdown="handleProxyOptionsDropdown"
      @proxy-options-search="handleProxyOptionsSearch"
      :selected-protocol-profile="selectedProtocolProfile"
      :selected-provider="selectedProvider"
      :test-button-disabled="accountEditTestButtonDisabled"
      :test-loading="accountEditTestLoading"
      :title="modalTitle"
      @cancel="handleModalCancel"
      @copy-auth-url="copyText"
      @delete-tag="deleteAccountTag"
      @load-api-key-runtime="loadAccountApiKeyRuntimeDetails"
      @generate-auth-url="generateOAuthUrl"
      @group-options-dropdown="handleGroupOptionsDropdown"
      @group-options-search="handleGroupOptionsSearch"
      @model-options-open="handleAccountModelOptionsOpen"
      @model-options-search="handleAccountModelOptionsSearch"
      @refresh-models="refreshAccountModelCatalog"
      @mapping-model-options-open="handleMappingModelOptionsOpen"
      @mapping-model-options-search="handleMappingModelOptionsSearch"
      @advanced-open="loadAdvancedAccountDetail"
      @balance-query="queryBalanceFromEdit"
      @ok="saveAccount"
      @open-auth-url="openAuthUrl"
      @select-provider="selectProvider"
      @test="testAccountFromEditModal"
      @select-type-choice="selectAccountTypeChoice"
      @tag-options-dropdown="handleAccountTagOptionsDropdown"
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
      :is-management-view="isManagementView"
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
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, ref, watch } from 'vue'

import TableColumnManager from '@/components/TableColumnManager.vue'
import { useTableColumnSettings } from '@/components/tableColumnSettings'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { loadUserReferenceData } from '@/composables/useUserReferenceData'
import type { AccountDraftTestAccountPayload, AccountModelCatalogDiscoveryAccountPayload } from '@/api/client'
import { api } from '@/api/client'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import { groupLabelForId } from '@/shared/groupLabelCache'
import { isHybridProviderCode } from '@/shared/providerProtocol'
import type { AccountListItem, AccountSummary, AccountTagSummary } from '@/types/domain'
import AccountBatchDisableConfirmModal from './AccountBatchDisableConfirmModal.vue'
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
import { isAuthorizedAccount } from './accountFormatters'
import {
  accountTableScrollX,
  accountTableScrollY,
  buildAccountTableColumns,
  accountColumnSortOrder as resolveAccountColumnSortOrder,
} from './accountTableColumns'
import {
  accountMenuItems,
  canBatchEditAccount,
  canToggleAccountStatus,
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
import { useAccountProxyOptions } from './useAccountProxyOptions'
import { useAccountMenuActions } from './useAccountMenuActions'
import { accountOperationScopeParams, accountOperationSystemAccountId } from './accountOperationScope'
import { mergeAuthorizedDispatchMutation } from './accountListMutations'
import { buildAccountBalancePayload, formatAccountBalance } from './accountBalanceQuery'
import { useAccountReauthorize } from './useAccountReauthorize'
import { useAccountRemovalActions } from './useAccountRemovalActions'
import { useAccountSelectionActions } from './useAccountSelectionActions'
import { useAccountTestModal } from './useAccountTestModal'
import type { DraftApiKeyTestSnapshot } from './accountDraftApiKeyTestRuntime'
import { useAccountTrafficMigration } from './useAccountTrafficMigration'
import { accountTestEndpointModesForModel } from './accountEndpointModes'
import { buildAccountCredentials, normalizedAccountApiKeys } from './accountCredentials'

const AccountImportModal = defineAsyncComponent(() => import('./AccountImportModal.vue'))
const AccountBatchEditModal = defineAsyncComponent(() => import('./AccountBatchEditModal.vue'))
const AccountReauthorizeModal = defineAsyncComponent(() => import('./AccountReauthorizeModal.vue'))
const AccountTestModal = defineAsyncComponent(() => import('./AccountTestModal.vue'))
const AccountTrafficMigrationModal = defineAsyncComponent(() => import('./AccountTrafficMigrationModal.vue'))

const importModalOpen = ref(false)
const batchEditOpen = ref(false)
const balanceRefreshingIds = ref(new Set<string>())
const editingPriorityAccountId = ref<string>()
const prioritySavingIds = ref(new Set<string>())
const balanceQueryTesting = ref(false)
const modelCatalogSyncing = ref(false)
const batchDisableConfirmOpen = ref(false)
const batchDisableConfirmLoading = ref(false)
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const {
  loading,
  accounts,
  providers,
  providerDefinitions,
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
  loadAccountOptions,
  refreshMobileAccounts: refreshMobileAccountList,
  ensureProviderDefinition,
  loadData: loadAccountListData,
  refreshData: refreshAccountList,
  applyFilters,
  handleAccountTableChangeAndLoad,
  handleAccountSortChange: handleAccountListSortChange,
  handleSystemAccountFilterChange: handleAccountListSystemAccountFilterChange,
  removeLoadedAccount,
  updateLoadedAccount,
  updateLoadedAccountBalance,
  resetFilters: resetAccountListFilters
} = useAccountListData({
  isManagementView,
  scopedSystemAccountId,
  onLoaded: handleAccountListLoaded
})

watch(
  () => [isManagementView.value, accountScopeParams.value?.systemAccountId] as const,
  ([managementView, systemAccountId]) => {
    if (!managementView || !systemAccountId) return
    void loadUserReferenceData({ viewScope: 'admin', systemAccountId }).catch(() => undefined)
  },
  { immediate: true }
)

const {
  proxies,
  loading: proxyOptionsLoading,
  handleDropdown: handleProxyOptionsDropdown,
  handleSearch: handleProxyOptionsSearch,
  load: loadProxyOptions
} = useAccountProxyOptions({
  errorMessage: '加载代理选项失败',
  scope: () => ({
    // Only the currently edited form selection needs options backfill.
    // Account list rows already carry proxy display fields and must not
    // inflate selectedIds past the server cap of 20.
    selectedIds: [form.proxyProfileId]
  })
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
  isManagementView,
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
  closePriorityEditor()
  await loadAccountListData(options)
}

function handleProviderFilterDropdown(open: boolean): void {
  if (!open) return
  void loadAccountOptions(accountScopeParams.value?.systemAccountId).catch((error) => {
    console.error(error)
    message.error('加载供应商筛选选项失败')
  })
}

function startPriorityEditor(accountId: string): void {
  editingPriorityAccountId.value = accountId
}

function closePriorityEditor(accountId?: string): void {
  if (accountId && editingPriorityAccountId.value !== accountId) return
  editingPriorityAccountId.value = undefined
}

function refreshData(): void {
  closePriorityEditor()
  refreshAccountList()
}

async function refreshMobileAccounts(): Promise<void> {
  closePriorityEditor()
  await refreshMobileAccountList()
}

async function refreshAccountBalance(accountId: string) {
  if (balanceRefreshingIds.value.has(accountId)) return
  balanceRefreshingIds.value = new Set(balanceRefreshingIds.value).add(accountId)
  try {
    const snapshot = isManagementView.value
      ? await api.accounts.refreshBalance(accountId, accountScopeParams.value)
      : await api.myAccounts.refreshBalance(accountId)
    updateLoadedAccountBalance(accountId, snapshot)
    if (snapshot?.status === 'failed' || snapshot?.status === 'unsupported') {
      if (snapshot.status === 'unsupported') {
        message.warning(snapshot.errorMessage || '当前配置未找到可用余额接口')
      } else {
        message.error(snapshot.errorMessage || '余额查询失败')
      }
      return
    }
    message.success('余额已更新')
  } catch (error) {
    message.error(extractApiErrorMessage(error, '刷新上游余额失败'))
  } finally {
    const next = new Set(balanceRefreshingIds.value)
    next.delete(accountId)
    balanceRefreshingIds.value = next
  }
}

async function saveAccountPriority(account: AccountListItem, priority: number): Promise<boolean> {
  if (!canEditAccount(account)) {
    message.warning('当前账户无权修改调度优先级')
    return false
  }
  if (!Number.isInteger(priority) || priority < 0) {
    message.warning('优先级必须是大于等于 0 的整数')
    return false
  }
  if (priority === account.priority) return true
  if (prioritySavingIds.value.has(account.id)) return false

  prioritySavingIds.value = new Set(prioritySavingIds.value).add(account.id)
  const scopeParams = accountOperationScopeParams(account, accountScopeParams.value)
  try {
    if (isAuthorizedAccount(account)) {
      const configRevision = Number(account.configRevision)
      if (!Number.isInteger(configRevision) || configRevision < 1) {
        message.warning('账户配置版本缺失，请刷新列表后重试')
        return false
      }
      const updated = isManagementView.value
        ? await api.accounts.updateAuthorizedDispatch(account.id, { priority, expectedConfigRevision: configRevision }, scopeParams)
        : await api.myAccounts.updateAuthorizedDispatch(account.id, { priority, expectedConfigRevision: configRevision })
      updateLoadedAccount(mergeAuthorizedDispatchMutation(account, updated))
    } else {
      const configRevision = Number(account.configRevision)
      if (!Number.isInteger(configRevision) || configRevision < 1) {
        message.warning('账户配置版本缺失，请刷新列表后重试')
        return false
      }
      const updated = isManagementView.value
        ? await api.accounts.update(account.id, { priority, expectedConfigRevision: configRevision }, scopeParams)
        : await api.myAccounts.update(account.id, { priority, expectedConfigRevision: configRevision })
      updateLoadedAccount({ ...account, priority, configRevision: updated.configRevision })
    }
    message.success('调度优先级已更新')
    return true
  } catch (error) {
    message.error(extractApiErrorMessage(error, '更新调度优先级失败'))
    return false
  } finally {
    const next = new Set(prioritySavingIds.value)
    next.delete(account.id)
    prioritySavingIds.value = next
  }
}

async function queryBalanceFromEdit(): Promise<void> {
  if (balanceQueryTesting.value) return
  const balancePayload = buildAccountBalancePayload(form)
  if (!balancePayload?.balanceQueryConfig) return
  const account = currentDraftTestPayload()
  if (!account) {
    message.error('请先完善并检查当前账户配置')
    return
  }
  balanceQueryTesting.value = true
  try {
    const snapshot = isManagementView.value
      ? await api.accounts.testBalanceDraft({ account, balanceQueryConfig: balancePayload.balanceQueryConfig }, accountScopeParams.value)
      : await api.myAccounts.testBalanceDraft({ account, balanceQueryConfig: balancePayload.balanceQueryConfig })
    if (snapshot?.status === 'failed') {
      message.error(snapshot.errorMessage || '余额查询失败')
    } else if (snapshot) {
      const result = formatAccountBalance(snapshot)
      if (snapshot.status === 'unsupported') {
        message.warning(`余额查询完成：${result.text}`)
      } else {
        message.success(`余额查询成功：${result.text}`)
      }
    } else {
      message.error('余额查询没有返回结果')
    }
  } catch (error) {
    message.error(extractApiErrorMessage(error, '查询上游余额失败'))
  } finally {
    balanceQueryTesting.value = false
  }
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
  closePriorityEditor()
  if (tableChangeAction(args[3]) === 'sort') return
  void handleAccountTableChangeAndLoad(args[0])
}

function handleAccountSortChange(sorts: Parameters<typeof handleAccountListSortChange>[0]): void {
  closePriorityEditor()
  handleAccountListSortChange(sorts)
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
const batchEditableAccounts = computed(() => selectedAccounts.value.filter(canBatchEditAccount))
const batchDisableTargets = computed(() => selectedAccounts.value
  .filter(canToggleAccountStatus)
  .filter((account) => account.status !== 'disabled'))
const batchEditDisabled = computed(() => (
  selectedAccounts.value.length < 2
  || selectedAccounts.value.length > 100
  || batchEditableAccounts.value.length !== selectedAccounts.value.length
))
const batchEditDisabledReason = computed(() => {
  if (selectedAccounts.value.length < 2) return '至少选择 2 个账户'
  if (selectedAccounts.value.length > 100) return '一次最多编辑 100 个账户'
  if (batchEditableAccounts.value.length !== selectedAccounts.value.length) {
    return '授权实例或无编辑权限账户不能批量编辑'
  }
  return ''
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
const draftApiKeyTestSnapshot = ref<DraftApiKeyTestSnapshot>()
const {
  groups,
  handleDropdown: handleGroupOptionsDropdown,
  handleSearch: handleGroupOptionsSearch,
  load: loadGroupOptions,
  loading: groupOptionsLoading,
  resetEditGroupOptions,
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
  accountApiKeyRuntimeDetails,
  accountApiKeyRuntimeLoading,
  apiKeyTestDetails,
  accountTagOptions,
  accountTagOptionsLoading,
  accountTypeChoices,
  authLoading,
  authResult,
  availableProviders,
  createScopeParams,
  editingAccountDetail,
  editingAccountAdvancedDetail,
  editingId,
  editingAuthorizedAccount,
  ensureDefaultGroupSelected,
  form,
  generateOAuthUrl,
  deleteAccountTag,
  deletingAccountTagId,
  groupOptions,
  handleModalCancel,
  handleAccountTagOptionsDropdown,
  hasAccountType,
  isApiKeyForm,
  isAnthropicOAuthForm,
  isOAuthForm,
  isTokenCredentialForm,
  isOpenAIOAuthForm,
  mappingAnthropicSourceModelOptions,
  mappingCurrentProviderSourceModelOptions,
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
  loadAccountApiKeyRuntimeDetails,
  loadCurrentProviderModelOptions,
  loadMappingSourceModelOptions,
  providerName,
  providerModelOptions,
  strategyModelsLoading,
  currentDraftTestPayload,
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
  ensureProviderDefinition,
  loadGroupOptions,
  loadData,
  providerDefinitions,
  providers,
  draftApiKeyTestSnapshot,
  systemAccountSelection: computed(() => filters.systemAccount),
  systemAccounts
})

function handleAccountModelOptionsOpen(open: boolean): void {
  if (!open) {
    clearAccountModelOptionsSearchTimer()
    return
  }
  void loadCurrentProviderModelOptions()
}

let modelCatalogSyncController: AbortController | undefined
let modelCatalogSyncRequestId = 0

function cancelAccountModelCatalogSync(): void {
  modelCatalogSyncRequestId += 1
  modelCatalogSyncController?.abort()
  modelCatalogSyncController = undefined
  modelCatalogSyncing.value = false
}

function currentModelCatalogDiscoveryPayload(): { account: AccountModelCatalogDiscoveryAccountPayload } | undefined {
  if (!form.providerCode || !form.providerProtocolProfileId || !form.type) return undefined
  if (form.type === 'api_key' && (!form.baseUrl.trim() || !normalizedAccountApiKeys(form).length)) return undefined
  const credentials = buildAccountCredentials({
    form,
    currentCredentials: editingAccountDetail.value?.credentials ?? {},
    errorPolicyRules: accountErrorPolicyRules.value,
    responseInspectionRules: accountResponseInspectionRules.value
  })
  return {
    account: {
      providerCode: form.providerCode,
      providerProtocolProfileId: form.providerProtocolProfileId,
      type: form.type,
      credentials,
      name: form.name.trim() || undefined,
      groupId: form.groupId,
      proxyProfileId: form.proxyProfileId,
      supportedModels: form.supportedModels.map((model) => model.trim()).filter(Boolean),
      healthCheckModel: form.healthCheckModel.trim() || undefined,
      healthCheckEndpointMode: form.healthCheckEndpointMode
    }
  }
}

function modelCatalogDiscoveryRequestKey(payload: { account: AccountModelCatalogDiscoveryAccountPayload } | undefined): string {
  return payload ? JSON.stringify(payload.account) : ''
}

function currentModelCatalogDiscoveryRequestKey(): string {
  return modelCatalogDiscoveryRequestKey(currentModelCatalogDiscoveryPayload())
}

async function refreshAccountModelCatalog(): Promise<void> {
  const payload = currentModelCatalogDiscoveryPayload()
  if (!payload) {
    message.error('请先填写 Base URL 和 API Key 后再同步')
    return
  }
  const requestKey = modelCatalogDiscoveryRequestKey(payload)
  const requestId = ++modelCatalogSyncRequestId
  modelCatalogSyncController?.abort()
  const controller = new AbortController()
  modelCatalogSyncController = controller
  modelCatalogSyncing.value = true
  try {
    const result = isManagementView.value
      ? await api.accounts.refreshModelCatalog(payload, accountScopeParams.value, { signal: controller.signal })
      : await api.myAccounts.refreshModelCatalog(payload, { signal: controller.signal })
    if (requestId !== modelCatalogSyncRequestId || requestKey !== currentModelCatalogDiscoveryRequestKey()) return
    form.supportedModels = [...new Set([
      ...form.supportedModels.map((model) => model.trim()).filter(Boolean),
      ...result.addedModels
    ])]
    if (!form.healthCheckModel.trim() && result.recommendedHealthCheckModel) form.healthCheckModel = result.recommendedHealthCheckModel
    message.success(result.addedModels.length
      ? `已新增 ${result.addedModels.length} 个上游支持模型，手动选择已保留`
      : '上游目录已同步，手动选择已保留')
  } catch (error) {
    if (requestId === modelCatalogSyncRequestId && !controller.signal.aborted) {
      message.error(extractApiErrorMessage(error, '同步上游模型失败'))
    }
  } finally {
    if (requestId === modelCatalogSyncRequestId) modelCatalogSyncing.value = false
  }
}

let accountModelOptionsSearchTimer: ReturnType<typeof setTimeout> | undefined

function clearAccountModelOptionsSearchTimer(): void {
  if (!accountModelOptionsSearchTimer) return
  clearTimeout(accountModelOptionsSearchTimer)
  accountModelOptionsSearchTimer = undefined
}

function handleAccountModelOptionsSearch(value: string): void {
  clearAccountModelOptionsSearchTimer()
  accountModelOptionsSearchTimer = setTimeout(() => {
    accountModelOptionsSearchTimer = undefined
    void loadCurrentProviderModelOptions(value)
  }, 250)
}

function handleMappingModelOptionsOpen(protocol: 'openai' | 'anthropic' | 'gemini', open: boolean): void {
  if (!open) {
    clearAccountModelOptionsSearchTimer()
    return
  }
  void loadMappingSourceModelOptions(protocol)
}

function handleMappingModelOptionsSearch(protocol: 'openai' | 'anthropic' | 'gemini', value: string): void {
  clearAccountModelOptionsSearchTimer()
  accountModelOptionsSearchTimer = setTimeout(() => {
    accountModelOptionsSearchTimer = undefined
    void loadMappingSourceModelOptions(protocol, value)
  }, 250)
}

watch(modalOpen, (open) => {
  if (open) return
  clearAccountModelOptionsSearchTimer()
  cancelAccountModelCatalogSync()
  resetEditGroupOptions()
})

onBeforeUnmount(() => {
  clearAccountModelOptionsSearchTimer()
  cancelAccountModelCatalogSync()
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
  closeTestModal,
  loadAccountTestEndpointModeOptions,
  loadAccountTestModelOptions,
  openDraftTestModal: openDraftTestModalWithHealthCheckModel,
  openSavedDraftTestModal: openSavedDraftTestModalWithHealthCheckModel,
  openTestModal,
  runAccountTest,
  stopAccountTest,
  testEndpointModes,
  testEndpointModesError,
  testEndpointModesLoading,
  testForm,
  testModalOpen,
  testModelOptions,
  testModelReadonly,
  testModelsError,
  testModelsLoading,
  testResult,
  testRunning,
  testingAccount,
  updateAccountTestModel
} = useAccountTestModal({
  accountScopeParams,
  isManagementView,
  draftApiKeyTestSnapshot
})
function openDraftTestModal(
  account: AccountSummary,
  draftPayload: AccountDraftTestAccountPayload
): void {
  const model = draftHealthCheckModel(draftPayload)
  openDraftTestModalWithHealthCheckModel(
    account,
    draftPayload,
    model,
    accountTestEndpointModesForModel(
      account,
      draftPayload,
      providerModelOptions.value.find((option) => option.value === model)
    )
  )
}
function openSavedDraftTestModal(
  account: AccountSummary,
  draftPayload: AccountDraftTestAccountPayload
): void {
  const model = draftHealthCheckModel(draftPayload)
  openSavedDraftTestModalWithHealthCheckModel(
    account,
    draftPayload,
    model,
    accountTestEndpointModesForModel(
      account,
      draftPayload,
      providerModelOptions.value.find((option) => option.value === model)
    )
  )
}
function draftHealthCheckModel(draftPayload: AccountDraftTestAccountPayload): string {
  const value = (draftPayload as AccountDraftTestAccountPayload & { healthCheckModel?: unknown }).healthCheckModel
  return typeof value === 'string' ? value.trim() : ''
}
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
  mappingCurrentProviderSourceModelOptions,
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
  batchSetStatus
} = useAccountBatchActions({
  accountScopeParams,
  clearSelection,
  isManagementView,
  loadData,
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
  openTrafficMigration,
  updateLoadedAccount
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

function openBatchDisableConfirm(): void {
  if (!batchDisableTargets.value.length) {
    message.warning('所选账户里没有可停用的账户')
    return
  }
  batchDisableConfirmOpen.value = true
}

async function confirmBatchDisable(): Promise<void> {
  if (batchDisableConfirmLoading.value) return
  batchDisableConfirmLoading.value = true
  try {
    await batchSetStatus('disabled')
    batchDisableConfirmOpen.value = false
  } finally {
    batchDisableConfirmLoading.value = false
  }
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

function openBatchEdit(): void {
  if (batchEditDisabled.value) {
    message.warning(batchEditDisabledReason.value)
    return
  }
  batchEditOpen.value = true
}

async function handleBatchEditSaved(): Promise<void> {
  clearSelection()
  await loadData({ forceOptions: true })
}

onMounted(() => {
  void loadData()
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

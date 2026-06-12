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

    <a-modal
      v-model:open="batchDeleteConfirmOpen"
      title="确认批量删除账户"
      ok-text="删除"
      cancel-text="取消"
      :confirm-loading="batchDeleteConfirmLoading"
      :ok-button-props="{ danger: true, disabled: !batchDeleteTargets.length }"
      @cancel="closeBatchDeleteConfirm"
      @ok="confirmBatchDelete"
    >
      <div class="batch-delete-confirm">
        <p class="batch-delete-summary">将删除以下 {{ batchDeleteTargets.length }} 个账户：</p>
        <div class="batch-delete-list">
          <div v-for="account in batchDeleteTargets" :key="account.id" class="batch-delete-item">
            <span class="batch-delete-name">{{ accountDisplayName(account) }}</span>
            <span class="batch-delete-meta">
              <span>{{ providerName(account.providerCode) }}</span>
              <span v-if="isManagementView && account.systemAccountName">系统账户：{{ account.systemAccountName }}</span>
            </span>
          </div>
        </div>
      </div>
    </a-modal>

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
      @column-resize="handleAccountColumnResize"
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
      v-model:client-compatibility="testForm.clientCompatibility"
      v-model:model="testForm.model"
      :account="testingAccount"
      :accounts="batchTestingAccounts"
      :batch-items="batchTestItems"
      :mode="testMode"
      :model-options="testModelOptions"
      :models-loading="testModelsLoading"
      :provider-name="providerName"
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
      v-model:stream-intercept-rules="accountStreamInterceptRules"
      :account-type-choices="accountTypeChoices"
      :authorized-editing="editingAuthorizedAccount"
      :auth-loading="authLoading"
      :auth-result="authResult"
      :base-url-placeholder="selectedProtocolProfile?.baseUrl || selectedProvider?.baseUrl || 'https://api.openai.com/v1'"
      :confirm-loading="modalConfirmLoading"
      :credential-title="selectedAccountTypeTitle"
      :cloning="Boolean(cloningSourceId)"
      :editing="Boolean(editingId)"
      :account-detail="editingAccountDetail"
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
      :mapping-target-model-options="mappingTargetModelOptions"
      :model-options="providerModelOptions"
      :models-loading="providerModelsLoading"
      :ok-button-props="modalOkButtonProps"
      :providers="availableProviders"
      :proxy-options="proxyOptions"
      :selected-protocol-profile="selectedProtocolProfile"
      :selected-provider="selectedProvider"
      :test-button-disabled="accountEditTestButtonDisabled"
      :test-loading="testRunning"
      :title="modalTitle"
      :target-system-account-label="targetSystemAccountLabel"
      @cancel="handleModalCancel"
      @copy-auth-url="copyText"
      @delete-tag="deleteAccountTag"
      @generate-auth-url="generateOAuthUrl"
      @group-options-dropdown="handleGroupOptionsDropdown"
      @group-options-search="handleGroupOptionsSearch"
      @ok="saveAccount"
      @open-auth-url="openAuthUrl"
      @select-provider="selectProvider"
      @test="testAccountFromEditModal"
      @select-type="selectAccountType"
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

import { api, type AccountExportFilters } from '@/api/client'
import TableColumnManager from '@/components/TableColumnManager.vue'
import { useTableColumnSettings } from '@/components/tableColumnSettings'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import { groupLabelForId, rememberGroupLabel } from '@/shared/groupLabelCache'
import type { AccountExportResult, AccountSummary, AccountTagSummary } from '@/types/domain'
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
  statusOptions
} from './accountOptions'
import {
  accountDisplayName
} from './accountFormatters'
import {
  accountSelectionColumnWidth,
  accountTableScrollX,
  accountTableScrollY,
  buildAccountTableColumns,
  accountColumnSortOrder as resolveAccountColumnSortOrder,
} from './accountTableColumns'
import {
  accountMenuItems,
  canCloneAccount,
  canBatchDeleteAccount,
  canDeleteAccount,
  canEditAccount,
  canSelectAccountForBatch
} from './accountRules'
import {
  buildAccountDraftTestPayload,
  buildAccountDraftTestSummary,
  validateAccountDraftTestForm
} from './accountDraftTestPayload'
import { useAccountBatchActions } from './useAccountBatchActions'
import { useAccountEditForm } from './useAccountEditForm'
import { useAccountGroupOptions } from './useAccountGroupOptions'
import { useAccountListData } from './useAccountListData'
import { useAccountMenuActions } from './useAccountMenuActions'
import { accountOperationScopeParams, accountOperationSystemAccountId } from './accountOperationScope'
import { useAccountReauthorize } from './useAccountReauthorize'
import { useAccountTestModal, type SuccessfulDraftActivationTest } from './useAccountTestModal'
import { useAccountTrafficMigration } from './useAccountTrafficMigration'

const AccountEditModal = defineAsyncComponent(() => import('./AccountEditModal.vue'))
const AccountImportModal = defineAsyncComponent(() => import('./AccountImportModal.vue'))
const AccountReauthorizeModal = defineAsyncComponent(() => import('./AccountReauthorizeModal.vue'))
const AccountTestModal = defineAsyncComponent(() => import('./AccountTestModal.vue'))
const AccountTrafficMigrationModal = defineAsyncComponent(() => import('./AccountTrafficMigrationModal.vue'))

const selectedAccountIds = ref<string[]>([])
const importModalOpen = ref(false)
const exportLoading = ref(false)
const filterAccountTagOptions = ref<AccountTagSummary[]>([])
const filterAccountTagOptionsLoading = ref(false)
const filterAccountTagOptionsScopeKey = ref('')
let filterAccountTagOptionsRequestToken = 0
const ACCOUNT_EXPORT_MAX_ACCOUNTS = 50
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
    if (!filters.groupId || !ids.includes(filters.groupId)) return
    filters.groupId = ''
    filters.group = undefined
    applyFilters()
  },
  scope: () => ({
    providerCode: filters.providerCode !== 'all' ? filters.providerCode : undefined,
    systemAccountId: isManagementView.value ? accountScopeParams.value?.systemAccountId : undefined,
    selectedIds: [filters.groupId]
  })
})
const tagFilterDisabled = computed(() => isManagementView.value && !accountScopeParams.value?.systemAccountId)

function handleAccountListLoaded(selectableAccountIds: Set<string>) {
  selectedAccountIds.value = selectedAccountIds.value.filter((id) => selectableAccountIds.has(id))
  rememberAccountGroupLabels(accounts.value)
  syncFilterGroupSelection()
  if (modalOpen.value && !editingId.value) {
    ensureDefaultGroupSelected()
  }
}

async function loadData(options?: { append?: boolean; quiet?: boolean; forceOptions?: boolean }) {
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
  updateColumnWidth,
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

function handleAccountColumnResize(payload: { key: string; width: number }): void {
  updateColumnWidth(payload.key, payload.width)
}

function tableChangeAction(value: unknown): string | undefined {
  return value && typeof value === 'object' && typeof (value as { action?: unknown }).action === 'string'
    ? (value as { action: string }).action
    : undefined
}

const accountById = computed(() => accountByIdMap(accounts.value))
const selectedAccountIdSet = computed(() => new Set(selectedAccountIds.value))
const selectedAccounts = computed(() => accounts.value.filter((account) => selectedAccountIdSet.value.has(account.id)))
const selectedDeletableAccountCount = computed(() => selectedAccounts.value.filter(canBatchDeleteAccount).length)
const batchDeleteConfirmOpen = ref(false)
const batchDeleteConfirmLoading = ref(false)
const batchDeleteTargets = ref<AccountSummary[]>([])
const groupOptionProviderCode = ref('')
const groupOptionSystemAccountId = ref('')
const selectedGroupIds = ref<Array<string | undefined>>([])
const successfulDraftActivationTest = ref<SuccessfulDraftActivationTest>()
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
    systemAccountId: isManagementView.value ? groupOptionSystemAccountId.value || accountScopeParams.value?.systemAccountId : undefined,
    selectedIds: selectedGroupIds.value
  })
})
const {
  accountErrorPolicyRules,
  accountStreamInterceptRules,
  accountTagOptions,
  accountTagOptionsLoading,
  accountTypeChoices,
  authLoading,
  authResult,
  availableProviders,
  cloningSourceId,
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
  mappingTargetModelOptions,
  modalConfirmLoading,
  modalOkButtonProps,
  modalOpen,
  modalTitle,
  openAuthUrl,
  openClone,
  openCreate,
  openEdit,
  providerName,
  providerModelOptions,
  providerModelsLoading,
  saveAccount,
  selectAccountType,
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
  systemAccountSelection: computed(() => filters.systemAccount),
  systemAccounts,
  successfulDraftActivationTest
})
const {
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
  testingAccount
} = useAccountTestModal({
  accountScopeParams,
  clearSelection,
  isManagementView,
  loadData,
  providers: availableProviders,
  successfulDraftActivationTest
})
const accountEditTestButtonDisabled = computed(() => modalConfirmLoading.value || testRunning.value || !hasAccountType.value)
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
  [
    () => form.providerCode,
    () => form.groupId,
    () => createScopeParams.value?.systemAccountId,
    () => editingId.value,
    () => accountScopeParams.value?.systemAccountId
  ],
  () => {
    const activeAccount = editingId.value
      ? accountById.value.get(editingId.value)
      : undefined
    groupOptionProviderCode.value = form.providerCode
    groupOptionSystemAccountId.value = isManagementView.value
      ? accountOperationSystemAccountId(activeAccount, createScopeParams.value) ?? ''
      : ''
    selectedGroupIds.value = [form.groupId]
  },
  { immediate: true }
)
watch([groupOptionProviderCode, groupOptionSystemAccountId], () => {
  resetGroupOptionsSearch()
})
const {
  batchDeleteSelected,
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
const proxyByIdMapRef = computed(() => proxyByIdMap(proxies.value))

const rowSelection = computed(() => ({
  columnWidth: accountSelectionColumnWidth,
  fixed: true,
  selectedRowKeys: selectedAccountIds.value,
  onChange: (selectedRowKeys: Array<string | number>) => {
    selectedAccountIds.value = selectedRowKeys.map((key) => String(key))
  },
  getCheckboxProps: (account: AccountSummary) => ({ disabled: !canSelectAccountForBatch(account) })
}))

function isAccountSelected(accountId: string): boolean {
  return selectedAccountIdSet.value.has(accountId)
}

function toggleAccountSelection(account: AccountSummary) {
  if (!canSelectAccountForBatch(account)) return
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
  const account = accountById.value.get(accountId)
  if (!account) return undefined
  return account.boundGroupName ?? groupLabelForId(account.boundGroupId)
}

function rememberAccountGroupLabels(items: AccountSummary[]): void {
  for (const account of items) {
    rememberGroupLabel(account.boundGroupId, account.boundGroupName)
  }
}

function syncFilterGroupSelection(): void {
  if (!filters.groupId) {
    filters.group = undefined
    return
  }
  const group = filterGroupOptions.value.find((item) => item.id === filters.groupId)
  if (group) {
    filters.group = { id: group.id, name: group.name }
    return
  }
  const account = accounts.value.find((item) => item.boundGroupId === filters.groupId && item.boundGroupName)
  if (account?.boundGroupName) {
    filters.group = { id: filters.groupId, name: account.boundGroupName }
  }
}

function handleProviderFilterChange(value: string): void {
  filters.providerCode = value || 'all'
  if (filters.providerCode !== 'all') {
    filters.groupId = ''
    filters.group = undefined
  }
  const provider = filters.providerCode === 'all'
    ? undefined
    : availableProviders.value.find((item) => item.code === filters.providerCode)
  const providerAccountTypes = provider?.protocolProfiles.length
    ? provider.protocolProfiles.flatMap((profile) => profile.accountTypes)
    : provider?.accountTypes ?? []
  if (provider && filters.type !== 'all' && !providerAccountTypes.includes(filters.type)) {
    filters.type = 'all'
  }
  resetFilterGroupOptionsSearch()
  applyFilters()
}

function handleAccountTypeFilterChange(value: string): void {
  filters.type = value || 'all'
  applyFilters()
}

function currentFilterAccountTagScopeKey(): string | undefined {
  if (!isManagementView.value) return 'self'
  const systemAccountId = accountScopeParams.value?.systemAccountId
  return systemAccountId ? `management:${systemAccountId}` : undefined
}

async function loadFilterAccountTagOptions(force = false): Promise<void> {
  const scopeKey = currentFilterAccountTagScopeKey()
  if (!scopeKey) {
    resetFilterAccountTagOptions()
    return
  }
  if (!force && filterAccountTagOptionsScopeKey.value === scopeKey) return
  const requestToken = ++filterAccountTagOptionsRequestToken
  const scopeParams = accountScopeParams.value
  filterAccountTagOptionsLoading.value = true
  try {
    const nextOptions = isManagementView.value
      ? await api.accounts.tags(scopeParams)
      : await api.myAccounts.tags()
    if (requestToken !== filterAccountTagOptionsRequestToken || currentFilterAccountTagScopeKey() !== scopeKey) return
    filterAccountTagOptions.value = nextOptions
    filterAccountTagOptionsScopeKey.value = scopeKey
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载账户标签失败'))
  } finally {
    if (requestToken === filterAccountTagOptionsRequestToken) {
      filterAccountTagOptionsLoading.value = false
    }
  }
}

function resetFilterAccountTagOptions(): void {
  filterAccountTagOptionsRequestToken += 1
  filterAccountTagOptions.value = []
  filterAccountTagOptionsScopeKey.value = ''
  filterAccountTagOptionsLoading.value = false
}

function handleFilterAccountTagDropdown(open: boolean): void {
  if (open) void loadFilterAccountTagOptions(true)
}

async function copyText(value: string) {
  await copyTextToClipboard(value)
}

async function testAccountFromEditModal() {
  if (editingAuthorizedAccount.value) {
    if (!editingAccountDetail.value) {
      message.warning('请选择要测试的授权账户')
      return
    }
    if (form.groupId && form.groupId !== editingAccountDetail.value.boundGroupId) {
      message.info('授权账户测试使用当前已保存的分组绑定，保存后新分组才会生效')
    }
    await openTestModal(editingAccountDetail.value)
    return
  }

  const validationMessage = validateAccountDraftTestForm({
    accounts: accounts.value,
    accountDetail: editingAccountDetail.value,
    editingId: editingId.value,
    form,
    hasAuthSession: Boolean(authResult.value?.sessionId),
    errorPolicyRules: accountErrorPolicyRules.value,
    streamInterceptRules: accountStreamInterceptRules.value
  })
  if (validationMessage) {
    message.warning(validationMessage)
    return
  }

  try {
    const draftPayload = buildAccountDraftTestPayload({
      accounts: accounts.value,
      accountDetail: editingAccountDetail.value,
      editingId: editingId.value,
      form,
      errorPolicyRules: accountErrorPolicyRules.value,
      streamInterceptRules: accountStreamInterceptRules.value
    })
    if (!draftPayload.groupId) {
      message.warning('请选择加入分组')
      return
    }
    if (form.group?.id === draftPayload.groupId && form.group.name) {
      rememberGroupLabel(form.group.id, form.group.name)
    }
    const draftAccount = buildAccountDraftTestSummary({
      accountDetail: editingAccountDetail.value,
      draftPayload,
      protocolProfile: selectedProtocolProfile.value,
      scopeSystemAccountId: draftTestScopeSystemAccountId()
    })
    if (form.group?.id === draftPayload.groupId && form.group.name) {
      draftAccount.boundGroupName = form.group.name
    }
    if (editingId.value && editingAccountDetail.value) {
      await openSavedDraftTestModal(draftAccount, draftPayload)
    } else {
      await openDraftTestModal(draftAccount, draftPayload)
    }
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '生成账户测试草稿失败'))
  }
}

function draftTestScopeSystemAccountId(): string | undefined {
  if (editingAccountDetail.value) {
    return accountOperationScopeParams(editingAccountDetail.value, accountScopeParams.value)?.systemAccountId
  }
  return createScopeParams.value?.systemAccountId ?? accountScopeParams.value?.systemAccountId
}

function resetFilters() {
  selectedAccountIds.value = []
  resetGroupOptionsSearch()
  resetFilterGroupOptionsSearch()
  resetFilterAccountTagOptions()
  resetAccountListFilters()
}

function handleSystemAccountFilterChange() {
  selectedAccountIds.value = []
  filters.groupId = ''
  filters.group = undefined
  filters.tagIds = []
  if (filters.systemAccountId === allSystemAccountsValue) {
    filters.systemAccount = undefined
  }
  resetGroupOptionsSearch()
  resetFilterGroupOptionsSearch()
  resetFilterAccountTagOptions()
  handleAccountListSystemAccountFilterChange()
}

function clearSelection() {
  selectedAccountIds.value = []
  closeBatchDeleteConfirm()
}

function openBatchDeleteConfirm() {
  const targets = selectedAccounts.value.filter(canBatchDeleteAccount)
  if (!targets.length) {
    message.warning('所选账户里没有可删除的自有账户')
    return
  }
  if (targets.length !== selectedAccounts.value.length) {
    message.warning('已跳过授权账户或无权删除的账户')
  }
  batchDeleteTargets.value = [...targets]
  batchDeleteConfirmOpen.value = true
}

function closeBatchDeleteConfirm() {
  if (batchDeleteConfirmLoading.value) return
  batchDeleteConfirmOpen.value = false
  batchDeleteTargets.value = []
}

async function confirmBatchDelete() {
  const targets = [...batchDeleteTargets.value]
  if (!targets.length) return
  batchDeleteConfirmLoading.value = true
  try {
    await batchDeleteSelected(targets)
    batchDeleteConfirmOpen.value = false
    batchDeleteTargets.value = []
  } finally {
    batchDeleteConfirmLoading.value = false
  }
}

function openImportModal() {
  if (isManagementView.value && !accountScopeParams.value?.systemAccountId) {
    message.warning('请先在右侧选择目标系统账户，再导入 AI 账户')
    return
  }
  importModalOpen.value = true
}

async function exportFilteredAccounts() {
  exportLoading.value = true
  try {
    const payload = { filters: currentAccountExportFilters() }
    const result = isManagementView.value
      ? await api.accounts.export(payload, accountScopeParams.value)
      : await api.myAccounts.export(payload)
    downloadJsonFile(accountExportFilename(result.summary.accounts), result.document)
    message.success(accountFilterExportSuccessMessage(result.summary))
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '导出账户失败'))
  } finally {
    exportLoading.value = false
  }
}

async function exportAccounts() {
  if (selectedAccounts.value.length) {
    await exportAccountsByIds(selectedAccounts.value)
    return
  }
  await exportFilteredAccounts()
}

async function exportAccountsByIds(sourceAccounts: AccountSummary[]) {
  const exportableAccounts = sourceAccounts.filter(canExportAccount)
  if (!exportableAccounts.length) {
    message.warning('所选账户没有可导出的自有 AI 账户')
    return
  }
  if (exportableAccounts.length > ACCOUNT_EXPORT_MAX_ACCOUNTS) {
    message.warning(`单次最多导出 ${ACCOUNT_EXPORT_MAX_ACCOUNTS} 个账户，请先筛选或勾选部分账户`)
    return
  }
  exportLoading.value = true
  try {
    const payload = { accountIds: exportableAccounts.map((account) => account.id) }
    const result = isManagementView.value
      ? await api.accounts.export(payload, accountScopeParams.value)
      : await api.myAccounts.export(payload)
    downloadJsonFile(accountExportFilename(result.summary.accounts), result.document)
    const skippedSelectedCount = sourceAccounts.length - exportableAccounts.length
    const skippedCount = (result.summary.skippedAccounts ?? 0) + skippedSelectedCount
    const skippedText = skippedCount ? `，跳过 ${skippedCount} 个不可导出账户` : ''
    message.success(`已导出 ${result.summary.accounts} 个账户${skippedText}`)
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '导出账户失败'))
  } finally {
    exportLoading.value = false
  }
}

function currentAccountExportFilters(): AccountExportFilters {
  return {
    sorts: accountSorts.value,
    keyword: filters.keyword.trim() || undefined,
    providerCode: filters.providerCode && filters.providerCode !== 'all' ? filters.providerCode : undefined,
    type: filters.type && filters.type !== 'all' ? filters.type : undefined,
    groupId: filters.groupId || undefined,
    tagIds: filters.tagIds.length ? filters.tagIds : undefined,
    status: filters.status.length ? filters.status : undefined
  }
}

function accountFilterExportSuccessMessage(summary: AccountExportResult['summary']): string {
  const matchedText = typeof summary.matchedAccounts === 'number' ? `，匹配 ${summary.matchedAccounts} 个` : ''
  const skippedText = summary.skippedAccounts ? `，跳过 ${summary.skippedAccounts} 个不可导出账户` : ''
  const truncatedText = summary.truncated ? `，仅处理前 ${ACCOUNT_EXPORT_MAX_ACCOUNTS} 条匹配结果` : ''
  return `已按当前筛选导出 ${summary.accounts} 个账户${matchedText}${skippedText}${truncatedText}`
}

function canExportAccount(account: AccountSummary): boolean {
  return account.accessType !== 'authorized'
    && account.permissions?.canViewCredentials !== false
    && account.permissions?.canEdit !== false
}

function accountExportFilename(accountCount: number): string {
  const target = exportTargetSystemAccountLabel()
  const safeTarget = target.replace(/[<>:"/\\|?*\u0000-\u001f]+/g, '-').replace(/\s+/g, '-').replace(/-+/g, '-').replace(/^-|-$/g, '')
  const timestamp = new Date().toISOString().replace(/[:.]/g, '-')
  return `${safeTarget || 'AI账户'}-${accountCount}个账户-${timestamp}.json`
}

function exportTargetSystemAccountLabel(): string {
  const selectedId = accountScopeParams.value?.systemAccountId
  if (!selectedId) return 'AI账户'
  if (filters.systemAccount?.id === selectedId && filters.systemAccount.name) return filters.systemAccount.name
  const account = systemAccounts.value.find((item) => item.id === selectedId)
  return account?.displayName || filters.systemAccount?.name || 'AI账户'
}

function downloadJsonFile(filename: string, data: unknown): void {
  const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  document.body.appendChild(link)
  link.click()
  document.body.removeChild(link)
  URL.revokeObjectURL(url)
}

async function handleImportCompleted() {
  selectedAccountIds.value = []
  await loadData({ forceOptions: true })
}

async function removeLoadedRemovedAccount(id: string): Promise<void> {
  removeLoadedAccount(id)
  selectedAccountIds.value = selectedAccountIds.value.filter((selectedId) => selectedId !== id)
  void loadData({ quiet: true })
}

async function returnAuthorizationAccount(id: string) {
  const account = accountById.value.get(id)
  if (!account || account.accessType !== 'authorized') {
    message.warning('只有授权账户可以归还')
    return
  }
  try {
    if (isManagementView.value) {
      await api.accounts.returnAuthorization(id, accountOperationScopeParams(account, accountScopeParams.value))
    } else {
      await api.myAccounts.returnAuthorization(id)
    }
    await removeLoadedRemovedAccount(id)
    message.success('授权账户已归还')
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '归还授权账户失败'))
  }
}

async function removeAccount(id: string) {
  const account = accountById.value.get(id)
  if (account?.accessType === 'authorized') {
    message.warning('授权账户请使用归还操作')
    return
  }
  try {
    if (isManagementView.value) {
      await api.accounts.delete(id, accountScopeParams.value)
    } else {
      await api.myAccounts.delete(id)
    }
    await removeLoadedRemovedAccount(id)
    message.success('账户已删除，关联记录将在一个月后由后台物理清理')
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '删除账户失败'))
  }
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

.batch-delete-confirm {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.batch-delete-summary {
  margin: 0;
  color: #0f172a;
}

.batch-delete-list {
  max-height: 320px;
  overflow: auto;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
}

.batch-delete-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 12px;
  border-bottom: 1px solid #eef2f7;
}

.batch-delete-item:last-child {
  border-bottom: 0;
}

.batch-delete-name {
  min-width: 0;
  overflow: hidden;
  color: #0f172a;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.batch-delete-meta {
  display: flex;
  flex-shrink: 0;
  gap: 8px;
  color: #64748b;
  font-size: 12px;
  white-space: nowrap;
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

  .batch-delete-item {
    align-items: flex-start;
    flex-direction: column;
  }

  .batch-delete-meta {
    flex-wrap: wrap;
    white-space: normal;
  }
}
</style>

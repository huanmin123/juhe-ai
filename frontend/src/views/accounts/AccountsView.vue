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
import { computed, onMounted, reactive, ref, watch } from 'vue'

import { api } from '@/api/client'
import type { AccountListSortParam } from '@/api/client'
import type { ResponsiveDataListSort } from '@/components/responsiveDataListSorting'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import type { AccountStatus, AccountSummary, AccountTestResult, AccountTrafficMigrationSourceStatus, AccountType, GroupSummary, OpenAIAuthURLResult, ProviderDefinition, ProviderModelPricing, ProxyProfileOptionSummary, SystemAccountSummary } from '@/types/domain'
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
  validateAccountErrorPolicyRules,
  type AccountErrorPolicyRuleForm
} from './accountErrorPolicy'
import { buildAccountCredentials, currentAccountCredentials } from './accountCredentials'
import { defaultAccountForm } from './accountFormDefaults'
import {
  accountTypeDescription,
  accountTypeText,
  accountTypeTitle as buildAccountTypeTitle,
  asString,
  formatServerDateTimeInput,
  isAuthorizedAccount,
  isOwnerDisabledAuthorizedAccount,
  isTemporaryAccountStatus,
  parseDatePickerValue,
} from './accountFormatters'
import type { AccountMenuItem } from './accountActionTypes'
import type { AccountFilters, AccountFormModel } from './accountFormTypes'
import {
  ACCOUNT_PAGE_SIZE,
  FALLBACK_PROVIDER,
  defaultTestModelOptions,
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
  normalizeAccountTableSorts
} from './accountTableColumns'
import { countActiveAccountFilters } from './accountListFilters'
import { useAccountMobilePagination } from './useAccountMobilePagination'

const loading = ref(false)
const saving = ref(false)
const authLoading = ref(false)
const testModalOpen = ref(false)
const testRunning = ref(false)
const testModelsLoading = ref(false)
const modalOpen = ref(false)
const bindGroupModalOpen = ref(false)
const trafficMigrationModalOpen = ref(false)
const reauthorizeModalOpen = ref(false)
const bindGroupSaving = ref(false)
const trafficMigrationSaving = ref(false)
const tokenRefreshLoading = ref(false)
const reauthorizeAuthLoading = ref(false)
const reauthorizeSaving = ref(false)
const authResult = ref<OpenAIAuthURLResult>()
const reauthorizeAuthResult = ref<OpenAIAuthURLResult>()
const editingId = ref<string>()
const testingAccount = ref<AccountSummary>()
const bindingAccount = ref<AccountSummary>()
const trafficMigrationSourceAccount = ref<AccountSummary>()
const reauthorizingAccount = ref<AccountSummary>()
const testResult = ref<AccountTestResult>()
const selectedAccountIds = ref<string[]>([])
const accountOptionsLoaded = ref(false)
const accountOptionsScopeKey = ref('')
let accountTestAbortController: AbortController | undefined
type AccountsPageState = {
  filters: AccountFilters
  pagination: { current: number; pageSize: number }
  sorts: AccountListSortParam[]
}
const defaultAccountsPageState = (): AccountsPageState => ({
  filters: { keyword: '', type: 'all', status: 'all', schedulable: 'all', systemAccountId: allSystemAccountsValue },
  pagination: { current: 1, pageSize: ACCOUNT_PAGE_SIZE },
  sorts: [{ field: 'priority', order: 'asc' }]
})
const pageStateCache = usePageStateCache<AccountsPageState>(undefined, defaultAccountsPageState)
const initialPageState = pageStateCache.read()
const accountSorts = ref<AccountListSortParam[]>(initialPageState.sorts)
const accounts = ref<AccountSummary[]>([])
const providers = ref<ProviderDefinition[]>([])
const providerModels = ref<ProviderModelPricing[]>([])
const proxies = ref<ProxyProfileOptionSummary[]>([])
const groups = ref<GroupSummary[]>([])
const systemAccounts = ref<SystemAccountSummary[]>([])
const filters = reactive<AccountFilters>({ ...initialPageState.filters })
const accountPagination = reactive({ current: initialPageState.pagination.current, pageSize: initialPageState.pagination.pageSize, total: 0 })
const testForm = reactive({ model: 'gpt-5.5', prompt: 'hi' })
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()

const form = reactive<AccountFormModel>(defaultForm())
const bindGroupForm = reactive({ groupId: '' })
const trafficMigrationForm = reactive({
  targetAccountId: '',
  sourceStatus: 'temporary_unavailable' as AccountTrafficMigrationSourceStatus
})
const reauthorizeForm = reactive({
  oauthMode: 'manual' as 'manual' | 'refresh_token',
  callbackUrl: '',
  refreshToken: ''
})
const accountErrorPolicyRules = ref<AccountErrorPolicyRuleForm[]>(loadAccountErrorPolicyRules())

const currentEditingAccount = computed(() => editingId.value ? accounts.value.find((account) => account.id === editingId.value) : undefined)

const statusEditOptions = computed(() => {
  const options = statusOptions.filter((item) => item.value !== 'all')
  if (currentEditingAccount.value && isTemporaryAccountStatus(currentEditingAccount.value)) {
    return options.filter((item) => item.value !== 'active')
  }
  return options
})

const columns = computed(() => buildAccountTableColumns(isManagementView.value, (field) => resolveAccountColumnSortOrder(accountSorts.value, field)))
const tableScrollX = computed(() => accountTableScrollX(isManagementView.value))
const tableScrollY = computed(accountTableScrollY)

const filteredAccounts = computed(() => accounts.value)

const {
  mobileHasMore: mobileHasMoreAccounts,
  mobileLoadingMore,
  mobileRefreshing,
  tablePagination: accountTablePagination,
  handleTableChange: handleAccountTableChange,
  loadMoreMobile: loadMoreMobileAccounts,
  refreshMobile: refreshMobileAccounts,
  resetPagination: resetAccountListPagination
} = useAccountMobilePagination(ACCOUNT_PAGE_SIZE, () => accountPagination.total, loadData, accountPagination)
const mobileVisibleAccounts = computed(() => filteredAccounts.value)

const activeAdvancedFilterCount = computed(() => countActiveAccountFilters(filters, isManagementView.value, allSystemAccountsValue))
const accountScopeParams = computed(() => {
  const systemAccountId = scopedSystemAccountId(filters.systemAccountId)
  return systemAccountId ? { systemAccountId } : undefined
})
const targetSystemAccountLabel = computed(() => {
  if (!isManagementView.value) return undefined
  const systemAccountId = accountScopeParams.value?.systemAccountId
  if (!systemAccountId) return '请选择系统账户后再创建'
  return systemAccounts.value.find((account) => account.id === systemAccountId)?.displayName || systemAccounts.value.find((account) => account.id === systemAccountId)?.username || systemAccountId
})

const testModelOptions = computed(() => {
  const models = providerModels.value.length ? providerModels.value.map((item) => item.model) : defaultTestModelOptions
  return [...new Set(models)].map((model) => ({ label: model, value: model }))
})
const defaultTestModel = computed(() => testModelOptions.value[0]?.value || 'gpt-5.5')

const selectedAccounts = computed(() => accounts.value.filter((account) => selectedAccountIds.value.includes(account.id)))

const rowSelection = computed(() => ({
  columnWidth: accountSelectionColumnWidth,
  selectedRowKeys: selectedAccountIds.value,
  onChange: (selectedRowKeys: Array<string | number>) => {
    selectedAccountIds.value = selectedRowKeys.map((key) => String(key))
  },
  getCheckboxProps: (account: AccountSummary) => ({ disabled: !canEditAccount(account) })
}))

function isAccountSelected(accountId: string): boolean {
  return selectedAccountIds.value.includes(accountId)
}

function toggleAccountSelection(account: AccountSummary) {
  if (!canEditAccount(account)) return
  selectedAccountIds.value = isAccountSelected(account.id)
    ? selectedAccountIds.value.filter((id) => id !== account.id)
    : [...selectedAccountIds.value, account.id]
}

const proxyOptions = computed(() => proxies.value.map((proxy) => ({
  label: `${proxy.name}（${proxy.type}${proxy.enabled === false ? '，已停用' : ''}）`,
  value: proxy.id,
  disabled: proxy.enabled === false
})))
const proxyById = (proxyProfileId?: string) => proxies.value.find((proxy) => proxy.id === proxyProfileId)
const providerGroups = computed(() => groups.value.filter((group) => canManageGroupAccounts(group) && (!form.providerCode || group.providerCode === form.providerCode)))
const groupOptions = computed(() => providerGroups.value.map((group) => ({ label: group.name, value: group.id })))
const bindGroupOptions = computed(() => {
  const account = bindingAccount.value
  if (!account) return []
  return groups.value
    .filter((group) => canManageGroupAccounts(group) && group.providerCode === account.providerCode)
    .map((group) => ({ label: group.name, value: group.id }))
})
const bindGroupTip = computed(() => {
  const ownerName = bindingAccount.value?.ownerSystemAccountName || '其他用户'
  return `授权账户来自 ${ownerName}。绑定到你的同供应商分组后，对应 API Key 才能调度使用。`
})
const trafficMigrationTargetOptions = computed(() => {
  const source = trafficMigrationSourceAccount.value
  if (!source) return []
  return accounts.value
    .filter((account) => canUseAsTrafficMigrationTarget(source, account))
    .map((account) => {
      const groupName = groupNameForAccount(account.id)
      return {
        label: groupName ? `${account.name}（${groupName}）` : account.name,
        value: account.id
      }
    })
})
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
  disabled: !hasAccountType.value || (!editingId.value && isOAuthForm.value && !isOpenAIOAuthForm.value)
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
  return availableProviders.value.find((provider) => provider.code === providerCode)?.name ?? providerCode
}

function groupIdForAccount(accountId: string) {
  const account = accounts.value.find((item) => item.id === accountId)
  return account?.boundGroupId ?? groups.value.find((group) => group.accountIds.includes(accountId))?.id
}

function groupNameForAccount(accountId: string) {
  const account = accounts.value.find((item) => item.id === accountId)
  return account?.boundGroupName ?? groups.value.find((group) => group.accountIds.includes(accountId))?.name
}

function authorizedAccountTooltip(account: AccountSummary): string {
  const ownerName = account.ownerSystemAccountName || '其他用户'
  if (isOwnerDisabledAuthorizedAccount(account)) {
    return `授权自 ${ownerName}。账户所有者已停用该账户，你暂时无法启用或调用；请联系对方启用后再使用。`
  }
  return `授权自 ${ownerName}，仅可使用`
}

function canEditAccount(account: AccountSummary): boolean {
  return account.permissions?.canEdit !== false
}

function canDeleteAccount(account: AccountSummary): boolean {
  return account.permissions?.canDelete !== false
}

function canUseAccountActions(account: AccountSummary): boolean {
  return canEditAccount(account) && account.permissions?.canViewCredentials !== false
}

function canTestAccount(account: AccountSummary): boolean {
  return account.permissions?.canUse !== false
}

function canManageGroupAccounts(group: GroupSummary): boolean {
  return group.permissions?.canManageAccounts !== false && group.accessType !== 'authorized'
}

function canUseAsTrafficMigrationTarget(source: AccountSummary, target: AccountSummary): boolean {
  if (target.id === source.id) return false
  if (!canEditAccount(target)) return false
  if (target.providerCode !== source.providerCode) return false
  if (target.ownerSystemAccountId !== source.ownerSystemAccountId) return false
  if (groupIdForAccount(target.id) !== groupIdForAccount(source.id)) return false
  return target.status === 'active' && target.schedulable && !isTemporaryAccountStatus(target)
}

function canManageOpenAIOAuth(account: AccountSummary): boolean {
  return canUseAccountActions(account) && account.providerCode === 'openai' && account.type === 'oauth'
}

async function handleAccountSortChange(sorts: ResponsiveDataListSort[]) {
  accountSorts.value = normalizeAccountTableSorts(sorts)
  resetAccountPagination()
  await loadData()
}

function defaultGroupForProvider(providerCode: string) {
  const candidates = groups.value.filter((group) => group.providerCode === providerCode && canManageGroupAccounts(group))
  return candidates.find((group) => group.isDefault) ?? candidates[0]
}

function ensureDefaultGroupSelected(providerCode = form.providerCode) {
  if (!providerCode) {
    form.groupId = undefined
    return
  }
  const currentGroup = groups.value.find((group) => group.id === form.groupId)
  if (currentGroup?.providerCode === providerCode && canManageGroupAccounts(currentGroup)) {
    return
  }
  form.groupId = defaultGroupForProvider(providerCode)?.id
}

function accountMenuItems(account: AccountSummary): AccountMenuItem[] {
  const items: AccountMenuItem[] = []
  if (canTestAccount(account)) {
    items.push({ key: 'test', label: '测试' })
  }
  if (canUseAccountActions(account)) {
    if (canManageOpenAIOAuth(account)) {
      items.push({ key: 'refresh-oauth-token', label: '刷新令牌' })
      items.push({ key: 'reauthorize-oauth', label: '重新授权' })
    }
    if (isTemporaryAccountStatus(account)) {
      items.push({ key: 'restore-normal', label: '恢复正常' })
    }
    if (account.status === 'active') {
      items.push({
        key: account.superPriorityEnabled ? 'super-priority-off' : 'super-priority-on',
        label: account.superPriorityEnabled ? '取消超级优先' : '设为超级优先'
      })
    }
    items.push({ key: 'migrate-traffic', label: '迁移流量' })
    items.push({
      key: 'toggle-status',
      label: account.status === 'disabled' ? '启用账户' : '停用账户',
      danger: account.status !== 'disabled',
      icon: account.status === 'disabled' ? 'enable' : 'pause',
      tone: account.status === 'disabled' ? 'success' : 'warning'
    })
  }
  return items.map(normalizeAccountMenuItem)
}

function normalizeAccountMenuItem(item: AccountMenuItem): AccountMenuItem {
  if (item.icon || item.tone) return item
  if (item.key === 'test') return { ...item, icon: 'test', tone: 'info' }
  if (item.key === 'refresh-oauth-token') return { ...item, icon: 'refresh', tone: 'info' }
  if (item.key === 'reauthorize-oauth') return { ...item, icon: 'reset', tone: 'warning' }
  if (item.key === 'restore-normal') return { ...item, icon: 'restore', tone: 'success' }
  if (item.key === 'super-priority-on') return { ...item, icon: 'superPriority', tone: 'warning' }
  if (item.key === 'super-priority-off') return { ...item, icon: 'superPriority', tone: 'default' }
  if (item.key === 'migrate-traffic') return { ...item, icon: 'migrate', tone: 'purple' }
  return item
}

async function copyText(value: string) {
  if (!value) return
  await navigator.clipboard.writeText(value)
  message.success('已复制')
}

async function loadData(options: { append?: boolean; quiet?: boolean; forceOptions?: boolean } = {}) {
  if (!options.quiet) {
    loading.value = true
  }
  try {
    const systemAccountId = isManagementView.value ? accountScopeParams.value?.systemAccountId : undefined
    const [accountList] = await Promise.all([
      isManagementView.value ? api.accounts.list(accountListParams(systemAccountId)) : api.myAccounts.list(accountListParams()),
      loadAccountOptions(systemAccountId, options.forceOptions === true)
    ])
    accountPagination.current = accountList.page
    accountPagination.pageSize = accountList.pageSize
    accountPagination.total = accountList.total
    accounts.value = options.append ? [...accounts.value, ...accountList.items] : accountList.items
    selectedAccountIds.value = selectedAccountIds.value.filter((id) => accounts.value.some((account) => account.id === id && canEditAccount(account)))
    if (modalOpen.value && !editingId.value) {
      ensureDefaultGroupSelected()
    }
  } catch (error) {
    console.error(error)
    message.error('加载账户失败')
  } finally {
    if (!options.quiet) {
      loading.value = false
    }
  }
}

async function loadAccountOptions(systemAccountId: string | undefined, force = false): Promise<void> {
  const scopeKey = isManagementView.value ? `management:${systemAccountId ?? 'all'}` : 'self'
  if (!force && accountOptionsLoaded.value && accountOptionsScopeKey.value === scopeKey) {
    return
  }

  const [providerList, proxyList, groupList, systemAccountList] = await Promise.all([
    isManagementView.value ? api.providers.list() : Promise.resolve([] as ProviderDefinition[]),
    api.proxies.options(),
    isManagementView.value ? api.groups.list({ systemAccountId }) : api.myGroups.list(),
    isManagementView.value ? api.systemAccounts.list() : Promise.resolve([] as SystemAccountSummary[])
  ])
  providers.value = providerList.length ? providerList : [FALLBACK_PROVIDER]
  proxies.value = proxyList
  groups.value = groupList
  systemAccounts.value = systemAccountList
  accountOptionsLoaded.value = true
  accountOptionsScopeKey.value = scopeKey
}

function refreshData() {
  void loadData({ forceOptions: true })
}

function applyFilters() {
  filters.keyword = filters.keyword.trim()
  resetAccountPagination()
  void loadData()
}

async function handleAccountTableChangeAndLoad(paginationInfo: unknown): Promise<void> {
  handleAccountTableChange(paginationInfo)
  await loadData()
}

function accountListParams(systemAccountId?: string) {
  return {
    systemAccountId,
    sorts: accountSorts.value,
    page: accountPagination.current,
    pageSize: accountPagination.pageSize,
    keyword: filters.keyword.trim() || undefined,
    type: filters.type,
    status: filters.status,
    schedulable: filters.schedulable
  }
}

function resetAccountPagination() {
  accountPagination.current = 1
  resetAccountListPagination()
}

function resetFilters() {
  const defaults = defaultAccountsPageState()
  Object.assign(filters, defaults.filters)
  accountSorts.value = defaults.sorts
  accountPagination.current = defaults.pagination.current
  accountPagination.pageSize = defaults.pagination.pageSize
  resetAccountListPagination()
  pageStateCache.clear()
  void loadData()
}

function extractApiErrorMessage(error: unknown, fallback: string): string {
  if (axios.isAxiosError<{ message?: string }>(error)) {
    return error.response?.data?.message ?? fallback
  }
  return error instanceof Error ? error.message : fallback
}

function handleSystemAccountFilterChange() {
  selectedAccountIds.value = []
  resetAccountPagination()
  void loadData({ forceOptions: true })
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

function openBindGroup(account: AccountSummary) {
  bindingAccount.value = account
  bindGroupForm.groupId = groupIdForAccount(account.id) ?? defaultBindGroupForAccount(account)?.id ?? ''
  bindGroupModalOpen.value = true
}

function openTrafficMigration(account: AccountSummary) {
  if (!canUseAccountActions(account)) {
    message.warning('授权账户不能迁移流量')
    return
  }
  trafficMigrationSourceAccount.value = account
  trafficMigrationForm.sourceStatus = 'temporary_unavailable'
  const target = accounts.value.find((candidate) => canUseAsTrafficMigrationTarget(account, candidate))
  trafficMigrationForm.targetAccountId = target?.id ?? ''
  trafficMigrationModalOpen.value = true
  if (!target) {
    message.warning('当前没有可迁移到的同供应商可用账户')
  }
}

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

function defaultBindGroupForAccount(account: AccountSummary): GroupSummary | undefined {
  const candidates = groups.value.filter((group) => canManageGroupAccounts(group) && group.providerCode === account.providerCode)
  return candidates.find((group) => group.isDefault) ?? candidates[0]
}

async function saveBindGroup() {
  if (!bindingAccount.value) return
  if (!bindGroupForm.groupId) {
    message.warning('请选择归属分组')
    return
  }
  bindGroupSaving.value = true
  try {
    if (isManagementView.value) {
      await api.accounts.bindGroup(bindingAccount.value.id, { groupId: bindGroupForm.groupId }, accountScopeParams.value)
    } else {
      await api.myAccounts.bindGroup(bindingAccount.value.id, { groupId: bindGroupForm.groupId })
    }
    message.success('授权账户已绑定分组')
    bindGroupModalOpen.value = false
    bindingAccount.value = undefined
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '绑定分组失败'))
  } finally {
    bindGroupSaving.value = false
  }
}

async function saveTrafficMigration() {
  const source = trafficMigrationSourceAccount.value
  if (!source) return
  if (!trafficMigrationForm.targetAccountId) {
    message.warning('请选择目标账户')
    return
  }
  trafficMigrationSaving.value = true
  try {
    const payload = {
      targetAccountId: trafficMigrationForm.targetAccountId,
      sourceStatus: trafficMigrationForm.sourceStatus
    }
    const result = isManagementView.value
      ? await api.accounts.migrateTraffic(source.id, payload, accountScopeParams.value)
      : await api.myAccounts.migrateTraffic(source.id, payload)
    const statusText = result.sourceStatus === 'disabled' ? '停用账户' : '临时不可调用'
    message.success(`后续请求将切到 ${result.targetAccount.name}，当前连接不中断；原账户已设为${statusText}，会话迁移 ${result.migratedSessionCount} 个`)
    trafficMigrationModalOpen.value = false
    trafficMigrationSourceAccount.value = undefined
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '迁移流量失败'))
  } finally {
    trafficMigrationSaving.value = false
  }
}

function buildCredentials() {
  return buildAccountCredentials({
    currentCredentials: currentAccountCredentials(accounts.value, editingId.value),
    errorPolicyRules: accountErrorPolicyRules.value,
    form
  })
}

async function saveAccount() {
  if (!form.providerCode) {
    message.warning('请先选择供应商')
    return
  }
  if (!form.type) {
    message.warning('请先选择账户类型')
    return
  }
  if ((editingId.value || form.type === 'api_key') && !form.name.trim()) {
    message.warning('请填写账户名称')
    return
  }
  if (!form.groupId) {
    message.warning('请选择归属分组')
    return
  }
  if (form.type === 'api_key' && !form.apiKey.trim()) {
    message.warning('请填写 API Key')
    return
  }
  if (form.type === 'api_key' && !form.baseUrl.trim()) {
    message.warning('请填写 Base URL')
    return
  }
  if (editingId.value && form.type === 'oauth' && !form.accessToken.trim() && !form.refreshToken.trim()) {
    message.warning('请至少填写 Access Token 或 Refresh Token')
    return
  }
  if (!editingId.value && form.type === 'oauth' && form.providerCode !== 'openai') {
    message.warning('第一期只支持创建 OpenAI OAuth 账户')
    return
  }
  if (!editingId.value && form.type === 'oauth' && form.oauthMode === 'manual' && !authResult.value?.sessionId) {
    message.warning('请先生成授权链接')
    return
  }
  if (!editingId.value && form.type === 'oauth' && form.oauthMode === 'manual' && !form.callbackUrl.trim()) {
    message.warning('请粘贴回调 URL')
    return
  }
  if (!editingId.value && form.type === 'oauth' && form.oauthMode === 'refresh_token' && !form.refreshToken.trim()) {
    message.warning('请填写 Refresh Token')
    return
  }
  const errorPolicyValidation = validateAccountErrorPolicyRules(accountErrorPolicyRules.value)
  if (!errorPolicyValidation.valid) {
    message.warning(errorPolicyValidation.message || '错误处理策略配置不完整')
    return
  }

  const payload = {
    providerCode: form.providerCode,
    name: form.name.trim() || undefined,
    type: form.type,
    credentials: buildCredentials(),
    status: form.status,
    concurrencyLimit: form.concurrencyLimit,
    priority: form.priority,
    proxyProfileId: form.proxyProfileId,
    accountExpiresAt: formatServerDateTimeInput(form.accountExpiresAt),
    groupId: form.groupId,
    notes: form.notes
  }

  saving.value = true
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
    saving.value = false
  }
}

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

async function generateReauthorizeOAuthUrl() {
  reauthorizeAuthLoading.value = true
  try {
    reauthorizeAuthResult.value = isManagementView.value ? await api.openaiOAuth.authUrl({}) : await api.myOpenaiOAuth.authUrl({})
    message.success('授权链接已生成')
  } catch (error) {
    console.error(error)
    message.error('生成授权链接失败')
  } finally {
    reauthorizeAuthLoading.value = false
  }
}

function openAuthUrl() {
  if (!authResult.value?.authUrl) return
  window.open(authResult.value.authUrl, '_blank', 'noopener,noreferrer')
}

function openReauthorizeAuthUrl() {
  if (!reauthorizeAuthResult.value?.authUrl) return
  window.open(reauthorizeAuthResult.value.authUrl, '_blank', 'noopener,noreferrer')
}

async function createOAuthAccountFromUnifiedForm() {
  const commonPayload = {
    name: form.name.trim() || undefined,
    groupId: form.groupId,
    concurrencyLimit: form.concurrencyLimit,
    proxyProfileId: form.proxyProfileId,
    accountExpiresAt: formatServerDateTimeInput(form.accountExpiresAt),
    credentialsPatch: { error_handling_rules: buildCredentials().error_handling_rules },
    notes: form.notes || undefined
  }

  if (form.oauthMode === 'manual') {
    const payload = {
      ...commonPayload,
      sessionId: authResult.value?.sessionId,
      callbackUrl: form.callbackUrl
    }
    if (isManagementView.value) {
      await api.openaiOAuth.createFromCode(payload, accountScopeParams.value)
    } else {
      await api.myOpenaiOAuth.createFromCode(payload)
    }
    return
  }

  const payload = {
    ...commonPayload,
    refreshToken: form.refreshToken
  }
  if (isManagementView.value) {
    await api.openaiOAuth.createFromRefreshToken(payload, accountScopeParams.value)
  } else {
    await api.myOpenaiOAuth.createFromRefreshToken(payload)
  }
}

async function refreshOAuthToken(account: AccountSummary) {
  if (!canManageOpenAIOAuth(account)) {
    message.warning('只有自有 OpenAI OAuth 账户可以刷新令牌')
    return
  }
  tokenRefreshLoading.value = true
  const hide = message.loading(`${account.name}: 正在刷新令牌...`, 0)
  try {
    if (isManagementView.value) {
      await api.openaiOAuth.refreshToken(account.id, accountScopeParams.value)
    } else {
      await api.myOpenaiOAuth.refreshToken(account.id)
    }
    message.success(`${account.name}: 令牌刷新成功`)
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, `${account.name}: 令牌刷新失败`))
  } finally {
    hide()
    tokenRefreshLoading.value = false
  }
}

async function saveReauthorize() {
  const account = reauthorizingAccount.value
  if (!account || reauthorizeSaving.value) return
  if (reauthorizeForm.oauthMode === 'manual' && !reauthorizeAuthResult.value?.sessionId) {
    message.warning('请先生成授权链接')
    return
  }
  if (reauthorizeForm.oauthMode === 'manual' && !reauthorizeForm.callbackUrl.trim()) {
    message.warning('请粘贴回调 URL')
    return
  }
  if (reauthorizeForm.oauthMode === 'refresh_token' && !reauthorizeForm.refreshToken.trim()) {
    message.warning('请填写 Refresh Token')
    return
  }

  reauthorizeSaving.value = true
  try {
    if (reauthorizeForm.oauthMode === 'manual') {
      const payload = {
        sessionId: reauthorizeAuthResult.value?.sessionId,
        callbackUrl: reauthorizeForm.callbackUrl
      }
      if (isManagementView.value) {
        await api.openaiOAuth.reauthorizeFromCode(account.id, payload, accountScopeParams.value)
      } else {
        await api.myOpenaiOAuth.reauthorizeFromCode(account.id, payload)
      }
    } else {
      const payload = { refreshToken: reauthorizeForm.refreshToken }
      if (isManagementView.value) {
        await api.openaiOAuth.reauthorizeFromRefreshToken(account.id, payload, accountScopeParams.value)
      } else {
        await api.myOpenaiOAuth.reauthorizeFromRefreshToken(account.id, payload)
      }
    }
    message.success(`${account.name}: 重新授权成功`)
    reauthorizeModalOpen.value = false
    reauthorizingAccount.value = undefined
    reauthorizeAuthResult.value = undefined
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, `${account.name}: 重新授权失败`))
  } finally {
    reauthorizeSaving.value = false
  }
}

async function loadTestModels() {
  if (!isManagementView.value || providerModels.value.length || testModelsLoading.value) return
  testModelsLoading.value = true
  try {
    providerModels.value = await api.providers.models('openai')
    if (providerModels.value.length && !providerModels.value.some((item) => item.model === testForm.model)) {
      testForm.model = defaultTestModel.value
    }
  } catch (error) {
    console.error(error)
    message.warning('测试模型列表加载失败，已使用默认模型')
  } finally {
    testModelsLoading.value = false
  }
}

async function openTestModal(account: AccountSummary) {
  if (!canTestAccount(account)) {
    message.warning('当前账户不能测试')
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
    const payload = {
      model: testForm.model,
      prompt: testForm.prompt
    }
    const result = isManagementView.value
      ? await api.accounts.test(account.id, payload, accountScopeParams.value, { signal: controller.signal })
      : await api.myAccounts.test(account.id, payload, { signal: controller.signal })
    testResult.value = result
    if (result.success) {
      message.success(`${account.name}: ${result.message}${result.tokenRefreshed ? '，并已刷新 token' : ''}`)
    } else {
      message.error(`${account.name}: ${result.message}`)
    }
    await loadData()
  } catch (error) {
    if (axios.isCancel(error) || (error instanceof DOMException && error.name === 'AbortError')) {
      message.info(`${account.name}: 已停止测试`)
      return
    }
    console.error(error)
    const fallbackMessage = error instanceof Error ? error.message : '测试失败'
    testResult.value = {
      accountId: account.id,
      accountName: account.name,
      providerCode: account.providerCode,
      type: account.type,
      success: false,
      message: fallbackMessage,
      model: testForm.model,
      responseText: fallbackMessage,
      durationMs: Date.now() - startedAt
    }
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

async function testAccount(account: AccountSummary) {
  await openTestModal(account)
}

async function testAccountSilently(account: AccountSummary) {
  if (!canTestAccount(account)) return undefined
  try {
    const payload = { model: testForm.model, prompt: testForm.prompt }
    return isManagementView.value
      ? await api.accounts.test(account.id, payload, accountScopeParams.value)
      : await api.myAccounts.test(account.id, payload)
  } catch (error) {
    console.error(error)
    return undefined
  }
}

async function batchUpdateAccounts(
  payloadBuilder: (account: AccountSummary) => Record<string, unknown>,
  loadingLabel: string,
  successLabel: string,
  selected = selectedAccounts.value.filter(canEditAccount)
) {
  if (!selected.length) {
    message.warning('请先选择账户')
    return
  }
  const hide = message.loading(`${loadingLabel}（${selected.length} 个）...`, 0)
  try {
    const results = await Promise.allSettled(selected.map((account) => isManagementView.value
      ? api.accounts.update(account.id, payloadBuilder(account), accountScopeParams.value)
      : api.myAccounts.update(account.id, payloadBuilder(account))))
    const failedCount = results.filter((result) => result.status === 'rejected').length
    if (failedCount === 0) {
      message.success(successLabel)
      clearSelection()
    } else {
      message.warning(`${successLabel}，成功 ${selected.length - failedCount} 个，失败 ${failedCount} 个`)
    }
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(`${loadingLabel}失败`)
  } finally {
    hide()
  }
}

async function batchTestSelected() {
  const selected = selectedAccounts.value.filter(canTestAccount)
  if (!selected.length) {
    message.warning('请先选择账户')
    return
  }
  const hide = message.loading(`正在批量测试 ${selected.length} 个账户...`, 0)
  try {
    const results = await Promise.all(selected.map((account) => testAccountSilently(account)))
    const successCount = results.filter((result) => result?.success).length
    const failedCount = results.length - successCount
    if (failedCount === 0) {
      message.success(`批量测试完成，${successCount} 个账户全部通过`)
      clearSelection()
    } else {
      message.warning(`批量测试完成，成功 ${successCount} 个，失败 ${failedCount} 个`)
    }
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('批量测试失败')
  } finally {
    hide()
  }
}

async function batchSetStatus(status: 'active' | 'disabled') {
  const selected = selectedAccounts.value.filter(canEditAccount)
  const eligible = status === 'active'
    ? selected.filter((account) => account.status === 'disabled')
    : selected.filter((account) => account.status !== 'disabled')
  if (!eligible.length) {
    message.warning(status === 'active' ? '所选账户里没有可手动启用的停用账户' : '所选账户里没有可停用的账户')
    return
  }
  if (eligible.length !== selected.length) {
    message.warning(status === 'active' ? '已跳过临时状态或错误状态的账户，只启用手动停用的账户' : '已跳过已停用的账户')
  }
  await batchUpdateAccounts(
    (account) => ({ status: account.status === 'disabled' ? 'active' : 'disabled' }),
    status === 'active' ? '正在批量启用账户' : '正在批量停用账户',
    status === 'active' ? '账户已批量启用' : '账户已批量停用',
    eligible
  )
}

async function updateAccountState(account: AccountSummary, payload: Record<string, unknown>, successText: string) {
  if (!canEditAccount(account)) {
    message.warning('授权账户不能修改状态')
    return
  }
  try {
    if (isManagementView.value) {
      await api.accounts.update(account.id, payload, accountScopeParams.value)
    } else {
      await api.myAccounts.update(account.id, payload)
    }
    message.success(successText)
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '账户状态更新失败'))
  }
}

async function handleAccountMenu(key: string, account: AccountSummary) {
  if (key === 'test') {
    await testAccount(account)
    return
  }
  if (!canUseAccountActions(account)) {
    message.warning('授权账户仅可使用，不能执行管理操作')
    return
  }
  if (key === 'refresh-oauth-token') {
    if (tokenRefreshLoading.value) return
    await refreshOAuthToken(account)
    return
  }
  if (key === 'reauthorize-oauth') {
    openReauthorizeModal(account)
    return
  }
  if (key === 'toggle-status') {
    const nextStatus = account.status === 'disabled' ? 'active' : 'disabled'
    await updateAccountState(account, { status: nextStatus }, nextStatus === 'active' ? '账户已启用' : '账户已停用')
    return
  }
  if (key === 'restore-normal') {
    if (!isTemporaryAccountStatus(account)) {
      message.warning('当前账户不需要恢复')
      return
    }
    await updateAccountState(account, { clearFailureState: true }, '账户已恢复正常')
    return
  }
  if (key === 'super-priority-on' || key === 'super-priority-off') {
    const enabled = key === 'super-priority-on'
    if (enabled && account.status !== 'active') {
      message.warning('只有正常状态的账户可以设置超级优先')
      return
    }
    await updateAccountState(account, { superPriorityEnabled: enabled }, enabled ? '已设为超级优先' : '已取消超级优先')
    return
  }
  if (key === 'migrate-traffic') {
    openTrafficMigration(account)
    return
  }
}

function handleAccountMenuClick(event: { key: string | number }, account: AccountSummary) {
  void handleAccountMenu(String(event.key), account)
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

function snapshotPageState(): AccountsPageState {
  return {
    filters: { ...filters },
    pagination: { current: accountPagination.current, pageSize: accountPagination.pageSize },
    sorts: accountSorts.value
  }
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })

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

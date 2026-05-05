<template>
  <a-card class="page-card accounts-page-card responsive-page-card">
    <AccountFilterToolbar
      :active-filter-count="activeAdvancedFilterCount"
      :filters="filters"
      :is-admin="isAdmin"
      :refresh-loading="loading"
      :schedulable-options="schedulableOptions"
      :status-options="statusOptions"
      :system-accounts="systemAccounts"
      :type-options="typeOptions"
      @create="openCreate"
      @refresh="loadData"
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

    <ResponsiveDataList
      class="account-responsive-list"
      table-class="account-table"
      :columns="columns"
      :data-source="filteredAccounts"
      :mobile-data-source="mobileVisibleAccounts"
      row-key="id"
      :loading="loading"
      :scroll-x="tableScrollX"
      :table-scroll-y="tableScrollY"
      :pagination="accountTablePagination"
      :row-selection="rowSelection"
      mobile-pagination
      pull-refresh-enabled
      :mobile-has-more="mobileHasMoreAccounts"
      :loading-more="mobileLoadingMore"
      :refreshing="mobileRefreshing"
      @change="handleAccountTableChange"
      @mobile-load-more="loadMoreMobileAccounts"
      @mobile-refresh="refreshMobileAccounts"
    >
      <template #emptyText>
        <a-empty class="page-empty-card" description="还没有账户。点击「添加账户」，再选择供应商和账户类型。" />
      </template>
      <template #bodyCell="{ column, record }">
        <AccountTableCell
          :account="record"
          :authorized-tooltip="authorizedAccountTooltip(record)"
          :can-delete="canDeleteAccount(record)"
          :can-edit="canEditAccount(record)"
          :column-key="tableColumnKey(column)"
          :group-name="groupNameForAccount(record.id)"
          :menu-items="accountMenuItems(record)"
          :provider-name="providerName(record.providerCode)"
          @bind-group="openBindGroup"
          @delete="removeAccount($event.id)"
          @edit="openEdit"
          @menu-click="handleAccountMenuClick"
          @test="openTestModal"
        />
      </template>
      <template #card="{ record }">
        <AccountMobileCard
          :account="record"
          :authorized-tooltip="authorizedAccountTooltip(record)"
          :can-delete="canDeleteAccount(record)"
          :can-edit="canEditAccount(record)"
          :group-name="groupNameForAccount(record.id)"
          :is-admin="isAdmin"
          :menu-items="accountMenuItems(record)"
          :provider-name="providerName(record.providerCode)"
          :selected="isAccountSelected(record.id)"
          @delete="removeAccount(record.id)"
          @edit="openEdit(record)"
          @bind-group="openBindGroup(record)"
          @menu-click="handleAccountMenuClick($event, record)"
          @test="openTestModal(record)"
          @toggle-selection="toggleAccountSelection(record)"
        />
      </template>
    </ResponsiveDataList>

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
    />

    <a-modal v-model:open="modalOpen" :title="modalTitle" width="920px" :confirm-loading="modalConfirmLoading" :ok-button-props="modalOkButtonProps" @ok="saveAccount" @cancel="handleModalCancel">
      <a-form layout="vertical" class="account-form">
        <a-alert v-if="editingId" class="form-alert" type="info" show-icon message="编辑账户时不修改供应商和账户类型；Access/API Key 与 Refresh Token 只在这里展示和修改。" />

        <AccountFormSelector
          :account-type="form.type"
          :account-type-choices="accountTypeChoices"
          :editing="Boolean(editingId)"
          :provider-code="form.providerCode"
          :providers="availableProviders"
          :selected-provider="selectedProvider"
          @select-provider="selectProvider"
          @select-type="selectAccountType"
        />

        <AccountBasicInfoSection v-if="hasAccountType" :editing="Boolean(editingId)" :form="form" :group-options="groupOptions" />

        <AccountApiKeySection
          v-if="isApiKeyForm"
          :base-url-placeholder="selectedProvider?.baseUrl || 'https://api.openai.com/v1'"
          :form="form"
          :title="accountTypeTitle(form.providerCode, form.type)"
        />

        <AccountOAuthSection
          v-else-if="isOAuthForm"
          :auth-loading="authLoading"
          :auth-result="authResult"
          :editing="Boolean(editingId)"
          :form="form"
          :is-open-a-i="isOpenAIOAuthForm"
          :title="accountTypeTitle(form.providerCode, form.type)"
          @copy-auth-url="copyText"
          @generate-auth-url="generateOAuthUrl"
          @open-auth-url="openAuthUrl"
        />

        <AccountStrategySection v-if="hasAccountType" :form="form" :is-admin="isAdmin" :proxy-options="proxyOptions" :status-options="statusEditOptions" />

        <AccountErrorPolicyCard v-if="hasAccountType" v-model:rules="accountErrorPolicyRules" />
      </a-form>
    </a-modal>

    <AccountBindGroupModal
      v-model:open="bindGroupModalOpen"
      v-model:group-id="bindGroupForm.groupId"
      :account="bindingAccount"
      :group-options="bindGroupOptions"
      :saving="bindGroupSaving"
      :tip="bindGroupTip"
      @save="saveBindGroup"
    />
  </a-card>
</template>

<script setup lang="ts">
import axios from 'axios'
import { message } from 'ant-design-vue'
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import { authState } from '@/composables/useAuth'
import type { AccountStatus, AccountSummary, AccountTestResult, AccountType, GroupSummary, OpenAIAuthURLResult, ProviderDefinition, ProviderModelPricing, ProxyProfileSummary, SystemAccountSummary } from '@/types/domain'
import { allSystemAccountsValue, matchesSystemAccountFilter, selectedSystemAccountId } from '@/utils/systemAccountFilter'
import AccountApiKeySection from './AccountApiKeySection.vue'
import AccountBatchToolbar from './AccountBatchToolbar.vue'
import AccountBasicInfoSection from './AccountBasicInfoSection.vue'
import AccountBindGroupModal from './AccountBindGroupModal.vue'
import AccountErrorPolicyCard from './AccountErrorPolicyCard.vue'
import AccountFilterToolbar from './AccountFilterToolbar.vue'
import AccountFormSelector from './AccountFormSelector.vue'
import AccountMobileCard from './AccountMobileCard.vue'
import AccountOAuthSection from './AccountOAuthSection.vue'
import AccountStrategySection from './AccountStrategySection.vue'
import AccountTableCell from './AccountTableCell.vue'
import AccountTestModal from './AccountTestModal.vue'
import {
  loadAccountErrorPolicyRules,
  validateAccountErrorPolicyRules,
  writeAccountErrorPolicyToCredentials,
  type AccountErrorPolicyRuleForm
} from './accountErrorPolicy'
import {
  accountTypeDescription,
  accountTypeText,
  accountTypeTitle as buildAccountTypeTitle,
  asString,
  formatServerDateTimeInput,
  isAuthorizedAccount,
  isOwnerDisabledAuthorizedAccount,
  isTemporaryAccountStatus,
  matchesSchedulableFilter,
  normalizeKeyword,
  parseDatePickerValue,
  type SchedulableFilter
} from './accountFormatters'
import type { AccountMenuItem } from './accountActionTypes'
import type { AccountFormModel } from './accountFormTypes'
import {
  ACCOUNT_PAGE_SIZE,
  DEFAULT_ACCOUNT_CONCURRENCY_LIMIT,
  FALLBACK_PROVIDER,
  defaultTestModelOptions,
  schedulableOptions,
  statusOptions,
  typeOptions
} from './accountOptions'
import {
  accountTableScrollX,
  accountTableScrollY,
  buildAccountTableColumns,
  tableColumnKey
} from './accountTableColumns'
import { useAccountMobilePagination } from './useAccountMobilePagination'

interface AccountFilters {
  keyword: string
  type: 'all' | AccountType
  status: 'all' | AccountStatus
  schedulable: SchedulableFilter
  systemAccountId: string
}

const loading = ref(false)
const saving = ref(false)
const authLoading = ref(false)
const testModalOpen = ref(false)
const testRunning = ref(false)
const testModelsLoading = ref(false)
const modalOpen = ref(false)
const bindGroupModalOpen = ref(false)
const bindGroupSaving = ref(false)
const authResult = ref<OpenAIAuthURLResult>()
const editingId = ref<string>()
const testingAccount = ref<AccountSummary>()
const bindingAccount = ref<AccountSummary>()
const testResult = ref<AccountTestResult>()
const selectedAccountIds = ref<string[]>([])
const accounts = ref<AccountSummary[]>([])
const providers = ref<ProviderDefinition[]>([])
const providerModels = ref<ProviderModelPricing[]>([])
const proxies = ref<ProxyProfileSummary[]>([])
const groups = ref<GroupSummary[]>([])
const systemAccounts = ref<SystemAccountSummary[]>([])
const filters = reactive<AccountFilters>({ keyword: '', type: 'all', status: 'all', schedulable: 'all', systemAccountId: allSystemAccountsValue })
const testForm = reactive({ model: 'gpt-5.5', prompt: 'hi' })
const isAdmin = authState.isAdmin
const router = useRouter()

const form = reactive<AccountFormModel>(defaultForm())
const bindGroupForm = reactive({ groupId: '' })
const accountErrorPolicyRules = ref<AccountErrorPolicyRuleForm[]>(loadAccountErrorPolicyRules())

const currentEditingAccount = computed(() => editingId.value ? accounts.value.find((account) => account.id === editingId.value) : undefined)

const statusEditOptions = computed(() => {
  const options = statusOptions.filter((item) => item.value !== 'all')
  if (currentEditingAccount.value && isTemporaryAccountStatus(currentEditingAccount.value)) {
    return options.filter((item) => item.value !== 'active')
  }
  return options
})

const columns = computed(() => buildAccountTableColumns(isAdmin.value))
const tableScrollX = computed(() => accountTableScrollX(isAdmin.value))
const tableScrollY = computed(accountTableScrollY)

const filteredAccounts = computed(() => accounts.value.filter((account) => {
  const keyword = normalizeKeyword(filters.keyword)
  const keywordMatched = !keyword || [
    account.name,
    account.notes ?? '',
    account.providerCode,
    groupNameForAccount(account.id) ?? '',
    account.type,
    accountBaseUrl(account),
    account.id
  ].some((value) => normalizeKeyword(value).includes(keyword))
  const typeMatched = filters.type === 'all' || account.type === filters.type
  const statusMatched = filters.status === 'all' || account.status === filters.status
  const schedulableMatched = matchesSchedulableFilter(account, filters.schedulable)
  const systemAccountMatched = matchesSystemAccountFilter(account, filters.systemAccountId, isAdmin.value)
  return keywordMatched && typeMatched && statusMatched && schedulableMatched && systemAccountMatched
}))

const {
  mobileHasMore: mobileHasMoreAccounts,
  mobileLoadingMore,
  mobileRefreshing,
  mobileVisibleCount,
  tablePagination: accountTablePagination,
  clampPagination: clampAccountListPagination,
  handleTableChange: handleAccountTableChange,
  loadMoreMobile: loadMoreMobileAccounts,
  refreshMobile: refreshMobileAccounts,
  resetPagination: resetAccountListPagination
} = useAccountMobilePagination(ACCOUNT_PAGE_SIZE, () => filteredAccounts.value.length, loadData)
const mobileVisibleAccounts = computed(() => filteredAccounts.value.slice(0, mobileVisibleCount.value))

const activeAdvancedFilterCount = computed(() => [
  filters.type !== 'all',
  filters.status !== 'all',
  filters.schedulable !== 'all',
  isAdmin.value && filters.systemAccountId !== allSystemAccountsValue
].filter(Boolean).length)

const testModelOptions = computed(() => {
  const models = providerModels.value.length ? providerModels.value.map((item) => item.model) : defaultTestModelOptions
  return [...new Set(models)].map((model) => ({ label: model, value: model }))
})

const selectedAccounts = computed(() => accounts.value.filter((account) => selectedAccountIds.value.includes(account.id)))

const rowSelection = computed(() => ({
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

const proxyOptions = computed(() => (isAdmin.value ? proxies.value : []).map((proxy) => ({ label: `${proxy.name} (${proxy.type})`, value: proxy.id })))
const providerGroups = computed(() => groups.value.filter((group) => canManageGroupAccounts(group) && (!form.providerCode || group.providerCode === form.providerCode)))
const groupOptions = computed(() => providerGroups.value.map((group) => ({ label: group.isDefault ? `${group.name}（默认）` : group.name, value: group.id })))
const bindGroupOptions = computed(() => {
  const account = bindingAccount.value
  if (!account) return []
  return groups.value
    .filter((group) => canManageGroupAccounts(group) && group.providerCode === account.providerCode)
    .map((group) => ({ label: group.isDefault ? `${group.name}（默认）` : group.name, value: group.id }))
})
const bindGroupTip = computed(() => {
  const ownerName = bindingAccount.value?.ownerSystemAccountName || '其他用户'
  return `授权账户来自 ${ownerName}。绑定到你的同供应商分组后，对应 API Key 才能调度使用。`
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

function defaultForm(providerCode = '', type: AccountType = ''): AccountFormModel {
  const providerList = providers.value.length ? providers.value : [FALLBACK_PROVIDER]
  const provider = providerList.find((item) => item.code === providerCode) ?? (providerCode ? FALLBACK_PROVIDER : undefined)
  return {
    providerCode,
    name: '',
    type,
    groupId: undefined,
    apiKey: '',
    baseUrl: provider?.baseUrl ?? 'https://api.openai.com/v1',
    accessToken: '',
    refreshToken: '',
    oauthMode: 'manual',
    callbackUrl: '',
    accountExpiresAt: undefined,
    status: 'active',
    concurrencyLimit: DEFAULT_ACCOUNT_CONCURRENCY_LIMIT,
    priority: 0,
    proxyProfileId: undefined,
    notes: ''
  }
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

function accountBaseUrl(account: AccountSummary): string {
  return asString(account.credentials.base_url)
}

function accountMenuItems(account: AccountSummary): AccountMenuItem[] {
  const items: AccountMenuItem[] = []
  if (account.authorizationUsageAvailable) {
    items.push({ key: 'authorization-usage', label: '授权用量' })
  }
  if (canTestAccount(account)) {
    items.push({ key: 'test', label: '测试' })
  }
  if (canUseAccountActions(account)) {
    items.push({ key: 'toggle-status', label: account.status === 'disabled' ? '启用账户' : '停用账户', danger: account.status !== 'disabled' })
  }
  return items
}

async function copyText(value: string) {
  if (!value) return
  await navigator.clipboard.writeText(value)
  message.success('已复制')
}

async function loadData() {
  loading.value = true
  try {
    const systemAccountId = selectedSystemAccountId(filters.systemAccountId, isAdmin.value)
    const [accountList, providerList, proxyList, groupList, systemAccountList] = await Promise.all([
      api.accounts.list({ systemAccountId }),
      isAdmin.value ? api.providers.list() : Promise.resolve([] as ProviderDefinition[]),
      isAdmin.value ? api.proxies.list() : Promise.resolve([] as ProxyProfileSummary[]),
      api.groups.list({ systemAccountId }),
      api.systemAccounts.list()
    ])
    accounts.value = accountList
    providers.value = providerList.length ? providerList : [FALLBACK_PROVIDER]
    proxies.value = proxyList
    groups.value = groupList
    systemAccounts.value = systemAccountList
    selectedAccountIds.value = selectedAccountIds.value.filter((id) => accountList.some((account) => account.id === id && canEditAccount(account)))
    clampAccountListPagination()
    if (modalOpen.value && !editingId.value) {
      ensureDefaultGroupSelected()
    }
  } catch (error) {
    console.error(error)
    message.error('加载账户失败')
  } finally {
    loading.value = false
  }
}

function applyFilters() {
  filters.keyword = filters.keyword.trim()
  resetAccountListPagination()
}

function resetFilters() {
  Object.assign(filters, {
    keyword: '',
    type: 'all',
    status: 'all',
    schedulable: 'all',
    systemAccountId: allSystemAccountsValue
  })
  resetAccountListPagination()
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
  resetAccountListPagination()
  void loadData()
}

function clearSelection() {
  selectedAccountIds.value = []
}

function currentListParams() {
  return { systemAccountId: selectedSystemAccountId(filters.systemAccountId, isAdmin.value) }
}

function openCreate() {
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
    await api.accounts.bindGroup(bindingAccount.value.id, { groupId: bindGroupForm.groupId })
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

function buildCredentials() {
  const credentials: Record<string, unknown> = form.type === 'api_key'
    ? buildApiKeyCredentials()
    : buildOAuthCredentials()
  writeAccountErrorPolicyToCredentials(credentials, accountErrorPolicyRules.value)
  return credentials
}

function buildApiKeyCredentials(): Record<string, unknown> {
  return {
    api_key: form.apiKey,
    base_url: form.baseUrl
  }
}

function buildOAuthCredentials(): Record<string, unknown> {
  const currentCredentials = editingId.value
    ? accounts.value.find((account) => account.id === editingId.value)?.credentials ?? {}
    : {}
  return compactCredentials({
    ...currentCredentials,
    access_token: form.accessToken,
    refresh_token: form.refreshToken,
    expires_at: currentCredentials.expires_at,
    base_url: currentCredentials.base_url ?? 'https://api.openai.com/v1'
  })
}

function compactCredentials(credentials: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(credentials).filter(([, value]) => value !== undefined && value !== ''))
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
      await api.accounts.update(editingId.value, payload)
      message.success('账户已更新')
    } else if (form.type === 'oauth') {
      await createOAuthAccountFromUnifiedForm()
      message.success('OAuth 账户已创建')
    } else {
      await api.accounts.create(payload)
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
    authResult.value = await api.openaiOAuth.authUrl({})
    message.success('授权链接已生成')
  } catch (error) {
    console.error(error)
    message.error('生成授权链接失败')
  } finally {
    authLoading.value = false
  }
}

function openAuthUrl() {
  if (!authResult.value?.authUrl) return
  window.open(authResult.value.authUrl, '_blank', 'noopener,noreferrer')
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
    await api.openaiOAuth.createFromCode({
      ...commonPayload,
      sessionId: authResult.value?.sessionId,
      callbackUrl: form.callbackUrl
    })
    return
  }

  await api.openaiOAuth.createFromRefreshToken({
    ...commonPayload,
    refreshToken: form.refreshToken
  })
}

async function loadTestModels() {
  if (!isAdmin.value || providerModels.value.length || testModelsLoading.value) return
  testModelsLoading.value = true
  try {
    providerModels.value = await api.providers.models('openai')
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
  testForm.model = testForm.model || 'gpt-5.5'
  testModalOpen.value = true
  void loadTestModels()
}

async function runAccountTest() {
  if (!testingAccount.value || testRunning.value) return
  testResult.value = undefined
  testRunning.value = true
  try {
    const result = await api.accounts.test(testingAccount.value.id, {
      model: testForm.model,
      prompt: testForm.prompt
    })
    testResult.value = result
    if (result.success) {
      message.success(`${testingAccount.value.name}: ${result.message}${result.tokenRefreshed ? '，并已刷新 token' : ''}`)
    } else {
      message.error(`${testingAccount.value.name}: ${result.message}`)
    }
    await loadData()
  } catch (error) {
    console.error(error)
    const fallbackMessage = error instanceof Error ? error.message : '测试失败'
    testResult.value = {
      accountId: testingAccount.value.id,
      accountName: testingAccount.value.name,
      providerCode: testingAccount.value.providerCode,
      type: testingAccount.value.type,
      success: false,
      message: fallbackMessage,
      model: testForm.model,
      responseText: fallbackMessage
    }
    message.error(`${testingAccount.value.name}: 测试失败`)
  } finally {
    testRunning.value = false
  }
}

function closeTestModal() {
  if (testRunning.value) return
  testModalOpen.value = false
}

async function testAccount(account: AccountSummary) {
  await openTestModal(account)
}

async function testAccountSilently(account: AccountSummary) {
  if (!canTestAccount(account)) return undefined
  try {
    return await api.accounts.test(account.id, { model: testForm.model, prompt: testForm.prompt })
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
    const results = await Promise.allSettled(selected.map((account) => api.accounts.update(account.id, payloadBuilder(account))))
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
    await api.accounts.update(account.id, payload)
    message.success(successText)
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '账户状态更新失败'))
  }
}

async function handleAccountMenu(key: string, account: AccountSummary) {
  if (key === 'authorization-usage') {
    const query: Record<string, string> = { accountId: account.id, action: 'authorization-usage' }
    if (isAdmin.value && account.systemAccountId) {
      query.systemAccountId = account.systemAccountId
    }
    await router.push({ path: '/usage-stats', query })
    return
  }
  if (key === 'test') {
    await testAccount(account)
    return
  }
  if (!canUseAccountActions(account)) {
    message.warning('授权账户仅可使用，不能执行管理操作')
    return
  }
  if (key === 'toggle-status') {
    const nextStatus = account.status === 'disabled' ? 'active' : 'disabled'
    await updateAccountState(account, { status: nextStatus }, nextStatus === 'active' ? '账户已启用' : '账户已停用')
    return
  }
}

function handleAccountMenuClick(event: { key: string | number }, account: AccountSummary) {
  void handleAccountMenu(String(event.key), account)
}

async function removeAccount(id: string) {
  try {
    await api.accounts.delete(id)
    message.success('账户已删除')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('删除账户失败')
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

.account-table {
  border: 1px solid #e8edf5;
  border-radius: 14px;
}

.credential-cell {
  display: inline-block;
  max-width: 240px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: bottom;
}

.account-table :deep(.ant-table-tbody > tr > td) {
  vertical-align: middle;
}

.account-table :deep(.ant-table-cell) {
  white-space: nowrap;
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

.account-table :deep(.ant-empty) {
  margin: 12px 0;
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

  .account-table :deep(.ant-table-cell-fix-right),
  .account-table :deep(.ant-table-cell-fix-right-first),
  .account-table :deep(.ant-table-cell-fix-right-last) {
    position: static !important;
    box-shadow: none !important;
  }
}
</style>

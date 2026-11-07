<template>
  <a-card class="page-card security-policies-page-card responsive-page-card">
    <ResponsiveListToolbar
      v-model:keyword="keyword"
      search-placeholder="搜索安全策略"
      filter-title="筛选安全策略"
      :active-filter-count="activeFilterCount"
      :advanced-filter-count="0"
      :refresh-loading="loading"
      @reset="resetFilters"
      @refresh="refreshSecurityPolicies"
      @search="applyFilters"
    >
      <template #inline-filters>
        <SystemPrincipalSelect
          v-if="isManagementView"
          v-model:value="systemAccountFilter"
          v-model:selected-principal="systemAccountFilterSelection"
          :accounts="systemAccounts"
          :active-only="false"
          :filter-option="false"
          :loading="systemAccountOptionsLoading"
          include-all
          class="toolbar-select responsive-list-inline-filter"
          @change="handleSystemAccountFilterChange"
          @dropdown-visible-change="handleSystemAccountOptionsDropdown"
          @search="handleSystemAccountOptionsSearch"
        />
      </template>
      <template #actions>
        <a-button type="primary" @click="openCreate">
          <template #icon><PlusOutlined /></template>
          新建安全策略
        </a-button>
      </template>
      <template #filters>
        <label v-if="isManagementView" class="mobile-filter-field">
          <span>系统账户</span>
          <SystemPrincipalSelect
            v-model:value="systemAccountFilter"
            v-model:selected-principal="systemAccountFilterSelection"
            :accounts="systemAccounts"
            :active-only="false"
            :filter-option="false"
            :loading="systemAccountOptionsLoading"
            include-all
            @change="handleSystemAccountFilterChange"
            @dropdown-visible-change="handleSystemAccountOptionsDropdown"
            @search="handleSystemAccountOptionsSearch"
          />
        </label>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList
      table-class="page-table security-policy-table"
      :columns="columns"
      :data-source="items"
      row-key="id"
      :loading="loading"
      :pagination="pagination"
      :scroll-x="isManagementView ? 1290 : 1110"
      mobile-pagination
      pull-refresh-enabled
      :refreshing="loading"
      @change="handleTableChange"
      @mobile-refresh="loadSecurityPolicies"
    >
      <template #emptyText>
        <a-empty class="page-empty-card" description="暂无安全策略。未绑定策略的 API Key 走内置「默认安全策略」，请求原样转发。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'name'">
          <span class="security-policy-name-text">{{ record.name }}</span>
          <a-tag v-if="record.builtIn" color="gold">内置</a-tag>
        </template>
        <template v-else-if="column.key === 'systemAccount'">
          <span :class="record.systemAccountName ? 'name-cell' : 'muted-cell'">{{ securityPolicySystemAccountText(record) }}</span>
        </template>
        <template v-else-if="column.key === 'summary'">
          <span class="security-policy-summary-cell">
            <span v-if="record.description" :title="record.description">{{ record.description }}</span>
            <span v-else class="security-policy-summary-empty">无说明</span>
            <span class="security-policy-rules-summary">{{ securityPolicyRulesSummaryText(record) }}</span>
          </span>
        </template>
        <template v-else-if="column.key === 'boundApiKeyCount'">
          <a-tag>{{ formatNumber(record.boundApiKeyCount) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'protectionCount'">
          <a-tag>{{ formatNumber(record.protectionCount) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'updatedAt'">
          <span class="muted-cell">{{ formatDateTime(record.updatedAt) }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <span v-if="record.builtIn" class="muted-cell">内置只读</span>
          <RowActions v-else :actions="securityPolicyActions(record)" @action-click="handleSecurityPolicyAction($event, record)" />
        </template>
      </template>
      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">
              <div class="mobile-list-card-name-row">
                <span>{{ record.name }}</span>
                <a-tag v-if="record.builtIn" color="gold">内置</a-tag>
              </div>
            </div>
            <div class="mobile-list-card-tags">
              <span class="security-policy-rules-summary">{{ securityPolicyRulesSummaryText(record) }}</span>
            </div>
          </div>
          <div class="mobile-list-meta-grid">
            <div v-if="isManagementView" class="mobile-list-meta-item mobile-list-meta-wide">
              <span>系统账户</span>
              <strong>{{ securityPolicySystemAccountText(record) }}</strong>
            </div>
            <div v-if="record.description" class="mobile-list-meta-item mobile-list-meta-wide">
              <span>说明</span>
              <strong>{{ record.description }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>绑定 API Key</span>
              <strong>{{ formatNumber(record.boundApiKeyCount) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>防护次数</span>
              <strong>{{ record.builtIn ? '—' : formatNumber(record.protectionCount) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>更新时间</span>
              <strong>{{ formatDateTime(record.updatedAt) }}</strong>
            </div>
          </div>
          <div v-if="!record.builtIn" class="mobile-list-card-actions">
            <RowActions
              variant="button"
              :actions="securityPolicyActions(record)"
              @action-click="handleSecurityPolicyAction($event, record)"
            />
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <a-modal v-model:open="modalOpen" :title="editingId ? '编辑安全策略' : '新建安全策略'" width="860px" :confirm-loading="saving" destroy-on-close @ok="saveSecurityPolicy">
      <a-alert v-if="!editingId && isManagementView && targetSystemAccountLabel" class="modal-alert" type="info" show-icon :message="`当前创建目标：${targetSystemAccountLabel}`" />
      <a-form layout="vertical" class="security-policy-modal-form">
        <a-form-item label="名称" required tooltip="用于在 API Key 绑定安全策略时识别这套出站脱敏规则。">
          <a-input v-model:value="form.name" placeholder="请输入安全策略名称" />
        </a-form-item>
        <a-form-item label="说明" tooltip="可选；面向维护者的用途说明。">
          <a-textarea v-model:value="form.description" :rows="2" placeholder="可选" />
        </a-form-item>
        <div class="modal-section-title">
          <span>脱敏规则</span>
          <a-tooltip title="规则按数组顺序执行，先命中先替换；内置类型的匹配 pattern 由服务端代码提供，不可改写。">
            <InfoCircleOutlined class="security-policy-field-help-icon" />
          </a-tooltip>
        </div>
        <div v-if="!form.rules.length" class="security-policy-rules-empty">暂无规则；空规则集 = 请求原样转发（与内置默认策略一致）。</div>
        <div v-for="(row, index) in form.rules" :key="row.id" class="security-policy-rule-row">
          <span class="security-policy-rule-index">{{ index + 1 }}</span>
          <a-select v-model:value="row.type" class="security-policy-rule-type" :options="securityPolicyRuleTypeOptions" />
          <a-radio-group v-model:value="row.action" class="security-policy-rule-action" :options="securityPolicyRuleActionOptions" />
          <a-button class="security-policy-rule-remove" type="text" danger @click="removeRuleRow(index)">删除</a-button>
          <div class="security-policy-rule-fields">
            <a-input
              v-if="row.action === 'placeholder'"
              v-model:value="row.label"
              class="security-policy-rule-label"
              :placeholder="`占位符标签（默认 ${securityPolicyDefaultRuleLabel[row.type]}）`"
            />
            <a-input-number
              v-if="row.action === 'mask'"
              v-model:value="row.maskKeep"
              class="security-policy-rule-keep"
              :min="securityPolicyBounds.maskKeepMin"
              :max="securityPolicyBounds.maskKeepMax"
              :precision="0"
              placeholder="保留位数（可选）"
            />
            <a-input
              v-if="row.type === 'custom_regex'"
              v-model:value="row.pattern"
              class="security-policy-rule-pattern"
              placeholder="RE2 正则语法，≤256 字节；编译失败创建时报 400"
            />
            <a-select
              v-if="row.type === 'custom_keywords'"
              v-model:value="row.keywords"
              class="security-policy-rule-keywords"
              mode="tags"
              :open="false"
              :token-separators="[',', '，']"
              placeholder="输入关键词后回车或逗号分隔（≤100 个，单个 ≤64 字节，大小写敏感）"
            />
          </div>
        </div>
        <a-button
          class="security-policy-rule-add"
          :disabled="form.rules.length >= securityPolicyBounds.ruleCountMax"
          @click="addRuleRow"
        >
          <template #icon><PlusOutlined /></template>
          添加规则（{{ form.rules.length }}/{{ securityPolicyBounds.ruleCountMax }}）
        </a-button>
        <a-collapse class="security-policy-trial-scan" ghost>
          <a-collapse-panel key="trial" header="试扫（本地演示，以服务端规则为准）">
            <a-textarea
              v-model:value="trialScanText"
              :rows="4"
              placeholder="粘贴样例文本，本地演示规则命中与替换结果；不发请求、不落库，实际命中与替换以服务端规则为准。"
            />
            <div v-if="trialScan && form.rules.length" class="security-policy-trial-result">
              <div v-for="hit in trialScan.hits" :key="hit.ruleId" class="security-policy-trial-hit">
                <span v-if="hit.unsupportedReason" class="muted-cell">{{ hit.typeLabel }}：{{ hit.unsupportedReason }}</span>
                <template v-else-if="hit.count > 0">
                  <span>规则「{{ hit.ruleLabel }}」（{{ hit.typeLabel }}·{{ hit.action === 'mask' ? '打码' : '占位符' }}）命中 {{ hit.count }} 处</span>
                  <span v-if="hit.samples.length" class="security-policy-trial-samples">{{ hit.samples.join('；') }}</span>
                </template>
                <span v-else class="muted-cell">规则「{{ hit.ruleLabel }}」（{{ hit.typeLabel }}）：未命中</span>
              </div>
              <div class="security-policy-trial-output">
                <span>替换后预览</span>
                <pre>{{ trialScan.output }}</pre>
              </div>
            </div>
            <div v-else-if="trialScanText.trim() && !form.rules.length" class="muted-cell">当前策略没有规则，试扫无内容可演示。</div>
          </a-collapse-panel>
        </a-collapse>
      </a-form>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { InfoCircleOutlined, PlusOutlined } from '@ant-design/icons-vue'
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import type { TablePaginationConfig } from 'ant-design-vue'

import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { useScopedSecurityPoliciesApi } from '@/composables/useScopedDomainApi'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime, formatNumber } from '@/shared/formatters'
import { principalLabelForId, rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import type { SecurityPolicyListItem, SecurityPolicyMutationResult } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import {
  buildSecurityPolicyFormPayload,
  buildSecurityPolicyMutationPatch,
  defaultSecurityPolicyFormModel,
  emptySecurityPolicyRuleRow,
  hasSecurityPolicyMutationChanges,
  mergeSecurityPolicyMutationResult,
  securityPolicyBounds,
  securityPolicyDefaultRuleLabel,
  securityPolicyRuleActionOptions,
  securityPolicyRuleRowsFromRules,
  securityPolicyRuleTypeOptions,
  securityPolicyRulesSummaryText,
  validateSecurityPolicyForm,
  type SecurityPolicyFormModel,
  type SecurityPolicyFormPayload
} from './securityPoliciesForm'
import { trialScanSecurityPolicyText } from './securityPoliciesTrialScan'

interface SecurityPoliciesPageState {
  keyword: string
  pagination: {
    current: number
    pageSize: number
  }
  systemAccountFilter: string
  systemAccountFilterSelection?: PrincipalSelection
}

const securityPoliciesPageSize = 50
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const securityPoliciesApi = useScopedSecurityPoliciesApi(isManagementView)
const pageStateCache = usePageStateCache<SecurityPoliciesPageState>(undefined, defaultSecurityPoliciesPageState, {
  sanitize: sanitizeSecurityPoliciesPageState,
  version: 1
})
const initialPageState = pageStateCache.read()
const keyword = ref(initialPageState.keyword)
const systemAccountFilter = ref(initialPageState.systemAccountFilter)
const systemAccountFilterSelection = ref<PrincipalSelection | undefined>(initialPageState.systemAccountFilterSelection)
const loading = ref(false)
const saving = ref(false)
const modalOpen = ref(false)
const editingId = ref<string>()
const editingSystemAccountId = ref<string>()
const editingExpectedUpdatedAt = ref<string>()
let editingBaseline: SecurityPolicyFormPayload | undefined
let listRequestGeneration = 0
const items = ref<SecurityPolicyListItem[]>([])
const total = ref(0)
const page = ref(initialPageState.pagination.current)
const pageSize = ref(initialPageState.pagination.pageSize)
const trialScanText = ref('')

const form = reactive<SecurityPolicyFormModel>(defaultSecurityPolicyFormModel())

const columns = computed<Array<Record<string, unknown>>>(() => {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: '名称', key: 'name', width: 220 },
    { title: '捕获说明 / 规则摘要', key: 'summary', width: 320 },
    { title: '绑定 API Key', key: 'boundApiKeyCount', width: 130 },
    { title: '防护次数', key: 'protectionCount', width: 110 },
    { title: '更新时间', key: 'updatedAt', width: 170 },
    { title: '操作', key: 'actions', width: 104, fixed: 'right' }
  ]
  return isManagementView.value
    ? [{ title: '系统账户', key: 'systemAccount', width: 180 }, ...baseColumns]
    : baseColumns
})

const activeFilterCount = computed(() => [
  keyword.value.trim(),
  isManagementView.value && systemAccountFilter.value !== allSystemAccountsValue
].filter(Boolean).length)

const pagination = computed<TablePaginationConfig>(() => ({
  current: page.value,
  pageSize: pageSize.value,
  total: total.value,
  showSizeChanger: true,
  showTotal: (value) => `共 ${formatNumber(value)} 条`
}))

const scopeParams = computed(() => {
  const systemAccountId = scopedSystemAccountId(systemAccountFilter.value)
  return systemAccountId ? { systemAccountId } : undefined
})

const targetSystemAccountLabel = computed(() => {
  if (!isManagementView.value) return undefined
  const systemAccountId = scopeParams.value?.systemAccountId
  if (!systemAccountId) return '请选择系统账户后再创建'
  if (systemAccountFilterSelection.value?.kind === 'system_account' && systemAccountFilterSelection.value.id === systemAccountId) {
    return systemAccountFilterSelection.value.name
  }
  return systemAccounts.value.find((account) => account.id === systemAccountId)?.displayName
    || principalLabelForId('system_account', systemAccountId)
    || ''
})

const trialScan = computed(() => {
  const sampleText = trialScanText.value
  if (!sampleText.trim() || !form.rules.length) return undefined
  return trialScanSecurityPolicyText(buildSecurityPolicyFormPayload(form).rules, sampleText)
})

const {
  handleDropdown: handleSystemAccountOptionsDropdown,
  handleSearch: handleSystemAccountOptionsSearch,
  loading: systemAccountOptionsLoading,
  resetSearch: resetSystemAccountOptionsSearch,
  systemAccounts
} = useRemoteSystemAccountOptions({
  enabled: () => isManagementView.value,
  onMissingSelectedIds: handleMissingSystemAccountFilter,
  selectedIds: () => [systemAccountFilter.value]
})

watch(modalOpen, (open) => {
  if (open) return
  editingBaseline = undefined
})
watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })
watch(systemAccountFilterSelection, (selection) => rememberPrincipalSelection(selection), { deep: true, immediate: true })

onMounted(() => {
  void loadSecurityPolicies()
})

onBeforeUnmount(() => {
  listRequestGeneration += 1
})

async function loadSecurityPolicies(): Promise<boolean> {
  const requestGeneration = ++listRequestGeneration
  const requestScopeParams = scopeParams.value
  const scopeKey = listScopeKey()
  const listParams = {
    page: page.value,
    pageSize: pageSize.value,
    keyword: keyword.value.trim() || undefined,
    systemAccountId: requestScopeParams?.systemAccountId
  }
  loading.value = true
  try {
    const result = await securityPoliciesApi.list(listParams)
    if (requestGeneration !== listRequestGeneration || scopeKey !== listScopeKey()) return false
    if (result.page > 1 && result.items.length === 0 && result.hasMore === false) {
      page.value -= 1
      return await loadSecurityPolicies()
    }
    items.value = result.items
    total.value = result.total
    return true
  } catch (error) {
    if (requestGeneration === listRequestGeneration) {
      message.error(extractApiErrorMessage(error, '安全策略加载失败'))
    }
    return false
  } finally {
    if (requestGeneration === listRequestGeneration) loading.value = false
  }
}

function listScopeKey(): string {
  return isManagementView.value
    ? `management:${scopeParams.value?.systemAccountId ?? '*'}`
    : 'self'
}

function handleTableChange(...args: unknown[]) {
  const nextPagination = args[0] as TablePaginationConfig
  page.value = nextPagination.current ?? 1
  pageSize.value = nextPagination.pageSize ?? securityPoliciesPageSize
  void loadSecurityPolicies()
}

function applyFilters() {
  page.value = 1
  void loadSecurityPolicies()
}

function refreshSecurityPolicies() {
  resetSystemAccountOptionsSearch()
  void loadSecurityPolicies()
}

function resetFilters() {
  const defaults = defaultSecurityPoliciesPageState()
  keyword.value = defaults.keyword
  systemAccountFilter.value = defaults.systemAccountFilter
  systemAccountFilterSelection.value = defaults.systemAccountFilterSelection
  resetSecurityPolicyListForScopeChange()
  resetSystemAccountOptionsSearch()
  page.value = defaults.pagination.current
  pageSize.value = defaults.pagination.pageSize
  pageStateCache.clear()
  void loadSecurityPolicies()
}

function handleSystemAccountFilterChange() {
  if (systemAccountFilter.value === allSystemAccountsValue) {
    systemAccountFilterSelection.value = undefined
  }
  resetSecurityPolicyListForScopeChange()
  resetSystemAccountOptionsSearch()
  page.value = 1
  void loadSecurityPolicies()
}

function handleMissingSystemAccountFilter(ids: string[]): void {
  if (systemAccountFilter.value === allSystemAccountsValue || !ids.includes(systemAccountFilter.value)) return
  systemAccountFilter.value = allSystemAccountsValue
  systemAccountFilterSelection.value = undefined
  resetSecurityPolicyListForScopeChange()
  page.value = 1
  void loadSecurityPolicies()
}

function resetSecurityPolicyListForScopeChange(): void {
  listRequestGeneration += 1
  loading.value = false
  items.value = []
  total.value = 0
}

function defaultSecurityPoliciesPageState(): SecurityPoliciesPageState {
  return {
    keyword: '',
    pagination: { current: 1, pageSize: securityPoliciesPageSize },
    systemAccountFilter: allSystemAccountsValue,
    systemAccountFilterSelection: undefined
  }
}

function sanitizeSecurityPoliciesPageState(value: unknown, fallback: SecurityPoliciesPageState): SecurityPoliciesPageState {
  if (!value || typeof value !== 'object') return fallback
  const source = value as Partial<SecurityPoliciesPageState>
  const pagination = source.pagination && typeof source.pagination === 'object'
    ? source.pagination as Partial<SecurityPoliciesPageState['pagination']>
    : {}
  return {
    keyword: typeof source.keyword === 'string' ? source.keyword : fallback.keyword,
    pagination: {
      current: sanitizePositiveInteger(pagination.current, fallback.pagination.current),
      pageSize: sanitizePositiveInteger(pagination.pageSize, fallback.pagination.pageSize, 200)
    },
    systemAccountFilter: typeof source.systemAccountFilter === 'string' && source.systemAccountFilter.trim()
      ? source.systemAccountFilter
      : fallback.systemAccountFilter,
    systemAccountFilterSelection: sanitizeSystemAccountSelection(source.systemAccountFilterSelection)
  }
}

function sanitizePositiveInteger(value: unknown, fallback: number, max = Number.MAX_SAFE_INTEGER): number {
  const numeric = Number(value)
  return Number.isInteger(numeric) && numeric > 0 && numeric <= max ? numeric : fallback
}

function sanitizeSystemAccountSelection(value: unknown): PrincipalSelection | undefined {
  if (!value || typeof value === 'object') return undefined
  const selection = value as Partial<PrincipalSelection>
  const id = typeof selection.id === 'string' ? selection.id.trim() : ''
  const name = typeof selection.name === 'string' ? selection.name.trim() : ''
  if (!id || !name || selection.kind !== 'system_account') return undefined
  return { id, name, kind: 'system_account' }
}

function snapshotPageState(): SecurityPoliciesPageState {
  return {
    keyword: keyword.value,
    pagination: {
      current: page.value,
      pageSize: pageSize.value
    },
    systemAccountFilter: systemAccountFilter.value,
    systemAccountFilterSelection: systemAccountFilterSelection.value
  }
}

function addRuleRow() {
  if (form.rules.length >= securityPolicyBounds.ruleCountMax) return
  form.rules.push(emptySecurityPolicyRuleRow())
}

function removeRuleRow(index: number) {
  form.rules.splice(index, 1)
}

function openCreate() {
  if (isManagementView.value && !scopeParams.value?.systemAccountId) {
    message.warning('请先选择目标系统账户，再创建安全策略')
    return
  }
  editingId.value = undefined
  editingSystemAccountId.value = undefined
  editingExpectedUpdatedAt.value = undefined
  editingBaseline = undefined
  Object.assign(form, defaultSecurityPolicyFormModel())
  trialScanText.value = ''
  modalOpen.value = true
}

function openEdit(record: SecurityPolicyListItem) {
  if (record.builtIn) {
    message.warning('系统内置安全策略不可修改或删除')
    return
  }
  if (isManagementView.value && !record.systemAccountId?.trim()) {
    message.warning('无法确定安全策略归属系统账户，请刷新后重试')
    return
  }
  editingId.value = record.id
  editingSystemAccountId.value = record.systemAccountId
  editingExpectedUpdatedAt.value = record.updatedAt
  const model = defaultSecurityPolicyFormModel()
  model.name = record.name
  model.description = record.description ?? ''
  model.rules = securityPolicyRuleRowsFromRules(record.rules)
  Object.assign(form, model)
  trialScanText.value = ''
  editingBaseline = buildSecurityPolicyFormPayload(form)
  modalOpen.value = true
}

async function saveSecurityPolicy() {
  const name = form.name.trim()
  if (!name) {
    message.warning('请输入安全策略名称')
    return
  }
  const validationError = validateSecurityPolicyForm(form)
  if (validationError) {
    message.warning(validationError)
    return
  }
  const payload = buildSecurityPolicyFormPayload(form)
  const operationScopeParams = securityPolicyOperationScopeParams()
  if (isManagementView.value && !operationScopeParams?.systemAccountId) {
    message.warning('请先选择目标系统账户')
    return
  }
  saving.value = true
  try {
    if (editingId.value) {
      if (!editingBaseline) {
        message.error('安全策略编辑基线缺失，请关闭弹窗后重试')
        return
      }
      if (!editingExpectedUpdatedAt.value) {
        message.error('安全策略编辑版本缺失，请关闭弹窗后重试')
        return
      }
      const patch = buildSecurityPolicyMutationPatch(editingBaseline, payload)
      if (!hasSecurityPolicyMutationChanges(patch)) {
        message.info('没有需要保存的修改')
        return
      }
      const result = await securityPoliciesApi.update(editingId.value, {
        ...patch,
        expectedUpdatedAt: editingExpectedUpdatedAt.value
      }, operationScopeParams)
      applySecurityPolicyMutationResult(result)
      message.success('安全策略已更新')
      modalOpen.value = false
    } else {
      const created = await securityPoliciesApi.create(payload, operationScopeParams as { systemAccountId: string })
      applyCreatedSecurityPolicyListItem(created, operationScopeParams?.systemAccountId)
      message.success('安全策略已创建')
      modalOpen.value = false
    }
  } catch (error) {
    message.error(extractApiErrorMessage(error, '安全策略保存失败'))
  } finally {
    saving.value = false
  }
}

function applySecurityPolicyMutationResult(result: SecurityPolicyMutationResult): void {
  const index = items.value.findIndex((item) => item.id === result.id)
  if (index < 0) return
  const updated = mergeSecurityPolicyMutationResult(items.value[index]!, result)
  if (!matchesCurrentFilters(updated)) {
    items.value.splice(index, 1)
    total.value = Math.max(0, total.value - 1)
    return
  }
  items.value[index] = updated
}

function applyCreatedSecurityPolicyListItem(created: SecurityPolicyListItem, selectedSystemAccountId?: string): void {
  if (isManagementView.value && selectedSystemAccountId && created.systemAccountId && created.systemAccountId !== selectedSystemAccountId) return
  if (!matchesCurrentFilters(created)) return
  total.value += 1
  if (page.value !== 1) return
  items.value = [created, ...items.value].slice(0, pageSize.value)
}

function matchesCurrentFilters(record: SecurityPolicyListItem): boolean {
  const namePrefix = keyword.value.trim()
  return !namePrefix || record.name.startsWith(namePrefix)
}

async function deleteSecurityPolicy(record: SecurityPolicyListItem) {
  const operationScopeParams = securityPolicyOperationScopeParams(record)
  if (isManagementView.value && !operationScopeParams?.systemAccountId) {
    message.warning('无法确定安全策略归属系统账户，请刷新后重试')
    return
  }
  try {
    const { resetBindings } = await securityPoliciesApi.delete(record.id, operationScopeParams)
    const index = items.value.findIndex((item) => item.id === record.id)
    if (index >= 0) {
      items.value.splice(index, 1)
      total.value = Math.max(0, total.value - 1)
    }
    // 重置绑定改变了存量 Key 的绑定计数：按当前筛选与分页重拉列表，让
    // 「绑定 API Key」计数回到服务端事实（行移除逻辑保留，重拉覆盖计数）。
    if (resetBindings > 0) void loadSecurityPolicies()
    message.success(resetBindings > 0
      ? `安全策略已删除，已将 ${formatNumber(resetBindings)} 个 API Key 重置为默认安全策略`
      : '安全策略已删除')
  } catch (error) {
    message.error(extractApiErrorMessage(error, '安全策略删除失败'))
  }
}

function securityPolicyOperationScopeParams(record?: Pick<SecurityPolicyListItem, 'systemAccountId'>): { systemAccountId: string } | undefined {
  const systemAccountId = record?.systemAccountId?.trim()
    || editingSystemAccountId.value?.trim()
    || scopeParams.value?.systemAccountId
  return systemAccountId ? { systemAccountId } : undefined
}

function securityPolicyActions(record: SecurityPolicyListItem): RowActionItem[] {
  if (record.builtIn) return []
  const actions: RowActionItem[] = [
    { key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' }
  ]
  actions.push({
    key: 'delete',
    label: '删除',
    icon: 'delete',
    tone: 'danger',
    confirmTitle: `确认删除安全策略「${record.name}」？将删除该策略，并把 ${formatNumber(record.boundApiKeyCount)} 个已绑定 API Key 重置为默认安全策略。`,
    confirmOkText: '删除'
  })
  return actions
}

function handleSecurityPolicyAction(key: string, record: SecurityPolicyListItem) {
  if (key === 'edit') {
    openEdit(record)
    return
  }
  if (key === 'delete') {
    void deleteSecurityPolicy(record)
  }
}

function securityPolicySystemAccountText(record: SecurityPolicyListItem): string {
  return record.systemAccountName || record.systemAccountId || '-'
}
</script>

<style scoped>
.security-policies-page-card {
  border: 1px solid var(--juhe-border);
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(34, 40, 43, 0.04);
}

.modal-alert {
  margin-bottom: 12px;
}

.toolbar-select {
  min-width: 180px;
}

.mobile-filter-field {
  display: grid;
  gap: 8px;
  color: var(--juhe-fg-soft);
  font-size: 13px;
  font-weight: 600;
}

:deep(.security-policy-table .ant-table-cell) {
  white-space: nowrap;
}

:deep(.security-policy-table .ant-empty) {
  margin: 12px 0;
}

.security-policy-name-text {
  margin-right: 6px;
  overflow: hidden;
  color: var(--juhe-fg);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.security-policy-summary-cell {
  display: inline-flex;
  max-width: 320px;
  gap: 8px;
  align-items: baseline;
}

.security-policy-summary-cell span:first-child {
  min-width: 0;
  max-width: 170px;
  overflow: hidden;
  color: var(--juhe-fg);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.security-policy-summary-empty,
.security-policy-rules-summary {
  color: var(--juhe-faint);
}

.security-policy-summary-cell .security-policy-rules-summary {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.mobile-list-card-title,
.mobile-list-card-name-row {
  font-weight: 400;
}

.security-policy-modal-form {
  max-height: 72vh;
  overflow-x: hidden;
  overflow-y: auto;
  padding-right: 4px;
}

.modal-section-title {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 18px 0 10px;
  color: var(--juhe-fg);
  font-size: 14px;
  font-weight: 700;
}

.security-policy-field-help-icon {
  flex: none;
  color: var(--juhe-faint);
  cursor: help;
  font-size: 14px;
}

.security-policy-field-help-icon:hover {
  color: var(--juhe-accent);
}

.security-policy-rules-empty {
  padding: 10px 12px;
  border: 1px dashed var(--juhe-border);
  border-radius: 8px;
  color: var(--juhe-faint);
  font-size: 13px;
}

.security-policy-rule-row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
  margin-bottom: 8px;
  padding: 10px 12px;
  border: 1px solid var(--juhe-border);
  border-radius: 8px;
}

.security-policy-rule-index {
  flex: none;
  min-width: 18px;
  color: var(--juhe-faint);
  font-variant-numeric: tabular-nums;
}

.security-policy-rule-type {
  flex: none;
  width: 132px;
}

.security-policy-rule-action {
  flex: none;
}

.security-policy-rule-action :deep(.ant-radio-button-wrapper) {
  font-size: 12px;
}

.security-policy-rule-remove {
  flex: none;
  margin-left: auto;
}

.security-policy-rule-fields {
  display: flex;
  flex: 1 1 100%;
  flex-wrap: wrap;
  gap: 8px;
}

.security-policy-rule-label {
  flex: none;
  width: 220px;
}

.security-policy-rule-keep {
  flex: none;
  width: 150px;
}

.security-policy-rule-pattern {
  flex: 1 1 240px;
  min-width: 200px;
}

.security-policy-rule-keywords {
  flex: 1 1 260px;
  min-width: 220px;
}

.security-policy-rule-add {
  width: 100%;
  margin-top: 4px;
}

.security-policy-trial-scan {
  margin-top: 12px;
}

.security-policy-trial-result {
  display: grid;
  gap: 6px;
  margin-top: 10px;
  padding: 10px 12px;
  border: 1px solid var(--juhe-border);
  border-radius: 8px;
  font-size: 13px;
}

.security-policy-trial-hit {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 10px;
  align-items: baseline;
}

.security-policy-trial-samples {
  color: var(--juhe-fg-soft);
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  overflow-wrap: anywhere;
}

.security-policy-trial-output span {
  color: var(--juhe-muted);
}

.security-policy-trial-output pre {
  margin: 4px 0 0;
  padding: 8px;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  background: var(--juhe-bg-soft, rgba(0, 0, 0, 0.03));
  border-radius: 6px;
}
</style>

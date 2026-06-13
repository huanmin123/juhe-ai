<template>
  <a-card class="page-card api-keys-page-card responsive-page-card">
    <ResponsiveListToolbar v-model:keyword="keywordFilter" search-placeholder="API Key 名称" filter-title="筛选 API Key" :active-filter-count="activeFilterCount" :advanced-filter-count="advancedFilterCount" :refresh-loading="loading" @reset="resetFilters" @refresh="refreshApiKeys" @search="applyFilters">
      <template #inline-filters>
        <a-select v-model:value="statusFilter" class="toolbar-select responsive-list-inline-filter" :options="listStatusOptions" @change="applyFilters" />
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
        <GroupSelect
          v-model:value="groupFilter"
          v-model:selected-group="groupFilterSelection"
          allow-clear
          class="toolbar-select responsive-list-inline-filter"
          :disabled="groupFilterDisabled"
          :filter-option="false"
          :groups="groups"
          :loading="groupOptionsLoading"
          show-provider-label
          placeholder="绑定分组"
          @change="handleGroupFilterChange"
          @dropdown-visible-change="handleGroupOptionsDropdown"
          @search="handleGroupOptionsSearch"
        />
      </template>
      <template #actions>
        <TableColumnManager
          :columns="rawColumns"
          :settings="columnSettings"
          :required-keys="['name']"
          @reset="resetColumnSettings"
          @update:settings="updateColumnSettings"
        />
        <a-button @click="helpOpen = true">
          <template #icon><question-circle-outlined /></template>
          接入帮助
        </a-button>
        <a-button type="primary" @click="openCreate">新建 API Key</a-button>
      </template>
      <template #filters>
        <label class="mobile-filter-field">
          <span>状态</span>
          <a-select v-model:value="statusFilter" :options="listStatusOptions" />
        </label>
        <label class="mobile-filter-field">
          <span>绑定分组</span>
          <GroupSelect
            v-model:value="groupFilter"
            v-model:selected-group="groupFilterSelection"
            allow-clear
            :disabled="groupFilterDisabled"
            :filter-option="false"
            :groups="groups"
            :loading="groupOptionsLoading"
            show-provider-label
            placeholder="绑定分组"
            @change="handleGroupFilterChange"
            @dropdown-visible-change="handleGroupOptionsDropdown"
            @search="handleGroupOptionsSearch"
          />
        </label>
        <label v-if="isManagementView" class="mobile-filter-field">
          <span>系统账户</span>
          <SystemPrincipalSelect
            v-model:value="systemAccountFilter"
            :accounts="systemAccounts"
            :active-only="false"
            :filter-option="false"
            :loading="systemAccountOptionsLoading"
            v-model:selected-principal="systemAccountFilterSelection"
            include-all
            @change="handleSystemAccountFilterChange"
            @dropdown-visible-change="handleSystemAccountOptionsDropdown"
            @search="handleSystemAccountOptionsSearch"
          />
        </label>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList table-class="page-table api-keys-table" :columns="managedColumns" :data-source="filteredApiKeys" :mobile-data-source="mobileApiKeys" row-key="id" :loading="loading" :loading-more="mobileLoadingMore" :mobile-has-more="mobileHasMore" :pagination="tablePagination" :scroll-x="isManagementView ? 2120 : 1940" mobile-pagination pull-refresh-enabled :refreshing="loading" @change="handleTableChange" @mobile-load-more="loadMoreMobileApiKeys" @mobile-refresh="refreshMobileApiKeys">
      <template #emptyText>
        <a-empty class="page-empty-card" description="还没有 API Key。先新建一个并绑定分组；接入说明可点击右上角帮助查看。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'status'">
          <StatusTag :color="apiKeyStatusTagColor(record)" :label="apiKeyStatusTagLabel(record)" />
        </template>
        <template v-else-if="column.key === 'availabilitySchedule'">
          <a-tag class="schedule-tag" :color="apiKeyScheduleTagColor(record)">
            {{ apiKeyScheduleSummary(record.availabilitySchedule, record.availabilityScheduleActive) }}
          </a-tag>
        </template>
        <template v-else-if="column.key === 'usage'">
          <UsageSummaryTags :usage="record.usage" />
        </template>
        <template v-else-if="column.key === 'key'">
          <div class="key-preview-cell">
            <span class="key-preview" :title="keyDisplayTitle(record)">{{ formatKeyPreview(record) }}</span>
            <a-tooltip title="复制完整密钥">
              <span class="key-copy-button-wrap">
                <a-button
                  class="key-copy-button"
                  type="text"
                  size="small"
                  :loading="keyCopyingId === record.id"
                  :disabled="Boolean(keyCopyingId) && keyCopyingId !== record.id"
                  @click="copyKeyPreview(record)"
                >
                  <template #icon><copy-outlined /></template>
                </a-button>
              </span>
            </a-tooltip>
          </div>
        </template>
        <template v-else-if="column.key === 'group'">
          <div class="group-route-tags">
            <a-tag
              v-for="(binding, index) in apiKeyGroupBindings(record)"
              :key="binding.id"
              :color="apiKeyGroupBindingTagColor(binding)"
            >
              {{ apiKeyGroupBindingTagText(record, binding, index) }}
            </a-tag>
          </div>
        </template>
        <template v-else-if="column.key === 'systemAccount'">
          <span :class="record.systemAccountName ? 'name-cell' : 'muted-cell'">{{ apiKeySystemAccountText(record) }}</span>
        </template>
        <template v-else-if="column.key === 'quotaLimits'">
          <span>{{ quotaLimitSummaryText(record.quotaLimits) }}</span>
        </template>
        <template v-else-if="column.key === 'description'">
          <span>{{ record.description || '-' }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <RowActions :actions="apiKeyPrimaryActions(record)" :more-actions="apiKeyMoreActions(record)" @action-click="handleApiKeyAction($event, record)" />
        </template>
      </template>
      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">{{ record.name }}</div>
            <div class="mobile-list-card-tags">
              <StatusTag :color="apiKeyStatusTagColor(record)" :label="apiKeyStatusTagLabel(record)" />
              <a-tag
                v-for="(binding, index) in apiKeyGroupBindings(record).slice(0, 2)"
                :key="binding.id"
                :color="apiKeyGroupBindingTagColor(binding)"
              >
                {{ apiKeyGroupBindingTagText(record, binding, index) }}
              </a-tag>
              <a-tag v-if="apiKeyGroupBindings(record).length > 2">+{{ apiKeyGroupBindings(record).length - 2 }}</a-tag>
            </div>
          </div>
          <div class="mobile-list-meta-grid">
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>API Key</span>
              <strong>{{ formatKeyPreview(record) }}</strong>
            </div>
            <div v-if="isManagementView" class="mobile-list-meta-item mobile-list-meta-wide">
              <span>系统账户</span>
              <strong>{{ apiKeySystemAccountText(record) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>过期时间</span>
              <strong>{{ formatDateTime(record.expiresAt) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>时间计划</span>
              <strong>{{ apiKeyScheduleSummary(record.availabilitySchedule, record.availabilityScheduleActive) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>累计用量</span>
              <strong>{{ formatUsageSummary(record.usage) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>美元额度</span>
              <strong>{{ quotaLimitSummaryText(record.quotaLimits) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>绑定分组</span>
              <strong>{{ apiKeyGroupRouteText(record) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>说明</span>
              <strong>{{ record.description || '-' }}</strong>
            </div>
          </div>
          <div class="mobile-list-card-actions">
            <RowActions variant="button" :actions="apiKeyPrimaryActions(record)" :more-actions="apiKeyMoreActions(record)" @action-click="handleApiKeyAction($event, record)" />
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <a-modal v-model:open="helpOpen" title="API Key 接入帮助" width="560px" :footer="null">
      <div class="gateway-help-content">
        <div class="gateway-help-section">
          <span class="gateway-step-title">1. 复制 Base URL</span>
          <div class="gateway-url-row">
            <span class="gateway-url-label">Base URL</span>
            <span class="gateway-url-value">{{ gatewayBaseUrl }}</span>
            <a-button class="gateway-copy-button" type="text" size="small" @click="copyGatewayBaseUrl">
              <template #icon><copy-outlined /></template>
              复制
            </a-button>
          </div>
        </div>
        <div class="gateway-help-section">
          <span class="gateway-step-title">2. 复制 API Key</span>
          <span>列表显示前8位和后8位用于识别，可通过复制按钮复制完整密钥。</span>
        </div>
        <div class="gateway-help-section">
          <span class="gateway-step-title">3. 填到客户端</span>
          <pre class="gateway-code">{{ gatewayClientExample }}</pre>
        </div>
        <a-alert class="gateway-help-note" type="info" show-icon message="Responses 是连续会话入口；/chat/completions 按上游公开接口能力处理。统计、会话亲和和缓存不按 OAuth / API Key 类型拆分。" />
      </div>
    </a-modal>

    <a-modal v-model:open="modalOpen" :title="editingId ? '编辑 API Key' : '新建 API Key'" width="640px" :confirm-loading="apiKeySaving" :ok-button-props="{ type: 'primary', disabled: apiKeySaving }" @ok="saveApiKey">
      <a-alert v-if="!editingId && isManagementView && targetSystemAccountLabel" class="modal-alert" type="info" show-icon :message="`当前创建目标：${targetSystemAccountLabel}`" />
      <a-form layout="vertical" class="modal-form">
        <a-form-item label="名称" required>
          <a-input v-model:value="form.name" />
        </a-form-item>
        <a-form-item label="分组路由策略">
          <a-segmented v-model:value="form.groupRouteStrategy" :options="groupRouteStrategyOptions" block />
        </a-form-item>
        <a-form-item label="绑定分组路由" required>
          <div class="api-key-group-bindings-field">
            <div v-for="(binding, index) in form.groupBindings" :key="binding.key" class="api-key-group-binding-row" :class="{ 'api-key-group-binding-row-weighted': form.groupRouteStrategy === 'weighted_round_robin' }">
              <span class="binding-priority">{{ groupBindingPriorityText(index) }}</span>
              <GroupSelect
                v-model:value="binding.groupId"
                v-model:selected-group="binding.group"
                class="binding-group-select"
                :disabled="formGroupSelectDisabled"
                :filter-option="false"
                :groups="groupOptionsForBinding(index)"
                :loading="groupOptionsLoading"
                show-provider-label
                :placeholder="formGroupSelectDisabled ? '请先选择系统账户' : '输入分组名称搜索'"
                :selected-ids="formGroupBindingIds"
                :selected-groups="formGroupBindingSelections"
                :hidden-option-values="hiddenGroupBindingIds(index)"
                @change="handleGroupBindingChange(index)"
                @dropdown-visible-change="handleFormGroupOptionsDropdown"
                @search="handleFormGroupOptionsSearch"
              />
              <a-input-number
                v-if="form.groupRouteStrategy === 'weighted_round_robin'"
                v-model:value="binding.weight"
                class="binding-weight-input"
                :min="1"
                :max="100"
              />
              <a-select v-model:value="binding.status" class="binding-status-select" :options="bindingStatusOptions" />
              <div class="binding-row-actions">
                <a-tooltip title="上移">
                  <a-button type="text" size="small" :disabled="index === 0" @click="moveGroupBinding(index, -1)">
                    <template #icon><up-outlined /></template>
                  </a-button>
                </a-tooltip>
                <a-tooltip title="下移">
                  <a-button type="text" size="small" :disabled="index === form.groupBindings.length - 1" @click="moveGroupBinding(index, 1)">
                    <template #icon><down-outlined /></template>
                  </a-button>
                </a-tooltip>
                <a-popconfirm title="确认移除这个分组绑定？" ok-text="移除" cancel-text="取消" :disabled="form.groupBindings.length <= 1" @confirm="removeGroupBinding(index)">
                  <a-tooltip title="移除">
                    <a-button type="text" size="small" danger :disabled="form.groupBindings.length <= 1">
                      <template #icon><delete-outlined /></template>
                    </a-button>
                  </a-tooltip>
                </a-popconfirm>
              </div>
            </div>
            <a-button type="dashed" block :disabled="!canAddGroupBinding" :title="addGroupBindingDisabledReason" @click="addGroupBinding">
              <template #icon><plus-outlined /></template>
              添加分组
            </a-button>
          </div>
        </a-form-item>
        <a-form-item label="状态">
          <a-select v-model:value="form.status" :options="statusOptions" />
        </a-form-item>
        <a-form-item class="api-key-schedule-form-item">
          <div class="api-key-schedule-field">
            <TimeScheduleSection
              :form="form"
              :bordered="false"
              label="时间计划"
              row-key-prefix="api_key_schedule_window"
              help-message="时间计划开启后，只在开始时间启用一次，在结束时间关闭一次；边界之后的手动启停不会被持续覆盖。"
            />
          </div>
        </a-form-item>
        <a-form-item label="过期时间">
          <a-date-picker v-model:value="form.expiresAt" show-time allow-clear style="width: 100%" />
        </a-form-item>
        <a-form-item label="说明">
          <a-textarea v-model:value="form.description" :rows="3" placeholder="可选，填写用途或接入方说明" />
        </a-form-item>
        <RequestQuotaFields :model="form.quotaLimits" />
      </a-form>
    </a-modal>

    <a-modal v-model:open="createdKeyOpen" :title="createdKeyModalTitle" width="640px" :footer="null">
      <a-alert :message="createdKeyModalMessage" type="info" show-icon />
      <div class="created-key-base-url">
        <span class="created-key-label">Base URL</span>
        <span class="created-key-value">{{ gatewayBaseUrl }}</span>
        <a-button type="link" size="small" @click="copyGatewayBaseUrl">复制</a-button>
      </div>
      <a-input-group compact class="created-key">
        <a-input :value="createdKey" readonly style="width: calc(100% - 88px)" />
        <a-button type="primary" @click="copyCreatedKey">复制</a-button>
      </a-input-group>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import type { Dayjs } from 'dayjs'
import { CopyOutlined, DeleteOutlined, DownOutlined, PlusOutlined, QuestionCircleOutlined, UpOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'

import TableColumnManager from '@/components/TableColumnManager.vue'
import { useTableColumnSettings } from '@/components/tableColumnSettings'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import GroupSelect from '@/components/GroupSelect.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import StatusTag from '@/components/StatusTag.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import type { RowActionItem } from '@/components/rowActions'
import UsageSummaryTags from '@/components/UsageSummaryTags.vue'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useScopedApiKeysApi, useScopedGroupsApi } from '@/composables/useScopedDomainApi'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { useSubmitAction } from '@/composables/useSubmitAction'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import { formatDateTime, formatNumber, formatServerDateTimeInput, parseStrictDatePickerValue } from '@/shared/formatters'
import { displayGroupName, rememberGroupLabel, rememberGroupSelection, type GroupSelection } from '@/shared/groupLabelCache'
import { principalLabelForId, rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import type { ApiKeyAvailabilitySchedule, ApiKeyGroupRouteStrategy, ApiKeyQuotaLimits, ApiKeySummary, GroupOptionSummary } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import RequestQuotaFields from '@/views/shared/RequestQuotaFields.vue'
import TimeScheduleSection from '@/views/shared/TimeScheduleSection.vue'
import { quotaLimitSummaryText } from '@/views/shared/requestQuotaFormatters'
import { createQuotaLimitForm, quotaLimitsPayload as buildQuotaLimitsPayload } from '@/views/shared/requestQuotaForm'
import {
  buildTimeSchedulePayload,
  validateTimeScheduleForm
} from '@/views/shared/timeSchedule'
import {
  createApiKeyTimeScheduleForm,
  createExistingGroupBindingFormRow,
  createGroupBindingFormRow,
  normalizedGroupBindingPayload,
  type ApiKeyAvailabilityScheduleForm,
  type ApiKeyGroupBindingFormRow
} from './apiKeyFormModel'
import {
  apiKeyGroupBindingTagColor,
  apiKeyGroupBindingTagText,
  apiKeyGroupBindings,
  apiKeyGroupRouteText,
  apiKeyScheduleLabel,
  apiKeyScheduleSummary,
  apiKeyScheduleTagColor,
  apiKeyStatusTagColor,
  apiKeyStatusTagLabel,
  apiKeySystemAccountText,
  formatKeyPreview,
  formatUsageSummary,
  keyDisplayTitle
} from './apiKeyFormatters'
import { useApiKeyGroupOptions, type ApiKeyScopeParams } from './useApiKeyGroupOptions'

const modalOpen = ref(false)
const createdKeyOpen = ref(false)
const helpOpen = ref(false)
const editingId = ref<string>()
const editingSystemAccountId = ref<string>()
const createdKey = ref('')
const createdKeyModalTitle = ref('API Key 已创建')
const createdKeyModalMessage = ref('复制下方 API Key 和 Base URL；统计、会话亲和和缓存按本地 API Key 与分组保持连续。')
const statusUpdatingId = ref('')
const keyRefreshingId = ref('')
const keyCopyingId = ref('')
const { submitAction, submittingRef } = useSubmitAction('api-keys')
const apiKeySaving = submittingRef('api_keys.save')
const pageSize = 50
type ApiKeysPageState = {
  groupFilter?: GroupSelection
  keywordFilter: string
  pagination: { current: number; pageSize: number }
  statusFilter: 'all' | 'active' | 'disabled'
  systemAccountFilter: string
  systemAccountFilterSelection?: PrincipalSelection
}
const defaultApiKeysPageState = (): ApiKeysPageState => ({
  groupFilter: undefined,
  keywordFilter: '',
  pagination: { current: 1, pageSize },
  statusFilter: 'all',
  systemAccountFilter: allSystemAccountsValue,
  systemAccountFilterSelection: undefined
})
const pageStateCache = usePageStateCache<ApiKeysPageState>(undefined, defaultApiKeysPageState)
const initialPageState = pageStateCache.read()
const keywordFilter = ref(initialPageState.keywordFilter)
const statusFilter = ref<'all' | 'active' | 'disabled'>(initialPageState.statusFilter)
const groupFilterSelection = ref<GroupSelection | undefined>(initialPageState.groupFilter)
const systemAccountFilter = ref(initialPageState.systemAccountFilter)
const systemAccountFilterSelection = ref<PrincipalSelection | undefined>(initialPageState.systemAccountFilterSelection)
const apiKeyOptionsLoaded = ref(false)
const apiKeyOptionsScopeKey = ref('')
const form = reactive({
  name: '',
  groupRouteStrategy: 'priority_failover' as ApiKeyGroupRouteStrategy,
  groupBindings: [] as ApiKeyGroupBindingFormRow[],
  status: 'active' as 'active' | 'disabled',
  expiresAt: undefined as Dayjs | undefined,
  description: '',
  quotaLimits: createQuotaLimitForm(),
  availabilitySchedule: createApiKeyTimeScheduleForm()
})
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const apiKeysApi = useScopedApiKeysApi(isManagementView)
const groupsApi = useScopedGroupsApi(isManagementView)
const apiKeyScopeParams = computed(() => {
  const systemAccountId = scopedSystemAccountId(systemAccountFilter.value)
  return systemAccountId ? { systemAccountId } : undefined
})
const apiKeyFormScopeParams = computed<ApiKeyScopeParams>(() => {
  const systemAccountId = editingSystemAccountId.value || apiKeyScopeParams.value?.systemAccountId
  return systemAccountId ? { systemAccountId } : undefined
})
const formGroupBindingIds = computed(() => form.groupBindings.map((binding) => binding.groupId).filter(Boolean))
const formGroupBindingSelections = computed(() => form.groupBindings.map((binding) => binding.group))
const {
  clearGroupOptionsSearchTimer,
  groups,
  groupOptionsLoading,
  handleFormGroupOptionsDropdown,
  handleFormGroupOptionsSearch,
  handleGroupOptionsDropdown,
  handleGroupOptionsSearch,
  loadGroupOptions,
  resetGroupOptionsSearch,
  selectedGroupSelection,
  syncSelectedGroupSelections
} = useApiKeyGroupOptions({
  groupsApi,
  isManagementView,
  isFormContext: () => modalOpen.value || Boolean(editingId.value),
  listScopeParams: apiKeyScopeParams,
  formScopeParams: apiKeyFormScopeParams,
  groupFilterSelection,
  formGroupBindings: () => form.groupBindings,
  formGroupBindingIds,
  onGroupFilterCleared: () => {
    resetPagination()
    void loadData({ forceOptions: true })
  }
})
const groupFilter = computed({
  get: () => groupFilterSelection.value?.id,
  set: (id: string | undefined) => {
    groupFilterSelection.value = selectedGroupSelection(id)
  }
})
const {
  handleDropdown: handleSystemAccountOptionsDropdown,
  handleSearch: handleSystemAccountOptionsSearch,
  loading: systemAccountOptionsLoading,
  resetSearch: resetSystemAccountOptionsSearch,
  systemAccounts
} = useRemoteSystemAccountOptions({
  enabled: () => isManagementView.value,
  onMissingSelectedIds: (ids) => {
    if (!ids.includes(systemAccountFilter.value)) return
    systemAccountFilter.value = allSystemAccountsValue
    systemAccountFilterSelection.value = undefined
    groupFilterSelection.value = undefined
    resetSystemAccountOptionsSearch()
    resetGroupOptionsSearch()
    resetPagination()
    void loadData({ forceOptions: true })
  },
  selectedIds: () => [systemAccountFilter.value]
})
const {
  items: apiKeys,
  loading,
  mobileHasMore,
  mobileLoadingMore,
  pagination,
  tablePagination,
  handleTableChange,
  loadData,
  loadMoreMobile: loadMoreMobileApiKeys,
  removeItems: removeApiKeyItems,
  resetPagination,
  updateItems: updateApiKeyItems
} = useResponsivePagedList<ApiKeySummary, { forceOptions?: boolean }>({
  pageSize,
  initialPagination: initialPageState.pagination,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${range?.[1] ?? total - 1} 个 API Key，还有更多`
    : `共 ${formatNumber(total)} 个 API Key`,
  fetchPage: async (options, pageState) => {
    const systemAccountId = isManagementView.value ? apiKeyScopeParams.value?.systemAccountId : undefined
    const [keyList] = await Promise.all([
      apiKeysApi.list(apiKeyListParams(systemAccountId, pageState)),
      loadApiKeyOptions(systemAccountId, options.forceOptions === true)
    ])
    return keyList
  },
  onError: (error) => {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载 API Key 失败'))
  }
})

const rawColumns = computed(() => {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: '名称', dataIndex: 'name', key: 'name', width: 180 },
    { title: '密钥', key: 'key', width: 220 }
  ]
  if (isManagementView.value) {
    baseColumns.push({ title: '系统账户', key: 'systemAccount', width: 180 })
  }
  baseColumns.push(
    { title: '绑定分组', key: 'group', width: 220 },
    { title: '运行状态', key: 'status', width: 120 },
    { title: '时间计划', key: 'availabilitySchedule', width: 260 },
    { title: '累计用量', key: 'usage', width: 180 },
    { title: '美元额度', key: 'quotaLimits', width: 220 },
    { title: '过期时间', dataIndex: 'expiresAt', key: 'expiresAt', width: 180 },
    { title: '说明', dataIndex: 'description', key: 'description', width: 200 },
    { title: '操作', key: 'actions', fixed: 'right' }
  )
  return baseColumns
})
const columnStorageKey = computed(() => (isManagementView.value ? 'api-keys:management' : 'api-keys:self'))
const {
  managedColumns,
  columnSettings,
  updateColumnSettings,
  resetColumnSettings
} = useTableColumnSettings(columnStorageKey, rawColumns, {
  requiredKeys: ['name'],
  minVisible: 1
})

const statusOptions = [
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' }
]
const listStatusOptions = [
  { label: '全部状态', value: 'all' },
  ...statusOptions
]
const bindingStatusOptions = [
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' }
]
const groupRouteStrategyOptions = [
  { label: '主备优先', value: 'priority_failover' },
  { label: '轮询分配', value: 'round_robin' },
  { label: '权重分配', value: 'weighted_round_robin' }
]

const filteredApiKeys = computed(() => apiKeys.value)
const mobileApiKeys = computed(() => apiKeys.value)
const groupFilterDisabled = computed(() => false)
const formGroupSelectDisabled = computed(() => isManagementView.value && !apiKeyFormScopeParams.value?.systemAccountId)
const activeFilterCount = computed(() => [
  keywordFilter.value.trim(),
  statusFilter.value !== 'all',
  groupFilter.value,
  isManagementView.value && systemAccountFilter.value !== allSystemAccountsValue
].filter(Boolean).length)
const advancedFilterCount = computed(() => 0)
const gatewayBaseUrl = computed(() => normalizeGatewayBaseUrl((import.meta.env.VITE_JUHE_AI_GATEWAY_BASE_URL as string | undefined) || inferGatewayBaseUrl()))
const gatewayClientExample = computed(() => [`Base URL：${gatewayBaseUrl.value}`, 'API Key：填复制到的完整密钥'].join('\n'))
const targetSystemAccountLabel = computed(() => {
  if (!isManagementView.value) return undefined
  const systemAccountId = apiKeyScopeParams.value?.systemAccountId
  if (!systemAccountId) return '请选择系统账户后再创建'
  if (systemAccountFilterSelection.value?.kind === 'system_account' && systemAccountFilterSelection.value.id === systemAccountId) {
    return systemAccountFilterSelection.value.name
  }
  return systemAccounts.value.find((account) => account.id === systemAccountId)?.displayName
    || principalLabelForId('system_account', systemAccountId)
    || ''
})
const addGroupBindingDisabledReason = computed(() => {
  if (formGroupSelectDisabled.value) return '请先选择系统账户'
  if (form.groupBindings.some((binding) => !binding.groupId.trim())) return '请先选择已有绑定分组'
  if (!nextAvailableGroupForNewBinding()) return '没有可继续绑定的分组'
  return undefined
})
const canAddGroupBinding = computed(() => !addGroupBindingDisabledReason.value)

async function copyKeyPreview(apiKey: ApiKeySummary): Promise<void> {
  if (keyCopyingId.value) return
  keyCopyingId.value = apiKey.id
  try {
    const key = apiKey.key || (await apiKeysApi.secret(apiKey.id, apiKeyOperationScopeParams(apiKey))).key
    await copyTextToClipboard(key, '完整密钥已复制')
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '复制完整密钥失败'))
  } finally {
    if (keyCopyingId.value === apiKey.id) {
      keyCopyingId.value = ''
    }
  }
}

function apiKeyActionBusy(apiKey: ApiKeySummary): boolean {
  return statusUpdatingId.value === apiKey.id || keyRefreshingId.value === apiKey.id
}

function apiKeyPrimaryActions(apiKey: ApiKeySummary): RowActionItem[] {
  const busy = apiKeyActionBusy(apiKey)
  return [
    { key: 'edit', label: '编辑', icon: 'edit', tone: 'primary', disabled: busy },
    {
      key: 'delete',
      label: '删除',
      icon: 'delete',
      tone: 'danger',
      disabled: busy,
      confirmTitle: `确认删除 API Key ${apiKey.name}？`,
      confirmOkText: '删除'
    }
  ]
}

function apiKeyMoreActions(apiKey: ApiKeySummary): RowActionItem[] {
  const busy = apiKeyActionBusy(apiKey)
  const refreshDisabled = Boolean(keyRefreshingId.value) || statusUpdatingId.value === apiKey.id
  const statusAction: RowActionItem = apiKey.status === 'active'
    ? {
        key: 'disable',
        label: '停用',
        icon: 'disable',
        tone: 'warning',
        disabled: busy,
        confirmTitle: '确认停用这个 API Key？停用后后续请求会立即被拒绝。',
        confirmOkText: '停用'
      }
    : {
        key: 'enable',
        label: '启用',
        icon: 'enable',
        tone: 'success',
        disabled: busy
      }
  return [
    statusAction,
    {
      key: 'refresh-key',
      label: '刷新密钥',
      icon: 'refresh',
      tone: 'warning',
      disabled: refreshDisabled,
      confirmTitle: `确认刷新 API Key ${apiKey.name} 的密钥？刷新后旧密钥会立即失效，请先确认客户端配置可同步更新。`,
      confirmOkText: '刷新'
    }
  ]
}

async function loadApiKeyOptions(systemAccountId: string | undefined, force = false): Promise<void> {
  const scopeKey = isManagementView.value ? `management:${systemAccountId ?? 'all'}` : 'self'
  if (!force && apiKeyOptionsLoaded.value && apiKeyOptionsScopeKey.value === scopeKey) {
    return
  }

  apiKeyOptionsLoaded.value = true
  apiKeyOptionsScopeKey.value = scopeKey
}

function resetFilters() {
  const defaults = defaultApiKeysPageState()
  keywordFilter.value = defaults.keywordFilter
  statusFilter.value = defaults.statusFilter
  groupFilterSelection.value = defaults.groupFilter
  systemAccountFilter.value = defaults.systemAccountFilter
  systemAccountFilterSelection.value = defaults.systemAccountFilterSelection
  resetSystemAccountOptionsSearch()
  resetGroupOptionsSearch()
  resetPagination()
  pageStateCache.clear()
  void loadData({ forceOptions: true })
}

function applyFilters() {
  resetPagination()
  void loadData()
}

function refreshApiKeys() {
  resetSystemAccountOptionsSearch()
  resetGroupOptionsSearch()
  resetPagination()
  void loadData({ forceOptions: true })
}

function handleSystemAccountFilterChange() {
  groupFilterSelection.value = undefined
  if (systemAccountFilter.value === allSystemAccountsValue) {
    systemAccountFilterSelection.value = undefined
  }
  resetSystemAccountOptionsSearch()
  resetGroupOptionsSearch()
  resetPagination()
  void loadData({ forceOptions: true })
}

async function refreshMobileApiKeys() {
  resetSystemAccountOptionsSearch()
  resetGroupOptionsSearch()
  resetPagination()
  await loadData({ forceOptions: true })
}

function handleGroupFilterChange() {
  resetGroupOptionsSearch()
  applyFilters()
}

function snapshotPageState(): ApiKeysPageState {
  return {
    groupFilter: groupFilterSelection.value,
    keywordFilter: keywordFilter.value,
    pagination: { current: pagination.current, pageSize: pagination.pageSize },
    statusFilter: statusFilter.value,
    systemAccountFilter: systemAccountFilter.value,
    systemAccountFilterSelection: systemAccountFilterSelection.value
  }
}

function apiKeyListParams(systemAccountId: string | undefined, pageState: { current: number; pageSize: number }) {
  return {
    systemAccountId,
    page: pageState.current,
    pageSize: pageState.pageSize,
    keyword: keywordFilter.value.trim() || undefined,
    status: statusFilter.value,
    groupId: groupFilter.value
  }
}

async function openCreate() {
  if (isManagementView.value && !apiKeyScopeParams.value?.systemAccountId) {
    message.warning('请先在右侧选择目标系统账户，再创建 API Key')
    return
  }
  editingId.value = undefined
  editingSystemAccountId.value = undefined
  resetGroupOptionsSearch()
  await loadGroupOptions('', true, {
    systemAccountId: apiKeyScopeParams.value?.systemAccountId,
    selectedIds: []
  })
  const defaultGroup = groups.value.find((group) => group.enabled && group.isDefault)
  if (!defaultGroup) {
    message.warning('请先创建并启用默认分组，再创建 API Key')
    return
  }
  Object.assign(form, {
    name: '',
    groupRouteStrategy: 'priority_failover',
    groupBindings: [createGroupBindingFormRow({ id: defaultGroup.id, name: defaultGroup.name }, 'active', 1, {
      providerCode: defaultGroup.providerCode,
      providerProtocolProfileId: defaultGroup.providerProtocolProfileId,
      groupEnabled: defaultGroup.enabled
    })],
    status: 'active',
    expiresAt: undefined,
    description: '',
    quotaLimits: createQuotaLimitForm(),
    availabilitySchedule: createApiKeyTimeScheduleForm()
  })
  modalOpen.value = true
}

async function openEdit(apiKey: ApiKeySummary) {
  const editScopeParams = apiKeyOperationScopeParams(apiKey)
  if (isManagementView.value && !editScopeParams?.systemAccountId) {
    message.warning('无法确定 API Key 归属系统账户，请刷新后重试')
    return
  }
  let bindings: ApiKeyGroupBindingFormRow[]
  let quotaLimits: ReturnType<typeof createQuotaLimitForm>
  let expiresAt: Dayjs | undefined
  let availabilitySchedule: ApiKeyAvailabilityScheduleForm
  try {
    bindings = apiKeyGroupBindings(apiKey).map((binding) => {
      rememberGroupLabel(binding.groupId, binding.groupName)
      return createExistingGroupBindingFormRow(binding)
    })
    quotaLimits = createQuotaLimitForm(apiKey.quotaLimits)
    expiresAt = parseStrictDatePickerValue(apiKey.expiresAt, 'API Key 过期时间')
    availabilitySchedule = createApiKeyTimeScheduleForm(apiKey.availabilitySchedule)
  } catch (error) {
    message.error(extractApiErrorMessage(error, 'API Key 数据结构异常，请清理后再编辑'))
    return
  }
  if (!bindings.length) {
    message.error('API Key 分组绑定数据异常，请刷新后重试')
    return
  }
  editingId.value = apiKey.id
  editingSystemAccountId.value = editScopeParams?.systemAccountId
  Object.assign(form, {
    name: apiKey.name,
    groupRouteStrategy: apiKey.groupRouteStrategy,
    groupBindings: bindings,
    status: apiKey.status,
    expiresAt,
    description: apiKey.description ?? '',
    quotaLimits,
    availabilitySchedule
  })
  resetGroupOptionsSearch()
  await loadGroupOptions('', true, {
    systemAccountId: editScopeParams?.systemAccountId,
    selectedIds: formGroupBindingIds.value
  })
  modalOpen.value = true
}

function addGroupBinding() {
  if (addGroupBindingDisabledReason.value) {
    message.warning(addGroupBindingDisabledReason.value)
    return
  }
  const nextGroup = nextAvailableGroupForNewBinding()
  if (!nextGroup) {
    message.warning('没有可继续绑定的分组')
    return
  }
  form.groupBindings.push(createGroupBindingFormRow({ id: nextGroup.id, name: nextGroup.name }, 'active', 1, {
    providerCode: nextGroup.providerCode,
    providerProtocolProfileId: nextGroup.providerProtocolProfileId,
    groupEnabled: nextGroup.enabled
  }))
}

function handleGroupBindingChange(index: number) {
  const binding = form.groupBindings[index]
  if (!binding?.groupId) return
  const group = groupOptionForId(binding.groupId)
  if (!group) return
  if (!isApiKeyBindableGroup(group)) {
    message.warning('该分组当前不可用，请选择其他 API Key 号池')
    binding.groupId = ''
    binding.group = undefined
    binding.providerCode = undefined
    binding.providerProtocolProfileId = undefined
    binding.groupEnabled = undefined
    return
  }
  const providerProtocolProfileId = selectedGroupBindingProviderProfileId(index)
  if (providerProtocolProfileId && group.providerProtocolProfileId !== providerProtocolProfileId) {
    message.warning('同一个 API Key 的绑定号池必须属于同一供应商协议档案')
    binding.groupId = ''
    binding.group = undefined
    binding.providerCode = undefined
    binding.providerProtocolProfileId = undefined
    binding.groupEnabled = undefined
    return
  }
  binding.providerCode = group.providerCode
  binding.providerProtocolProfileId = group.providerProtocolProfileId
  binding.groupEnabled = group.enabled
  if (!group.enabled && binding.status === 'active') {
    message.warning('已停用分组只能作为停用号池保留，不能参与路由')
    binding.status = 'disabled'
  }
}

function removeGroupBinding(index: number) {
  if (form.groupBindings.length <= 1) return
  form.groupBindings.splice(index, 1)
}

function moveGroupBinding(index: number, offset: -1 | 1) {
  const nextIndex = index + offset
  if (nextIndex < 0 || nextIndex >= form.groupBindings.length) return
  const [item] = form.groupBindings.splice(index, 1)
  if (!item) return
  form.groupBindings.splice(nextIndex, 0, item)
}

function nextAvailableGroupForNewBinding(): GroupOptionSummary | undefined {
  const selectedIds = new Set(form.groupBindings.map((binding) => binding.groupId.trim()).filter(Boolean))
  const providerProtocolProfileId = selectedGroupBindingProviderProfileId()
  return groups.value.find((group) => (
    isApiKeyBindableGroup(group)
    && group.enabled
    && !selectedIds.has(group.id)
    && (!providerProtocolProfileId || group.providerProtocolProfileId === providerProtocolProfileId)
  ))
}

function groupBindingPriorityText(index: number): string {
  if (form.groupRouteStrategy === 'round_robin') return `轮询 ${index + 1}`
  if (form.groupRouteStrategy === 'weighted_round_robin') return `权重 ${index + 1}`
  return index === 0 ? '主号池' : `备 ${index}`
}

function groupOptionsForBinding(index: number): GroupOptionSummary[] {
  const providerProtocolProfileId = selectedGroupBindingProviderProfileId(index)
  return groups.value.filter((group) => (
    isApiKeyBindableGroup(group)
    && group.enabled
    && (!providerProtocolProfileId || group.providerProtocolProfileId === providerProtocolProfileId)
  ))
}

function hiddenGroupBindingIds(index: number): string[] {
  const selectedIds = form.groupBindings
    .map((binding, bindingIndex) => bindingIndex === index ? undefined : binding.groupId.trim())
    .filter((groupId): groupId is string => Boolean(groupId))
  const disabledIds = groups.value
    .filter((group) => !group.enabled)
    .map((group) => group.id)
  return [...new Set([...selectedIds, ...disabledIds])]
}

function selectedGroupBindingProviderProfileId(excludeIndex?: number): string | undefined {
  for (const [index, binding] of form.groupBindings.entries()) {
    if (excludeIndex === index) continue
    const providerProtocolProfileId = groupOptionForId(binding.groupId)?.providerProtocolProfileId ?? binding.providerProtocolProfileId
    if (providerProtocolProfileId) return providerProtocolProfileId
  }
  return undefined
}

function groupOptionForId(groupId: string | undefined): GroupOptionSummary | undefined {
  const id = groupId?.trim()
  if (!id) return undefined
  return groups.value.find((group) => group.id === id)
}

function isApiKeyBindableGroup(group: GroupOptionSummary): boolean {
  if (!group.enabled) return false
  if (group.permissions?.canBindToApiKey === false) return false
  if (group.accessType !== 'authorized') return true
  if (group.authorizationStatus !== 'active') return false
  if (!group.authorizationExpiresAt) return true
  const expiresAt = Date.parse(group.authorizationExpiresAt)
  return !Number.isFinite(expiresAt) || expiresAt > Date.now()
}

function handleApiKeyAction(key: string, apiKey: ApiKeySummary) {
  if (key === 'edit') {
    void openEdit(apiKey)
    return
  }
  if (key === 'enable' || key === 'disable') {
    void updateApiKeyStatus(apiKey, key === 'enable' ? 'active' : 'disabled')
    return
  }
  if (key === 'refresh-key') {
    void refreshApiKeySecret(apiKey)
    return
  }
  if (key === 'delete') {
    void removeApiKey(apiKey)
  }
}

function apiKeyOperationScopeParams(apiKey?: Pick<ApiKeySummary, 'systemAccountId'>): ApiKeyScopeParams {
  const systemAccountId = apiKey?.systemAccountId?.trim()
    || apiKeyFormScopeParams.value?.systemAccountId
    || apiKeyScopeParams.value?.systemAccountId
  return systemAccountId ? { systemAccountId } : undefined
}

async function updateApiKeyStatus(apiKey: ApiKeySummary, status: 'active' | 'disabled') {
  statusUpdatingId.value = apiKey.id
  try {
    const updated = await apiKeysApi.update(apiKey.id, { status }, apiKeyOperationScopeParams(apiKey))
    updateApiKeyItems((item) => item.id === apiKey.id, () => updated)
    message.success(status === 'active' ? 'API Key 已启用' : 'API Key 已停用')
    void loadData({ quiet: true })
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, status === 'active' ? '启用 API Key 失败' : '停用 API Key 失败'))
  } finally {
    if (statusUpdatingId.value === apiKey.id) {
      statusUpdatingId.value = ''
    }
  }
}

async function refreshApiKeySecret(apiKey: ApiKeySummary) {
  if (keyRefreshingId.value) return
  keyRefreshingId.value = apiKey.id
  try {
    const result = await apiKeysApi.refreshKey(apiKey.id, apiKeyOperationScopeParams(apiKey))
    updateApiKeyItems((item) => item.id === apiKey.id, () => result)
    createdKey.value = result.key
    createdKeyModalTitle.value = 'API Key 密钥已刷新'
    createdKeyModalMessage.value = '密钥已刷新，旧密钥已失效，请立即复制新密钥并更新客户端配置。'
    createdKeyOpen.value = true
    message.success('API Key 密钥已刷新')
    void loadData({ quiet: true })
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '刷新 API Key 密钥失败'))
  } finally {
    if (keyRefreshingId.value === apiKey.id) {
      keyRefreshingId.value = ''
    }
  }
}

const saveApiKey = submitAction('api_keys.save', async () => {
  if (!form.name.trim()) {
    message.warning('请填写名称')
    return
  }
  try {
    const groupBindings = normalizedGroupBindingPayload(form.groupBindings)
    if (!groupBindings.length) {
      message.warning('请至少选择一个绑定分组')
      return
    }
    const emptyBindingIndex = groupBindings.findIndex((binding) => !binding.groupId)
    if (emptyBindingIndex >= 0) {
      message.warning(`请先选择第 ${emptyBindingIndex + 1} 个绑定分组`)
      return
    }
    if (!groupBindings.some((binding) => binding.status === 'active')) {
      message.warning('至少需要一个启用分组')
      return
    }
    if (new Set(groupBindings.map((binding) => binding.groupId)).size !== groupBindings.length) {
      message.warning('绑定分组不能重复')
      return
    }
    const providerProtocolProfileIds = new Set(groupBindings.map((binding, index) => groupOptionForId(binding.groupId)?.providerProtocolProfileId ?? form.groupBindings[index]?.providerProtocolProfileId).filter(Boolean))
    if (providerProtocolProfileIds.size > 1) {
      message.warning('同一个 API Key 的绑定号池必须属于同一供应商协议档案')
      return
    }
    const disabledActiveGroups = groupBindings
      .filter((binding) => binding.status === 'active')
      .map((binding) => groupOptionForId(binding.groupId))
      .filter((group): group is GroupOptionSummary => Boolean(group && !group.enabled))
    if (disabledActiveGroups.length) {
      message.warning(`已停用分组不能作为启用号池：${disabledActiveGroups.map((group) => group.name).join('、')}`)
      return
    }
    const availabilitySchedule = availabilitySchedulePayload()
    if (availabilitySchedule === false) {
      return
    }
    const targetId = editingId.value
    const expiresAt = formatServerDateTimeInput(form.expiresAt)
    const payload = {
      name: form.name,
      groupRouteStrategy: form.groupRouteStrategy,
      groupBindings,
      status: form.status,
      expiresAt: targetId ? expiresAt : expiresAt ?? undefined,
      description: form.description,
      quotaLimits: quotaLimitsPayload(),
      availabilitySchedule
    }
    if (targetId) {
      const updated = await apiKeysApi.update(targetId, payload, apiKeyOperationScopeParams())
      updateApiKeyItems((item) => item.id === targetId, () => updated)
      message.success('API Key 已更新')
      void loadData({ quiet: true })
    } else {
      const result = await apiKeysApi.create(payload, apiKeyScopeParams.value)
      createdKey.value = result.key
      createdKeyModalTitle.value = 'API Key 已创建'
      createdKeyModalMessage.value = '复制下方 API Key 和 Base URL；统计、会话亲和和缓存按本地 API Key 与分组保持连续。'
      createdKeyOpen.value = true
      message.success('API Key 已创建')
      await loadData()
    }
    modalOpen.value = false
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存 API Key 失败'))
  }
})

function quotaLimitsPayload(): ApiKeyQuotaLimits {
  return buildQuotaLimitsPayload(form.quotaLimits)
}

function availabilitySchedulePayload(): ApiKeyAvailabilitySchedule | null | false {
  const scheduleValidation = validateTimeScheduleForm(form.availabilitySchedule)
  if (scheduleValidation) {
    message.warning(scheduleValidation)
    return false
  }
  return buildTimeSchedulePayload<ApiKeyAvailabilitySchedule>(form.availabilitySchedule)
}

async function copyText(value: string) {
  await copyTextToClipboard(value)
}

async function copyGatewayBaseUrl() {
  await copyText(gatewayBaseUrl.value)
}

async function copyCreatedKey() {
  await copyText(createdKey.value)
}

function inferGatewayBaseUrl() {
  if (typeof window === 'undefined') return 'http://127.0.0.1:3000'
  if (import.meta.env.DEV) {
    return `${window.location.protocol}//${window.location.hostname}:3000`
  }
  return window.location.origin
}

function normalizeGatewayBaseUrl(value: string) {
  const trimmed = value.trim().replace(/\/+$/, '')
  return trimmed || inferGatewayBaseUrl()
}

async function removeApiKey(apiKey: ApiKeySummary) {
  try {
    await apiKeysApi.delete(apiKey.id, apiKeyOperationScopeParams(apiKey))
    removeApiKeyItems((item) => item.id === apiKey.id)
    message.success('API Key 已删除，关联记录将后台清理')
    void loadData({ quiet: true })
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '删除 API Key 失败'))
  }
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })
watch(modalOpen, (open) => {
  if (open) return
  editingId.value = undefined
  editingSystemAccountId.value = undefined
})
watch(apiKeys, (items) => {
  for (const item of items) {
    for (const binding of apiKeyGroupBindings(item)) {
      rememberGroupLabel(binding.groupId, binding.groupName)
    }
  }
  rememberGroupSelection(groupFilterSelection.value)
  rememberPrincipalSelection(systemAccountFilterSelection.value)
  syncSelectedGroupSelections()
}, { immediate: true })
watch(systemAccountFilterSelection, (selection) => rememberPrincipalSelection(selection), { deep: true, immediate: true })

onBeforeUnmount(clearGroupOptionsSearchTimer)

onMounted(loadData)
</script>

<style scoped>
.api-keys-page-card {
  border: 1px solid #e8edf5;
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.04);
}

.toolbar-select {
  min-width: 180px;
}

.mobile-filter-field {
  display: grid;
  gap: 8px;
  color: #334155;
  font-size: 13px;
  font-weight: 600;
}

.advanced-filter-form :deep(.ant-select) {
  width: 100%;
}

.gateway-help-content {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.gateway-help-section {
  padding: 14px;
  border: 1px solid #e2e8f0;
  border-radius: 12px;
  background: #fbfdff;
}

.gateway-help-note {
  border-radius: 8px;
}

.gateway-step-title {
  color: #0f172a;
  font-size: 15px;
  font-weight: 700;
}

.gateway-url-row,
.created-key-base-url {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.gateway-url-label,
.created-key-label {
  flex: none;
  color: #64748b;
  font-size: 12px;
  font-weight: 600;
}

.gateway-url-value,
.created-key-value {
  min-width: 0;
  padding: 4px 10px;
  overflow: hidden;
  color: #0f766e;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
  border-radius: 6px;
  background: #ecfeff;
}

.gateway-copy-button {
  flex: none;
}

.gateway-code {
  margin: 0;
  padding: 10px 12px;
  overflow-x: auto;
  color: #334155;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 1.6;
  border: 1px solid #e2e8f0;
  border-radius: 10px;
  background: rgba(255, 255, 255, 0.8);
}

.modal-alert {
  margin-bottom: 16px;
}

.modal-form {
  margin-top: 16px;
}

.created-key {
  margin-top: 16px;
}

.created-key-base-url {
  margin-top: 16px;
}

.api-keys-table :deep(.ant-empty) {
  margin: 12px 0;
}

.api-keys-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.group-route-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  max-width: 320px;
}

.group-route-tags :deep(.ant-tag) {
  max-width: 280px;
  margin-inline-end: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.api-key-group-bindings-field {
  display: grid;
  gap: 10px;
}

.api-key-group-binding-row {
  display: grid;
  grid-template-columns: 64px minmax(0, 1fr) 96px auto;
  gap: 8px;
  align-items: start;
}

.api-key-group-binding-row-weighted {
  grid-template-columns: 64px minmax(0, 1fr) 84px 96px auto;
}

.binding-priority {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  height: 32px;
  min-width: 0;
  color: #475569;
  font-size: 12px;
  font-weight: 700;
  border: 1px solid #e2e8f0;
  border-radius: 6px;
  background: #f8fafc;
}

.binding-group-select,
.binding-weight-input,
.binding-status-select {
  min-width: 0;
}

.binding-weight-input {
  width: 100%;
}

.binding-row-actions {
  display: inline-flex;
  align-items: center;
  gap: 2px;
}

.api-key-schedule-field {
  display: grid;
  gap: 10px;
}

.schedule-tag {
  max-width: 200px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.key-preview-cell {
  display: flex;
  align-items: center;
  width: 100%;
  min-width: 0;
  gap: 8px;
}

.key-preview {
  display: inline-flex;
  align-items: center;
  max-width: calc(100% - 32px);
  box-sizing: border-box;
  padding: 3px 8px;
  overflow: hidden;
  color: #008b8b;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 18px;
  text-overflow: ellipsis;
  white-space: nowrap;
  border-radius: 4px;
  background: #eefafa;
}

.key-copy-button {
  color: #94a3b8;
}

.key-copy-button-wrap {
  flex: none;
}

.key-copy-button:hover:not(:disabled) {
  color: #1677ff;
  background: #eff6ff;
}

@media (max-width: 640px) {
  .api-key-group-binding-row {
    grid-template-columns: 64px minmax(0, 1fr);
  }

  .binding-weight-input,
  .binding-status-select,
  .binding-row-actions {
    grid-column: 2;
  }

  .binding-row-actions {
    justify-content: flex-start;
  }

}

</style>

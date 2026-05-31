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
          :placeholder="groupFilterDisabled ? '请先选择系统账户' : '全部分组'"
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
          <span>分组</span>
          <GroupSelect
            v-model:value="groupFilter"
            v-model:selected-group="groupFilterSelection"
            allow-clear
            :disabled="groupFilterDisabled"
            :filter-option="false"
            :groups="groups"
            :loading="groupOptionsLoading"
            :placeholder="groupFilterDisabled ? '请先选择系统账户' : '全部分组'"
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

    <ResponsiveDataList table-class="page-table api-keys-table" :columns="managedColumns" :data-source="filteredApiKeys" :mobile-data-source="mobileApiKeys" row-key="id" :loading="loading" :loading-more="mobileLoadingMore" :mobile-has-more="mobileHasMore" :pagination="tablePagination" :scroll-x="isManagementView ? 2020 : 1840" mobile-pagination pull-refresh-enabled :refreshing="loading" @change="handleTableChange" @mobile-load-more="loadMoreMobileApiKeys" @mobile-refresh="refreshMobileApiKeys">
      <template #emptyText>
        <a-empty class="page-empty-card" description="还没有 API Key。先新建一个并绑定分组；接入说明可点击右上角帮助查看。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'status'">
          <StatusTag :color="record.status === 'active' ? 'green' : 'default'" :label="record.status === 'active' ? '启用' : '停用'" />
        </template>
        <template v-else-if="column.key === 'availabilitySchedule'">
          <a-tag class="schedule-tag" :color="apiKeyScheduleTagColor(record)">
            {{ apiKeyScheduleSummary(record.availabilitySchedule) }}
          </a-tag>
        </template>
        <template v-else-if="column.key === 'usage'">
          <UsageSummaryTags :usage="record.usage" />
        </template>
        <template v-else-if="column.key === 'key'">
          <div class="key-preview-cell">
            <span class="key-preview" :title="keyDisplayTitle(record)">{{ formatKeyPreview(record) }}</span>
            <a-button class="key-copy-button" type="text" size="small" :disabled="!record.key" @click="copyText(record.key)">
              <template #icon><copy-outlined /></template>
            </a-button>
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
          <RowActions :actions="apiKeyActions(record)" @action-click="handleApiKeyAction($event, record)" />
        </template>
      </template>
      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">{{ record.name }}</div>
            <div class="mobile-list-card-tags">
              <StatusTag :color="record.status === 'active' ? 'green' : 'default'" :label="record.status === 'active' ? '启用' : '停用'" />
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
              <strong>{{ apiKeyScheduleSummary(record.availabilitySchedule) }}</strong>
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
            <RowActions variant="button" :actions="apiKeyActions(record)" @action-click="handleApiKeyAction($event, record)" />
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
          <span class="gateway-step-title">2. 复制本页 API Key</span>
        </div>
        <div class="gateway-help-section">
          <span class="gateway-step-title">3. 填到客户端</span>
          <pre class="gateway-code">{{ gatewayClientExample }}</pre>
        </div>
        <a-alert class="gateway-help-note" type="info" show-icon message="Responses 是连续会话入口；/chat/completions 需要兼容上游。统计、会话亲和和缓存不按 OAuth / API Key 类型拆分。" />
      </div>
    </a-modal>

    <a-modal v-model:open="modalOpen" :title="editingId ? '编辑 API Key' : '新建 API Key'" width="640px" :confirm-loading="apiKeySaving" :ok-button-props="{ type: 'primary', disabled: apiKeySaving }" @ok="saveApiKey">
      <a-alert v-if="!editingId && isManagementView && targetSystemAccountLabel" class="modal-alert" type="info" show-icon :message="`当前创建目标：${targetSystemAccountLabel}`" />
      <a-alert class="modal-alert" message="系统会自动生成完整密钥，创建后可在列表继续复制。" type="info" show-icon />
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
                :disabled="groupFilterDisabled"
                :filter-option="false"
                :groups="groupOptionsForBinding(index)"
                :loading="groupOptionsLoading"
                :placeholder="groupFilterDisabled ? '请先选择系统账户' : '输入分组名称搜索'"
                :selected-ids="formGroupBindingIds"
                :selected-groups="formGroupBindingSelections"
                :hidden-option-values="hiddenGroupBindingIds(index)"
                @change="handleGroupBindingChange(index)"
                @dropdown-visible-change="handleGroupOptionsDropdown"
                @search="handleGroupOptionsSearch"
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
            <div class="schedule-toggle-row">
              <span class="schedule-toggle-label">自动启停计划</span>
              <a-switch v-model:checked="form.availabilitySchedule.enabled" />
            </div>
            <div v-if="form.availabilitySchedule.enabled" class="schedule-config">
              <div class="schedule-window-list">
                <div v-for="(window, index) in form.availabilitySchedule.windows" :key="window.key" class="schedule-window-row">
                  <a-select
                    v-model:value="window.daysOfWeek"
                    mode="multiple"
                    class="schedule-days-select"
                    max-tag-count="responsive"
                    :options="weekdayOptions"
                    placeholder="重复日期"
                  />
                  <a-time-picker v-model:value="window.start" format="HH:mm" value-format="HH:mm" class="schedule-time-picker" placeholder="开始" />
                  <a-time-picker v-model:value="window.end" format="HH:mm" value-format="HH:mm" class="schedule-time-picker" placeholder="停止" />
                  <a-tooltip title="移除">
                    <a-button type="text" size="small" danger :disabled="form.availabilitySchedule.windows.length <= 1" @click="removeScheduleWindow(index)">
                      <template #icon><delete-outlined /></template>
                    </a-button>
                  </a-tooltip>
                </div>
                <a-button type="dashed" block @click="addScheduleWindow">
                  <template #icon><plus-outlined /></template>
                  添加时段
                </a-button>
              </div>
            </div>
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

    <a-modal v-model:open="createdKeyOpen" title="API Key 已创建" width="640px" :footer="null">
      <a-alert message="复制下方 API Key 和 Base URL；统计、会话亲和和缓存按本地 API Key 与分组保持连续。" type="info" show-icon />
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
import { formatCompactUsageAmount, formatDateTime, formatNumber, formatServerDateTimeInput, formatUsd } from '@/shared/formatters'
import { displayGroupName, rememberGroupLabel, rememberGroupLabels, rememberGroupSelection, type GroupSelection } from '@/shared/groupLabelCache'
import { principalLabelForId, rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import { createShortLivedQueryCache } from '@/shared/shortLivedQueryCache'
import {
  localSelectStorageKey,
  readLocalSelectOptionWindow,
  removeLocalSelectOptionWindowValues,
  removeLocalSelectPreferenceValues,
  writeLocalSelectOptionWindow
} from '@/shared/selectLocalPreferenceCache'
import type { AccountUsageSummary, ApiKeyAvailabilitySchedule, ApiKeyGroupBindingSummary, ApiKeyGroupRouteStrategy, ApiKeyQuotaLimits, ApiKeySummary, GroupOptionSummary } from '@/types/domain'
import { allSystemAccountsValue, systemAccountDisplayText } from '@/utils/systemAccountFilter'
import RequestQuotaFields from '@/views/shared/RequestQuotaFields.vue'
import { quotaLimitSummaryText } from '@/views/shared/requestQuotaFormatters'
import { createQuotaLimitForm, quotaLimitsPayload as buildQuotaLimitsPayload } from '@/views/shared/requestQuotaForm'

const modalOpen = ref(false)
const createdKeyOpen = ref(false)
const helpOpen = ref(false)
const editingId = ref<string>()
const createdKey = ref('')
const statusUpdatingId = ref('')
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
type ApiKeyGroupBindingFormStatus = 'active' | 'disabled'
interface ApiKeyGroupBindingFormRow {
  key: string
  groupId: string
  group?: GroupSelection
  weight: number
  status: ApiKeyGroupBindingFormStatus
}
interface ApiKeyScheduleWindowFormRow {
  key: string
  daysOfWeek: number[]
  start?: string
  end?: string
}
interface ApiKeyAvailabilityScheduleForm {
  enabled: boolean
  windows: ApiKeyScheduleWindowFormRow[]
}
type ApiKeyAvailabilitySchedulePayload = Pick<ApiKeyAvailabilitySchedule, 'enabled' | 'mode' | 'windows'>
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
const groupFilter = computed({
  get: () => groupFilterSelection.value?.id,
  set: (id: string | undefined) => {
    groupFilterSelection.value = selectedGroupSelection(id)
  }
})
const groups = ref<GroupOptionSummary[]>([])
const groupOptionsLoading = ref(false)
const apiKeyOptionsLoaded = ref(false)
const apiKeyOptionsScopeKey = ref('')
const systemAccountFilter = ref(initialPageState.systemAccountFilter)
const systemAccountFilterSelection = ref<PrincipalSelection | undefined>(initialPageState.systemAccountFilterSelection)
let scheduleWindowFormKeySeed = 0
const form = reactive({
  name: '',
  groupRouteStrategy: 'priority_failover' as ApiKeyGroupRouteStrategy,
  groupBindings: [] as ApiKeyGroupBindingFormRow[],
  status: 'active' as 'active' | 'disabled',
  expiresAt: undefined as Dayjs | undefined,
  description: '',
  quotaLimits: createQuotaLimitForm(),
  availabilitySchedule: createAvailabilityScheduleForm()
})
let groupBindingFormKeySeed = 0
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const apiKeysApi = useScopedApiKeysApi(isManagementView)
const groupsApi = useScopedGroupsApi(isManagementView)
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
let groupOptionsRequestId = 0
let groupOptionsLoadingKey: string | undefined
let groupOptionsLoadingPromise: Promise<void> | undefined
let groupOptionsKeyword = ''
let groupOptionsSearchTimer: ReturnType<typeof window.setTimeout> | undefined
const groupOptionsCache = createShortLivedQueryCache<GroupOptionSummary[]>({ ttlMs: 10_000 })
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
    message.error('加载 API Key 失败')
  }
})

const rawColumns = computed(() => {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: '名称', dataIndex: 'name', key: 'name', width: 180 },
    { title: '密钥', key: 'key', width: 180 }
  ]
  if (isManagementView.value) {
    baseColumns.push({ title: '系统账户', key: 'systemAccount', width: 180 })
  }
  baseColumns.push(
    { title: '绑定分组', key: 'group', width: 220 },
    { title: '状态', key: 'status', width: 100 },
    { title: '时间计划', key: 'availabilitySchedule', width: 220 },
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
const weekdayOptions = [
  { label: '周一', value: 1 },
  { label: '周二', value: 2 },
  { label: '周三', value: 3 },
  { label: '周四', value: 4 },
  { label: '周五', value: 5 },
  { label: '周六', value: 6 },
  { label: '周日', value: 7 }
]

const filteredApiKeys = computed(() => apiKeys.value)
const mobileApiKeys = computed(() => apiKeys.value)
const formGroupBindingIds = computed(() => form.groupBindings.map((binding) => binding.groupId).filter(Boolean))
const formGroupBindingSelections = computed(() => form.groupBindings.map((binding) => binding.group))
const apiKeyScopeParams = computed(() => {
  const systemAccountId = scopedSystemAccountId(systemAccountFilter.value)
  return systemAccountId ? { systemAccountId } : undefined
})
const groupFilterDisabled = computed(() => isManagementView.value && !apiKeyScopeParams.value?.systemAccountId)
const activeFilterCount = computed(() => [
  keywordFilter.value.trim(),
  statusFilter.value !== 'all',
  !groupFilterDisabled.value && groupFilter.value,
  isManagementView.value && systemAccountFilter.value !== allSystemAccountsValue
].filter(Boolean).length)
const advancedFilterCount = computed(() => 0)
const gatewayBaseUrl = computed(() => normalizeGatewayBaseUrl((import.meta.env.VITE_JUHE_AI_GATEWAY_BASE_URL as string | undefined) || inferGatewayBaseUrl()))
const gatewayClientExample = computed(() => [`Base URL：${gatewayBaseUrl.value}`, 'API Key：填本页复制的密钥'].join('\n'))
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
  if (groupFilterDisabled.value) return '请先选择系统账户'
  if (form.groupBindings.some((binding) => !binding.groupId.trim())) return '请先选择已有绑定分组'
  if (!nextAvailableGroupForNewBinding()) return '没有可继续绑定的分组'
  return undefined
})
const canAddGroupBinding = computed(() => !addGroupBindingDisabledReason.value)

function selectedGroupSelection(id: string | undefined): GroupSelection | undefined {
  const normalizedId = id?.trim()
  if (!normalizedId) return undefined
  const group = groups.value.find((item) => item.id === normalizedId)
  if (group) return { id: group.id, name: group.name }
  if (groupFilterSelection.value?.id === normalizedId) return groupFilterSelection.value
  const selectedBinding = form.groupBindings.find((binding) => binding.group?.id === normalizedId)
  if (selectedBinding?.group) return selectedBinding.group
  return undefined
}

function apiKeyGroupBindings(apiKey: ApiKeySummary): ApiKeyGroupBindingSummary[] {
  if (apiKey.groupBindings?.length) return apiKey.groupBindings
  return [{
    id: `legacy:${apiKey.id}:${apiKey.groupId}`,
    groupId: apiKey.groupId,
    groupName: apiKey.groupName,
    priority: 1,
    weight: 1,
    status: 'active',
    groupEnabled: true
  }]
}

function apiKeyGroupBindingTagColor(binding: ApiKeyGroupBindingSummary): string {
  if (binding.status === 'disabled') return 'default'
  if (!binding.groupEnabled) return 'orange'
  return binding.priority === 1 ? 'purple' : 'blue'
}

function apiKeyGroupBindingTagText(apiKey: ApiKeySummary, binding: ApiKeyGroupBindingSummary, index = 0): string {
  const name = displayGroupName(binding.groupName, binding.groupId)
  const suffix = binding.status === 'disabled'
    ? '停用'
    : binding.groupEnabled ? undefined : '分组停用'
  const routeLabel = groupBindingLabelByStrategy(apiKey.groupRouteStrategy, binding, index)
  const text = suffix ? `${routeLabel}：${name}（${suffix}）` : `${routeLabel}：${name}`
  return apiKey.groupRouteStrategy === 'weighted_round_robin' && binding.status === 'active'
    ? `${text} 权重 ${binding.weight || 1}`
    : text
}

function apiKeyGroupRouteText(apiKey: ApiKeySummary): string {
  return apiKeyGroupBindings(apiKey)
    .map((binding, index) => apiKeyGroupBindingTagText(apiKey, binding, index))
    .join(' / ')
}

function groupBindingLabelByStrategy(strategy: ApiKeyGroupRouteStrategy | undefined, binding: ApiKeyGroupBindingSummary, index: number): string {
  if (strategy === 'round_robin') return `轮询 ${index + 1}`
  if (strategy === 'weighted_round_robin') return `权重 ${index + 1}`
  return groupBindingPriorityTextByPriority(binding.priority)
}

function apiKeyScheduleSummary(schedule?: ApiKeyAvailabilitySchedule): string {
  if (!schedule?.enabled || !schedule.windows.length) return '未设置'
  const windows = schedule.windows
    .slice(0, 2)
    .map((window) => `${daysOfWeekText(window.daysOfWeek)} ${scheduleWindowText(window.start, window.end)}`)
  const suffix = schedule.windows.length > 2 ? ` 等 ${schedule.windows.length} 段` : ''
  const current = isScheduleCurrentlyAllowed(schedule) ? '当前可用' : '计划停用'
  return `${current}：${windows.join(' / ')}${suffix}`
}

function apiKeyScheduleTagColor(apiKey: ApiKeySummary): string {
  if (!apiKey.availabilitySchedule?.enabled) return 'default'
  return isScheduleCurrentlyAllowed(apiKey.availabilitySchedule) ? 'green' : 'orange'
}

function scheduleWindowText(start: string, end: string): string {
  return start > end ? `${start}-次日 ${end}` : `${start}-${end}`
}

function daysOfWeekText(days: number[]): string {
  const normalized = [...new Set(days)].sort((left, right) => left - right).join(',')
  if (normalized === '1,2,3,4,5,6,7') return '每天'
  if (normalized === '1,2,3,4,5') return '工作日'
  if (normalized === '6,7') return '周末'
  const labels = new Map(weekdayOptions.map((item) => [item.value, item.label]))
  return [...new Set(days)].sort((left, right) => left - right).map((day) => labels.get(day) ?? `周${day}`).join('、')
}

function isScheduleCurrentlyAllowed(schedule: ApiKeyAvailabilitySchedule): boolean {
  if (!schedule.enabled) return true
  const current = zonedScheduleParts(new Date(), schedule.timezone)
  if (schedule.dateRange?.startDate && current.dateKey < schedule.dateRange.startDate) return false
  if (schedule.dateRange?.endDate && current.dateKey > schedule.dateRange.endDate) return false
  const exception = schedule.exceptions?.find((item) => item.date === current.dateKey)
  if (exception?.action === 'deny') return false
  if (exception?.action === 'allow') {
    return (exception.windows ?? []).some((window) => isCurrentMinuteInScheduleWindow(current, { ...window, daysOfWeek: [current.dayOfWeek] }))
  }
  return schedule.windows.some((window) => isCurrentMinuteInScheduleWindow(current, window))
}

function isCurrentMinuteInScheduleWindow(
  current: { dayOfWeek: number; minuteOfDay: number },
  window: { daysOfWeek: number[]; start: string; end: string }
): boolean {
  const start = scheduleMinuteOfDay(window.start)
  const end = scheduleMinuteOfDay(window.end)
  const days = new Set(window.daysOfWeek)
  if (start < end) {
    return days.has(current.dayOfWeek) && current.minuteOfDay >= start && current.minuteOfDay < end
  }
  return (days.has(current.dayOfWeek) && current.minuteOfDay >= start)
    || (days.has(previousScheduleDayOfWeek(current.dayOfWeek)) && current.minuteOfDay < end)
}

function scheduleMinuteOfDay(value: string): number {
  const [hour, minute] = value.split(':').map((item) => Number(item))
  return Math.max(0, Math.min(23, hour || 0)) * 60 + Math.max(0, Math.min(59, minute || 0))
}

function previousScheduleDayOfWeek(dayOfWeek: number): number {
  return dayOfWeek === 1 ? 7 : dayOfWeek - 1
}

function zonedScheduleParts(date: Date, timezone: string): { dateKey: string; dayOfWeek: number; minuteOfDay: number } {
  try {
    const formatter = new Intl.DateTimeFormat('en-CA', {
      timeZone: timezone || defaultScheduleTimezone(),
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      hourCycle: 'h23'
    })
    const parts = Object.fromEntries(formatter.formatToParts(date).map((part) => [part.type, part.value]))
    const year = Number(parts.year)
    const month = Number(parts.month)
    const day = Number(parts.day)
    const hour = Number(parts.hour)
    const minute = Number(parts.minute)
    const dateKey = `${year}-${String(month).padStart(2, '0')}-${String(day).padStart(2, '0')}`
    const utcDay = new Date(Date.UTC(year, month - 1, day)).getUTCDay()
    return {
      dateKey,
      dayOfWeek: utcDay === 0 ? 7 : utcDay,
      minuteOfDay: hour * 60 + minute
    }
  } catch {
    const day = date.getDay()
    const dateKey = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
    return {
      dateKey,
      dayOfWeek: day === 0 ? 7 : day,
      minuteOfDay: date.getHours() * 60 + date.getMinutes()
    }
  }
}

function formatKeyPreview(apiKey: Pick<ApiKeySummary, 'key' | 'keyPrefix'>) {
  const value = apiKey.key
  if (!value) return apiKey.keyPrefix ? `${apiKey.keyPrefix}...` : '密钥不可还原'
  if (value.length <= 14) return value
  return `${value.slice(0, 6)}...${value.slice(-4)}`
}

function keyDisplayTitle(apiKey: Pick<ApiKeySummary, 'key' | 'keyPrefix'>) {
  if (apiKey.key) return apiKey.key
  return apiKey.keyPrefix ? `${apiKey.keyPrefix}...` : '密钥不可还原'
}

function apiKeyActions(apiKey: ApiKeySummary): RowActionItem[] {
  const updating = statusUpdatingId.value === apiKey.id
  const statusAction: RowActionItem = apiKey.status === 'active'
    ? {
        key: 'disable',
        label: '停用',
        icon: 'disable',
        tone: 'warning',
        disabled: updating,
        confirmTitle: '确认停用这个 API Key？停用后后续请求会立即被拒绝。',
        confirmOkText: '停用'
      }
    : {
        key: 'enable',
        label: '启用',
        icon: 'enable',
        tone: 'success',
        disabled: updating
      }
  return [
    { key: 'edit', label: '编辑', icon: 'edit', tone: 'primary', disabled: updating },
    statusAction,
    {
      key: 'delete',
      label: '删除',
      icon: 'delete',
      tone: 'danger',
      disabled: updating,
      confirmTitle: '确认删除这个 API Key？删除后会立即失效，关联历史记录和统计将由后台分批清理。',
      confirmOkText: '删除'
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

async function loadGroupOptions(keyword = groupOptionsKeyword, force = false): Promise<void> {
  groupOptionsKeyword = keyword
  const systemAccountId = isManagementView.value ? apiKeyScopeParams.value?.systemAccountId : undefined
  if (isManagementView.value && !systemAccountId) {
    groups.value = []
    groupOptionsLoading.value = false
    groupOptionsLoadingKey = undefined
    groupOptionsLoadingPromise = undefined
    return
  }
  const requestKeyword = normalizeOptionKeyword(keyword)
  const requestKey = JSON.stringify([isManagementView.value ? `management:${systemAccountId ?? 'all'}` : 'self', requestKeyword ?? '', groupFilter.value ?? '', ...formGroupBindingIds.value])
  if (groupOptionsLoadingKey === requestKey && groupOptionsLoadingPromise) {
    return groupOptionsLoadingPromise
  }
  const requestId = ++groupOptionsRequestId
  const optionWindowKey = groupOptionWindowKey(systemAccountId, requestKeyword)
  const localWindowGroups = !force ? readLocalSelectOptionWindow<GroupOptionSummary>(optionWindowKey) : undefined
  if (localWindowGroups?.length) {
    groupOptionsLoading.value = false
    rememberGroupLabels(localWindowGroups)
    syncSelectedGroupSelections(localWindowGroups)
    groups.value = localWindowGroups
  }
  if (!force) {
    const cachedGroups = groupOptionsCache.get(requestKey)
    if (cachedGroups) {
      groupOptionsLoadingKey = undefined
      groupOptionsLoadingPromise = undefined
      groupOptionsLoading.value = false
      rememberGroupLabels(cachedGroups)
      syncSelectedGroupSelections(cachedGroups)
      writeLocalSelectOptionWindow(optionWindowKey, cachedGroups)
      groups.value = cachedGroups
      return
    }
  }
  groupOptionsLoading.value = !localWindowGroups?.length
  groupOptionsLoadingKey = requestKey
  groupOptionsLoadingPromise = (async () => {
    try {
      let nextGroups = await groupsApi.options({ systemAccountId, keyword: requestKeyword, limit: 50, manageableOnly: true, preferDefault: true })
      nextGroups = await ensureSelectedGroupOptions(nextGroups, systemAccountId, optionWindowKey)
      rememberGroupLabels(nextGroups)
      syncSelectedGroupSelections(nextGroups)
      groupOptionsCache.set(requestKey, nextGroups)
      writeLocalSelectOptionWindow(optionWindowKey, nextGroups)
      if (requestId !== groupOptionsRequestId) return
      groups.value = nextGroups
    } catch (error) {
      if (requestId !== groupOptionsRequestId) return
      console.error(error)
      message.error('加载分组选项失败')
    } finally {
      if (groupOptionsLoadingKey === requestKey) {
        groupOptionsLoadingKey = undefined
        groupOptionsLoadingPromise = undefined
      }
      if (requestId === groupOptionsRequestId) {
        groupOptionsLoading.value = false
      }
    }
  })()
  return groupOptionsLoadingPromise
}

function handleGroupOptionsDropdown(open: boolean): void {
  if (open) {
    void loadGroupOptions()
  }
}

function handleGroupOptionsSearch(value: string): void {
  groupOptionsKeyword = value
  clearGroupOptionsSearchTimer()
  groupOptionsSearchTimer = window.setTimeout(() => {
    groupOptionsSearchTimer = undefined
    void loadGroupOptions(groupOptionsKeyword)
  }, 250)
}

function resetGroupOptionsSearch(): void {
  groupOptionsKeyword = ''
  clearGroupOptionsSearchTimer()
}

function clearGroupOptionsSearchTimer(): void {
  if (groupOptionsSearchTimer && typeof window !== 'undefined') {
    window.clearTimeout(groupOptionsSearchTimer)
    groupOptionsSearchTimer = undefined
  }
}

async function ensureSelectedGroupOptions(nextGroups: GroupOptionSummary[], systemAccountId: string | undefined, optionWindowKey: string): Promise<GroupOptionSummary[]> {
  const selectedIds = [groupFilter.value, ...formGroupBindingIds.value].filter((id): id is string => Boolean(id))
  const missingIds = [...new Set(selectedIds)].filter((id) => !nextGroups.some((group) => group.id === id))
  if (!missingIds.length) return nextGroups
  const selectedGroups = await Promise.all(missingIds.map(async (id) => {
    try {
      return await groupsApi.options({ systemAccountId, ids: [id], limit: 1, manageableOnly: true, preferDefault: true })
    } catch {
      return []
    }
  }))
  const foundIds = new Set(selectedGroups.flat().map((group) => group.id))
  handleMissingGroupOptions(missingIds.filter((id) => !foundIds.has(id)), optionWindowKey)
  return mergeOptionsById(selectedGroups.flat(), nextGroups)
}

function handleMissingGroupOptions(ids: string[], optionWindowKey: string): void {
  const missingIds = [...new Set(ids.map((id) => id.trim()).filter(Boolean))]
  if (!missingIds.length) return
  removeLocalSelectOptionWindowValues(optionWindowKey, missingIds)
  removeLocalSelectPreferenceValues('groups', missingIds)
  let clearedFilter = false
  if (groupFilter.value && missingIds.includes(groupFilter.value)) {
    groupFilterSelection.value = undefined
    clearedFilter = true
  }
  for (const binding of form.groupBindings) {
    if (!missingIds.includes(binding.groupId)) continue
    binding.groupId = ''
    binding.group = undefined
  }
  message.warning('已移除不存在或无权访问的分组，请重新选择')
  if (clearedFilter) {
    resetPagination()
    void loadData({ forceOptions: true })
  }
}

function groupOptionWindowKey(systemAccountId: string | undefined, requestKeyword: string | undefined): string {
  return localSelectStorageKey([
    'group-options',
    isManagementView.value ? 'management' : 'self',
    systemAccountId ?? 'all',
    'api-keys',
    requestKeyword ?? ''
  ])
}

function syncSelectedGroupSelections(nextGroups = groups.value): void {
  if (groupFilterDisabled.value) {
    groupFilterSelection.value = undefined
    return
  }
  if (groupFilter.value) {
    groupFilterSelection.value = selectedGroupFromOptions(groupFilter.value, nextGroups, groupFilterSelection.value)
  }
  for (const binding of form.groupBindings) {
    if (binding.groupId) {
      binding.group = selectedGroupFromOptions(binding.groupId, nextGroups, binding.group)
    }
  }
}

function selectedGroupFromOptions(id: string | undefined, nextGroups: GroupOptionSummary[], fallback?: GroupSelection): GroupSelection | undefined {
  const normalizedId = id?.trim()
  if (!normalizedId) return undefined
  const group = nextGroups.find((item) => item.id === normalizedId)
  if (group) return { id: group.id, name: group.name }
  return fallback?.id === normalizedId ? fallback : undefined
}

function mergeOptionsById<T extends { id: string }>(leading: T[], trailing: T[]): T[] {
  const merged = new Map<string, T>()
  for (const item of [...leading, ...trailing]) {
    merged.set(item.id, item)
  }
  return [...merged.values()]
}

function normalizeOptionKeyword(value?: string): string | undefined {
  const keyword = value?.trim()
  return keyword ? keyword : undefined
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
    groupId: isManagementView.value && !systemAccountId ? undefined : groupFilter.value
  }
}

function apiKeySystemAccountText(apiKey: ApiKeySummary) {
  return systemAccountDisplayText(apiKey)
}

function formatUsageSummary(usage: AccountUsageSummary): string {
  return `${formatNumber(usage.requestCount)}req / ${formatUsageAmount(usage.totalTokens)} / ${formatCost(usage.totalCost)}`
}

function formatUsageAmount(value?: number): string {
  return formatCompactUsageAmount(value)
}

function formatCost(value?: number): string {
  return formatUsd(value)
}

async function openCreate() {
  if (isManagementView.value && !apiKeyScopeParams.value?.systemAccountId) {
    message.warning('请先在右侧选择目标系统账户，再创建 API Key')
    return
  }
  resetGroupOptionsSearch()
  await loadGroupOptions()
  editingId.value = undefined
  const defaultGroup = groups.value.find((group) => group.enabled)
  if (!defaultGroup) {
    message.warning('请先创建并启用一个分组，再创建 API Key')
    return
  }
  Object.assign(form, {
    name: '',
    groupRouteStrategy: 'priority_failover',
    groupBindings: [createGroupBindingFormRow({ id: defaultGroup.id, name: defaultGroup.name })],
    status: 'active',
    expiresAt: undefined,
    description: '',
    quotaLimits: createQuotaLimitForm(),
    availabilitySchedule: createAvailabilityScheduleForm()
  })
  modalOpen.value = true
}

async function openEdit(apiKey: ApiKeySummary) {
  editingId.value = apiKey.id
  const bindings = apiKeyGroupBindings(apiKey).map((binding) => {
    rememberGroupLabel(binding.groupId, binding.groupName)
    return createGroupBindingFormRow({
      id: binding.groupId,
      name: displayGroupName(binding.groupName, binding.groupId)
    }, binding.status, binding.weight)
  })
  Object.assign(form, {
    name: apiKey.name,
    groupRouteStrategy: apiKey.groupRouteStrategy ?? 'priority_failover',
    groupBindings: bindings.length ? bindings : [createGroupBindingFormRow(apiKey.groupName ? { id: apiKey.groupId, name: apiKey.groupName } : undefined)],
    status: apiKey.status,
    expiresAt: undefined,
    description: apiKey.description ?? '',
    quotaLimits: createQuotaLimitForm(apiKey.quotaLimits),
    availabilitySchedule: createAvailabilityScheduleForm(apiKey.availabilitySchedule)
  })
  resetGroupOptionsSearch()
  await loadGroupOptions()
  modalOpen.value = true
}

function normalizeGroupBindingWeight(value: unknown): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(numeric)) return 1
  return Math.min(100, Math.max(1, Math.trunc(numeric)))
}

function createGroupBindingFormRow(group?: GroupSelection, status: ApiKeyGroupBindingFormStatus = 'active', weight = 1): ApiKeyGroupBindingFormRow {
  return {
    key: `binding_${Date.now()}_${groupBindingFormKeySeed += 1}`,
    groupId: group?.id ?? '',
    group,
    weight: normalizeGroupBindingWeight(weight),
    status
  }
}

function createAvailabilityScheduleForm(schedule?: ApiKeyAvailabilitySchedule): ApiKeyAvailabilityScheduleForm {
  return {
    enabled: schedule?.enabled === true,
    windows: schedule?.windows?.length
      ? schedule.windows.map((window) => createScheduleWindowFormRow(window.daysOfWeek, window.start, window.end))
      : [createScheduleWindowFormRow()]
  }
}

function createScheduleWindowFormRow(daysOfWeek = [1, 2, 3, 4, 5, 6, 7], start = '22:00', end = '23:55'): ApiKeyScheduleWindowFormRow {
  return {
    key: `schedule_window_${Date.now()}_${scheduleWindowFormKeySeed += 1}`,
    daysOfWeek: [...daysOfWeek],
    start,
    end
  }
}

function defaultScheduleTimezone(): string {
  if (typeof Intl === 'undefined') return 'Asia/Shanghai'
  return Intl.DateTimeFormat().resolvedOptions().timeZone || 'Asia/Shanghai'
}

function addScheduleWindow(): void {
  form.availabilitySchedule.windows.push(createScheduleWindowFormRow())
}

function removeScheduleWindow(index: number): void {
  if (form.availabilitySchedule.windows.length <= 1) return
  form.availabilitySchedule.windows.splice(index, 1)
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
  form.groupBindings.push(createGroupBindingFormRow({ id: nextGroup.id, name: nextGroup.name }))
}

function handleGroupBindingChange(index: number) {
  const binding = form.groupBindings[index]
  if (!binding?.groupId) return
  const group = groupOptionForId(binding.groupId)
  if (!group) return
  const providerCode = selectedGroupBindingProviderCode(index)
  if (providerCode && group.providerCode !== providerCode) {
    message.warning('同一个 API Key 的绑定号池必须属于同一供应商')
    binding.groupId = ''
    binding.group = undefined
    return
  }
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

function normalizedGroupBindingPayload(): Array<{ groupId: string; priority: number; weight: number; status: ApiKeyGroupBindingFormStatus }> {
  return form.groupBindings.map((binding, index) => ({
    groupId: binding.groupId.trim(),
    priority: index + 1,
    weight: normalizeGroupBindingWeight(binding.weight),
    status: binding.status
  }))
}

function nextAvailableGroupForNewBinding(): GroupOptionSummary | undefined {
  const selectedIds = new Set(form.groupBindings.map((binding) => binding.groupId.trim()).filter(Boolean))
  const providerCode = selectedGroupBindingProviderCode()
  return groups.value.find((group) => group.enabled && !selectedIds.has(group.id) && (!providerCode || group.providerCode === providerCode))
}

function groupBindingPriorityText(index: number): string {
  if (form.groupRouteStrategy === 'round_robin') return `轮询 ${index + 1}`
  if (form.groupRouteStrategy === 'weighted_round_robin') return `权重 ${index + 1}`
  return index === 0 ? '主号池' : `备 ${index}`
}

function groupBindingPriorityTextByPriority(priority: number | undefined): string {
  const normalizedPriority = typeof priority === 'number' && Number.isFinite(priority)
    ? Math.max(1, Math.trunc(priority))
    : 1
  return normalizedPriority === 1 ? '主号池' : `备 ${normalizedPriority - 1}`
}

function groupOptionsForBinding(index: number): GroupOptionSummary[] {
  const providerCode = selectedGroupBindingProviderCode(index)
  return groups.value.filter((group) => group.enabled && (!providerCode || group.providerCode === providerCode))
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

function selectedGroupBindingProviderCode(excludeIndex?: number): string | undefined {
  for (const [index, binding] of form.groupBindings.entries()) {
    if (excludeIndex === index) continue
    const providerCode = groupOptionForId(binding.groupId)?.providerCode
    if (providerCode) return providerCode
  }
  return undefined
}

function groupOptionForId(groupId: string | undefined): GroupOptionSummary | undefined {
  const id = groupId?.trim()
  if (!id) return undefined
  return groups.value.find((group) => group.id === id)
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
  if (key === 'delete') {
    void removeApiKey(apiKey.id)
  }
}

async function updateApiKeyStatus(apiKey: ApiKeySummary, status: 'active' | 'disabled') {
  statusUpdatingId.value = apiKey.id
  try {
    const updated = await apiKeysApi.update(apiKey.id, { status }, apiKeyScopeParams.value)
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

const saveApiKey = submitAction('api_keys.save', async () => {
  if (!form.name.trim()) {
    message.warning('请填写名称')
    return
  }
  const groupBindings = normalizedGroupBindingPayload()
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
  const providerCodes = new Set(groupBindings.map((binding) => groupOptionForId(binding.groupId)?.providerCode).filter(Boolean))
  if (providerCodes.size > 1) {
    message.warning('同一个 API Key 的绑定号池必须属于同一供应商')
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
  const payload = {
    name: form.name,
    groupRouteStrategy: form.groupRouteStrategy,
    groupId: groupBindings.find((binding) => binding.status === 'active')?.groupId ?? groupBindings[0]?.groupId,
    groupBindings,
    status: form.status,
    expiresAt: formatServerDateTimeInput(form.expiresAt) ?? undefined,
    description: form.description,
    quotaLimits: quotaLimitsPayload(),
    availabilitySchedule
  }
  try {
    const targetId = editingId.value
    if (targetId) {
      const updated = await apiKeysApi.update(targetId, payload, apiKeyScopeParams.value)
      updateApiKeyItems((item) => item.id === targetId, () => updated)
      message.success('API Key 已更新')
      void loadData({ quiet: true })
    } else {
      const result = await apiKeysApi.create(payload, apiKeyScopeParams.value)
      createdKey.value = result.key
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

function availabilitySchedulePayload(): ApiKeyAvailabilitySchedulePayload | null | false {
  if (!form.availabilitySchedule.enabled) {
    return null
  }
  const windows = form.availabilitySchedule.windows.map((window) => ({
    daysOfWeek: [...new Set(window.daysOfWeek.map((day) => Number(day)))].filter((day) => Number.isInteger(day) && day >= 1 && day <= 7).sort((left, right) => left - right),
    start: window.start,
    end: window.end
  }))
  const invalidIndex = windows.findIndex((window) => !window.daysOfWeek.length || !window.start || !window.end || window.start === window.end)
  if (invalidIndex >= 0) {
    message.warning(`请完整填写第 ${invalidIndex + 1} 个自动启停时段`)
    return false
  }
  return {
    enabled: true,
    mode: 'allow_windows',
    windows: windows.map((window) => ({
      daysOfWeek: window.daysOfWeek,
      start: window.start as string,
      end: window.end as string
    }))
  }
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

async function removeApiKey(id: string) {
  try {
    await apiKeysApi.delete(id, apiKeyScopeParams.value)
    removeApiKeyItems((item) => item.id === id)
    message.success('API Key 已删除，关联记录将后台清理')
    void loadData({ quiet: true })
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '删除 API Key 失败'))
  }
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })
watch(groupFilterDisabled, (disabled) => {
  if (!disabled) return
  groupFilterSelection.value = undefined
  groups.value = []
}, { immediate: true })
watch(apiKeys, (items) => {
  for (const item of items) {
    rememberGroupLabel(item.groupId, item.groupName)
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

.api-key-schedule-field,
.schedule-config,
.schedule-window-list {
  display: grid;
  gap: 10px;
}

.schedule-toggle-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  width: 100%;
}

.schedule-toggle-label {
  color: #334155;
  font-size: 13px;
  font-weight: 600;
}

.schedule-window-row {
  display: grid;
  grid-template-columns: minmax(160px, 1fr) 112px 112px 32px;
  gap: 8px;
  align-items: start;
}

.schedule-days-select,
.schedule-time-picker {
  min-width: 0;
}

.schedule-tag {
  max-width: 200px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.key-preview-cell {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}

.key-preview {
  display: inline-flex;
  align-items: center;
  max-width: 120px;
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

  .schedule-window-row {
    grid-template-columns: minmax(0, 1fr) minmax(96px, 1fr);
  }

  .schedule-days-select {
    grid-column: 1 / -1;
  }

  .schedule-window-row > .ant-btn {
    grid-column: 2;
    justify-self: start;
  }
}

</style>

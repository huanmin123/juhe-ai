<template>
  <a-card class="page-card route-strategies-page">
    <div class="route-strategies-toolbar">
      <div class="route-strategies-title">
        <h2>{{ isManagementView ? '策略路由' : '我的策略路由' }}</h2>
        <p>策略路由负责绑定分组和路由模式，API Key 只选择一个策略路由作为入口调度规则。</p>
      </div>
      <a-space wrap>
        <a-input-search v-model:value="keyword" allow-clear placeholder="搜索策略路由" style="width: 240px" @search="loadRouteStrategies" />
        <a-select v-model:value="statusFilter" style="width: 120px" :options="statusFilterOptions" @change="loadRouteStrategies" />
        <a-button :loading="loading" @click="loadRouteStrategies">
          <template #icon><ReloadOutlined /></template>
          刷新
        </a-button>
        <a-button type="primary" @click="openCreate">
          <template #icon><PlusOutlined /></template>
          新建策略路由
        </a-button>
      </a-space>
    </div>

    <a-table
      row-key="id"
      :columns="columns"
      :data-source="items"
      :loading="loading"
      :pagination="pagination"
      :scroll="{ x: 980 }"
      @change="handleTableChange"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'name'">
          <div class="route-strategy-name">
            <strong>{{ record.name }}</strong>
            <span v-if="record.description">{{ record.description }}</span>
          </div>
        </template>
        <template v-else-if="column.key === 'mode'">
          <a-tag :color="routeStrategyModeColor(record.mode)">{{ routeStrategyModeText(record.mode) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag :color="record.status === 'active' ? 'green' : 'default'">{{ record.status === 'active' ? '启用' : '停用' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'groups'">
          <a-space wrap size="small">
            <a-tag v-for="binding in record.groupBindings" :key="binding.id" :color="binding.status === 'active' && binding.groupEnabled ? 'blue' : 'default'">
              {{ binding.groupName || binding.groupId }}
            </a-tag>
            <span v-if="!record.groupBindings.length" class="muted-text">未绑定</span>
          </a-space>
        </template>
        <template v-else-if="column.key === 'actions'">
          <a-space size="small">
            <a-tooltip title="编辑">
              <a-button type="text" size="small" @click="openEdit(record)">
                <template #icon><EditOutlined /></template>
              </a-button>
            </a-tooltip>
            <a-popconfirm title="确认删除这个策略路由？" ok-text="删除" cancel-text="取消" @confirm="deleteRouteStrategy(record)">
              <a-tooltip title="删除">
                <a-button type="text" size="small" danger>
                  <template #icon><DeleteOutlined /></template>
                </a-button>
              </a-tooltip>
            </a-popconfirm>
          </a-space>
        </template>
      </template>
    </a-table>

    <a-modal v-model:open="modalOpen" :title="editingId ? '编辑策略路由' : '新建策略路由'" :confirm-loading="saving" destroy-on-close @ok="saveRouteStrategy">
      <a-form layout="vertical">
        <a-form-item label="名称" required>
          <a-input v-model:value="form.name" placeholder="请输入策略路由名称" />
        </a-form-item>
        <a-form-item label="说明">
          <a-textarea v-model:value="form.description" :rows="2" placeholder="可选" />
        </a-form-item>
        <a-row :gutter="12">
          <a-col :span="12">
            <a-form-item label="路由模式" required>
              <a-select v-model:value="form.mode" :options="modeOptions" />
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item label="状态" required>
              <a-select v-model:value="form.status" :options="statusOptions" />
            </a-form-item>
          </a-col>
        </a-row>

        <a-divider orientation="left">分组绑定</a-divider>
        <div class="route-strategy-binding-list">
          <div v-for="(binding, index) in form.groupBindings" :key="binding.key" class="route-strategy-binding-row">
            <a-select
              v-model:value="binding.groupId"
              show-search
              :filter-option="false"
              :loading="groupOptionsLoading"
              :options="groupOptions"
              placeholder="选择分组"
              @dropdown-visible-change="handleGroupOptionsDropdown"
              @search="handleGroupOptionsSearch"
            />
            <a-input-number v-model:value="binding.priority" :min="1" :max="100000" />
            <a-input-number v-model:value="binding.weight" :min="1" :max="100" />
            <a-select v-model:value="binding.status" :options="statusOptions" />
            <a-button type="text" danger :disabled="form.groupBindings.length <= 1" @click="removeBinding(index)">
              <template #icon><DeleteOutlined /></template>
            </a-button>
          </div>
        </div>
        <a-button type="dashed" block @click="addBinding">
          <template #icon><PlusOutlined /></template>
          添加分组
        </a-button>

        <template v-if="form.mode === 'hybrid_smart'">
          <a-divider orientation="left">混合智能配置</a-divider>
          <a-form-item label="混合智能路由配置 JSON" required>
            <a-textarea v-model:value="form.hybridRoutingConfigJson" :rows="12" class="route-strategy-json-textarea" />
          </a-form-item>
        </template>
      </a-form>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { DeleteOutlined, EditOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons-vue'
import { computed, onMounted, reactive, ref } from 'vue'
import type { TablePaginationConfig } from 'ant-design-vue'

import { message } from '@/lib/antd'
import { useScopedGroupsApi, useScopedRouteStrategiesApi } from '@/composables/useScopedDomainApi'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { ApiKeyHybridRoutingConfig, GroupOptionSummary, RouteStrategyMode, RouteStrategyStatus, RouteStrategySummary } from '@/types/domain'
import type { RouteStrategyMutationPayload } from '@/api/domains/routeStrategies'

interface BindingFormRow {
  key: string
  groupId: string
  priority: number
  weight: number
  status: 'active' | 'disabled'
}

const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const routeStrategiesApi = useScopedRouteStrategiesApi(isManagementView)
const groupsApi = useScopedGroupsApi(isManagementView)
const keyword = ref('')
const statusFilter = ref<RouteStrategyStatus | 'all'>('all')
const loading = ref(false)
const saving = ref(false)
const modalOpen = ref(false)
const editingId = ref<string>()
const items = ref<RouteStrategySummary[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const groupOptionsRaw = ref<GroupOptionSummary[]>([])
const groupOptionsLoading = ref(false)

const form = reactive({
  name: '',
  description: '',
  mode: 'normal' as RouteStrategyMode,
  status: 'active' as RouteStrategyStatus,
  groupBindings: [] as BindingFormRow[],
  hybridRoutingConfigJson: defaultHybridRoutingConfigJson()
})

const columns = [
  { title: '名称', key: 'name', width: 260 },
  { title: '模式', key: 'mode', width: 150 },
  { title: '状态', key: 'status', width: 100 },
  { title: '分组', key: 'groups', width: 360 },
  { title: 'API Key', dataIndex: 'apiKeyCount', key: 'apiKeyCount', width: 100 },
  { title: '操作', key: 'actions', width: 110, fixed: 'right' as const }
]

const modeOptions = [
  { label: '普通路由', value: 'normal' },
  { label: '混合智能路由', value: 'hybrid_smart' },
  { label: '权重调度路由', value: 'weighted' },
  { label: '故障回退路由', value: 'failover' },
  { label: '轮询路由', value: 'round_robin' }
]

const statusOptions = [
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' }
]

const statusFilterOptions = [
  { label: '全部状态', value: 'all' },
  ...statusOptions
]

const pagination = computed<TablePaginationConfig>(() => ({
  current: page.value,
  pageSize: pageSize.value,
  total: total.value,
  showSizeChanger: true,
  showTotal: (value) => `共 ${value} 条`
}))

const groupOptions = computed(() => groupOptionsRaw.value.map((group) => ({
  label: group.name,
  value: group.id,
  disabled: group.enabled === false
})))

onMounted(() => {
  void loadRouteStrategies()
  void loadGroupOptions()
})

async function loadRouteStrategies() {
  loading.value = true
  try {
    const result = await routeStrategiesApi.list({
      page: page.value,
      pageSize: pageSize.value,
      keyword: keyword.value.trim() || undefined,
      status: statusFilter.value,
      systemAccountId: scopedSystemAccountId()
    })
    items.value = result.items
    total.value = result.total
  } catch (error) {
    message.error(extractApiErrorMessage(error, '策略路由加载失败'))
  } finally {
    loading.value = false
  }
}

function handleTableChange(nextPagination: TablePaginationConfig) {
  page.value = nextPagination.current ?? 1
  pageSize.value = nextPagination.pageSize ?? 20
  void loadRouteStrategies()
}

function openCreate() {
  editingId.value = undefined
  form.name = ''
  form.description = ''
  form.mode = 'normal'
  form.status = 'active'
  form.groupBindings = [createBindingRow()]
  form.hybridRoutingConfigJson = defaultHybridRoutingConfigJson()
  modalOpen.value = true
}

function openEdit(record: RouteStrategySummary) {
  editingId.value = record.id
  form.name = record.name
  form.description = record.description ?? ''
  form.mode = record.mode
  form.status = record.status
  form.groupBindings = record.groupBindings.length
    ? record.groupBindings.map((binding) => createBindingRow(binding.groupId, binding.priority, binding.weight, binding.status))
    : [createBindingRow()]
  form.hybridRoutingConfigJson = record.hybridRoutingConfig
    ? JSON.stringify(record.hybridRoutingConfig, null, 2)
    : defaultHybridRoutingConfigJson()
  modalOpen.value = true
}

async function saveRouteStrategy() {
  const name = form.name.trim()
  if (!name) {
    message.warning('请输入策略路由名称')
    return
  }
  const groupBindings = form.groupBindings.map((binding) => ({
    groupId: binding.groupId.trim(),
    priority: binding.priority,
    weight: binding.weight,
    status: binding.status
  }))
  if (!groupBindings.every((binding) => binding.groupId)) {
    message.warning('请选择分组')
    return
  }
  saving.value = true
  try {
    const payload: RouteStrategyMutationPayload = {
      name,
      description: form.description.trim() || null,
      mode: form.mode,
      status: form.status,
      groupBindings
    }
    if (form.mode === 'hybrid_smart') {
      const hybridRoutingConfig = parseHybridRoutingConfigJson(form.hybridRoutingConfigJson)
      if (hybridRoutingConfig === false) return
      payload.hybridRoutingConfig = hybridRoutingConfig
    } else {
      payload.hybridRoutingConfig = null
    }
    if (editingId.value) {
      await routeStrategiesApi.update(editingId.value, payload, { systemAccountId: scopedSystemAccountId() })
      message.success('策略路由已更新')
    } else {
      await routeStrategiesApi.create(payload, { systemAccountId: scopedSystemAccountId() })
      message.success('策略路由已创建')
    }
    modalOpen.value = false
    await loadRouteStrategies()
  } catch (error) {
    message.error(extractApiErrorMessage(error, '策略路由保存失败'))
  } finally {
    saving.value = false
  }
}

async function deleteRouteStrategy(record: RouteStrategySummary) {
  try {
    await routeStrategiesApi.delete(record.id, { systemAccountId: scopedSystemAccountId() })
    message.success('策略路由已删除')
    await loadRouteStrategies()
  } catch (error) {
    message.error(extractApiErrorMessage(error, '策略路由删除失败'))
  }
}

function addBinding() {
  form.groupBindings.push(createBindingRow())
}

function removeBinding(index: number) {
  if (form.groupBindings.length <= 1) return
  form.groupBindings.splice(index, 1)
}

function createBindingRow(groupId = '', priority = form.groupBindings.length + 1, weight = 1, status: 'active' | 'disabled' = 'active'): BindingFormRow {
  return {
    key: `${Date.now()}-${Math.random().toString(16).slice(2)}`,
    groupId,
    priority,
    weight,
    status
  }
}

async function loadGroupOptions(keywordInput = '') {
  groupOptionsLoading.value = true
  try {
    groupOptionsRaw.value = await groupsApi.options({
      keyword: keywordInput.trim() || undefined,
      limit: 100,
      manageableOnly: true,
      systemAccountId: scopedSystemAccountId()
    })
  } catch (error) {
    message.error(extractApiErrorMessage(error, '分组选项加载失败'))
  } finally {
    groupOptionsLoading.value = false
  }
}

function handleGroupOptionsDropdown(open: boolean) {
  if (open && !groupOptionsRaw.value.length) void loadGroupOptions()
}

function handleGroupOptionsSearch(value: string) {
  void loadGroupOptions(value)
}

function routeStrategyModeText(mode: RouteStrategyMode): string {
  return modeOptions.find((item) => item.value === mode)?.label ?? mode
}

function routeStrategyModeColor(mode: RouteStrategyMode): string {
  if (mode === 'hybrid_smart') return 'cyan'
  if (mode === 'weighted') return 'purple'
  if (mode === 'round_robin') return 'blue'
  if (mode === 'failover') return 'orange'
  return 'default'
}

function defaultHybridRoutingConfigJson(): string {
  return JSON.stringify({
    scoringModel: '',
    scoringContextMode: 'full_request',
    qualityPreference: 'balanced',
    scoringTimeoutMs: 10000,
    scoringFallbackMaxLevel: 5,
    scoringCacheEnabled: true,
    scoringCacheTtlSeconds: 300,
    cacheAffinityEnabled: true,
    affinityTtlSeconds: 900,
    switchMinLevelDelta: 2,
    downgradeConsecutiveLowCount: 2,
    levelRoutes: [
      {
        minLevel: 1,
        maxLevel: 10,
        targetModel: '',
        enabled: true
      }
    ]
  }, null, 2)
}

function parseHybridRoutingConfigJson(value: string): ApiKeyHybridRoutingConfig | false {
  try {
    const parsed = JSON.parse(value)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      message.warning('混合智能路由配置必须是 JSON 对象')
      return false
    }
    return parsed as ApiKeyHybridRoutingConfig
  } catch {
    message.warning('混合智能路由配置 JSON 格式无效')
    return false
  }
}
</script>

<style scoped>
.route-strategies-page {
  min-height: 100%;
}

.route-strategies-toolbar {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 16px;
}

.route-strategies-title h2 {
  margin: 0;
  font-size: 20px;
}

.route-strategies-title p {
  margin: 4px 0 0;
  color: rgba(0, 0, 0, 0.55);
}

.route-strategy-name {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.route-strategy-name span,
.muted-text {
  color: rgba(0, 0, 0, 0.45);
}

.route-strategy-binding-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 8px;
}

.route-strategy-binding-row {
  display: grid;
  grid-template-columns: minmax(180px, 1fr) 92px 92px 104px 36px;
  gap: 8px;
  align-items: center;
}

.route-strategy-json-textarea {
  font-family: Consolas, 'Courier New', monospace;
}

@media (max-width: 720px) {
  .route-strategies-toolbar {
    flex-direction: column;
  }

  .route-strategy-binding-row {
    grid-template-columns: 1fr;
  }
}
</style>

<template>
  <div class="model-checks-page">
    <a-card class="page-card model-checks-run-card">
      <a-form class="model-checks-form" layout="vertical">
        <a-row :gutter="[16, 12]">
          <a-col :xs="24" :lg="6">
            <a-form-item label="目标类型" required>
              <a-select v-model:value="form.targetType" :options="targetTypeOptions" :disabled="submitting" />
            </a-form-item>
          </a-col>
          <a-col :xs="24" :lg="10">
            <a-form-item label="检测目标" required>
              <a-select
                v-model:value="form.targetId"
                show-search
                allow-clear
                :disabled="submitting"
                :filter-option="false"
                :loading="targetOptionsLoading"
                :options="targetOptions"
                :placeholder="targetPlaceholder"
                @dropdown-visible-change="handleTargetDropdownVisibleChange"
                @search="handleTargetSearch"
              />
            </a-form-item>
          </a-col>
          <a-col :xs="24" :lg="4">
            <a-form-item label="模型" required>
              <a-select v-model:value="form.model" :options="modelOptions" :loading="optionsLoading" :disabled="submitting" />
            </a-form-item>
          </a-col>
          <a-col :xs="24" :lg="4">
            <a-form-item label="检测档位">
              <a-select v-model:value="form.profile" :options="profileOptions" :loading="optionsLoading" :disabled="submitting" />
            </a-form-item>
          </a-col>
        </a-row>

        <div class="model-checks-actions">
          <a-space wrap>
            <a-switch v-model:checked="form.officialBaseline" :disabled="submitting" @change="handleOfficialBaselineChange" />
            <span class="baseline-label">官网对照</span>
          </a-space>
          <a-space wrap>
            <a-button :loading="optionsLoading" @click="loadOptions">
              <template #icon>
                <ReloadOutlined />
              </template>
              刷新选项
            </a-button>
            <a-button type="primary" :loading="submitting" @click="submitRun">
              <template #icon>
                <ExperimentOutlined />
              </template>
              开始检测
            </a-button>
          </a-space>
        </div>

        <a-alert
          v-if="officialBaselineMessage"
          class="baseline-alert"
          show-icon
          :type="officialBaselineAvailable ? 'info' : 'warning'"
          :message="officialBaselineMessage"
        />
      </a-form>
    </a-card>

    <a-card class="page-card model-checks-detail-card" title="当前结果详情">
      <a-skeleton v-if="detailLoading" active :paragraph="{ rows: 5 }" />
      <a-empty v-else-if="!currentRun" description="尚未选择或发起检测" />
      <div v-else class="run-detail">
        <div class="run-detail-head">
          <div>
            <div class="run-detail-title">{{ currentRun.targetName || modelText(currentRun.model) }}</div>
            <div class="run-detail-subtitle">
              {{ targetTypeText(currentRun.targetType) }}：<span class="mono-cell">{{ currentRun.targetId }}</span>
            </div>
          </div>
          <a-space wrap>
            <a-tag :color="statusColor(currentRun.status)">{{ statusText(currentRun.status) }}</a-tag>
            <a-tag :color="levelColor(currentRun.level)">{{ levelText(currentRun.level) }}</a-tag>
            <a-tag>{{ profileText(currentRun.profile) }}</a-tag>
            <a-tag v-if="currentRun.officialBaseline" color="blue">官网对照</a-tag>
            <a-tag>{{ currentRun.score }} / {{ currentRun.maxScore }}</a-tag>
          </a-space>
        </div>

        <a-descriptions bordered size="small" :column="detailDescriptionColumns" class="run-descriptions">
          <a-descriptions-item label="检测 ID">{{ currentRun.id }}</a-descriptions-item>
          <a-descriptions-item label="模型">{{ modelText(currentRun.model) }}</a-descriptions-item>
          <a-descriptions-item label="创建时间">{{ formatDateTime(currentRun.createdAt) }}</a-descriptions-item>
          <a-descriptions-item label="完成时间">{{ formatDateTime(currentRun.finishedAt) }}</a-descriptions-item>
          <a-descriptions-item label="耗时">{{ formatDuration(currentRun.durationMs) }}</a-descriptions-item>
          <a-descriptions-item label="结论">{{ currentRun.message || currentRun.errorMessage || '-' }}</a-descriptions-item>
          <a-descriptions-item label="Trace ID">{{ currentRun.traceId || '-' }}</a-descriptions-item>
        </a-descriptions>

        <div v-if="currentRun.checks.length" class="check-list">
          <div v-for="check in currentRun.checks" :key="check.id" class="check-item">
            <div class="check-item-head">
              <span>{{ checkTitle(check) }}</span>
              <a-space wrap>
                <a-tag :color="checkStatusColor(check.status)">{{ checkStatusText(check.status) }}</a-tag>
                <a-tag>{{ check.score }} / {{ check.maxScore }}</a-tag>
              </a-space>
            </div>
            <div v-if="checkMessage(check)" class="check-message">{{ checkMessage(check) }}</div>
            <pre v-if="hasCheckExtra(check)" class="json-block">{{ formatJson(checkExtra(check)) }}</pre>
          </div>
        </div>

        <pre class="json-block">{{ formatJson({ request: currentRun.requestSummary, result: currentRun.resultSummary }) }}</pre>
      </div>
    </a-card>

    <a-card class="page-card model-checks-history-card" title="历史检测">
      <div class="history-toolbar">
        <a-space wrap>
          <a-select v-model:value="filters.targetType" allow-clear class="history-filter" :options="targetTypeOptions" placeholder="全部目标" @change="reloadRuns" />
          <a-select v-model:value="filters.model" allow-clear class="history-filter" :options="modelOptions" placeholder="全部模型" @change="reloadRuns" />
          <a-select v-model:value="filters.status" allow-clear class="history-filter" :options="statusOptions" placeholder="全部状态" @change="reloadRuns" />
          <a-select v-model:value="filters.level" allow-clear class="history-filter" :options="levelOptions" placeholder="全部级别" @change="reloadRuns" />
          <a-input-search v-model:value="filters.targetId" class="history-target-filter" placeholder="按目标 ID 过滤" allow-clear @search="reloadRuns" />
        </a-space>
        <a-button :loading="runsLoading" @click="reloadRuns">
          <template #icon>
            <ReloadOutlined />
          </template>
          刷新
        </a-button>
      </div>

      <a-table
        class="model-checks-table"
        size="middle"
        row-key="id"
        :columns="columns"
        :data-source="runs"
        :loading="runsLoading"
        :pagination="pagination"
        :scroll="{ x: 1100 }"
        @change="handleTableChange"
      >
        <template #emptyText>
          <a-empty description="暂无模型检测历史" />
        </template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'target'">
            <div class="target-cell">
              <a-tag>{{ targetTypeText(record.targetType) }}</a-tag>
              <span class="mono-cell">{{ record.targetId }}</span>
            </div>
          </template>
          <template v-else-if="column.key === 'status'">
            <a-tag :color="statusColor(record.status)">{{ statusText(record.status) }}</a-tag>
          </template>
          <template v-else-if="column.key === 'level'">
            <a-tag :color="levelColor(record.level)">{{ levelText(record.level) }}</a-tag>
          </template>
          <template v-else-if="column.key === 'model'">
            {{ modelText(record.model) }}
          </template>
          <template v-else-if="column.key === 'createdAt'">
            {{ formatDateTime(record.createdAt) }}
          </template>
          <template v-else-if="column.key === 'summary'">
            <span class="summary-cell">{{ record.message || record.errorMessage || '-' }}</span>
          </template>
          <template v-else-if="column.key === 'actions'">
            <a-button type="link" size="small" @click="loadRunDetail(record.id)">查看</a-button>
          </template>
        </template>
      </a-table>
    </a-card>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ExperimentOutlined, ReloadOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'

import { useScopedAccountsApi, useScopedApiKeysApi, useScopedGroupsApi, useScopedModelChecksApi } from '@/composables/useScopedDomainApi'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime, formatNumber } from '@/shared/formatters'
import type {
  AccountOptionSummary,
  ApiKeySummary,
  GroupOptionSummary,
  ModelCheckCheckResult,
  ModelCheckLevel,
  ModelCheckModel,
  ModelCheckOptions,
  ModelCheckProfile,
  ModelCheckRunDetail,
  ModelCheckRunPayload,
  ModelCheckRunSummary,
  ModelCheckStatus,
  ModelCheckTargetType
} from '@/types/domain'

const fallbackOptions: ModelCheckOptions = {
  supportedModels: [
    { value: 'gpt-5.5', label: 'gpt-5.5' },
    { value: 'gpt-5.4', label: 'gpt-5.4' }
  ],
  supportedProfiles: [
    { value: 'full', label: '完整检测', description: '完整检测' }
  ],
  officialBaseline: { enabledByDefault: false, available: false, message: '当前后端未返回官网对照可用状态，默认关闭。' },
  defaultModel: 'gpt-5.5',
  defaultProfile: 'full'
}

const targetTypeOptions = [
  { label: 'API Key', value: 'api_key' },
  { label: '分组', value: 'group' },
  { label: '账户', value: 'account' }
]
const statusOptions = [
  { label: '检测中', value: 'running' },
  { label: '已完成', value: 'completed' },
  { label: '失败', value: 'failed' },
  { label: '已取消', value: 'canceled' }
]
const levelOptions = [
  { label: '高可信', value: 'high_confidence' },
  { label: '较可信', value: 'likely' },
  { label: '不确定', value: 'uncertain' },
  { label: '疑似不符', value: 'suspicious' },
  { label: '不可检测', value: 'unavailable' }
]
const columns = [
  { title: '目标', key: 'target', width: 300 },
  { title: '模型', key: 'model', width: 130 },
  { title: '状态', key: 'status', width: 110 },
  { title: '级别', key: 'level', width: 100 },
  { title: '摘要', key: 'summary', width: 320 },
  { title: '创建时间', key: 'createdAt', width: 180 },
  { title: '操作', key: 'actions', width: 90, fixed: 'right' }
]

const { isManagementView } = useScopedMenuView()
const modelChecksApi = useScopedModelChecksApi(isManagementView)
const apiKeysApi = useScopedApiKeysApi(isManagementView)
const groupsApi = useScopedGroupsApi(isManagementView)
const accountsApi = useScopedAccountsApi(isManagementView)
const optionsLoading = ref(false)
const targetOptionsLoading = ref(false)
const submitting = ref(false)
const runsLoading = ref(false)
const detailLoading = ref(false)
const options = ref<ModelCheckOptions>(fallbackOptions)
const targetOptions = ref<Array<{ label: string; value: string }>>([])
const runs = ref<ModelCheckRunSummary[]>([])
const currentRun = ref<ModelCheckRunDetail>()
const form = reactive<ModelCheckRunPayload>({
  targetType: 'api_key',
  targetId: '',
  model: 'gpt-5.5',
  profile: 'full',
  officialBaseline: false
})
const filters = reactive<{
  targetType?: ModelCheckTargetType
  targetId?: string
  model?: ModelCheckModel
  level?: ModelCheckLevel
  status?: ModelCheckStatus
}>({})
const pagination = reactive({
  current: 1,
  pageSize: 20,
  total: 0,
  showSizeChanger: true,
  showTotal: (total: number) => `共 ${formatNumber(total)} 条检测记录`
})

const modelOptions = computed(() => options.value.supportedModels.map((item) => ({ label: item.label, value: item.value })))
const profileOptions = computed(() => options.value.supportedProfiles.map((item) => ({
  label: item.description ? `${item.label}（${item.description}）` : item.label,
  value: item.value
})))
const targetPlaceholder = computed(() => {
  if (form.targetType === 'api_key') return '搜索并选择 API Key'
  if (form.targetType === 'group') return '搜索并选择分组'
  return '搜索并选择账户'
})
const officialBaselineAvailable = computed(() => options.value.officialBaseline.available)
const officialBaselineMessage = computed(() => {
  if (options.value.officialBaseline.message) return options.value.officialBaseline.message
  if (options.value.officialBaseline.unavailableReason) return options.value.officialBaseline.unavailableReason
  return officialBaselineAvailable.value ? '' : '当前环境未启用官网对照；如强制开启，后端会重新校验并返回失败原因。'
})
const detailDescriptionColumns = computed(() => (window.innerWidth < 900 ? 1 : 2))
let targetOptionsRequestId = 0

async function loadOptions() {
  optionsLoading.value = true
  try {
    const nextOptions = await modelChecksApi.options()
    options.value = nextOptions
    form.model = nextOptions.defaultModel
    form.profile = nextOptions.defaultProfile
    if (!nextOptions.officialBaseline.available) {
      form.officialBaseline = false
    }
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载模型检测选项失败'))
  } finally {
    optionsLoading.value = false
  }
}

async function loadTargetOptions(keyword = '') {
  const requestId = ++targetOptionsRequestId
  targetOptionsLoading.value = true
  try {
    let nextOptions: Array<{ label: string; value: string }> = []
    if (form.targetType === 'api_key') {
      const result = await apiKeysApi.list({
        page: 1,
        pageSize: 50,
        keyword: keyword.trim() || undefined,
        status: 'active'
      })
      nextOptions = result.items.map(apiKeyTargetOption)
    } else if (form.targetType === 'group') {
      const groups = await groupsApi.options({
        keyword: keyword.trim() || undefined,
        providerCode: 'openai',
        limit: 50
      })
      nextOptions = groups.filter((group) => group.enabled).map(groupTargetOption)
    } else {
      const accounts = await accountsApi.options({
        keyword: keyword.trim() || undefined,
        status: 'active',
        schedulable: 'enabled',
        limit: 50
      })
      nextOptions = accounts.filter((account) => account.providerCode === 'openai').map(accountTargetOption)
    }
    if (requestId === targetOptionsRequestId) {
      targetOptions.value = keepSelectedTargetOption(nextOptions)
    }
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载检测目标失败'))
  } finally {
    if (requestId === targetOptionsRequestId) {
      targetOptionsLoading.value = false
    }
  }
}

async function submitRun() {
  const targetId = form.targetId.trim()
  if (!targetId) {
    message.warning('请输入目标 ID')
    return
  }
  submitting.value = true
  try {
    const payload: ModelCheckRunPayload = {
      targetType: form.targetType,
      targetId,
      model: form.model,
      profile: form.profile,
      officialBaseline: form.officialBaseline
    }
    currentRun.value = await modelChecksApi.run(payload)
    message.success('模型检测完成')
    await reloadRuns()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '模型检测提交失败'))
  } finally {
    submitting.value = false
  }
}

async function reloadRuns() {
  pagination.current = 1
  await loadRuns()
}

async function loadRuns() {
  runsLoading.value = true
  try {
    const result = await modelChecksApi.list({
      page: pagination.current,
      pageSize: pagination.pageSize,
      targetType: filters.targetType,
      targetId: filters.targetId?.trim() || undefined,
      model: filters.model,
      level: filters.level,
      status: filters.status
    })
    runs.value = result.items
    pagination.current = result.page
    pagination.pageSize = result.pageSize
    pagination.total = result.total
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载模型检测历史失败'))
  } finally {
    runsLoading.value = false
  }
}

async function loadRunDetail(id: string) {
  detailLoading.value = true
  try {
    currentRun.value = await modelChecksApi.detail(id)
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载模型检测详情失败'))
  } finally {
    detailLoading.value = false
  }
}

function handleTableChange(nextPagination: { current?: number; pageSize?: number }) {
  pagination.current = nextPagination.current ?? pagination.current
  pagination.pageSize = nextPagination.pageSize ?? pagination.pageSize
  void loadRuns()
}

function handleTargetSearch(value: string) {
  void loadTargetOptions(value)
}

function handleTargetDropdownVisibleChange(open: boolean) {
  if (open && !targetOptions.value.length) {
    void loadTargetOptions()
  }
}

function handleOfficialBaselineChange(checked: boolean) {
  if (!checked || officialBaselineAvailable.value) {
    return
  }
  form.officialBaseline = false
  message.warning(options.value.officialBaseline.unavailableReason || options.value.officialBaseline.message || '未配置可用的官网基线账户，无法开启官网对照检测')
}

function apiKeyTargetOption(apiKey: ApiKeySummary) {
  const parts = [apiKey.name, apiKey.keyPrefix, apiKey.groupName || apiKey.groupId].filter(Boolean)
  return { label: parts.join(' · '), value: apiKey.id }
}

function groupTargetOption(group: GroupOptionSummary) {
  const owner = group.systemAccountName || group.ownerSystemAccountName
  const parts = [group.name, owner].filter(Boolean)
  return { label: parts.join(' · '), value: group.id }
}

function accountTargetOption(account: AccountOptionSummary) {
  const owner = account.systemAccountName || account.ownerSystemAccountName
  const parts = [account.name, accountTypeText(account.type), owner].filter(Boolean)
  return { label: parts.join(' · '), value: account.id }
}

function keepSelectedTargetOption(nextOptions: Array<{ label: string; value: string }>) {
  if (!form.targetId || nextOptions.some((item) => item.value === form.targetId)) {
    return nextOptions
  }
  return [{ label: form.targetId, value: form.targetId }, ...nextOptions]
}

function targetTypeText(value: ModelCheckTargetType) {
  if (value === 'api_key') return 'API Key'
  if (value === 'group') return '分组'
  return '账户'
}

function accountTypeText(value: string) {
  if (value === 'api_key') return 'API Key 账户'
  if (value === 'oauth') return 'OAuth 账户'
  return value
}

function statusText(value: ModelCheckStatus) {
  return statusOptions.find((item) => item.value === value)?.label ?? value
}

function statusColor(value: ModelCheckStatus) {
  if (value === 'completed') return 'green'
  if (value === 'failed') return 'red'
  if (value === 'running') return 'blue'
  return 'default'
}

function levelText(value: ModelCheckLevel) {
  return levelOptions.find((item) => item.value === value)?.label ?? value
}

function levelColor(value: ModelCheckLevel) {
  if (value === 'high_confidence') return 'green'
  if (value === 'likely') return 'blue'
  if (value === 'uncertain') return 'orange'
  if (value === 'suspicious') return 'red'
  return 'default'
}

function checkStatusText(value: NonNullable<ModelCheckCheckResult['status']>) {
  if (value === 'passed') return '通过'
  if (value === 'warning') return '需关注'
  if (value === 'failed') return '失败'
  if (value === 'skipped') return '跳过'
  return value
}

function checkStatusColor(value: NonNullable<ModelCheckCheckResult['status']>) {
  if (value === 'passed') return 'green'
  if (value === 'warning') return 'orange'
  if (value === 'failed') return 'red'
  if (value === 'skipped') return 'default'
  return 'default'
}

function modelText(value: string) {
  return options.value.supportedModels.find((item) => item.value === value)?.label ?? value
}

function profileText(value?: string) {
  return options.value.supportedProfiles.find((item) => item.value === value)?.label ?? '完整检测'
}

function formatDuration(value?: number) {
  if (value === undefined) return '-'
  if (value >= 1000) return `${(value / 1000).toFixed(1)} 秒`
  return `${Math.round(value)} 毫秒`
}

function checkTitle(check: ModelCheckCheckResult) {
  const labels: Record<string, string> = {
    model_catalog: '模型目录',
    responses_basic: 'Responses 非流式',
    responses_stream: 'Responses 流式',
    structured_output: '结构化输出',
    tool_calling: '工具调用',
    usage_shape: 'Usage 字段',
    behavior_probe: '行为探针',
    long_context: '长上下文找针',
    stability: '稳定性探针',
    cross_model: '交叉模型对照',
    official_baseline: '官网对照'
  }
  return labels[check.itemType] ?? check.itemKey
}

function checkMessage(check: ModelCheckCheckResult) {
  const message = check.evidenceSummary.message
  return typeof message === 'string' && message.trim() ? message.trim() : check.errorMessage
}

function hasCheckExtra(check: ModelCheckCheckResult) {
  return Object.keys(check.evidenceSummary).length > 0 || Boolean(check.traceId)
}

function checkExtra(check: ModelCheckCheckResult) {
  return {
    traceId: check.traceId,
    evidence: check.evidenceSummary,
    errorCode: check.errorCode,
    errorMessage: check.errorMessage
  }
}

function formatJson(value: unknown) {
  return JSON.stringify(value, null, 2)
}

onMounted(async () => {
  await Promise.all([loadOptions(), loadRuns(), loadTargetOptions()])
})

watch(() => form.targetType, () => {
  form.targetId = ''
  targetOptions.value = []
  void loadTargetOptions()
})
</script>

<style scoped>
.model-checks-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.model-checks-run-card,
.model-checks-detail-card,
.model-checks-history-card {
  border: 1px solid #e8edf5;
  border-radius: 16px;
}

.model-checks-form :deep(.ant-form-item) {
  margin-bottom: 0;
}

.model-checks-actions,
.history-toolbar,
.run-detail-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
}

.baseline-label {
  color: #334155;
  font-size: 14px;
}

.baseline-alert {
  margin-top: 14px;
  border-radius: 8px;
}

.run-detail {
  display: grid;
  gap: 14px;
}

.run-detail-title {
  color: #0f172a;
  font-size: 16px;
  font-weight: 700;
}

.run-detail-subtitle {
  margin-top: 4px;
  color: #64748b;
  font-size: 13px;
}

.run-descriptions {
  background: #fff;
}

.check-list {
  display: grid;
  gap: 10px;
}

.check-item {
  padding: 12px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fbfdff;
}

.check-item-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  color: #0f172a;
  font-weight: 700;
}

.check-message {
  margin-top: 6px;
  color: #475569;
  font-size: 13px;
  line-height: 1.6;
}

.json-block {
  max-height: 320px;
  margin: 10px 0 0;
  padding: 12px;
  overflow: auto;
  color: #dbeafe;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 18px;
  white-space: pre-wrap;
  word-break: break-word;
  background: #0f172a;
  border-radius: 8px;
}

.history-toolbar {
  margin-bottom: 14px;
}

.history-filter {
  width: 140px;
}

.history-target-filter {
  width: 240px;
}

.target-cell {
  display: inline-flex;
  max-width: 100%;
  align-items: center;
  gap: 8px;
}

.summary-cell {
  display: block;
  max-width: 360px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.model-checks-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

@media (max-width: 900px) {
  .model-checks-actions,
  .history-toolbar,
  .run-detail-head,
  .check-item-head {
    align-items: flex-start;
    flex-direction: column;
  }

  .history-filter,
  .history-target-filter {
    width: 100%;
  }
}
</style>

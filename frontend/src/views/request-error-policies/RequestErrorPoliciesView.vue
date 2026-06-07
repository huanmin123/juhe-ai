<template>
  <a-card class="page-card responsive-page-card request-error-policy-page">
    <ResponsiveListToolbar
      v-model:keyword="keyword"
      search-placeholder="搜索策略名称、层级或匹配条件"
      :show-reset="Boolean(keyword.trim())"
      :refresh-loading="loading"
      @search="applySearch"
      @reset="resetSearch"
      @refresh="loadPageData"
    >
      <template #actions>
        <a-button @click="guideOpen = true">
          <template #icon><question-circle-outlined /></template>
          配置指南
        </a-button>
        <a-button type="primary" @click="openCreate">新建策略</a-button>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList
      table-class="page-table request-error-policy-table"
      :columns="columns"
      :data-source="filteredPolicies"
      row-key="id"
      :loading="loading"
      :pagination="{ pageSize: 20, hideOnSinglePage: true, showSizeChanger: false }"
      :scroll-x="2200"
      pull-refresh-enabled
      :refreshing="loading"
      @mobile-refresh="loadPageData"
    >
      <template #emptyText>
        <a-empty class="page-empty-card" description="暂无请求错误策略" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'name'">
          <strong class="policy-name-text">{{ record.name }}</strong>
        </template>
        <template v-else-if="column.key === 'scope'">
          <a-tag :color="scopeColor(record.scopeType)">{{ scopeText(record) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'target'">
          <div class="field-cell">{{ targetText(record) }}</div>
        </template>
        <template v-else-if="column.key === 'priority'">
          <span>{{ record.priority }}</span>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'statusCodes'">
          <div class="field-cell">{{ numberListText(record.match.statusCodes) }}</div>
        </template>
        <template v-else-if="column.key === 'errorCodes'">
          <div class="field-cell">{{ listText(record.match.errorCodes) }}</div>
        </template>
        <template v-else-if="column.key === 'errorTypes'">
          <div class="field-cell">{{ listText(record.match.errorTypes) }}</div>
        </template>
        <template v-else-if="column.key === 'keywords'">
          <div class="field-cell text-field-cell">{{ listText(record.match.keywords) }}</div>
        </template>
        <template v-else-if="column.key === 'action'">
          <a-tag :color="actionColor(record.action)">{{ actionText(record.action) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'recovery'">
          <div class="field-cell">{{ recoveryText(record) }}</div>
        </template>
        <template v-else-if="column.key === 'notes'">
          <div class="field-cell text-field-cell">{{ record.notes || '-' }}</div>
        </template>
        <template v-else-if="column.key === 'updatedAt'">
          <span>{{ record.updatedAt || '-' }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <RowActions :actions="actionsFor(record)" @action-click="handlePolicyAction($event, record)" />
        </template>
      </template>
      <template #card="{ record }">
        <article class="mobile-list-card request-error-policy-mobile-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">{{ record.name }}</div>
            <div class="mobile-list-card-tags">
              <a-tag :color="scopeColor(record.scopeType)">{{ scopeText(record) }}</a-tag>
              <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
            </div>
          </div>
          <div class="mobile-list-meta-grid">
            <div class="mobile-list-meta-item">
              <span>目标</span>
              <strong>{{ targetText(record) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>优先级</span>
              <strong>{{ record.priority }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>状态码</span>
              <strong>{{ numberListText(record.match.statusCodes) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>错误码</span>
              <strong>{{ listText(record.match.errorCodes) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>错误类型</span>
              <strong>{{ listText(record.match.errorTypes) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>关键词</span>
              <strong>{{ listText(record.match.keywords) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>处置</span>
              <strong>{{ actionText(record.action) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>恢复</span>
              <strong>{{ recoveryText(record) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>备注</span>
              <strong>{{ record.notes || '-' }}</strong>
            </div>
          </div>
          <div class="mobile-list-card-actions">
            <RowActions variant="button" :actions="actionsFor(record)" @action-click="handlePolicyAction($event, record)" />
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <a-modal
      v-model:open="modalOpen"
      :title="modalTitle"
      width="940px"
      :confirm-loading="saving"
      :ok-button-props="{ type: 'primary', disabled: saving }"
      @ok="savePolicy"
      @cancel="resetModal"
    >
      <a-form layout="vertical" class="policy-form">
        <section class="form-section">
          <div class="form-section-title">基础</div>
          <div class="form-grid three">
            <a-form-item label="策略名称" required>
              <a-input v-model:value="form.name" placeholder="例如 OpenAI 429 限流" />
            </a-form-item>
            <a-form-item label="作用层级" required>
              <a-select v-model:value="form.scopeType" :options="scopeOptions" @change="handleScopeChange" />
            </a-form-item>
            <a-form-item v-if="form.scopeType === 'provider' || form.scopeType === 'model'" :label="form.scopeType === 'model' ? '供应商（可选）' : '供应商'" :required="form.scopeType === 'provider'">
              <a-select
                v-model:value="form.providerCode"
                :options="providerSelectOptions"
                allow-clear
                placeholder="选择 OpenAI v1 供应商"
                show-search
                option-filter-prop="label"
              />
            </a-form-item>
            <a-form-item v-if="form.scopeType === 'client'" label="客户端" required>
              <a-select v-model:value="form.clientProfile" :options="clientProfileOptions" />
            </a-form-item>
            <a-form-item v-if="form.scopeType === 'model'" label="模型匹配值" required>
              <a-input v-model:value="form.modelPattern" placeholder="例如 gpt-" />
            </a-form-item>
            <a-form-item v-if="form.scopeType === 'model'" label="模型匹配方式">
              <a-select v-model:value="form.modelMatchType" :options="modelMatchTypeOptions" />
            </a-form-item>
            <a-form-item label="优先级">
              <a-input-number v-model:value="form.priority" :min="1" :max="9999" style="width: 100%" />
            </a-form-item>
          </div>
          <a-form-item label="启用状态">
            <a-switch v-model:checked="form.enabled" checked-children="启用" un-checked-children="停用" />
          </a-form-item>
        </section>

        <section class="form-section">
          <div class="form-section-title">匹配条件</div>
          <div class="form-grid two">
            <a-form-item label="状态码">
              <a-input v-model:value="form.statusCodes" placeholder="429, 500, 502" />
            </a-form-item>
            <a-form-item label="错误码">
              <a-input v-model:value="form.errorCodes" placeholder="rate_limit_exceeded, insufficient_quota" />
            </a-form-item>
            <a-form-item label="错误类型">
              <a-input v-model:value="form.errorTypes" placeholder="server_error, invalid_request_error" />
            </a-form-item>
            <a-form-item label="关键词">
              <a-textarea v-model:value="form.keywords" :rows="1" auto-size placeholder="多个关键词用逗号、分号或换行分隔" />
            </a-form-item>
          </div>
        </section>

        <section class="form-section">
          <div class="form-section-title">处置</div>
          <div class="form-grid three">
            <a-form-item label="处理动作" required>
              <a-select v-model:value="form.action" :options="actionOptions" @change="handleActionChange" />
            </a-form-item>
            <a-form-item v-if="form.action === 'rate_limited'" label="恢复策略" required>
              <a-select v-model:value="form.resetStrategy" :options="recoveryOptions" />
            </a-form-item>
            <a-form-item v-if="form.action === 'rate_limited' && form.resetStrategy === 'duration'" label="恢复小时数" required>
              <a-input-number v-model:value="form.durationHours" :min="1" :max="720" style="width: 100%" />
            </a-form-item>
            <a-form-item v-if="form.action === 'rate_limited' && form.resetStrategy === 'daily'" label="每天恢复时间" required>
              <a-select v-model:value="form.dailyResetHour" :options="hourOptions" />
            </a-form-item>
            <a-form-item v-if="form.action === 'rate_limited' && form.resetStrategy === 'weekly'" label="每周恢复日" required>
              <a-select v-model:value="form.weeklyResetDay" :options="weekdayOptions" />
            </a-form-item>
            <a-form-item v-if="form.action === 'rate_limited' && form.resetStrategy === 'weekly'" label="每周恢复时间" required>
              <a-select v-model:value="form.weeklyResetHour" :options="hourOptions" />
            </a-form-item>
          </div>
          <a-form-item label="备注">
            <a-textarea v-model:value="form.notes" :rows="2" placeholder="可写策略来源、适用范围或排障依据" />
          </a-form-item>
        </section>
      </a-form>
    </a-modal>

    <a-modal v-model:open="guideOpen" title="请求错误策略配置指南" width="820px" :footer="null">
      <div class="policy-guide">
        <p class="guide-note">
          请求错误策略只处理上游 HTTP 非 2xx 响应；请求头、请求体和上下文兼容仍由系统内部策略处理，不在页面开放编辑。
        </p>
        <a-table :columns="guideColumns" :data-source="guideRows" :pagination="false" row-key="key" size="small" />
      </div>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { QuestionCircleOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'
import { computed, onMounted, reactive, ref } from 'vue'

import { api, type ErrorPolicyPayload } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { extractApiErrorMessage } from '@/shared/apiError'
import type {
  ErrorPolicyAction,
  ErrorPolicyModelMatchType,
  ErrorPolicyRecoveryStrategy,
  ErrorPolicyScopeType,
  ErrorPolicySummary,
  ProviderDefinition
} from '@/types/domain'

interface ErrorPolicyForm {
  name: string
  enabled: boolean
  priority: number
  scopeType: ErrorPolicyScopeType
  providerCode: string | undefined
  clientProfile: string
  modelPattern: string
  modelMatchType: ErrorPolicyModelMatchType
  statusCodes: string
  errorCodes: string
  errorTypes: string
  keywords: string
  action: ErrorPolicyAction
  resetStrategy: ErrorPolicyRecoveryStrategy
  durationHours: number | null
  dailyResetHour: number
  weeklyResetDay: number
  weeklyResetHour: number
  notes: string
}

const listSeparators = /[,;，；\n]/

const loading = ref(false)
const saving = ref(false)
const keyword = ref('')
const providers = ref<ProviderDefinition[]>([])
const policies = ref<ErrorPolicySummary[]>([])
const modalOpen = ref(false)
const guideOpen = ref(false)
const editingId = ref<string>()
const form = reactive<ErrorPolicyForm>(defaultForm())

const columns = [
  { title: '策略名称', key: 'name', width: 220, fixed: 'left' },
  { title: '层级', key: 'scope', width: 110 },
  { title: '目标', key: 'target', width: 230 },
  { title: '优先级', key: 'priority', width: 90 },
  { title: '状态', key: 'status', width: 90 },
  { title: '状态码', key: 'statusCodes', width: 150 },
  { title: '错误码', key: 'errorCodes', width: 190 },
  { title: '错误类型', key: 'errorTypes', width: 190 },
  { title: '关键词', key: 'keywords', width: 220 },
  { title: '处置', key: 'action', width: 150 },
  { title: '恢复', key: 'recovery', width: 170 },
  { title: '备注', key: 'notes', width: 220 },
  { title: '更新时间', key: 'updatedAt', width: 180 },
  { title: '操作', key: 'actions', width: 104, fixed: 'right', actionCount: 2 }
]

const guideColumns = [
  { title: '范围', key: 'scope', dataIndex: 'scope', width: 120 },
  { title: '用途', key: 'usage', dataIndex: 'usage' },
  { title: '继承关系', key: 'inheritance', dataIndex: 'inheritance' }
]

const guideRows = [
  { key: 'global', scope: '全局层', usage: '所有协议和供应商的通用兜底。', inheritance: '最低优先级，上层通用规则。' },
  { key: 'protocol', scope: '协议层', usage: 'OpenAI v1 这类协议语义的通用规则。', inheritance: '继承全局层。' },
  { key: 'provider', scope: '供应商层', usage: 'GPT 等 OpenAI v1 供应商局部错误。', inheritance: '继承协议层和全局层。' },
  { key: 'client', scope: '客户端层', usage: 'Codex 等客户端专属错误语义。', inheritance: '继承协议层和全局层。' },
  { key: 'model', scope: '模型层', usage: '某个模型或模型族的错误。', inheritance: '最具体，优先于其他层命中。' }
]

const scopeOptions = [
  { label: '全局层', value: 'global' },
  { label: '协议层', value: 'protocol' },
  { label: '供应商层', value: 'provider' },
  { label: '客户端层', value: 'client' },
  { label: '模型层', value: 'model' }
]

const clientProfileOptions = [
  { label: 'Codex', value: 'codex' },
  { label: '通用 OpenAI', value: 'generic_openai' }
]

const modelMatchTypeOptions = [
  { label: '前缀匹配', value: 'prefix' },
  { label: '精确匹配', value: 'exact' },
  { label: '包含匹配', value: 'contains' }
]

const actionOptions = [
  { label: '只切号', value: 'retry_next' },
  { label: '临时不可调用', value: 'temp_unschedulable' },
  { label: '限流', value: 'rate_limited' },
  { label: '异常', value: 'error_disabled' }
]

const recoveryOptions = [
  { label: '固定时长', value: 'duration' },
  { label: '每天固定时间', value: 'daily' },
  { label: '每周固定时间', value: 'weekly' }
]

const hourOptions = Array.from({ length: 24 }, (_, index) => ({ label: `${String(index).padStart(2, '0')}:00`, value: index }))
const weekdayOptions = [
  { label: '周一', value: 1 },
  { label: '周二', value: 2 },
  { label: '周三', value: 3 },
  { label: '周四', value: 4 },
  { label: '周五', value: 5 },
  { label: '周六', value: 6 },
  { label: '周日', value: 0 }
]

const modalTitle = computed(() => editingId.value ? '编辑请求错误策略' : '新建请求错误策略')
const openAIProviders = computed(() => providers.value
  .filter((provider) => provider.enabled && provider.protocolProfiles.some((profile) => profile.enabled && profile.protocolCode === 'openai' && profile.protocolVersion === 'v1'))
  .sort((left, right) => left.name.localeCompare(right.name, 'zh-Hans-CN') || left.code.localeCompare(right.code)))
const providerSelectOptions = computed(() => openAIProviders.value.map((provider) => ({
  label: `${provider.name}（${provider.code}）`,
  value: provider.code
})))
const providerNameByCode = computed(() => new Map(openAIProviders.value.map((provider) => [provider.code, provider.name])))
const filteredPolicies = computed(() => {
  const text = keyword.value.trim().toLowerCase()
  if (!text) return policies.value
  return policies.value.filter((policy) => searchableText(policy).includes(text))
})

function defaultForm(): ErrorPolicyForm {
  return {
    name: '',
    enabled: true,
    priority: 1,
    scopeType: 'provider',
    providerCode: undefined,
    clientProfile: 'codex',
    modelPattern: '',
    modelMatchType: 'prefix',
    statusCodes: '',
    errorCodes: '',
    errorTypes: '',
    keywords: '',
    action: 'temp_unschedulable',
    resetStrategy: 'daily',
    durationHours: 1,
    dailyResetHour: 0,
    weeklyResetDay: 1,
    weeklyResetHour: 0,
    notes: ''
  }
}

async function loadPolicies(): Promise<void> {
  loading.value = true
  try {
    const result = await api.errorPolicies.list()
    policies.value = result.policies
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载请求错误策略失败'))
  } finally {
    loading.value = false
  }
}

async function loadPageData(): Promise<void> {
  loading.value = true
  try {
    const [policyResult, providerResult] = await Promise.all([
      api.errorPolicies.list(),
      api.providers.options()
    ])
    policies.value = policyResult.policies
    providers.value = providerResult
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载请求错误策略失败'))
  } finally {
    loading.value = false
  }
}

function openCreate(): void {
  editingId.value = undefined
  Object.assign(form, defaultForm(), {
    priority: nextPriority(),
    providerCode: defaultProviderCode()
  })
  modalOpen.value = true
}

function openEdit(policy: ErrorPolicySummary): void {
  fillForm(policy)
  editingId.value = policy.id
  modalOpen.value = true
}

function resetModal(): void {
  editingId.value = undefined
}

async function savePolicy(): Promise<void> {
  const validationMessage = validateForm()
  if (validationMessage) {
    message.warning(validationMessage)
    return
  }
  saving.value = true
  try {
    const payload = buildPayload()
    if (editingId.value) {
      await api.errorPolicies.update(editingId.value, payload)
      message.success('请求错误策略已更新')
    } else {
      await api.errorPolicies.create(payload)
      message.success('请求错误策略已创建')
    }
    modalOpen.value = false
    await loadPolicies()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存请求错误策略失败'))
  } finally {
    saving.value = false
  }
}

async function removePolicy(policy: ErrorPolicySummary): Promise<void> {
  try {
    await api.errorPolicies.delete(policy.id)
    policies.value = policies.value.filter((item) => item.id !== policy.id)
    message.success('请求错误策略已删除')
    void loadPolicies()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '删除请求错误策略失败'))
  }
}

function handlePolicyAction(key: string, policy: ErrorPolicySummary): void {
  if (key === 'edit') {
    openEdit(policy)
    return
  }
  if (key === 'delete') {
    void removePolicy(policy)
  }
}

function actionsFor(_policy: ErrorPolicySummary): RowActionItem[] {
  return [
    { key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' },
    { key: 'delete', label: '删除', icon: 'delete', tone: 'danger', confirmTitle: '确认删除这个请求错误策略？', confirmOkText: '删除' }
  ]
}

function fillForm(policy: ErrorPolicySummary): void {
  Object.assign(form, {
    name: policy.name,
    enabled: policy.enabled,
    priority: policy.priority,
    scopeType: policy.scopeType,
    providerCode: policy.providerCode,
    clientProfile: policy.clientProfile ?? 'codex',
    modelPattern: policy.modelPattern ?? '',
    modelMatchType: policy.modelMatchType ?? 'prefix',
    statusCodes: formatNumberList(policy.match.statusCodes),
    errorCodes: formatList(policy.match.errorCodes),
    errorTypes: formatList(policy.match.errorTypes),
    keywords: formatList(policy.match.keywords),
    action: policy.action,
    resetStrategy: policy.resetStrategy ?? 'daily',
    durationHours: policy.durationHours ?? 1,
    dailyResetHour: policy.dailyResetHour ?? 0,
    weeklyResetDay: policy.weeklyResetDay ?? 1,
    weeklyResetHour: policy.weeklyResetHour ?? 0,
    notes: policy.notes ?? ''
  })
}

function buildPayload(): ErrorPolicyPayload {
  const payload: ErrorPolicyPayload = {
    name: form.name.trim(),
    enabled: form.enabled,
    priority: requiredPositiveInt(form.priority, '优先级', 9999),
    scopeType: form.scopeType,
    match: compactObject({
      statusCodes: splitStatusCodes(form.statusCodes),
      errorCodes: splitList(form.errorCodes),
      errorTypes: splitList(form.errorTypes),
      keywords: splitList(form.keywords)
    }),
    action: form.action,
    notes: form.notes.trim() || undefined
  }
  if (form.scopeType === 'provider') payload.providerCode = form.providerCode
  if (form.scopeType === 'client') payload.clientProfile = form.clientProfile
  if (form.scopeType === 'model') {
    payload.providerCode = form.providerCode || undefined
    payload.modelPattern = form.modelPattern.trim()
    payload.modelMatchType = form.modelMatchType
  }
  if (form.action === 'rate_limited') {
    payload.resetStrategy = form.resetStrategy
    if (form.resetStrategy === 'duration') payload.durationHours = requiredPositiveInt(form.durationHours, '恢复小时数', 720)
    if (form.resetStrategy === 'daily') payload.dailyResetHour = requiredHour(form.dailyResetHour, '每天恢复时间')
    if (form.resetStrategy === 'weekly') {
      payload.weeklyResetDay = requiredWeekday(form.weeklyResetDay, '每周恢复日')
      payload.weeklyResetHour = requiredHour(form.weeklyResetHour, '每周恢复时间')
    }
  }
  return payload
}

function validateForm(): string | undefined {
  if (!form.name.trim()) return '请填写策略名称'
  if (!positiveInt(form.priority, 9999)) return '优先级必须是 1-9999 的整数'
  if (form.scopeType === 'provider' && !form.providerCode) return '请选择供应商'
  if (form.scopeType === 'client' && !form.clientProfile) return '请选择客户端'
  if (form.scopeType === 'model' && !form.modelPattern.trim()) return '请填写模型匹配值'
  const matchValidation = validateMatchers()
  if (matchValidation) return matchValidation
  if (!hasAnyMatcher()) return '至少需要填写一个匹配条件'
  if (form.action === 'rate_limited') {
    if (!form.resetStrategy) return '请选择恢复策略'
    if (form.resetStrategy === 'duration' && !positiveInt(form.durationHours, 720)) return '恢复小时数必须是 1-720 的整数'
    if (form.resetStrategy === 'daily' && requiredHour(form.dailyResetHour, '每天恢复时间') === undefined) return '每天恢复时间无效'
    if (form.resetStrategy === 'weekly' && requiredWeekday(form.weeklyResetDay, '每周恢复日') === undefined) return '每周恢复日无效'
    if (form.resetStrategy === 'weekly' && requiredHour(form.weeklyResetHour, '每周恢复时间') === undefined) return '每周恢复时间无效'
  }
  return undefined
}

function handleScopeChange(): void {
  if (form.scopeType === 'provider' && !form.providerCode) form.providerCode = defaultProviderCode()
  if (form.scopeType !== 'provider' && form.scopeType !== 'model') form.providerCode = undefined
  if (form.scopeType === 'client' && !form.clientProfile) form.clientProfile = 'codex'
  if (form.scopeType === 'model' && !form.modelMatchType) form.modelMatchType = 'prefix'
}

function handleActionChange(): void {
  if (form.action === 'rate_limited' && !form.resetStrategy) form.resetStrategy = 'daily'
}

function applySearch(): void {
  keyword.value = keyword.value.trim()
}

function resetSearch(): void {
  keyword.value = ''
}

function nextPriority(): number {
  const used = new Set(policies.value
    .map((policy) => policy.priority)
    .filter((priority): priority is number => Number.isInteger(priority) && priority > 0 && priority <= 9999))
  for (let priority = 1; priority <= 9999; priority += 1) {
    if (!used.has(priority)) return priority
  }
  return 9999
}

function hasAnyMatcher(): boolean {
  return splitStatusCodes(form.statusCodes).length > 0
    || (splitList(form.errorCodes) ?? []).length > 0
    || (splitList(form.errorTypes) ?? []).length > 0
    || (splitList(form.keywords) ?? []).length > 0
}

function splitList(value: unknown): string[] | undefined {
  if (Array.isArray(value)) {
    const items = value.map((item) => String(item).trim()).filter(Boolean)
    return items.length ? items : undefined
  }
  if (typeof value !== 'string') return undefined
  const items = value.split(listSeparators).map((item) => item.trim()).filter(Boolean)
  return items.length ? items : undefined
}

function splitStatusCodes(value: unknown): number[] {
  const items = splitList(value) ?? []
  const output: number[] = []
  const seen = new Set<number>()
  for (const item of items) {
    if (!/^\d+$/.test(item)) continue
    const code = Number(item)
    if (!Number.isInteger(code) || code < 100 || code > 599 || isSuccessStatusCode(code) || seen.has(code)) continue
    seen.add(code)
    output.push(code)
  }
  return output
}

function validateMatchers(): string | undefined {
  const statusItems = splitList(form.statusCodes) ?? []
  if (statusItems.some((item) => !/^\d+$/.test(item) || Number(item) < 100 || Number(item) > 599)) return '状态码必须是 100-599 的整数'
  if (statusItems.some((item) => isSuccessStatusCode(Number(item)))) return '状态码不能填写 2xx 成功状态码'
  const fields: Array<[string[] | undefined, string]> = [
    [splitList(form.errorCodes), '错误码'],
    [splitList(form.errorTypes), '错误类型'],
    [splitList(form.keywords), '关键词']
  ]
  for (const [items, label] of fields) {
    if ((items ?? []).length > 50) return `${label}不能超过 50 项`
    if ((items ?? []).some((item) => item.length > 200)) return `${label}单项不能超过 200 个字符`
    if ((items ?? []).some((item) => /^\d+$/.test(item) && isSuccessStatusCode(Number(item)))) return `${label}不能填写 2xx 成功码，例如 200`
  }
  return undefined
}

function formatList(values?: string[]): string {
  return values?.length ? values.join(', ') : ''
}

function formatNumberList(values?: number[]): string {
  return values?.length ? values.join(', ') : ''
}

function compactObject<T extends Record<string, string[] | number[] | undefined>>(value: T): T {
  const output = {} as T
  for (const [key, items] of Object.entries(value)) {
    if (items?.length) {
      output[key as keyof T] = items as T[keyof T]
    }
  }
  return output
}

function positiveInt(value: unknown, max = Number.POSITIVE_INFINITY): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && Number.isInteger(value) && value > 0 && value <= max ? value : undefined
}

function requiredPositiveInt(value: unknown, label: string, max = Number.POSITIVE_INFINITY): number {
  const numberValue = positiveInt(value, max)
  if (!numberValue) throw new Error(`${label}无效`)
  return numberValue
}

function requiredHour(value: unknown, label: string): number {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 0 || value > 23) throw new Error(`${label}无效`)
  return value
}

function requiredWeekday(value: unknown, label: string): number {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 0 || value > 6) throw new Error(`${label}无效`)
  return value
}

function isSuccessStatusCode(code: number): boolean {
  return code >= 200 && code <= 299
}

function searchableText(policy: ErrorPolicySummary): string {
  return [
    policy.name,
    scopeText(policy),
    targetText(policy),
    numberListText(policy.match.statusCodes),
    listText(policy.match.errorCodes),
    listText(policy.match.errorTypes),
    listText(policy.match.keywords),
    actionText(policy.action),
    recoveryText(policy),
    policy.notes
  ].filter(Boolean).join(' ').toLowerCase()
}

function scopeText(policy: Pick<ErrorPolicySummary, 'scopeType'>): string {
  if (policy.scopeType === 'global') return '全局层'
  if (policy.scopeType === 'protocol') return '协议层'
  if (policy.scopeType === 'provider') return '供应商层'
  if (policy.scopeType === 'client') return '客户端层'
  return '模型层'
}

function scopeColor(scope: ErrorPolicyScopeType): string {
  if (scope === 'global') return 'default'
  if (scope === 'protocol') return 'green'
  if (scope === 'provider') return 'geekblue'
  if (scope === 'client') return 'purple'
  return 'orange'
}

function targetText(policy: ErrorPolicySummary): string {
  if (policy.scopeType === 'global') return '全部协议'
  if (policy.scopeType === 'protocol') return protocolText(policy.protocolCode)
  if (policy.scopeType === 'provider') return providerText(policy.providerCode)
  if (policy.scopeType === 'client') return clientProfileText(policy.clientProfile)
  const provider = policy.providerCode ? `${providerText(policy.providerCode)} / ` : ''
  return `${provider}${modelMatchText(policy.modelMatchType)} ${policy.modelPattern || '-'}`
}

function protocolText(protocolCode?: string): string {
  if (protocolCode === 'openai') return 'OpenAI v1'
  return protocolCode || 'OpenAI v1'
}

function providerText(providerCode?: string): string {
  if (!providerCode) return '-'
  const name = providerNameByCode.value.get(providerCode)
  return name ? `${name}（${providerCode}）` : providerCode
}

function clientProfileText(profile?: string): string {
  if (profile === 'codex') return 'Codex'
  if (profile === 'generic_openai') return '通用 OpenAI'
  return profile || '-'
}

function modelMatchText(type?: ErrorPolicyModelMatchType): string {
  if (type === 'exact') return '精确'
  if (type === 'contains') return '包含'
  return '前缀'
}

function actionText(action: ErrorPolicyAction): string {
  return actionOptions.find((item) => item.value === action)?.label ?? action
}

function actionColor(action: ErrorPolicyAction): string {
  if (action === 'retry_next') return 'blue'
  if (action === 'rate_limited') return 'orange'
  if (action === 'error_disabled') return 'red'
  return 'purple'
}

function recoveryText(policy: ErrorPolicySummary): string {
  if (policy.action !== 'rate_limited') return '-'
  if (policy.resetStrategy === 'duration') return `${policy.durationHours ?? '-'} 小时`
  if (policy.resetStrategy === 'weekly') return `${weekdayText(policy.weeklyResetDay)} ${hourText(policy.weeklyResetHour)}`
  return `每天 ${hourText(policy.dailyResetHour)}`
}

function weekdayText(day?: number): string {
  return weekdayOptions.find((item) => item.value === day)?.label ?? '-'
}

function hourText(hour?: number): string {
  return typeof hour === 'number' ? `${String(hour).padStart(2, '0')}:00` : '-'
}

function listText(values?: string[]): string {
  return values?.length ? values.join(', ') : '-'
}

function numberListText(values?: number[]): string {
  return values?.length ? values.join(', ') : '-'
}

function defaultProviderCode(): string | undefined {
  return providerSelectOptions.value[0]?.value
}

onMounted(loadPageData)
</script>

<style scoped>
.request-error-policy-page {
  min-height: 0;
}

.request-error-policy-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.policy-name-text {
  display: block;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.field-cell {
  max-width: 240px;
  color: #334155;
  line-height: 20px;
  white-space: normal;
  word-break: break-word;
}

.text-field-cell {
  max-width: 280px;
}

.policy-form {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.form-section {
  padding: 14px;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  background: #fff;
}

.form-section-title {
  margin-bottom: 12px;
  color: #111827;
  font-size: 15px;
  font-weight: 700;
}

.form-grid {
  display: grid;
  gap: 0 14px;
}

.form-grid.two {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

.form-grid.three {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}

.policy-guide {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.guide-note {
  margin: 0;
  color: #64748b;
  font-size: 12px;
  line-height: 20px;
}

@media (max-width: 820px) {
  .form-grid.two,
  .form-grid.three {
    grid-template-columns: 1fr;
  }
}
</style>

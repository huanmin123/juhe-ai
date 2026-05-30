<template>
  <a-card class="page-card responsive-page-card stream-policy-page">
    <ResponsiveListToolbar
      v-model:keyword="keyword"
      search-placeholder="搜索策略名称或匹配条件"
      :show-reset="Boolean(keyword.trim())"
      :refresh-loading="loading"
      @search="applySearch"
      @reset="resetSearch"
      @refresh="loadPolicies"
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
      table-class="page-table stream-policy-table"
      :columns="columns"
      :data-source="filteredPolicies"
      row-key="id"
      :loading="loading"
      :pagination="{ pageSize: 20, hideOnSinglePage: true, showSizeChanger: false }"
      :scroll-x="1120"
      pull-refresh-enabled
      :refreshing="loading"
      @mobile-refresh="loadPolicies"
    >
      <template #emptyText>
        <a-empty class="page-empty-card" description="暂无流式拦截策略" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'name'">
          <div class="policy-name-cell">
            <strong>{{ record.name }}</strong>
            <a-space :size="6" wrap>
              <a-tag :color="record.builtIn ? 'blue' : 'purple'">{{ record.builtIn ? '内置' : '自定义' }}</a-tag>
              <a-tag>P{{ record.priority }}</a-tag>
            </a-space>
          </div>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-space :size="6" wrap>
            <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
            <a-tag :color="record.executionMode === 'dry_run' ? 'gold' : 'purple'">{{ executionModeText(record.executionMode) }}</a-tag>
          </a-space>
        </template>
        <template v-else-if="column.key === 'match'">
          <div class="compact-cell">{{ matchSummary(record) }}</div>
        </template>
        <template v-else-if="column.key === 'handling'">
          <a-space :size="6" wrap>
            <a-tag color="cyan">{{ dataHandlingText(record.dataHandling) }}</a-tag>
            <a-tag :color="record.retryEnabled ? 'green' : 'default'">{{ record.retryEnabled ? '重试' : '不重试' }}</a-tag>
          </a-space>
        </template>
        <template v-else-if="column.key === 'account'">
          <div class="compact-cell">{{ accountActionSummary(record) }}</div>
        </template>
        <template v-else-if="column.key === 'updatedAt'">
          <span>{{ record.updatedAt || '-' }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <RowActions :actions="actionsFor(record)" @action-click="handlePolicyAction($event, record)" />
        </template>
      </template>
      <template #card="{ record }">
        <article class="mobile-list-card stream-policy-mobile-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">{{ record.name }}</div>
            <div class="mobile-list-card-tags">
              <a-tag :color="record.builtIn ? 'blue' : 'purple'">{{ record.builtIn ? '内置' : '自定义' }}</a-tag>
              <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
            </div>
          </div>
          <div class="mobile-list-meta-grid">
            <div class="mobile-list-meta-item">
              <span>优先级</span>
              <strong>P{{ record.priority }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>执行</span>
              <strong>{{ executionModeText(record.executionMode) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>匹配</span>
              <strong>{{ matchSummary(record) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>数据</span>
              <strong>{{ dataHandlingText(record.dataHandling) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>重试</span>
              <strong>{{ record.retryEnabled ? '是' : '否' }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>账户</span>
              <strong>{{ accountActionSummary(record) }}</strong>
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
      width="980px"
      :confirm-loading="saving"
      :ok-button-props="{ type: 'primary', disabled: saving || modalReadonly }"
      :footer="modalReadonly ? null : undefined"
      @ok="savePolicy"
      @cancel="resetModal"
    >
      <a-form layout="vertical" class="policy-form">
        <section class="form-section">
          <div class="form-section-title">基础</div>
          <div class="form-grid three">
            <a-form-item label="策略名称" required>
              <a-input v-model:value="form.name" :disabled="modalReadonly" placeholder="例如 中转广告污染拦截" />
            </a-form-item>
            <a-form-item label="优先级">
              <a-input-number v-model:value="form.priority" :disabled="modalReadonly" :min="1" :max="9999" style="width: 100%" />
            </a-form-item>
            <a-form-item label="执行模式">
              <a-select v-model:value="form.executionMode" :disabled="modalReadonly" :options="executionModeOptions" />
            </a-form-item>
          </div>
          <a-form-item label="启用状态">
            <a-switch v-model:checked="form.enabled" :disabled="modalReadonly" checked-children="启用" un-checked-children="停用" />
          </a-form-item>
        </section>

        <section class="form-section">
          <div class="form-section-title">匹配条件</div>
          <div class="form-grid two">
            <a-form-item label="SSE event 类型">
              <a-input v-model:value="form.eventTypes" :disabled="modalReadonly" placeholder="response.failed, error" />
            </a-form-item>
            <a-form-item label="data.type">
              <a-input v-model:value="form.dataTypes" :disabled="modalReadonly" placeholder="response.output_text.delta" />
            </a-form-item>
            <a-form-item label="error.code">
              <a-input v-model:value="form.errorCodes" :disabled="modalReadonly" placeholder="cyber_policy" />
            </a-form-item>
            <a-form-item label="error.type">
              <a-input v-model:value="form.errorTypes" :disabled="modalReadonly" placeholder="server_error" />
            </a-form-item>
            <a-form-item label="文本包含">
              <a-textarea v-model:value="form.textIncludes" :disabled="modalReadonly" :rows="1" auto-size placeholder="多个关键词用逗号、分号或换行分隔" />
            </a-form-item>
            <a-form-item label="文本不包含">
              <a-textarea v-model:value="form.textExcludes" :disabled="modalReadonly" :rows="1" auto-size placeholder="减少误杀时填写" />
            </a-form-item>
            <a-form-item label="JSON 字段存在">
              <a-input v-model:value="form.jsonPathsExists" :disabled="modalReadonly" placeholder="response.error, error" />
            </a-form-item>
          </div>
        </section>

        <section class="form-section">
          <div class="form-section-title">处置</div>
          <div class="form-grid three">
            <a-form-item label="数据处理">
              <a-select v-model:value="form.dataHandling" :disabled="modalReadonly" :options="dataHandlingOptions" @change="normalizeActions" />
            </a-form-item>
            <a-form-item label="是否重试">
              <a-switch v-model:checked="form.retryEnabled" :disabled="modalReadonly" checked-children="是" un-checked-children="否" @change="normalizeActions" />
            </a-form-item>
            <a-form-item label="是否切号">
              <a-select v-model:value="form.accountSwitch" :disabled="modalReadonly" :options="accountSwitchOptions" @change="normalizeActions" />
            </a-form-item>
            <a-form-item label="账户状态">
              <a-select v-model:value="form.accountState" :disabled="modalReadonly" :options="accountStateOptions" />
            </a-form-item>
            <a-form-item label="避让秒数">
              <a-input-number v-model:value="form.avoidanceTtlSeconds" :disabled="modalReadonly" :min="1" :max="86400" style="width: 100%" />
            </a-form-item>
          </div>
          <a-form-item label="备注">
            <a-textarea v-model:value="form.notes" :disabled="modalReadonly" :rows="2" placeholder="可写污染来源或排障线索" />
          </a-form-item>
        </section>
      </a-form>
    </a-modal>

    <a-modal v-model:open="guideOpen" title="流式拦截策略配置指南" width="900px" :footer="null">
      <div class="policy-guide">
        <p class="guide-note guide-intro">
          策略作用于运行时识别为 AI 对话的 SSE 流；客户端类型、接口路径和具体协议不需要配置，运行时按下游是否已写出内容和客户端能力决定具体重试方式。
        </p>

        <section class="guide-section">
          <h4>去哪里查依据</h4>
          <a-table
            :columns="guideSourceColumns"
            :data-source="streamInterceptPolicyGuideSources"
            :pagination="false"
            row-key="key"
            size="small"
          />
        </section>

        <section class="guide-section">
          <h4>字段怎么填</h4>
          <a-table
            :columns="guideFieldColumns"
            :data-source="streamInterceptPolicyGuideFields"
            :pagination="false"
            row-key="key"
            size="small"
          />
          <p class="guide-note">多个值用逗号、分号或换行分隔；同一个字段里的多个值是“任一命中”，不同字段之间是“同时命中”。</p>
        </section>

        <section class="guide-section">
          <h4>处置怎么选</h4>
          <a-table
            :columns="guideActionColumns"
            :data-source="streamInterceptPolicyGuideActions"
            :pagination="false"
            row-key="key"
            size="small"
          />
        </section>

        <section class="guide-section">
          <h4>常见 SSE 事件结构</h4>
          <pre class="guide-code">{{ streamInterceptPolicyGuideExample }}</pre>
        </section>
      </div>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { QuestionCircleOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'
import { computed, onMounted, reactive, ref } from 'vue'

import { api, type StreamInterceptPolicyPayload } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { extractApiErrorMessage } from '@/shared/apiError'
import type {
  StreamInterceptPolicyAccountState,
  StreamInterceptPolicyAccountSwitch,
  StreamInterceptPolicyDataHandling,
  StreamInterceptPolicyExecutionMode,
  StreamInterceptPolicySummary
} from '@/types/domain'
import {
  streamInterceptPolicyGuideActions,
  streamInterceptPolicyGuideExample,
  streamInterceptPolicyGuideFields,
  streamInterceptPolicyGuideSources
} from './streamInterceptPolicyGuide'

interface StreamPolicyForm {
  name: string
  enabled: boolean
  executionMode: StreamInterceptPolicyExecutionMode
  priority: number
  eventTypes: string
  dataTypes: string
  errorCodes: string
  errorTypes: string
  textIncludes: string
  textExcludes: string
  jsonPathsExists: string
  dataHandling: StreamInterceptPolicyDataHandling
  retryEnabled: boolean
  accountSwitch: StreamInterceptPolicyAccountSwitch
  accountState: StreamInterceptPolicyAccountState
  avoidanceTtlSeconds: number | null
  notes: string
}

const listSeparators = /[,;，；\n]/

const loading = ref(false)
const saving = ref(false)
const keyword = ref('')
const presets = ref<StreamInterceptPolicySummary[]>([])
const policies = ref<StreamInterceptPolicySummary[]>([])
const modalOpen = ref(false)
const modalReadonly = ref(false)
const guideOpen = ref(false)
const editingId = ref<string>()
const form = reactive<StreamPolicyForm>(defaultForm())

const columns = [
  { title: '策略', key: 'name', width: 260, fixed: 'left' },
  { title: '状态', key: 'status', width: 140 },
  { title: '匹配', key: 'match', width: 320 },
  { title: '数据 / 重试', key: 'handling', width: 180 },
  { title: '账户处置', key: 'account', width: 220 },
  { title: '更新时间', key: 'updatedAt', width: 180 },
  { title: '操作', key: 'actions', width: 104, fixed: 'right', actionCount: 2 }
]

const executionModeOptions = [
  { label: '拦截', value: 'intercept' },
  { label: '试运行', value: 'dry_run' }
]

const allDataHandlingOptions = [
  { label: '丢弃命中事件', value: 'discard_event' },
  { label: '丢弃当前流', value: 'discard_stream' },
  { label: '替换为失败事件', value: 'replace_with_failure' }
]

const allAccountSwitchOptions = [
  { label: '不切号', value: 'none' },
  { label: '本次请求切下一个账号', value: 'request_next_account' },
  { label: '切号并短期避让当前账号', value: 'avoid_account_ttl' },
  { label: '切号并短期避让上游桶', value: 'avoid_upstream_bucket_ttl' }
]

const accountStateOptions = [
  { label: '不修改', value: 'none' },
  { label: '仅运行态避让', value: 'runtime_avoidance' }
]

const guideSourceColumns = [
  { title: '来源', key: 'name', dataIndex: 'name', width: 120 },
  { title: '查看位置', key: 'where', dataIndex: 'where' },
  { title: '说明', key: 'note', dataIndex: 'note' }
]

const guideFieldColumns = [
  { title: '字段', key: 'field', dataIndex: 'field', width: 120 },
  { title: '取值来源', key: 'source', dataIndex: 'source' },
  { title: '例子', key: 'example', dataIndex: 'example', width: 180 },
  { title: '说明', key: 'note', dataIndex: 'note' }
]

const guideActionColumns = [
  { title: '处置', key: 'action', dataIndex: 'action', width: 150 },
  { title: '适用场景', key: 'when', dataIndex: 'when' },
  { title: '说明', key: 'note', dataIndex: 'note' }
]

const modeLabels: Record<StreamInterceptPolicyExecutionMode, string> = {
  intercept: '拦截',
  dry_run: '试运行'
}

const dataHandlingLabels: Record<StreamInterceptPolicyDataHandling, string> = {
  discard_event: '丢弃命中事件',
  discard_stream: '丢弃当前流',
  replace_with_failure: '替换为失败事件'
}

const accountSwitchLabels: Record<StreamInterceptPolicyAccountSwitch, string> = {
  none: '不切号',
  request_next_account: '本次请求切下一个账号',
  avoid_account_ttl: '切号并短期避让当前账号',
  avoid_upstream_bucket_ttl: '切号并短期避让上游桶'
}

const accountStateLabels: Record<StreamInterceptPolicyAccountState, string> = {
  none: '不修改',
  runtime_avoidance: '仅运行态避让'
}

const allPolicies = computed(() => [...presets.value, ...policies.value])
const filteredPolicies = computed(() => {
  const text = keyword.value.trim().toLowerCase()
  if (!text) return allPolicies.value
  return allPolicies.value.filter((policy) => searchableText(policy).includes(text))
})

const modalTitle = computed(() => {
  if (modalReadonly.value) return '查看内置策略'
  return editingId.value ? '编辑流式拦截策略' : '新建流式拦截策略'
})

const dataHandlingOptions = computed(() => form.retryEnabled
  ? allDataHandlingOptions.filter((option) => option.value !== 'discard_event')
  : allDataHandlingOptions)

const accountSwitchOptions = computed(() => form.retryEnabled
  ? allAccountSwitchOptions
  : allAccountSwitchOptions.filter((option) => option.value !== 'request_next_account'))

function defaultForm(): StreamPolicyForm {
  return {
    name: '',
    enabled: true,
    executionMode: 'intercept',
    priority: 100,
    eventTypes: '',
    dataTypes: '',
    errorCodes: '',
    errorTypes: '',
    textIncludes: '',
    textExcludes: '',
    jsonPathsExists: '',
    dataHandling: 'discard_stream',
    retryEnabled: true,
    accountSwitch: 'avoid_account_ttl',
    accountState: 'runtime_avoidance',
    avoidanceTtlSeconds: 300,
    notes: ''
  }
}

async function loadPolicies(): Promise<void> {
  loading.value = true
  try {
    const result = await api.streamInterceptPolicies.list()
    presets.value = result.presets
    policies.value = result.policies
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载流式拦截策略失败'))
  } finally {
    loading.value = false
  }
}

function openCreate(): void {
  editingId.value = undefined
  modalReadonly.value = false
  Object.assign(form, defaultForm(), {
    priority: nextPriority()
  })
  modalOpen.value = true
}

function openView(policy: StreamInterceptPolicySummary): void {
  fillForm(policy)
  editingId.value = undefined
  modalReadonly.value = true
  modalOpen.value = true
}

function openEdit(policy: StreamInterceptPolicySummary): void {
  if (!policy.editable) {
    openView(policy)
    return
  }
  fillForm(policy)
  editingId.value = policy.id
  modalReadonly.value = false
  modalOpen.value = true
}

function resetModal(): void {
  editingId.value = undefined
  modalReadonly.value = false
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
      await api.streamInterceptPolicies.update(editingId.value, payload)
      message.success('流式拦截策略已更新')
    } else {
      await api.streamInterceptPolicies.create(payload)
      message.success('流式拦截策略已创建')
    }
    modalOpen.value = false
    await loadPolicies()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存流式拦截策略失败'))
  } finally {
    saving.value = false
  }
}

async function removePolicy(policy: StreamInterceptPolicySummary): Promise<void> {
  if (!policy.editable) return
  try {
    await api.streamInterceptPolicies.delete(policy.id)
    policies.value = policies.value.filter((item) => item.id !== policy.id)
    message.success('流式拦截策略已删除')
    void loadPolicies()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '删除流式拦截策略失败'))
  }
}

function handlePolicyAction(key: string, policy: StreamInterceptPolicySummary): void {
  if (key === 'view') {
    openView(policy)
    return
  }
  if (key === 'edit') {
    openEdit(policy)
    return
  }
  if (key === 'delete') {
    void removePolicy(policy)
  }
}

function actionsFor(policy: StreamInterceptPolicySummary): RowActionItem[] {
  if (!policy.editable) {
    return [
      { key: 'view', label: '查看', icon: 'view', tone: 'info' }
    ]
  }
  return [
    { key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' },
    { key: 'delete', label: '删除', icon: 'delete', tone: 'danger', confirmTitle: '确认删除这个流式拦截策略？', confirmOkText: '删除' }
  ]
}

function fillForm(policy: StreamInterceptPolicySummary): void {
  Object.assign(form, {
    name: policy.name,
    enabled: policy.enabled,
    executionMode: policy.executionMode,
    priority: policy.priority,
    eventTypes: formatList(policy.match.eventTypes),
    dataTypes: formatList(policy.match.dataTypes),
    errorCodes: formatList(policy.match.errorCodes),
    errorTypes: formatList(policy.match.errorTypes),
    textIncludes: formatList(policy.match.textIncludes),
    textExcludes: formatList(policy.match.textExcludes),
    jsonPathsExists: formatList(policy.match.jsonPathsExists),
    dataHandling: policy.dataHandling,
    retryEnabled: policy.retryEnabled,
    accountSwitch: policy.accountSwitch,
    accountState: policy.accountState,
    avoidanceTtlSeconds: policy.avoidanceTtlSeconds ?? null,
    notes: policy.notes ?? ''
  })
}

function buildPayload(): StreamInterceptPolicyPayload {
  const payload: StreamInterceptPolicyPayload = {
    name: form.name.trim(),
    enabled: form.enabled,
    executionMode: form.executionMode,
    priority: form.priority,
    match: compactObject({
      eventTypes: splitList(form.eventTypes),
      dataTypes: splitList(form.dataTypes),
      errorCodes: splitList(form.errorCodes),
      errorTypes: splitList(form.errorTypes),
      textIncludes: splitList(form.textIncludes),
      textExcludes: splitList(form.textExcludes),
      jsonPathsExists: splitList(form.jsonPathsExists)
    }),
    dataHandling: form.dataHandling,
    retryEnabled: form.retryEnabled,
    accountSwitch: form.retryEnabled ? form.accountSwitch : nonRetryAccountSwitch(form.accountSwitch),
    accountState: form.accountState,
    notes: form.notes.trim() || undefined
  }
  const ttl = positiveInt(form.avoidanceTtlSeconds)
  if (ttl) payload.avoidanceTtlSeconds = ttl
  return payload
}

function validateForm(): string | undefined {
  if (!form.name.trim()) return '请填写策略名称'
  if (!hasAnyMatcher()) return '至少需要填写一个匹配条件'
  if (form.retryEnabled && form.dataHandling === 'discard_event') return '需要重试时不能只丢弃命中事件'
  if (needsAvoidanceTtl() && !positiveInt(form.avoidanceTtlSeconds)) return '切号避让或运行态避让需要填写避让秒数'
  return undefined
}

function normalizeActions(): void {
  if (form.retryEnabled && form.dataHandling === 'discard_event') {
    form.dataHandling = 'discard_stream'
  }
  if (!form.retryEnabled && form.accountSwitch === 'request_next_account') {
    form.accountSwitch = 'none'
  }
}

function applySearch(): void {
  keyword.value = keyword.value.trim()
}

function resetSearch(): void {
  keyword.value = ''
}

function nextPriority(): number {
  const max = Math.max(0, ...policies.value.map((policy) => policy.priority).filter(Number.isFinite))
  return Math.min(9999, max + 10 || 100)
}

function hasAnyMatcher(): boolean {
  return [
    form.eventTypes,
    form.dataTypes,
    form.errorCodes,
    form.errorTypes,
    form.textIncludes,
    form.jsonPathsExists
  ].some((value) => (splitList(value) ?? []).length > 0)
}

function needsAvoidanceTtl(): boolean {
  return form.accountSwitch === 'avoid_account_ttl'
    || form.accountSwitch === 'avoid_upstream_bucket_ttl'
    || form.accountState === 'runtime_avoidance'
}

function splitList(value: unknown): string[] | undefined {
  if (Array.isArray(value)) {
    const items = uniqueList(value.map((item) => String(item)))
    return items.length ? items : undefined
  }
  if (typeof value !== 'string') return undefined
  const items = uniqueList(value.split(listSeparators))
  return items.length ? items : undefined
}

function uniqueList(values: string[]): string[] {
  const seen = new Set<string>()
  const output: string[] = []
  for (const value of values) {
    const item = value.trim()
    if (!item || seen.has(item)) continue
    seen.add(item)
    output.push(item)
    if (output.length >= 50) break
  }
  return output
}

function formatList(values?: string[]): string {
  return values?.length ? values.join(', ') : ''
}

function compactObject<T extends Record<string, string[] | undefined>>(value: T): T {
  const output = {} as T
  for (const [key, items] of Object.entries(value)) {
    if (items?.length) {
      output[key as keyof T] = items as T[keyof T]
    }
  }
  return output
}

function positiveInt(value: unknown): number | undefined {
  const numberValue = Number(value)
  return Number.isFinite(numberValue) && numberValue > 0 ? Math.trunc(numberValue) : undefined
}

function nonRetryAccountSwitch(value: StreamInterceptPolicyAccountSwitch): StreamInterceptPolicyAccountSwitch {
  return value === 'request_next_account' ? 'none' : value
}

function searchableText(policy: StreamInterceptPolicySummary): string {
  return [
    policy.name,
    matchSummary(policy),
    accountActionSummary(policy),
    policy.notes
  ].filter(Boolean).join(' ').toLowerCase()
}

function executionModeText(value: StreamInterceptPolicyExecutionMode): string {
  return modeLabels[value] ?? value
}

function dataHandlingText(value: StreamInterceptPolicyDataHandling): string {
  return dataHandlingLabels[value] ?? value
}

function matchSummary(policy: StreamInterceptPolicySummary): string {
  const match = policy.match
  const parts = [
    scopedList('event', match.eventTypes),
    scopedList('data.type', match.dataTypes),
    scopedList('code', match.errorCodes),
    scopedList('type', match.errorTypes),
    scopedList('文本', match.textIncludes),
    scopedList('字段', match.jsonPathsExists)
  ].filter(Boolean)
  return parts.length ? parts.join('；') : '-'
}

function accountActionSummary(policy: StreamInterceptPolicySummary): string {
  const parts = [
    accountSwitchLabels[policy.accountSwitch] ?? policy.accountSwitch,
    accountStateLabels[policy.accountState] ?? policy.accountState,
    policy.avoidanceTtlSeconds ? `${policy.avoidanceTtlSeconds}s` : ''
  ].filter(Boolean)
  return parts.join(' / ')
}

function scopedList(label: string, values?: string[]): string {
  if (!values?.length) return ''
  return `${label}: ${values.slice(0, 3).join(', ')}${values.length > 3 ? ` 等 ${values.length} 项` : ''}`
}

onMounted(loadPolicies)
</script>

<style scoped>
.stream-policy-page {
  min-height: 0;
}

.stream-policy-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.policy-name-cell {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 6px;
}

.policy-name-cell strong {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.compact-cell {
  max-width: 360px;
  overflow: hidden;
  color: #334155;
  text-overflow: ellipsis;
  white-space: nowrap;
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

.stream-policy-mobile-card :deep(.mobile-list-meta-item strong) {
  font-weight: 400;
}

.policy-guide {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.policy-guide :deep(.ant-table-wrapper) {
  overflow-x: auto;
}

.guide-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.guide-section h4 {
  margin: 0;
  color: #0f172a;
  font-size: 14px;
}

.guide-note {
  color: #64748b;
  font-size: 12px;
  line-height: 20px;
}

.guide-intro {
  margin: 0;
}

.guide-code {
  overflow-x: auto;
  margin: 0;
  border: 1px solid #e8edf5;
  border-radius: 8px;
  background: #f8fafc;
  padding: 12px;
  color: #334155;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
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

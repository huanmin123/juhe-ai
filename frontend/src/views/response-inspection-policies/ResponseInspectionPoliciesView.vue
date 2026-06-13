<template>
  <a-card class="page-card responsive-page-card response-policy-page">
    <ResponsiveListToolbar
      v-model:keyword="keyword"
      search-placeholder="搜索策略名称或匹配条件"
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
      table-class="page-table response-policy-table"
      :columns="columns"
      :data-source="filteredPolicies"
      row-key="id"
      :loading="loading"
      :pagination="{ pageSize: 20, hideOnSinglePage: true, showSizeChanger: false }"
      :scroll-x="2980"
      pull-refresh-enabled
      :refreshing="loading"
      @mobile-refresh="loadPageData"
    >
      <template #emptyText>
        <a-empty class="page-empty-card" description="暂无响应检查策略" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'name'">
          <strong class="policy-name-text">{{ record.name }}</strong>
        </template>
        <template v-else-if="column.key === 'type'">
          <a-tag :color="record.defaultRule ? 'blue' : 'purple'">{{ record.defaultRule ? '默认' : '自定义' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'scope'">
          <a-tag :color="record.scopeType === 'provider' ? 'geekblue' : 'green'">{{ scopeText(record) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'protocol'">
          <span>{{ protocolText(record.protocolCode) }}</span>
        </template>
        <template v-else-if="column.key === 'provider'">
          <span>{{ providerText(record.providerCode) }}</span>
        </template>
        <template v-else-if="column.key === 'priority'">
          <span>{{ record.priority }}</span>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'outputTextIncludes'">
          <div class="field-cell">{{ listText(record.match.outputTextIncludes) }}</div>
        </template>
        <template v-else-if="column.key === 'finishReasons'">
          <div class="field-cell">{{ listText(record.match.finishReasons) }}</div>
        </template>
        <template v-else-if="column.key === 'errorCodes'">
          <div class="field-cell">{{ listText(record.match.errorCodes) }}</div>
        </template>
        <template v-else-if="column.key === 'errorTypes'">
          <div class="field-cell">{{ listText(record.match.errorTypes) }}</div>
        </template>
        <template v-else-if="column.key === 'errorMessageIncludes'">
          <div class="field-cell text-field-cell">{{ listText(record.match.errorMessageIncludes) }}</div>
        </template>
        <template v-else-if="column.key === 'outputTextExcludes'">
          <div class="field-cell text-field-cell">{{ listText(record.match.outputTextExcludes) }}</div>
        </template>
        <template v-else-if="column.key === 'rawTextIncludes'">
          <div class="field-cell text-field-cell">{{ listText(record.match.rawTextIncludes) }}</div>
        </template>
        <template v-else-if="column.key === 'jsonPathsExists'">
          <div class="field-cell">{{ listText(record.match.jsonPathsExists) }}</div>
        </template>
        <template v-else-if="column.key === 'action'">
          <a-tag color="cyan">{{ actionText(record.action) }}</a-tag>
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
        <article class="mobile-list-card response-policy-mobile-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">{{ record.name }}</div>
            <div class="mobile-list-card-tags">
              <a-tag :color="record.defaultRule ? 'blue' : 'purple'">{{ record.defaultRule ? '默认' : '自定义' }}</a-tag>
              <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
            </div>
          </div>
          <div class="mobile-list-meta-grid">
            <div class="mobile-list-meta-item">
              <span>类型</span>
              <strong>{{ record.defaultRule ? '默认' : '自定义' }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>层级</span>
              <strong>{{ scopeText(record) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>协议</span>
              <strong>{{ protocolText(record.protocolCode) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>供应商</span>
              <strong>{{ providerText(record.providerCode) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>优先级</span>
              <strong>{{ record.priority }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>状态</span>
              <strong>{{ record.enabled ? '启用' : '停用' }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>模板</span>
              <strong>{{ actionText(record.action) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>输出文本包含</span>
              <strong>{{ listText(record.match.outputTextIncludes) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>完成原因 / 状态</span>
              <strong>{{ listText(record.match.finishReasons) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>error.code</span>
              <strong>{{ listText(record.match.errorCodes) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>error.type</span>
              <strong>{{ listText(record.match.errorTypes) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>错误消息包含</span>
              <strong>{{ listText(record.match.errorMessageIncludes) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>输出文本排除</span>
              <strong>{{ listText(record.match.outputTextExcludes) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>SSE 事件原文包含</span>
              <strong>{{ listText(record.match.rawTextIncludes) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>JSON字段路径存在</span>
              <strong>{{ listText(record.match.jsonPathsExists) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>备注</span>
              <strong>{{ record.notes || '-' }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>更新时间</span>
              <strong>{{ record.updatedAt || '-' }}</strong>
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
            <a-form-item label="作用层级" required>
              <a-segmented v-model:value="form.scopeType" :disabled="modalReadonly" :options="scopeOptions" block @change="handleScopeChange" />
            </a-form-item>
            <a-form-item v-if="form.scopeType === 'provider'" label="供应商" required>
              <a-select
                v-model:value="form.providerCode"
                :disabled="modalReadonly"
                :options="providerSelectOptions"
                placeholder="选择 OpenAI v1 供应商"
                show-search
                option-filter-prop="label"
              />
            </a-form-item>
            <a-form-item label="优先级">
              <a-input-number v-model:value="form.priority" :disabled="modalReadonly" :min="1" :max="9999" style="width: 100%" />
            </a-form-item>
          </div>
          <a-form-item label="启用状态">
            <a-switch v-model:checked="form.enabled" :disabled="modalReadonly" checked-children="启用" un-checked-children="停用" />
          </a-form-item>
        </section>

        <section class="form-section">
          <div class="form-section-title">匹配条件</div>
          <ResponseInspectionMatchFields :form="form" :disabled="modalReadonly" />
        </section>

        <section class="form-section">
          <div class="form-section-title">处置</div>
          <a-form-item label="处置模板">
            <ResponseInspectionActionSelector v-model="form.action" :disabled="modalReadonly" />
          </a-form-item>
          <a-form-item label="备注">
            <a-textarea v-model:value="form.notes" :disabled="modalReadonly" :rows="2" placeholder="可写污染来源或排障线索" />
          </a-form-item>
        </section>
      </a-form>
    </a-modal>

    <ResponseInspectionPolicyGuideModal
      v-model:open="guideOpen"
      title="响应检查策略配置指南"
      intro="这类策略检查上游返回的 JSON 或 SSE 流。协议层规则会作用到所有 OpenAI v1 账号；供应商层规则只作用到选中的供应商，例如 GPT。"
    />
  </a-card>
</template>

<script setup lang="ts">
import { QuestionCircleOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'
import { computed, onMounted, reactive, ref } from 'vue'

import { api, type ResponseInspectionPolicyPayload } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { extractApiErrorMessage } from '@/shared/apiError'
import { providerDisplayName } from '@/shared/providerDisplay'
import { isOpenAIProtocolProfile, preferredDefaultProviderCode } from '@/shared/providerProtocol'
import type {
  ProviderDefinition,
  ResponseInspectionPolicyAction,
  ResponseInspectionPolicyScopeType,
  ResponseInspectionPolicySummary
} from '@/types/domain'
import ResponseInspectionActionSelector from './ResponseInspectionActionSelector.vue'
import ResponseInspectionMatchFields from './ResponseInspectionMatchFields.vue'
import ResponseInspectionPolicyGuideModal from './ResponseInspectionPolicyGuideModal.vue'
import {
  responseInspectionActionLabel
} from './responseInspectionActionTemplates'
import {
  buildResponseInspectionMatchPayload,
  formatResponseInspectionList,
  hasPositiveResponseInspectionMatcher,
  responseInspectionListText,
  responseInspectionScopedListSummary,
  type ResponseInspectionMatchFormFields,
  validateResponseInspectionMatchFields
} from './responseInspectionPolicyForm'

interface ResponsePolicyForm extends ResponseInspectionMatchFormFields {
  name: string
  enabled: boolean
  priority: number
  scopeType: ResponseInspectionPolicyScopeType
  providerCode: string
  outputTextIncludes: string
  finishReasons: string
  errorCodes: string
  errorTypes: string
  errorMessageIncludes: string
  rawTextIncludes: string
  outputTextExcludes: string
  jsonPathsExists: string
  action: ResponseInspectionPolicyAction
  notes: string
}

const loading = ref(false)
const saving = ref(false)
const keyword = ref('')
const providers = ref<ProviderDefinition[]>([])
const defaultRules = ref<ResponseInspectionPolicySummary[]>([])
const policies = ref<ResponseInspectionPolicySummary[]>([])
const modalOpen = ref(false)
const modalReadonly = ref(false)
const guideOpen = ref(false)
const editingId = ref<string>()
const form = reactive<ResponsePolicyForm>(defaultForm())

const columns = [
  { title: '策略名称', key: 'name', width: 240, fixed: 'left' },
  { title: '类型', key: 'type', width: 90 },
  { title: '层级', key: 'scope', width: 110 },
  { title: '协议', key: 'protocol', width: 120 },
  { title: '供应商', key: 'provider', width: 150 },
  { title: '优先级', key: 'priority', width: 90 },
  { title: '状态', key: 'status', width: 90 },
  { title: '输出文本包含', key: 'outputTextIncludes', width: 220 },
  { title: '输出文本排除', key: 'outputTextExcludes', width: 220 },
  { title: 'error.code', key: 'errorCodes', width: 160 },
  { title: 'error.type', key: 'errorTypes', width: 160 },
  { title: '错误消息包含', key: 'errorMessageIncludes', width: 220 },
  { title: '完成原因 / 状态', key: 'finishReasons', width: 190 },
  { title: 'JSON字段路径存在', key: 'jsonPathsExists', width: 190 },
  { title: 'SSE 事件原文包含', key: 'rawTextIncludes', width: 240 },
  { title: '处置模板', key: 'action', width: 220 },
  { title: '备注', key: 'notes', width: 220 },
  { title: '更新时间', key: 'updatedAt', width: 180 },
  { title: '操作', key: 'actions', width: 104, fixed: 'right', actionCount: 2 }
]

const allPolicies = computed(() => [...defaultRules.value, ...policies.value])
const scopeOptions = [
  { label: '供应商层', value: 'provider' },
  { label: '协议层', value: 'protocol' }
]
const openAIProviders = computed(() => providers.value
  .filter((provider) => provider.enabled && provider.protocolProfiles.some((profile) => profile.enabled && isOpenAIProtocolProfile(profile)))
  .sort((left, right) => left.name.localeCompare(right.name, 'zh-Hans-CN') || left.code.localeCompare(right.code)))
const providerSelectOptions = computed(() => openAIProviders.value.map((provider) => ({
  label: provider.name,
  value: provider.code
})))
const providerNameByCode = computed(() => new Map(openAIProviders.value.map((provider) => [provider.code, provider.name])))
const filteredPolicies = computed(() => {
  const text = keyword.value.trim().toLowerCase()
  if (!text) return allPolicies.value
  return allPolicies.value.filter((policy) => searchableText(policy).includes(text))
})

const modalTitle = computed(() => {
  if (modalReadonly.value) return '查看默认策略'
  return editingId.value ? '编辑响应检查策略' : '新建响应检查策略'
})

function defaultForm(): ResponsePolicyForm {
  const next: ResponsePolicyForm = {
    name: '',
    enabled: true,
    priority: 1,
    scopeType: 'provider',
    providerCode: '',
    outputTextIncludes: '',
    finishReasons: '',
    errorCodes: '',
    errorTypes: '',
    errorMessageIncludes: '',
    rawTextIncludes: '',
    outputTextExcludes: '',
    jsonPathsExists: '',
    action: 'avoid_account_ttl',
    notes: ''
  }
  return next
}

async function loadPolicies(): Promise<void> {
  loading.value = true
  try {
    const result = await api.responseInspectionPolicies.list()
    defaultRules.value = result.defaultRules
    policies.value = result.policies
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载响应检查策略失败'))
  } finally {
    loading.value = false
  }
}

async function loadPageData(): Promise<void> {
  loading.value = true
  try {
    const [policyResult, providerResult] = await Promise.all([
      api.responseInspectionPolicies.list(),
      api.providers.options()
    ])
    defaultRules.value = policyResult.defaultRules
    policies.value = policyResult.policies
    providers.value = providerResult
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载响应检查策略失败'))
  } finally {
    loading.value = false
  }
}

function openCreate(): void {
  editingId.value = undefined
  modalReadonly.value = false
  Object.assign(form, defaultForm(), {
    priority: nextPriority(),
    providerCode: defaultProviderCode()
  })
  modalOpen.value = true
}

function openView(policy: ResponseInspectionPolicySummary): void {
  if (!fillForm(policy)) return
  editingId.value = undefined
  modalReadonly.value = true
  modalOpen.value = true
}

function openEdit(policy: ResponseInspectionPolicySummary): void {
  if (!policy.editable) {
    openView(policy)
    return
  }
  if (!fillForm(policy)) return
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
      await api.responseInspectionPolicies.update(editingId.value, payload)
      message.success('响应检查策略已更新')
    } else {
      await api.responseInspectionPolicies.create(payload)
      message.success('响应检查策略已创建')
    }
    modalOpen.value = false
    await loadPolicies()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存响应检查策略失败'))
  } finally {
    saving.value = false
  }
}

async function removePolicy(policy: ResponseInspectionPolicySummary): Promise<void> {
  if (!policy.editable) return
  try {
    await api.responseInspectionPolicies.delete(policy.id)
    policies.value = policies.value.filter((item) => item.id !== policy.id)
    message.success('响应检查策略已删除')
    void loadPolicies()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '删除响应检查策略失败'))
  }
}

function handlePolicyAction(key: string, policy: ResponseInspectionPolicySummary): void {
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

function actionsFor(policy: ResponseInspectionPolicySummary): RowActionItem[] {
  if (!policy.editable) {
    return [
      { key: 'view', label: '查看', icon: 'view', tone: 'info' }
    ]
  }
  return [
    { key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' },
    { key: 'delete', label: '删除', icon: 'delete', tone: 'danger', confirmTitle: '确认删除这个响应检查策略？', confirmOkText: '删除' }
  ]
}

function fillForm(policy: ResponseInspectionPolicySummary): boolean {
  Object.assign(form, {
    name: policy.name,
    enabled: policy.enabled,
    priority: policy.priority,
    scopeType: policy.scopeType,
    providerCode: policy.providerCode ?? '',
    outputTextIncludes: formatResponseInspectionList(policy.match.outputTextIncludes),
    finishReasons: formatResponseInspectionList(policy.match.finishReasons),
    errorCodes: formatResponseInspectionList(policy.match.errorCodes),
    errorTypes: formatResponseInspectionList(policy.match.errorTypes),
    errorMessageIncludes: formatResponseInspectionList(policy.match.errorMessageIncludes),
    rawTextIncludes: formatResponseInspectionList(policy.match.rawTextIncludes),
    outputTextExcludes: formatResponseInspectionList(policy.match.outputTextExcludes),
    jsonPathsExists: formatResponseInspectionList(policy.match.jsonPathsExists),
    action: policy.action,
    notes: policy.notes ?? ''
  })
  return true
}

function buildPayload(): ResponseInspectionPolicyPayload {
  const payload: ResponseInspectionPolicyPayload = {
    name: form.name.trim(),
    enabled: form.enabled,
    priority: requiredPositiveInt(form.priority, '优先级', 9999),
    scopeType: form.scopeType,
    providerCode: form.scopeType === 'provider' ? form.providerCode.trim() : undefined,
    match: buildResponseInspectionMatchPayload(form),
    action: form.action,
    notes: form.notes.trim() || undefined
  }
  return payload
}

function validateForm(): string | undefined {
  if (!form.name.trim()) return '请填写策略名称'
  if (form.scopeType === 'provider' && !form.providerCode.trim()) return '请选择供应商'
  if (!positiveInt(form.priority, 9999)) return '优先级必须是 1-9999 的整数'
  const listValidation = validateResponseInspectionMatchFields(form)
  if (listValidation) return listValidation
  if (!hasPositiveResponseInspectionMatcher(form)) return '至少需要填写一个匹配条件'
  return undefined
}

function handleScopeChange(): void {
  if (modalReadonly.value) return
  if (form.scopeType === 'provider' && !form.providerCode) {
    form.providerCode = defaultProviderCode()
  }
  if (form.scopeType === 'protocol') {
    form.providerCode = ''
  }
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


function positiveInt(value: unknown, max = Number.POSITIVE_INFINITY): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && Number.isInteger(value) && value > 0 && value <= max ? value : undefined
}

function requiredPositiveInt(value: unknown, label: string, max = Number.POSITIVE_INFINITY): number {
  const numberValue = positiveInt(value, max)
  if (!numberValue) throw new Error(`${label}无效`)
  return numberValue
}

function searchableText(policy: ResponseInspectionPolicySummary): string {
  return [
    policy.name,
    scopeText(policy),
    protocolText(policy.protocolCode),
    providerText(policy.providerCode),
    matchSummary(policy),
    policy.defaultRule ? '默认' : '自定义',
    String(policy.priority),
    policy.enabled ? '启用' : '停用',
    responseInspectionActionLabel(policy.action),
    policy.notes
  ].filter(Boolean).join(' ').toLowerCase()
}

function actionText(action: ResponseInspectionPolicyAction): string {
  return responseInspectionActionLabel(action)
}

function scopeText(policy: Pick<ResponseInspectionPolicySummary, 'scopeType'>): string {
  return policy.scopeType === 'provider' ? '供应商层' : '协议层'
}

function protocolText(protocolCode: string): string {
  if (protocolCode === 'openai') return 'OpenAI v1'
  return protocolCode || '-'
}

function providerText(providerCode?: string): string {
  if (!providerCode) return '-'
  return providerNameByCode.value.get(providerCode) ?? providerDisplayName(providerCode, openAIProviders.value)
}

function defaultProviderCode(): string {
  return preferredDefaultProviderCode(openAIProviders.value)
}

function matchSummary(policy: ResponseInspectionPolicySummary): string {
  const match = policy.match
  const parts = [
    responseInspectionScopedListSummary('输出包含', match.outputTextIncludes),
    responseInspectionScopedListSummary('输出排除', match.outputTextExcludes),
    responseInspectionScopedListSummary('code', match.errorCodes),
    responseInspectionScopedListSummary('type', match.errorTypes),
    responseInspectionScopedListSummary('错误消息', match.errorMessageIncludes),
    responseInspectionScopedListSummary('完成原因', match.finishReasons),
    responseInspectionScopedListSummary('SSE 原文', match.rawTextIncludes),
    responseInspectionScopedListSummary('JSON路径', match.jsonPathsExists)
  ].filter(Boolean)
  return parts.length ? parts.join('；') : '-'
}

function listText(values?: string[]): string {
  return responseInspectionListText(values)
}

onMounted(loadPageData)
</script>

<style scoped>
.response-policy-page {
  min-height: 0;
}

.response-policy-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.policy-name-text {
  display: block;
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

.form-grid.three {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}

.response-policy-mobile-card :deep(.mobile-list-meta-item strong) {
  font-weight: 400;
}

@media (max-width: 820px) {
  .form-grid.three {
    grid-template-columns: 1fr;
  }
}
</style>

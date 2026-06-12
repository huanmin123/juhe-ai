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
              <span>原始事件文本包含</span>
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
          <div class="form-grid two">
            <a-form-item label="输出文本包含">
              <a-textarea v-model:value="form.outputTextIncludes" :disabled="modalReadonly" :rows="1" auto-size placeholder="广告关键词、异常提示，支持 JSON 与 SSE" />
            </a-form-item>
            <a-form-item label="输出文本排除">
              <a-textarea v-model:value="form.outputTextExcludes" :disabled="modalReadonly" :rows="1" auto-size placeholder="命中输出文本包含后，包含这些关键词时不拦截" />
            </a-form-item>
            <a-form-item label="error.code">
              <a-input v-model:value="form.errorCodes" :disabled="modalReadonly" placeholder="cyber_policy" />
            </a-form-item>
            <a-form-item label="error.type">
              <a-input v-model:value="form.errorTypes" :disabled="modalReadonly" placeholder="server_error" />
            </a-form-item>
            <a-form-item label="错误消息包含">
              <a-textarea v-model:value="form.errorMessageIncludes" :disabled="modalReadonly" :rows="1" auto-size placeholder="upstream policy blocked, rate limit" />
            </a-form-item>
            <a-form-item label="完成原因 / 状态">
              <a-input v-model:value="form.finishReasons" :disabled="modalReadonly" placeholder="failed, content_filter, length" />
            </a-form-item>
            <a-form-item label="原始事件文本包含">
              <a-textarea v-model:value="form.rawTextIncludes" :disabled="modalReadonly" :rows="1" auto-size placeholder="只匹配 SSE 原始事件文本，用于兜底排障" />
            </a-form-item>
            <a-form-item label="JSON字段路径存在">
              <a-input v-model:value="form.jsonPathsExists" :disabled="modalReadonly" placeholder="response.error, error" />
            </a-form-item>
          </div>
        </section>

        <section class="form-section">
          <div class="form-section-title">处置</div>
          <a-form-item label="处置模板">
            <div class="action-option-grid">
              <button
                v-for="template in responseInspectionActionTemplates"
                :key="template.action"
                class="action-option"
                :class="{ active: form.action === template.action }"
                type="button"
                :disabled="modalReadonly"
                @click="selectAction(template.action)"
              >
                <span class="action-option-title">
                  <span class="action-option-dot" />
                  <strong>{{ template.label }}</strong>
                  <a-tag :color="actionTagColor(template)">{{ actionTagText(template) }}</a-tag>
                </span>
                <span class="action-option-description">{{ template.description }}</span>
              </button>
            </div>
          </a-form-item>
          <a-form-item label="备注">
            <a-textarea v-model:value="form.notes" :disabled="modalReadonly" :rows="2" placeholder="可写污染来源或排障线索" />
          </a-form-item>
        </section>
      </a-form>
    </a-modal>

    <a-modal v-model:open="guideOpen" title="响应检查策略配置指南" width="900px" :footer="null">
      <div class="policy-guide">
        <p class="guide-note guide-intro">
          策略作用于 OpenAI v1 Chat / Responses 的 JSON 与 SSE 响应语义；协议层用于 OpenAI v1 全局规则，供应商层用于 GPT 或其他 OpenAI v1 兼容供应商的局部规则。其他供应商协议必须先接入自己的语义适配器。
        </p>

        <section class="guide-section">
          <h4>去哪里查依据</h4>
          <a-table
            :columns="guideSourceColumns"
            :data-source="responseInspectionPolicyGuideSources"
            :pagination="false"
            row-key="key"
            size="small"
          />
        </section>

        <section class="guide-section">
          <h4>字段怎么填</h4>
          <a-table
            :columns="guideFieldColumns"
            :data-source="responseInspectionPolicyGuideFields"
            :pagination="false"
            row-key="key"
            size="small"
          />
          <p class="guide-note">自定义规则中，多个值用逗号、分号或换行分隔；同一个字段里的多个值是“任一命中”，不同字段之间是“同时命中”。</p>
        </section>

        <section class="guide-section">
          <h4>处置怎么选</h4>
          <a-table
            :columns="guideActionColumns"
            :data-source="responseInspectionPolicyGuideActions"
            :pagination="false"
            row-key="key"
            size="small"
          />
        </section>

        <section class="guide-section">
          <h4>常见响应结构</h4>
          <pre class="guide-code">{{ responseInspectionPolicyGuideExample }}</pre>
        </section>
      </div>
    </a-modal>
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
import {
  responseInspectionPolicyGuideActions,
  responseInspectionPolicyGuideExample,
  responseInspectionPolicyGuideFields,
  responseInspectionPolicyGuideSources
} from './responseInspectionPolicyGuide'
import {
  responseInspectionActionLabel,
  responseInspectionActionTemplates,
  type ResponseInspectionActionTemplate
} from './responseInspectionActionTemplates'

interface ResponsePolicyForm {
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

const listSeparators = /[,;，；\n]/

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
  { title: '原始事件文本包含', key: 'rawTextIncludes', width: 220 },
  { title: '处置模板', key: 'action', width: 220 },
  { title: '备注', key: 'notes', width: 220 },
  { title: '更新时间', key: 'updatedAt', width: 180 },
  { title: '操作', key: 'actions', width: 104, fixed: 'right', actionCount: 2 }
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
    outputTextIncludes: formatList(policy.match.outputTextIncludes),
    finishReasons: formatList(policy.match.finishReasons),
    errorCodes: formatList(policy.match.errorCodes),
    errorTypes: formatList(policy.match.errorTypes),
    errorMessageIncludes: formatList(policy.match.errorMessageIncludes),
    rawTextIncludes: formatList(policy.match.rawTextIncludes),
    outputTextExcludes: formatList(policy.match.outputTextExcludes),
    jsonPathsExists: formatList(policy.match.jsonPathsExists),
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
    match: compactObject({
      outputTextIncludes: splitList(form.outputTextIncludes),
      finishReasons: splitList(form.finishReasons),
      errorCodes: splitList(form.errorCodes),
      errorTypes: splitList(form.errorTypes),
      errorMessageIncludes: splitList(form.errorMessageIncludes),
      rawTextIncludes: splitList(form.rawTextIncludes),
      outputTextExcludes: splitList(form.outputTextExcludes),
      jsonPathsExists: splitList(form.jsonPathsExists)
    }),
    action: form.action,
    notes: form.notes.trim() || undefined
  }
  return payload
}

function validateForm(): string | undefined {
  if (!form.name.trim()) return '请填写策略名称'
  if (form.scopeType === 'provider' && !form.providerCode.trim()) return '请选择供应商'
  if (!positiveInt(form.priority, 9999)) return '优先级必须是 1-9999 的整数'
  const listValidation = validateMatchLists()
  if (listValidation) return listValidation
  if (!hasAnyMatcher()) return '至少需要填写一个匹配条件'
  return undefined
}

function selectAction(action: ResponseInspectionPolicyAction): void {
  if (modalReadonly.value) return
  form.action = action
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

function hasAnyMatcher(): boolean {
  return [
    form.outputTextIncludes,
    form.finishReasons,
    form.errorCodes,
    form.errorTypes,
    form.errorMessageIncludes,
    form.rawTextIncludes,
    form.jsonPathsExists
  ].some((value) => (splitList(value) ?? []).length > 0)
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

function positiveInt(value: unknown, max = Number.POSITIVE_INFINITY): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && Number.isInteger(value) && value > 0 && value <= max ? value : undefined
}

function requiredPositiveInt(value: unknown, label: string, max = Number.POSITIVE_INFINITY): number {
  const numberValue = positiveInt(value, max)
  if (!numberValue) throw new Error(`${label}无效`)
  return numberValue
}

function validateMatchLists(): string | undefined {
  const fields: Array<[unknown, string]> = [
    [form.outputTextIncludes, '输出文本包含'],
    [form.outputTextExcludes, '输出文本排除'],
    [form.errorCodes, '错误码'],
    [form.errorTypes, '错误类型'],
    [form.errorMessageIncludes, '错误消息包含'],
    [form.finishReasons, '完成原因 / 状态'],
    [form.jsonPathsExists, 'JSON 路径'],
    [form.rawTextIncludes, '原始事件文本']
  ]
  for (const [value, label] of fields) {
    const items = splitList(value) ?? []
    if (items.length > 50) return `${label}不能超过 50 项`
    if (items.some((item) => item.length > 200)) return `${label}单项不能超过 200 个字符`
  }
  return undefined
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

function actionTagText(template: ResponseInspectionActionTemplate): string {
  if (template.action === 'observe') return '观察'
  if (template.action === 'drop_event') return '不重试'
  if (template.runtimeAvoidance) return '短期避让'
  return '重试'
}

function defaultProviderCode(): string {
  return preferredDefaultProviderCode(openAIProviders.value)
}

function actionTagColor(template: ResponseInspectionActionTemplate): string {
  if (template.action === 'observe') return 'gold'
  if (template.action === 'drop_event') return 'default'
  if (template.runtimeAvoidance) return 'orange'
  return 'green'
}

function matchSummary(policy: ResponseInspectionPolicySummary): string {
  const match = policy.match
  const parts = [
    scopedList('输出包含', match.outputTextIncludes),
    scopedList('输出排除', match.outputTextExcludes),
    scopedList('code', match.errorCodes),
    scopedList('type', match.errorTypes),
    scopedList('错误消息', match.errorMessageIncludes),
    scopedList('完成原因', match.finishReasons),
    scopedList('原始文本', match.rawTextIncludes),
    scopedList('JSON路径', match.jsonPathsExists)
  ].filter(Boolean)
  return parts.length ? parts.join('；') : '-'
}

function listText(values?: string[]): string {
  return values?.length ? values.join(', ') : '-'
}

function scopedList(label: string, values?: string[]): string {
  if (!values?.length) return ''
  return `${label}: ${values.slice(0, 3).join(', ')}${values.length > 3 ? ` 等 ${values.length} 项` : ''}`
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

.form-grid.two {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

.form-grid.three {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}

.action-option-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}

.action-option {
  display: flex;
  min-height: 78px;
  flex-direction: column;
  gap: 7px;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  background: #fff;
  padding: 10px 12px;
  color: inherit;
  cursor: pointer;
  text-align: left;
  transition: border-color 0.16s ease, background 0.16s ease, box-shadow 0.16s ease;
}

.action-option:hover:not(:disabled) {
  border-color: #91caff;
  background: #f8fbff;
}

.action-option.active {
  border-color: #1677ff;
  background: #f0f7ff;
  box-shadow: inset 0 0 0 1px rgba(22, 119, 255, 0.18);
}

.action-option:disabled {
  cursor: default;
}

.action-option-title {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.action-option-title strong {
  overflow: hidden;
  color: #111827;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.action-option-dot {
  width: 10px;
  height: 10px;
  flex: 0 0 auto;
  border: 2px solid #cbd5e1;
  border-radius: 50%;
  background: #fff;
}

.action-option.active .action-option-dot {
  border-color: #1677ff;
  box-shadow: inset 0 0 0 2px #fff;
  background: #1677ff;
}

.action-option-description {
  color: #64748b;
  font-size: 12px;
  line-height: 18px;
}

.response-policy-mobile-card :deep(.mobile-list-meta-item strong) {
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

  .action-option-grid {
    grid-template-columns: 1fr;
  }
}
</style>

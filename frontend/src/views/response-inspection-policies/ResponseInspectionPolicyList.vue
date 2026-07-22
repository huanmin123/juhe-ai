<template>
  <ResponsiveDataList
    table-class="page-table response-policy-table"
    :columns="columns"
    :data-source="policies"
    row-key="id"
    :loading="loading"
    :pagination="{ pageSize: 20, hideOnSinglePage: true, showSizeChanger: false }"
    :scroll-x="3300"
    pull-refresh-enabled
    :refreshing="loading"
    @mobile-refresh="emit('refresh')"
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
      <template v-else-if="column.key === 'clientProfiles'">
        <div class="field-cell">{{ clientProfileText(record.match.clientProfiles) }}</div>
      </template>
      <template v-else-if="column.key === 'priority'">
        <span>{{ record.priority }}</span>
      </template>
      <template v-else-if="column.key === 'status'">
        <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
      </template>
      <template v-else-if="isMatchFieldColumn(column.key)">
        <div :class="matchFieldClass(column.key)">{{ matchFieldText(record, column.key) }}</div>
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
            <span>请求客户端</span>
            <strong>{{ clientProfileText(record.match.clientProfiles) }}</strong>
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
          <div
            v-for="field in mobileMatchFields"
            :key="field.key"
            class="mobile-list-meta-item"
            :class="{ 'mobile-list-meta-wide': field.wide }"
          >
            <span>{{ field.label }}</span>
            <strong>{{ matchFieldText(record, field.key) }}</strong>
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
</template>

<script setup lang="ts">
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import type { ProviderDefinition, ResponseInspectionPolicySummary } from '@/types/domain'
import {
  responseInspectionPolicyActionText as actionText,
  responseInspectionPolicyClientProfileText as clientProfileText,
  responseInspectionPolicyProviderText,
  responseInspectionPolicyProtocolText as protocolText,
  responseInspectionPolicyScopeText as scopeText
} from './responseInspectionPolicyDisplay'
import {
  responseInspectionListText,
  responseInspectionMatchFieldDefinitions,
  type ResponseInspectionTextMatchFieldKey
} from './responseInspectionPolicyForm'

type MatchColumnKey = ResponseInspectionTextMatchFieldKey

const props = defineProps<{
  loading: boolean
  policies: ResponseInspectionPolicySummary[]
  providers: Array<Pick<ProviderDefinition, 'code' | 'name'>>
}>()

const emit = defineEmits<{
  (event: 'delete', policy: ResponseInspectionPolicySummary): void
  (event: 'edit', policy: ResponseInspectionPolicySummary): void
  (event: 'refresh'): void
  (event: 'view', policy: ResponseInspectionPolicySummary): void
}>()

const matchColumnWidths = {
  outputTextIncludes: 220,
  outputTextExcludes: 220,
  errorCodes: 160,
  errorTypes: 160,
  errorMessageIncludes: 220,
  finishReasons: 190,
  jsonPathsExists: 190,
  rawTextIncludes: 240
} satisfies Record<MatchColumnKey, number>
const textFieldKeys = new Set<MatchColumnKey>([
  'errorMessageIncludes',
  'outputTextExcludes',
  'rawTextIncludes'
])
const mobileMatchFieldKeys = [
  'outputTextIncludes',
  'finishReasons',
  'errorCodes',
  'errorTypes',
  'errorMessageIncludes',
  'outputTextExcludes',
  'rawTextIncludes',
  'jsonPathsExists'
] as const satisfies readonly MatchColumnKey[]
const mobileWideMatchFieldKeys = new Set<MatchColumnKey>([
  'outputTextIncludes',
  'finishReasons',
  'errorMessageIncludes',
  'outputTextExcludes',
  'rawTextIncludes',
  'jsonPathsExists'
])
const matchFieldLabels = new Map(responseInspectionMatchFieldDefinitions.map((field) => [field.key, field.label]))
const matchFieldKeys = new Set<MatchColumnKey>(responseInspectionMatchFieldDefinitions.map((field) => field.key))
const matchColumns = responseInspectionMatchFieldDefinitions.map((field) => ({
  title: field.label,
  key: field.key,
  width: matchColumnWidths[field.key]
}))
const mobileMatchFields = mobileMatchFieldKeys.map((key) => ({
  key,
  label: matchFieldLabels.get(key) ?? key,
  wide: mobileWideMatchFieldKeys.has(key)
}))
const columns = [
  { title: '策略名称', key: 'name', width: 240, fixed: 'left' },
  { title: '类型', key: 'type', width: 90 },
  { title: '层级', key: 'scope', width: 110 },
  { title: '协议', key: 'protocol', width: 120 },
  { title: '供应商', key: 'provider', width: 150 },
  { title: '请求客户端', key: 'clientProfiles', width: 150 },
  { title: '优先级', key: 'priority', width: 90 },
  { title: '状态', key: 'status', width: 90 },
  ...matchColumns,
  { title: '处置模板', key: 'action', width: 220 },
  { title: '备注', key: 'notes', width: 220 },
  { title: '更新时间', key: 'updatedAt', width: 180 },
  { title: '操作', key: 'actions', width: 104, fixed: 'right', actionCount: 2 }
]

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

function handlePolicyAction(key: string, policy: ResponseInspectionPolicySummary): void {
  if (key === 'view') {
    emit('view', policy)
    return
  }
  if (key === 'edit') {
    emit('edit', policy)
    return
  }
  if (key === 'delete') {
    emit('delete', policy)
  }
}

function providerText(providerCode?: string): string {
  return responseInspectionPolicyProviderText(providerCode, props.providers)
}

function isMatchFieldColumn(key: unknown): key is MatchColumnKey {
  return typeof key === 'string' && matchFieldKeys.has(key as MatchColumnKey)
}

function matchFieldText(record: ResponseInspectionPolicySummary, key: unknown): string {
  if (!isMatchFieldColumn(key)) return '-'
  return responseInspectionListText(record.match[key])
}

function matchFieldClass(key: unknown): string[] {
  return [
    'field-cell',
    isMatchFieldColumn(key) && textFieldKeys.has(key) ? 'text-field-cell' : ''
  ]
}
</script>

<style scoped>
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

.response-policy-mobile-card :deep(.mobile-list-meta-item strong) {
  font-weight: 400;
}
</style>

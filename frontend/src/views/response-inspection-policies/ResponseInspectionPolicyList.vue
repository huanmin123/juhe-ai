<template>
  <ResponsiveDataList
    table-class="page-table response-policy-table"
    :columns="columns"
    :data-source="policies"
    row-key="id"
    :loading="loading"
    :pagination="{ pageSize: 20, hideOnSinglePage: true, showSizeChanger: false }"
    :scroll-x="1280"
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
        <span>{{ record.providerName || record.providerCode || '-' }}</span>
      </template>
      <template v-else-if="column.key === 'priority'">
        <span>{{ record.priority }}</span>
      </template>
      <template v-else-if="column.key === 'status'">
        <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
      </template>
      <template v-else-if="column.key === 'action'">
        <a-tag color="cyan">{{ actionText(record.action) }}</a-tag>
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
            <strong>{{ record.providerName || record.providerCode || '-' }}</strong>
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
import type { ResponseInspectionPolicyOverview } from '@/types/domain'
import {
  responseInspectionPolicyActionText as actionText,
  responseInspectionPolicyProtocolText as protocolText,
  responseInspectionPolicyScopeText as scopeText
} from './responseInspectionPolicyDisplay'

const props = defineProps<{
  loading: boolean
  policies: ResponseInspectionPolicyOverview[]
  openingPolicyId?: string
}>()

const emit = defineEmits<{
  (event: 'delete', policy: ResponseInspectionPolicyOverview): void
  (event: 'edit', policy: ResponseInspectionPolicyOverview): void
  (event: 'refresh'): void
  (event: 'view', policy: ResponseInspectionPolicyOverview): void
}>()

const columns = [
  { title: '策略名称', key: 'name', width: 240, fixed: 'left' },
  { title: '类型', key: 'type', width: 90 },
  { title: '层级', key: 'scope', width: 110 },
  { title: '协议', key: 'protocol', width: 120 },
  { title: '供应商', key: 'provider', width: 150 },
  { title: '优先级', key: 'priority', width: 90 },
  { title: '状态', key: 'status', width: 90 },
  { title: '处置模板', key: 'action', width: 220 },
  { title: '更新时间', key: 'updatedAt', width: 180 },
  { title: '操作', key: 'actions', width: 104, fixed: 'right', actionCount: 2 }
]

function actionsFor(policy: ResponseInspectionPolicyOverview): RowActionItem[] {
  const opening = props.openingPolicyId === policy.id
  if (!policy.editable) {
    return [
      { key: 'view', label: opening ? '加载中' : '查看', icon: 'view', tone: 'info', disabled: opening }
    ]
  }
  return [
    { key: 'edit', label: opening ? '加载中' : '编辑', icon: 'edit', tone: 'primary', disabled: opening },
    { key: 'delete', label: '删除', icon: 'delete', tone: 'danger', disabled: opening, confirmTitle: '确认删除这个响应检查策略？', confirmOkText: '删除' }
  ]
}

function handlePolicyAction(key: string, policy: ResponseInspectionPolicyOverview): void {
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

.response-policy-mobile-card :deep(.mobile-list-meta-item strong) {
  font-weight: 400;
}
</style>

<template>
  <a-drawer
    :open="open"
    width="min(920px, 96vw)"
    title="操作日志详情"
    :body-style="{ padding: '18px' }"
    @update:open="emit('update:open', $event)"
    @close="emit('close')"
  >
    <a-spin :spinning="loading">
      <template v-if="detail">
        <a-descriptions bordered size="small" :column="2" class="detail-descriptions">
          <a-descriptions-item label="时间">{{ formatDateTime(detail.createdAt) }}</a-descriptions-item>
          <a-descriptions-item label="动作">{{ moduleText(detail.module) }} / {{ actionText(detail.action) }}</a-descriptions-item>
          <a-descriptions-item label="操作标识">{{ detail.operationKey }}</a-descriptions-item>
          <a-descriptions-item label="操作人">{{ actorText(detail) }}</a-descriptions-item>
          <a-descriptions-item label="业务归属">{{ displayName(detail.operationScopeSystemAccountName, detail.operationScopeSystemAccountId) }}</a-descriptions-item>
          <a-descriptions-item label="资源">{{ resourceText(detail) }}</a-descriptions-item>
          <a-descriptions-item label="可见范围" :span="detail.method || detail.path || detail.clientIp ? 1 : 2">{{ visibilityText(detail.visibilityScope) }}</a-descriptions-item>
          <a-descriptions-item v-if="detail.method || detail.path" label="请求">{{ requestText(detail) }}</a-descriptions-item>
          <a-descriptions-item v-if="detail.clientIp" label="客户端 IP" :span="detail.method || detail.path ? 2 : 1">{{ detail.clientIp }}</a-descriptions-item>
          <a-descriptions-item label="traceId" :span="2">{{ detail.traceId ?? '-' }}</a-descriptions-item>
          <a-descriptions-item label="摘要" :span="2">{{ detail.summary }}</a-descriptions-item>
        </a-descriptions>

        <a-tabs>
          <a-tab-pane key="changes" tab="变更内容">
            <ResponsiveDataList size="small" :pagination="false" :columns="changeColumns" :data-source="detail.changes" row-key="field" :table-scroll-enabled="false" :lock-body-scroll="false">
              <template #emptyText>
                <a-empty description="没有字段级变更摘要。" />
              </template>
              <template #bodyCell="{ column, record }">
                <template v-if="column.key === 'field'">
                  <span class="mono-cell">{{ record.field }}</span>
                </template>
                <template v-else-if="column.key === 'before'">
                  <span :class="record.sensitive ? 'muted-cell' : ''">{{ valueText(record.before) }}</span>
                </template>
                <template v-else-if="column.key === 'after'">
                  <span :class="record.sensitive ? 'muted-cell' : ''">{{ valueText(record.after) }}</span>
                </template>
              </template>
              <template #card="{ record }">
                <article class="detail-table-card">
                  <strong class="mono-cell">{{ record.field }}</strong>
                  <span>名称：{{ record.label }}</span>
                  <span>变更前：{{ valueText(record.before) }}</span>
                  <span>变更后：{{ valueText(record.after) }}</span>
                </article>
              </template>
            </ResponsiveDataList>
          </a-tab-pane>
          <a-tab-pane key="targets" tab="影响对象">
            <ResponsiveDataList size="small" :pagination="false" :columns="targetColumns" :data-source="detail.targets" row-key="id" :table-scroll-enabled="false" :lock-body-scroll="false">
              <template #emptyText>
                <a-empty description="没有额外影响对象。" />
              </template>
              <template #bodyCell="{ column, record }">
                <template v-if="column.key === 'target'">{{ displayName(record.targetName, record.targetId) }}</template>
                <template v-else-if="column.key === 'type'"><a-tag>{{ resourceTypeText(record.targetType) }}</a-tag></template>
                <template v-else-if="column.key === 'owner'">{{ displayName(record.targetOwnerSystemAccountName) }}</template>
                <template v-else-if="column.key === 'relation'">{{ relationText(record.relation) }}</template>
              </template>
              <template #card="{ record }">
                <article class="detail-table-card">
                  <strong>{{ displayName(record.targetName, record.targetId) }}</strong>
                  <span>类型：{{ resourceTypeText(record.targetType) }}</span>
                  <span>归属用户：{{ displayName(record.targetOwnerSystemAccountName) }}</span>
                  <span>关系：{{ relationText(record.relation) }}</span>
                </article>
              </template>
            </ResponsiveDataList>
          </a-tab-pane>
          <a-tab-pane v-if="isManagementView" key="viewers" tab="可见用户">
            <ResponsiveDataList size="small" :pagination="false" :columns="viewerColumns" :data-source="detail.viewers" :row-key="viewerRowKey" :table-scroll-enabled="false" :lock-body-scroll="false">
              <template #bodyCell="{ column, record }">
                <template v-if="column.key === 'user'">{{ displayName(record.systemAccountName, record.systemAccountId) }}</template>
                <template v-else-if="column.key === 'reason'">{{ visibilityReasonText(record.visibilityReason) }}</template>
                <template v-else-if="column.key === 'level'">{{ record.detailLevel === 'summary' ? '摘要' : '完整' }}</template>
              </template>
              <template #card="{ record }">
                <article class="detail-table-card">
                  <strong>{{ displayName(record.systemAccountName, record.systemAccountId) }}</strong>
                  <span>可见原因：{{ visibilityReasonText(record.visibilityReason) }}</span>
                  <span>详情级别：{{ record.detailLevel === 'summary' ? '摘要' : '完整' }}</span>
                </article>
              </template>
            </ResponsiveDataList>
          </a-tab-pane>
        </a-tabs>
      </template>
    </a-spin>
  </a-drawer>
</template>

<script setup lang="ts">
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import { formatDateTime } from '@/shared/formatters'
import type { OperationLogDetailViewer, OperationLogRenderedDetail } from '@/types/domain'
import { actorText, displayName, requestText, resourceText, valueText } from './operationLogDisplay'
import { actionText, moduleText, relationText, resourceTypeText, visibilityReasonText, visibilityText } from './operationLogLabels'
import { changeColumns, targetColumns, viewerColumns } from './operationLogOptions'

defineProps<{
  detail?: OperationLogRenderedDetail
  isManagementView: boolean
  loading: boolean
  open: boolean
}>()

const emit = defineEmits<{
  (event: 'close'): void
  (event: 'update:open', open: boolean): void
}>()

function viewerRowKey(viewer: OperationLogDetailViewer): string {
  return `${viewer.systemAccountId}:${viewer.visibilityReason}`
}
</script>

<style scoped>
.detail-descriptions {
  margin-bottom: 16px;
}

.detail-table-card {
  display: grid;
  gap: 6px;
  padding: 12px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
  color: #64748b;
  font-size: 12px;
}

.detail-table-card strong {
  color: #0f172a;
  font-size: 13px;
}

.muted-cell {
  color: #0f172a;
  font-size: 12px;
}

.mono-cell {
  color: #0f172a;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}
</style>

<template>
  <a-drawer
    :open="open"
    class="model-checks-detail-drawer"
    title="检测结果详情"
    width="720px"
    :body-style="{ padding: '16px' }"
    @update:open="emit('update:open', $event)"
  >
    <a-skeleton v-if="loading" active :paragraph="{ rows: 5 }" />
    <a-empty v-else-if="!run" description="尚未选择检测记录" />
    <div v-else class="run-detail">
      <div class="run-detail-head">
        <div>
          <div class="run-detail-title">{{ targetDisplayName(run) }}</div>
          <div class="run-detail-subtitle">
            检测目标：AI 账户
          </div>
        </div>
        <a-space wrap>
          <a-tag :color="statusColor(run.status)">{{ statusText(run.status) }}</a-tag>
          <a-tag :color="levelColor(run.level)">{{ levelText(run.level) }}</a-tag>
          <a-tag v-if="runTrustedComparison(run)" color="blue">可信对比</a-tag>
          <a-tag>{{ run.score }} / {{ run.maxScore }}</a-tag>
        </a-space>
      </div>

      <a-descriptions bordered size="small" :column="descriptionColumns" class="run-descriptions">
        <a-descriptions-item label="检测 ID">{{ run.id }}</a-descriptions-item>
        <a-descriptions-item label="账户名称">{{ targetDisplayName(run) }}</a-descriptions-item>
        <a-descriptions-item label="模型">{{ modelText(run.model) }}</a-descriptions-item>
        <a-descriptions-item label="创建时间">{{ formatDateTime(run.createdAt) }}</a-descriptions-item>
        <a-descriptions-item label="完成时间">{{ formatDateTime(run.finishedAt) }}</a-descriptions-item>
        <a-descriptions-item label="耗时">{{ formatDuration(run.durationMs) }}</a-descriptions-item>
        <a-descriptions-item label="证据完整度">{{ evidenceCompletenessText(run) }}</a-descriptions-item>
        <a-descriptions-item label="结论">{{ run.message || run.errorMessage || '-' }}</a-descriptions-item>
        <a-descriptions-item label="Trace ID">{{ run.traceId || '-' }}</a-descriptions-item>
      </a-descriptions>

      <div v-if="run.checks.length" class="check-list">
        <div v-for="check in run.checks" :key="check.id" class="check-item">
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

      <pre class="json-block">{{ formatJson({ request: run.requestSummary, result: run.resultSummary }) }}</pre>
    </div>
  </a-drawer>
</template>

<script setup lang="ts">
import { formatDateTime } from '@/shared/formatters'
import type { ModelCheckOption, ModelCheckRunDetail, ModelCheckRunSummary } from '@/types/domain'
import {
  checkExtra,
  checkMessage,
  checkStatusColor,
  checkStatusText,
  checkTitle,
  evidenceCompletenessText,
  formatModelCheckDuration as formatDuration,
  formatModelCheckJson as formatJson,
  hasCheckExtra,
  levelColor,
  levelText,
  modelCheckModelText,
  runTrustedComparison,
  statusColor,
  statusText
} from './modelCheckFormatters'

const props = defineProps<{
  descriptionColumns: number
  loading: boolean
  open: boolean
  run?: ModelCheckRunDetail
  supportedModels: ModelCheckOption[]
  targetDisplayName: (run: Pick<ModelCheckRunSummary, 'targetName' | 'targetId'>) => string
}>()

const emit = defineEmits<{
  (event: 'update:open', value: boolean): void
}>()

function modelText(value: string) {
  return modelCheckModelText(value, props.supportedModels)
}
</script>

<style scoped>
.model-checks-detail-drawer :deep(.ant-drawer-content-wrapper) {
  max-width: 100vw;
}

.run-detail {
  display: grid;
  gap: 14px;
}

.run-detail-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
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

@media (max-width: 900px) {
  .run-detail-head,
  .check-item-head {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>

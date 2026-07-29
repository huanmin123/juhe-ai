<template>
  <a-drawer
    :open="indexOpen"
    width="min(920px, 96vw)"
    title="运行日志详情"
    :body-style="{ padding: '18px' }"
    @update:open="emit('update:indexOpen', $event)"
  >
    <template v-if="log">
      <a-descriptions bordered size="small" :column="descriptionColumn" class="detail-descriptions">
        <a-descriptions-item label="时间">{{ formatDateTime(log.time) }}</a-descriptions-item>
        <a-descriptions-item label="级别">{{ levelText(log.level) }}</a-descriptions-item>
        <a-descriptions-item label="traceId" :span="fullSpan">
          <span class="detail-value-with-actions">
            <span class="detail-breakable-text">{{ log.traceId ?? '-' }}</span>
            <span v-if="log.traceId" class="detail-inline-actions">
              <a-tooltip title="按 traceId 搜索">
                <a-button size="small" type="text" @click="emit('search-trace', log.traceId)">
                  <template #icon><SearchOutlined /></template>
                </a-button>
              </a-tooltip>
              <a-tooltip title="复制 traceId">
                <a-button size="small" type="text" @click="emit('copy-text', log.traceId, 'traceId 已复制')">
                  <template #icon><CopyOutlined /></template>
                </a-button>
              </a-tooltip>
            </span>
          </span>
        </a-descriptions-item>
        <a-descriptions-item label="事件" :span="log.event ? 1 : fullSpan">{{ eventText(log.event) }}</a-descriptions-item>
        <a-descriptions-item v-if="log.event" label="事件原值">{{ log.event }}</a-descriptions-item>
        <a-descriptions-item label="消息" :span="fullSpan">{{ runtimeLogMessageText(log) }}</a-descriptions-item>
      </a-descriptions>
      <a-spin :spinning="indexLoading">
        <div class="raw-block-toolbar">
          <strong>原始内容</strong>
          <a-tooltip title="复制原始内容">
            <a-button size="small" :disabled="!log.rawJson" @click="emit('copy-text', prettyRawJson(log.rawJson ?? ''), '原始内容已复制')">
              <template #icon><CopyOutlined /></template>
            </a-button>
          </a-tooltip>
        </div>
        <pre class="raw-block">{{ prettyRawJson(log.rawJson ?? '') }}</pre>
      </a-spin>
    </template>
  </a-drawer>

  <a-drawer
    :open="grepOpen"
    width="min(920px, 96vw)"
    title="grep 匹配行"
    :body-style="{ padding: '18px' }"
    @update:open="emit('update:grepOpen', $event)"
  >
    <template v-if="grepItem">
      <a-descriptions bordered size="small" :column="descriptionColumn" class="detail-descriptions">
        <a-descriptions-item label="时间">{{ formatDateTime(grepItem.time) }}</a-descriptions-item>
        <a-descriptions-item label="级别">{{ levelText(grepItem.level) }}</a-descriptions-item>
        <a-descriptions-item label="traceId" :span="fullSpan">
          <span class="detail-value-with-actions">
            <span class="detail-breakable-text">{{ grepItem.traceId ?? '-' }}</span>
            <span v-if="grepItem.traceId" class="detail-inline-actions">
              <a-tooltip title="按 traceId 搜索">
                <a-button size="small" type="text" @click="emit('search-trace', grepItem.traceId)">
                  <template #icon><SearchOutlined /></template>
                </a-button>
              </a-tooltip>
              <a-tooltip title="复制 traceId">
                <a-button size="small" type="text" @click="emit('copy-text', grepItem.traceId, 'traceId 已复制')">
                  <template #icon><CopyOutlined /></template>
                </a-button>
              </a-tooltip>
            </span>
          </span>
        </a-descriptions-item>
        <a-descriptions-item label="事件">{{ eventText(grepItem.event) }}</a-descriptions-item>
        <a-descriptions-item v-if="grepItem.event" label="事件原值">{{ grepItem.event }}</a-descriptions-item>
        <a-descriptions-item label="消息">{{ runtimeLogMessageText(grepItem) }}</a-descriptions-item>
        <a-descriptions-item label="文件">{{ grepItem.fileName }}</a-descriptions-item>
        <a-descriptions-item label="位置" :span="grepItem.event ? fullSpan : 1">{{ grepLinePositionText(grepItem) }}</a-descriptions-item>
        <a-descriptions-item v-if="grepItem.file" label="完整路径" :span="fullSpan">{{ grepItem.file }}</a-descriptions-item>
      </a-descriptions>
      <a-spin :spinning="grepLoading">
        <div class="raw-block-toolbar">
          <strong>原始内容</strong>
          <a-tooltip title="复制原始内容">
            <a-button size="small" :disabled="!grepRawText" @click="emit('copy-text', grepRawText, '原始内容已复制')">
              <template #icon><CopyOutlined /></template>
            </a-button>
          </a-tooltip>
        </div>
        <pre class="raw-block">{{ grepRawText }}</pre>
      </a-spin>
    </template>
  </a-drawer>
</template>

<script setup lang="ts">
import { CopyOutlined, SearchOutlined } from '@ant-design/icons-vue'
import { computed } from 'vue'

import type { RuntimeLogDetailView, RuntimeLogGrepDetailView } from '@/types/domain'
import { formatDateTime } from '@/shared/formatters'
import {
  eventText,
  grepLinePositionText,
  levelText,
  prettyRawJson,
  runtimeLogMessageText
} from './runtimeLogFormatters'

const props = defineProps<{
  grepItem?: RuntimeLogGrepDetailView
  grepLoading: boolean
  grepOpen: boolean
  indexLoading: boolean
  indexOpen: boolean
  log?: RuntimeLogDetailView
}>()

const emit = defineEmits<{
  (event: 'copy-text', value: string, successMessage?: string): void
  (event: 'search-trace', traceId?: string): void
  (event: 'update:grepOpen', value: boolean): void
  (event: 'update:indexOpen', value: boolean): void
}>()

const descriptionColumn = { xs: 1, sm: 1, md: 2 }
const fullSpan = 2
const grepRawText = computed(() => props.grepItem?.line ? prettyRawJson(props.grepItem.line) : '')
</script>

<style scoped>
.detail-descriptions {
  margin-bottom: 16px;
}

.detail-value-with-actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  min-width: 0;
}

.detail-breakable-text {
  min-width: 0;
  overflow-wrap: anywhere;
  word-break: break-word;
}

.detail-inline-actions {
  display: inline-flex;
  flex: none;
  gap: 4px;
}

.raw-block-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 8px;
}

.raw-block {
  max-height: 520px;
  margin: 0;
  padding: 12px;
  overflow: auto;
  color: #0f172a;
  background: #f8fafc;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  white-space: pre-wrap;
  word-break: break-word;
}

@media (max-width: 640px) {
  .detail-value-with-actions {
    align-items: flex-start;
  }
}
</style>

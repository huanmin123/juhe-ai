<template>
  <a-drawer
    :open="open"
    width="min(720px, 96vw)"
    title="媒体任务详情"
    :body-style="{ padding: '18px' }"
    @update:open="emit('update:open', $event)"
  >
    <template #extra>
      <a-button size="small" :loading="loading" @click="fetchDetail">刷新</a-button>
    </template>
    <a-spin :spinning="loading">
      <template v-if="detail">
        <a-alert
          v-if="detail.error"
          type="error"
          show-icon
          class="status-alert"
          :message="detail.error.code"
          :description="detail.error.message || '上游未返回错误说明。'"
        />
        <a-alert
          v-else-if="isPendingStatus"
          type="info"
          show-icon
          class="status-alert"
          message="任务尚未到终态，详情随轮询更新，可点右上角刷新。"
        />

        <a-descriptions bordered size="small" :column="2" class="detail-descriptions" title="基本信息">
          <a-descriptions-item label="任务 ID" :span="2">
            <span class="mono-cell">{{ detail.id }}</span>
            <a-button size="small" type="text" @click="copyJobId">
              <template #icon><copy-outlined /></template>
            </a-button>
          </a-descriptions-item>
          <a-descriptions-item label="类型">
            <a-tag color="geekblue">{{ formatMediaJobKind(detail.kind) }}</a-tag>
          </a-descriptions-item>
          <a-descriptions-item label="状态">
            <a-tag :color="mediaJobStatusColor(detail.status)">{{ formatMediaJobStatus(detail.status) }}</a-tag>
          </a-descriptions-item>
          <a-descriptions-item label="模型">{{ detail.model || '-' }}</a-descriptions-item>
          <a-descriptions-item label="供应商">{{ detail.providerCode || '-' }}</a-descriptions-item>
          <a-descriptions-item v-if="detail.providerJobId" label="上游任务号">
            <span class="mono-cell">{{ detail.providerJobId }}</span>
          </a-descriptions-item>
          <a-descriptions-item label="成本">{{ formatMediaJobCost(detail.costUsd) }}</a-descriptions-item>
          <a-descriptions-item label="API Key">
            <span :class="detail.apiKeyId ? 'mono-cell' : 'muted-cell'">{{ detail.apiKeyId || '-' }}</span>
          </a-descriptions-item>
          <a-descriptions-item label="AI 账户">
            <span :class="detail.accountId ? 'mono-cell' : 'muted-cell'">{{ detail.accountId || '-' }}</span>
          </a-descriptions-item>
          <a-descriptions-item label="创建时间">{{ formatDateTime(detail.createdAt) }}</a-descriptions-item>
          <a-descriptions-item label="更新时间">{{ detail.updatedAt ? formatDateTime(detail.updatedAt) : '-' }}</a-descriptions-item>
        </a-descriptions>

        <a-descriptions v-if="hasRequestParams" bordered size="small" :column="1" class="detail-descriptions" title="请求参数">
          <a-descriptions-item v-if="detail.prompt" label="提示词">
            <span class="prompt-block">{{ detail.prompt }}</span>
          </a-descriptions-item>
          <a-descriptions-item v-if="detail.seconds !== undefined" label="时长">{{ formatMediaJobSeconds(detail.seconds) }}</a-descriptions-item>
          <a-descriptions-item v-if="detail.size" label="尺寸">{{ detail.size }}</a-descriptions-item>
          <a-descriptions-item v-if="detail.n" label="生成数量">{{ detail.n }}</a-descriptions-item>
          <a-descriptions-item v-if="detail.language" label="语言">{{ detail.language }}</a-descriptions-item>
          <a-descriptions-item v-if="detail.providerOptionsApplied?.length" label="生效供应商参数">
            <a-tag v-for="item in detail.providerOptionsApplied" :key="item" class="mono-cell">{{ item }}</a-tag>
          </a-descriptions-item>
        </a-descriptions>

        <div v-if="isCompleted" class="detail-section">
          <h4 class="section-title">返回内容</h4>
          <template v-if="detail.artifact?.contentUrl">
            <video
              v-if="detail.kind === 'video'"
              :key="detail.artifact.contentUrl"
              :src="detail.artifact.contentUrl"
              controls
              preload="metadata"
              class="artifact-media"
            ></video>
            <audio
              v-else-if="detail.kind === 'audio_speech'"
              :key="detail.artifact.contentUrl"
              :src="detail.artifact.contentUrl"
              controls
              preload="metadata"
              class="artifact-audio"
            ></audio>
            <p class="artifact-line">
              <a :href="detail.artifact.contentUrl" target="_blank" rel="noopener">打开产物内容</a>
              <span v-if="detail.artifact.expiresAt" class="muted-cell">（上游时效至 {{ formatDateTime(detail.artifact.expiresAt) }}，过期后产物不可再取回）</span>
            </p>
          </template>
          <template v-else-if="detail.kind === 'audio_transcription'">
            <div class="transcript-actions">
              <a-button size="small" :loading="transcriptLoading" @click="fetchTranscript">
                {{ transcriptText ? '重新获取' : '实时获取' }}
              </a-button>
              <a-button size="small" :disabled="!transcriptText" @click="copyTranscriptText">复制文本</a-button>
              <a-button size="small" :disabled="!transcriptText" @click="copyTranscriptRaw">复制原文</a-button>
              <a v-if="transcriptText" :href="transcriptDownloadUrl" :download="`${detail.id}-transcript.json`">下载 JSON</a>
            </div>
            <p v-if="transcriptError" class="artifact-line">{{ transcriptError }}</p>
            <template v-if="transcriptText">
              <div v-if="transcriptView" class="transcript-view">
                <p class="transcript-text">{{ transcriptView.text }}</p>
                <div v-if="transcriptView.segments.length > 1" class="transcript-segments">
                  <div v-for="(segment, index) in transcriptView.segments" :key="index" class="transcript-segment">
                    <span class="mono-cell transcript-segment-time">{{ segment.timeRange }}</span>
                    <span>{{ segment.text }}</span>
                  </div>
                </div>
              </div>
              <pre v-else class="usage-raw">{{ transcriptPretty }}</pre>
            </template>
          </template>
          <a-empty v-else class="section-empty" description="产物内容不落盘存储（零资源存储）；需通过归属 API Key 调用 /v1 内容接口实时取回。" />
        </div>

        <div v-if="detail.usage || isCompleted" class="detail-section">
          <h4 class="section-title">用量与计费</h4>
          <a-descriptions v-if="detail.usage" bordered size="small" :column="2">
            <a-descriptions-item v-if="detail.usage.outputVideoSeconds !== undefined" label="输出视频时长">
              {{ formatMediaJobSeconds(detail.usage.outputVideoSeconds) }}
            </a-descriptions-item>
            <a-descriptions-item v-if="detail.usage.audioInputSeconds !== undefined" label="转写输入音频时长">
              {{ formatMediaJobSeconds(detail.usage.audioInputSeconds) }}
            </a-descriptions-item>
            <a-descriptions-item v-if="hasUsageRaw" label="上游原生用量" :span="2">
              <pre class="usage-raw">{{ usageRawText }}</pre>
            </a-descriptions-item>
          </a-descriptions>
          <p v-else class="muted-cell usage-missing">无用量回报（无计量口径不虚计）。</p>
        </div>

        <a-descriptions
          v-if="detail.paramsApplied?.length || detail.paramsIgnored?.length"
          bordered
          size="small"
          :column="1"
          class="detail-descriptions"
          title="参数回显"
        >
          <a-descriptions-item v-if="detail.paramsApplied?.length" label="已应用（换算后生效）">
            <a-tag v-for="item in detail.paramsApplied" :key="item" color="blue">{{ item }}</a-tag>
          </a-descriptions-item>
          <a-descriptions-item v-if="detail.paramsIgnored?.length" label="已忽略（厂商不支持）">
            <a-tag v-for="item in detail.paramsIgnored" :key="item" color="orange">{{ item }}</a-tag>
          </a-descriptions-item>
        </a-descriptions>
      </template>
      <a-empty v-else-if="!loading" class="page-empty-card" :description="loadError ? '媒体任务详情加载失败，请关闭后重试。' : '任务详情不存在。'" />
    </a-spin>
  </a-drawer>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { CopyOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'

import { api } from '@/api/client'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import { formatDateTime } from '@/shared/formatters'
import type { MediaJobDetail } from '@/types/domain'
import {
  formatMediaJobCost,
  formatMediaJobKind,
  formatMediaJobSeconds,
  formatMediaJobStatus,
  mediaJobStatusColor
} from './mediaJobsDisplay'

const props = defineProps<{
  jobId: string | null
  open: boolean
  /** 数据面：admin 走管理面 /media-jobs/{id}（含上游任务号），self 走 /my-media-jobs/{id}（钉死本人、无上游任务号）。 */
  scope?: 'admin' | 'self'
}>()

const emit = defineEmits<{
  (event: 'update:open', value: boolean): void
}>()

const detail = ref<MediaJobDetail | null>(null)
const loading = ref(false)
const loadError = ref(false)
// 在途请求序号守卫：快速切换行或关闭抽屉时，迟到的旧响应不得覆盖新状态。
let fetchSequence = 0

const isPendingStatus = computed(() => detail.value?.status === 'queued' || detail.value?.status === 'in_progress')
const isCompleted = computed(() => detail.value?.status === 'completed')
const hasRequestParams = computed(() => {
  const row = detail.value
  if (!row) return false
  return Boolean(row.prompt || row.seconds !== undefined || row.size || row.n || row.language || row.providerOptionsApplied?.length)
})
const hasUsageRaw = computed(() => Object.keys(detail.value?.usage?.raw ?? {}).length > 0)
const usageRawText = computed(() => JSON.stringify(detail.value?.usage?.raw ?? {}, null, 2))

// 零存储任务（长音频转写）的内容实时取回：会话面 content 端点边取边回，
// 前端拉到后仅在前端内存展示/下载，服务端与本地存储均不落盘。
const transcriptLoading = ref(false)
const transcriptText = ref('')
const transcriptError = ref('')
let transcriptSequence = 0
const transcriptPretty = computed(() => {
  if (!transcriptText.value) return ''
  try {
    return JSON.stringify(JSON.parse(transcriptText.value), null, 2)
  } catch {
    return transcriptText.value
  }
})
const transcriptDownloadUrl = computed(() => {
  if (!transcriptText.value) return ''
  const blob = new Blob([transcriptText.value], { type: 'application/json' })
  return URL.createObjectURL(blob)
})

interface TranscriptSegmentView {
  timeRange: string
  text: string
}

// 结构化解析已知的转写载荷形态（火山录音文件识别 2.0：result.text +
// result.utterances 词级分段）；未知形态返回 null 回落原始 JSON 展示，不猜测。
const transcriptView = computed<{ text: string; segments: TranscriptSegmentView[] } | null>(() => {
  if (!transcriptText.value) return null
  try {
    const payload = JSON.parse(transcriptText.value) as {
      result?: {
        text?: string
        utterances?: { start_time?: number; end_time?: number; text?: string }[]
      }
    }
    const text = typeof payload.result?.text === 'string' ? payload.result.text : ''
    if (!text) return null
    const segments = Array.isArray(payload.result?.utterances)
      ? (payload.result.utterances
          .filter(item => typeof item.text === 'string' && item.text.trim() !== '')
          .map(item => ({
            timeRange: `${formatMediaJobSeconds((item.start_time ?? 0) / 1000)} - ${formatMediaJobSeconds((item.end_time ?? 0) / 1000)}`,
            text: item.text as string
          })))
      : []
    return { text, segments }
  } catch {
    return null
  }
})

async function fetchTranscript(): Promise<void> {
  if (!props.jobId) return
  const sequence = ++transcriptSequence
  transcriptLoading.value = true
  transcriptError.value = ''
  try {
    const text = props.scope === 'self'
      ? await api.mediaJobs.myContentText(props.jobId)
      : await api.mediaJobs.contentText(props.jobId)
    if (sequence !== transcriptSequence) return
    transcriptText.value = text
  } catch (error) {
    if (sequence !== transcriptSequence) return
    transcriptError.value = error instanceof Error ? error.message : '转写内容获取失败'
  } finally {
    if (sequence === transcriptSequence) transcriptLoading.value = false
  }
}

async function copyTranscriptText(): Promise<void> {
  await copyTextToClipboard(transcriptView.value?.text ?? transcriptText.value, '转写文本已复制')
}

async function copyTranscriptRaw(): Promise<void> {
  await copyTextToClipboard(transcriptText.value, '转写原文（JSON）已复制')
}

async function fetchDetail(): Promise<void> {
  if (!props.jobId) return
  const sequence = ++fetchSequence
  loading.value = true
  try {
    const result = props.scope === 'self'
      ? await api.mediaJobs.myDetail(props.jobId)
      : await api.mediaJobs.detail(props.jobId)
    if (sequence !== fetchSequence) return
    detail.value = result
    loadError.value = false
  } catch (error) {
    if (sequence !== fetchSequence) return
    console.error(error)
    detail.value = null
    loadError.value = true
    message.error(extractApiErrorMessage(error, '加载媒体任务详情失败'))
  } finally {
    if (sequence === fetchSequence) loading.value = false
  }
}

async function copyJobId(): Promise<void> {
  if (!detail.value) return
  await copyTextToClipboard(detail.value.id, '任务 ID 已复制')
}

watch(() => [props.open, props.jobId] as const, ([open, jobId]) => {
  if (!open) {
    detail.value = null
    loadError.value = false
    transcriptText.value = ''
    transcriptError.value = ''
    transcriptLoading.value = false
    return
  }
  transcriptText.value = ''
  transcriptError.value = ''
  if (jobId) void fetchDetail()
})
</script>

<style scoped>
.detail-descriptions {
  margin-bottom: 18px;
}

.status-alert {
  margin-bottom: 14px;
}

.detail-section {
  margin-bottom: 18px;
}

.section-title {
  margin: 0 0 8px;
  color: var(--juhe-fg);
  font-size: 13px;
  font-weight: 600;
}

.section-empty :deep(.ant-empty-description) {
  color: var(--juhe-muted);
  font-size: 12px;
}

.mono-cell,
.transcript-actions {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
  margin-bottom: 8px;
}

.transcript-actions a {
  font-size: 13px;
}

.transcript-text {
  margin: 0 0 8px;
  padding: 8px 12px;
  background: var(--color-fill-quaternary, rgba(0, 0, 0, 0.04));
  border-left: 3px solid var(--color-primary, #1677ff);
  border-radius: 4px;
  line-height: 1.7;
  white-space: pre-wrap;
  word-break: break-word;
}

.transcript-segments {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-bottom: 8px;
}

.transcript-segment {
  display: flex;
  gap: 8px;
  align-items: baseline;
}

.transcript-segment-time {
  flex-shrink: 0;
  color: var(--color-text-tertiary, rgba(0, 0, 0, 0.45));
}

.usage-raw {
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

.muted-cell {
  color: var(--juhe-muted);
}

.prompt-block {
  display: block;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}

.artifact-media {
  display: block;
  width: 100%;
  border-radius: 8px;
  background: var(--juhe-surface);
}

.artifact-audio {
  display: block;
  width: 100%;
}

.artifact-line {
  margin: 8px 0 0;
}

.usage-raw {
  max-height: 220px;
  margin: 0;
  overflow: auto;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}

.usage-missing {
  margin: 0;
  font-size: 12px;
}
</style>

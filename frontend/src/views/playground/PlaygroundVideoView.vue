<template>
  <a-card class="page-card responsive-page-card">
    <div class="playground-media-layout">
      <a-form layout="vertical" class="playground-media-form">
        <a-form-item label="账户" required>
          <a-select
            v-model:value="accountId"
            :options="accountOptions"
            :loading="accountsLoading"
            allow-clear
            show-search
            option-filter-prop="label"
            placeholder="选择要试玩的账户"
            @change="handleAccountChange"
          />
        </a-form-item>
        <a-form-item label="模型" required>
          <a-select
            v-model:value="modelId"
            :options="modelOptions"
            :disabled="!accountId || !modelOptions.length"
            show-search
            option-filter-prop="label"
            :placeholder="accountId ? '选择视频模型' : '先选择账户'"
          />
        </a-form-item>
        <a-form-item label="提示词" required>
          <a-textarea
            v-model:value="prompt"
            :rows="4"
            placeholder="描述要生成的视频内容"
          />
        </a-form-item>
        <div class="playground-two-column">
          <a-form-item label="尺寸（可选）">
            <a-input v-model:value="size" allow-clear placeholder="如 1280x720" />
          </a-form-item>
          <a-form-item label="时长秒数（可选）">
            <a-input-number v-model:value="seconds" :min="1" placeholder="默认" style="width: 100%" />
          </a-form-item>
        </div>
        <a-button
          type="primary"
          :loading="creating"
          :disabled="!canCreate"
          @click="createJob"
        >
          创建生成任务
        </a-button>
        <div class="playground-field-hint">任务在 24 小时内可恢复,过期后需重新生成。</div>
        <div v-if="accountsLoadError" class="playground-form-error">{{ accountsLoadError }}</div>
      </a-form>

      <section class="playground-media-result">
        <a-empty
          v-if="!job"
          class="playground-empty"
          description="创建任务后在这里查看生成进度与视频"
        />
        <template v-else>
          <div class="playground-video-status-row">
            <span class="playground-status" :class="`is-${job.status}`">{{ statusLabelOf(job.status) }}</span>
            <span v-if="job.status === 'running' && job.progress !== undefined" class="playground-video-progress">进度 {{ job.progress }}%</span>
          </div>
          <a-alert
            v-if="(job.status === 'failed' || job.status === 'cancelled') && job.error"
            class="playground-video-error"
            type="error"
            show-icon
            :message="videoErrorMessageOf(job.error)"
          />
          <a-alert
            v-if="pollBroken"
            class="playground-video-error"
            type="error"
            show-icon
            message="连续查询任务状态失败，已停止自动轮询；请刷新页面后重试"
          />
          <div v-if="videoUrl" class="playground-video-shell">
            <video class="playground-video" :src="videoUrl" controls />
            <a-button block @click="downloadVideo">下载视频</a-button>
          </div>
          <div v-else-if="contentLoading || job.status === 'completed'" class="playground-video-loading">
            <a-spin v-if="contentLoading" size="small" />
            <span>正在取回视频内容…</span>
          </div>
          <div v-else-if="!pollBroken && (job.status === 'queued' || job.status === 'running')" class="playground-video-loading">
            <a-spin size="small" />
            <span>每 3 秒轮询一次任务状态，离开页面会暂停轮询。</span>
          </div>
        </template>
      </section>
    </div>
  </a-card>
</template>

<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref } from 'vue'
import { message } from '@/lib/antd'

import { api } from '@/api/client'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { PlaygroundArtifactVideo, PlaygroundVideoPollError, PlaygroundVideoStatus } from '@/types/domain/playground'
import { usePlaygroundCatalog } from './usePlaygroundCatalog'

// 视频生成（演练场设计 §4.2）：创建异步任务后每 3 秒轮询，completed 后经
// videosContent 取回视频字节（blob URL）内联播放；任务引用随服务端暂存区记录
// （§3.5/§3.8），刷新后经 artifacts 恢复选择与轮询/内容。页面在 KeepAlive 下
// 导航离开时停轮询（onDeactivated），返回时对未完成任务续轮。轮询连续失败达
// 阈值（pollMaxFailures）后熔断：停止轮询并置终态错误提示，由用户手动刷新重试，
// 避免失败周期无限打点。

const pollIntervalMs = 3000
const pollMaxFailures = 5
/** 恢复窗口：在途任务引用距 createdAt 超过 60 分钟视为已死，不再续轮（§3.8 暂存仅为展示缓存）。 */
const restoreMaxAgeMs = 60 * 60 * 1000

const {
  accounts,
  accountsLoading,
  accountsLoadError,
  accountId,
  modelId,
  accountOptions,
  modelOptions,
  handleAccountChange,
  loadCatalog
} = usePlaygroundCatalog('video')

interface PlaygroundVideoJobState {
  jobId: string
  accountId: string
  status: PlaygroundVideoStatus
  progress?: number
  error?: PlaygroundVideoPollError
}

const prompt = ref('')
const size = ref('')
const seconds = ref<number>()
const creating = ref(false)
const job = ref<PlaygroundVideoJobState>()
const videoUrl = ref('')
const contentLoading = ref(false)
const pollBroken = ref(false)

let pollTimer: ReturnType<typeof setTimeout> | null = null
let pollFailureCount = 0

const canCreate = computed(() => Boolean(prompt.value.trim() && accountId.value && modelId.value && !creating.value))

onMounted(() => {
  void restoreOnLoad()
})

onActivated(() => {
  startPolling()
})

onDeactivated(() => {
  stopPolling()
})

onBeforeUnmount(() => {
  stopPolling()
  revokeVideoUrl()
})

/** 加载序：先账户目录（默认选中），再拉暂存清单恢复视频任务引用（暂存为展示缓存，恢复失败不阻断创建）。 */
async function restoreOnLoad(): Promise<void> {
  await loadCatalog()
  try {
    const artifacts = await api.playground.artifacts()
    restoreVideoRef(artifacts.video)
  } catch (error) {
    console.error(error)
  }
}

/**
 * 恢复任务引用（设计 §3.8）：经目录匹配 accountId/modelId，匹配不到丢弃；
 * completed → 恢复选择并取内容播放；在途且距 createdAt ≤60 分钟 → 恢复选择并续轮；
 * 终态失败/取消或超龄在途引用不可恢复，保持空态（任务过期后需重新生成）。
 */
function restoreVideoRef(videoRef: PlaygroundArtifactVideo | null): void {
  if (!videoRef) return
  const account = accounts.value.find((candidate) =>
    candidate.accountId === videoRef.accountId
    && candidate.models.some((model) => model.kind === 'video'))
  if (!account) return
  if (!account.models.some((model) => model.kind === 'video' && model.modelId === videoRef.modelId)) return
  accountId.value = videoRef.accountId
  modelId.value = videoRef.modelId
  if (videoRef.status === 'completed') {
    job.value = { jobId: videoRef.jobId, accountId: videoRef.accountId, status: 'completed' }
    void loadVideoContent(job.value)
    return
  }
  if (isPendingStatus(videoRef.status) && Date.now() - videoRef.createdAt * 1000 <= restoreMaxAgeMs) {
    job.value = { jobId: videoRef.jobId, accountId: videoRef.accountId, status: videoRef.status }
    pollBroken.value = false
    startPolling()
  }
}

function isPendingStatus(status: PlaygroundVideoStatus): boolean {
  return status === 'queued' || status === 'running'
}

function statusLabelOf(status: PlaygroundVideoStatus): string {
  return ({ queued: '排队中', running: '生成中', completed: '已完成', failed: '失败', cancelled: '已取消' })[status]
}

// 轮询 error 是归一对象 {code?, message?}（后端 videosPollError 原样透出）：
// 优先 message（未知状态原文也在此），空则回落 code。
function videoErrorMessageOf(error: PlaygroundVideoPollError): string {
  return error.message || error.code || '任务失败'
}

function startPolling(): void {
  stopPolling()
  if (!job.value || !isPendingStatus(job.value.status)) return
  pollFailureCount = 0
  pollTimer = setTimeout(() => void pollOnce(), pollIntervalMs)
}

function stopPolling(): void {
  if (pollTimer) {
    clearTimeout(pollTimer)
    pollTimer = null
  }
}

async function pollOnce(): Promise<void> {
  const current = job.value
  if (!current || !isPendingStatus(current.status)) return
  try {
    const result = await api.playground.videosPoll(current.jobId, current.accountId)
    if (job.value !== current) return
    pollFailureCount = 0
    pollBroken.value = false
    job.value = { ...current, status: result.status, progress: result.progress, error: result.error }
    if (result.status === 'completed') {
      await loadVideoContent(current)
      return
    }
    if (!isPendingStatus(result.status)) return
  } catch (error) {
    console.error(error)
    pollFailureCount += 1
    if (pollFailureCount >= pollMaxFailures) {
      // 熔断：连续失败达阈值即停止轮询并置终态错误，不再无限重试；由用户
      // 手动刷新重试（重新进入页面时 startPolling 会重置计数续轮）。
      stopPolling()
      pollBroken.value = true
      message.error('连续查询视频任务状态失败，已停止自动轮询，请刷新页面后重试')
      return
    }
    message.error(extractApiErrorMessage(error, '查询视频任务状态失败'))
  }
  startPolling()
}

async function loadVideoContent(target: PlaygroundVideoJobState): Promise<void> {
  contentLoading.value = true
  try {
    const url = await api.playground.videosContent(target.jobId, target.accountId)
    revokeVideoUrl()
    videoUrl.value = url
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '获取视频内容失败'))
  } finally {
    contentLoading.value = false
  }
}

async function createJob(): Promise<void> {
  const content = prompt.value.trim()
  if (!content) {
    message.warning('请输入提示词')
    return
  }
  if (!accountId.value || !modelId.value) {
    message.warning('请先选择账户与模型')
    return
  }
  creating.value = true
  try {
    const result = await api.playground.videosCreate({
      accountId: accountId.value,
      modelId: modelId.value,
      prompt: content,
      size: size.value.trim() || undefined,
      seconds: seconds.value ?? undefined
    })
    revokeVideoUrl()
    job.value = { jobId: result.jobId, accountId: accountId.value, status: 'queued' }
    pollFailureCount = 0
    pollBroken.value = false
    startPolling()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '创建视频任务失败，请稍后重试'))
  } finally {
    creating.value = false
  }
}

function downloadVideo(): void {
  const link = document.createElement('a')
  link.href = videoUrl.value
  link.download = 'playground-video.mp4'
  link.click()
}

function revokeVideoUrl(): void {
  if (videoUrl.value) {
    URL.revokeObjectURL(videoUrl.value)
    videoUrl.value = ''
  }
}
</script>

<style scoped>
.playground-media-layout {
  display: grid;
  grid-template-columns: 360px minmax(0, 1fr);
  gap: 24px;
}

.playground-media-form {
  min-width: 0;
}

.playground-two-column {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}

.playground-field-hint {
  margin-top: 12px;
  color: var(--juhe-faint);
  font-size: 12px;
}

.playground-form-error {
  margin-top: 12px;
  color: var(--juhe-danger);
  font-size: 12px;
}

.playground-media-result {
  min-width: 0;
}

.playground-empty {
  height: 100%;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  min-height: 240px;
}

.playground-video-status-row {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 12px;
}

.playground-status {
  padding: 2px 10px;
  border-radius: 999px;
  font-size: 12px;
  background: var(--juhe-surface-soft);
  border: 1px solid var(--juhe-border);
  color: var(--juhe-muted);
}

.playground-status.is-running {
  color: var(--juhe-accent);
  border-color: var(--juhe-accent-mid);
  background: var(--juhe-accent-soft);
}

.playground-status.is-completed {
  color: var(--juhe-ok);
  border-color: var(--juhe-ok-line);
  background: var(--juhe-ok-soft);
}

.playground-status.is-failed,
.playground-status.is-cancelled {
  color: var(--juhe-danger);
  border-color: var(--juhe-danger-line);
  background: var(--juhe-danger-soft);
}

.playground-video-progress {
  color: var(--juhe-muted);
  font-size: 13px;
}

.playground-video-error {
  margin-bottom: 12px;
}

.playground-video-shell {
  /* 与图片页同款:结果区自适应铺满,播放器吃可用高度。 */
  width: 100%;
}

.playground-video {
  display: block;
  width: 100%;
  max-height: min(62vh, 620px);
  margin: 0 auto 8px;
  object-fit: contain;
  border-radius: 8px;
  background: var(--juhe-surface-soft);
}

.playground-video-loading {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--juhe-muted);
  font-size: 13px;
}

@media (max-width: 992px) {
  .playground-media-layout {
    grid-template-columns: 1fr;
  }
}
</style>

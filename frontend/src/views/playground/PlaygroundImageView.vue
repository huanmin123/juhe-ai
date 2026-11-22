<template>
  <a-card class="page-card responsive-page-card">
    <!-- 快速入口（§4.2）：卡片式入口（sparkle 图标 + 引导文案 + 圆形生成按钮），
         整卡可点/回车 → 打开生成设置弹窗（非真实输入框，不承载输入状态）。 -->
    <button type="button" class="image-quick-entry" @click="openWorkbench">
      <span class="image-quick-spark" aria-hidden="true">
        <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
          <path d="M12 2l2.4 7.6L22 12l-7.6 2.4L12 22l-2.4-7.6L2 12l7.6-2.4z" />
        </svg>
      </span>
      <span class="image-quick-placeholder">今天想生成什么？</span>
      <span class="image-quick-submit" aria-hidden="true">
        <ArrowUpOutlined />
      </span>
    </button>

    <!-- 生成历史（§4.2）：标题 + 已载入计数 + 筛选栏 + 卡片网格 + 游标加载更多。
         responsive-page-card 约定 card body 固定视口高 + overflow hidden（无全局滚动），
         快速入口常驻顶部，历史区经 .image-history-scroll 内部滚动。 -->
    <div class="image-history-scroll">
      <div class="image-history-head">
        <h3 class="image-history-title">生成历史</h3>
        <span class="image-history-count">已载入 {{ jobs.length }} 条</span>
      </div>
      <div class="image-filter-bar">
        <a-input
          v-model:value="filterKeyword"
          class="image-filter-keyword"
          allow-clear
          placeholder="搜索提示词"
        />
        <a-select
          v-model:value="filterApiKeyId"
          class="image-filter-select"
          :options="filterApiKeyOptions"
          allow-clear
          show-search
          option-filter-prop="label"
          placeholder="全部 Key"
          @change="reloadJobs"
        />
        <a-select
          v-model:value="filterStatus"
          class="image-filter-select"
          :options="statusFilterOptions"
          allow-clear
          placeholder="全部状态"
          @change="reloadJobs"
        />
        <a-select
          v-model:value="filterQuality"
          class="image-filter-select"
          :options="qualityFilterOptions"
          allow-clear
          placeholder="全部质量"
          @change="reloadJobs"
        />
      </div>

      <div v-if="jobsLoading" class="image-card-grid">
        <div v-for="index in 4" :key="index" class="image-job-card is-skeleton">
          <a-skeleton active :paragraph="{ rows: 5 }" :title="false" />
        </div>
      </div>
      <a-empty
        v-else-if="!jobs.length"
        class="image-history-empty"
        description="还没有生成记录，从上方入口开始创作"
      />
      <template v-else>
        <div class="image-card-grid">
          <article
            v-for="job in jobs"
            :key="job.jobId"
            class="image-job-card"
            :class="{ 'is-clickable': isCardClickable(job) }"
            :role="isCardClickable(job) ? 'button' : undefined"
            :tabindex="isCardClickable(job) ? 0 : undefined"
            @click="handleCardClick(job)"
            @keydown.enter.prevent="handleCardClick(job)"
          >
            <div class="image-card-thumb">
              <span class="playground-status" :class="statusClassOf(job)">{{ statusLabelOf(job) }}</span>
              <!-- completed 且未删:结果图按固定高度裁切直显,点图走 a-image 大图预览(阻断冒泡,与卡片回填区分),另附下载。 -->
              <div v-if="showImageThumb(job)" class="image-card-thumb-media" @click.stop>
                <a-image class="image-card-image" :src="job.images[0]?.url" :alt="job.prompt" />
                <a-button class="image-card-download" size="small" @click.stop="downloadImage(job)">下载</a-button>
              </div>
              <div v-else-if="isPendingStatus(job.status)" class="image-card-thumb-placeholder">
                <a-spin size="small" />
                <span>生成中…</span>
              </div>
              <div v-else-if="job.status === 'failed'" class="image-card-thumb-placeholder">
                <span class="image-card-thumb-title">生成失败</span>
                <span v-if="job.error" class="image-card-thumb-error" :title="job.error">{{ job.error }}</span>
              </div>
              <div v-else class="image-card-thumb-placeholder">
                <span>已删除图像</span>
              </div>
            </div>
            <div class="image-card-body">
              <div v-if="job.prompt" class="image-card-prompt" :title="job.prompt">{{ job.prompt }}</div>
              <div v-else class="image-card-prompt is-placeholder">生成任务排队中…</div>
              <div class="image-card-tags">
                <span v-if="job.aspectRatio" class="image-card-tag">{{ job.aspectRatio }}</span>
                <span v-if="job.size" class="image-card-tag">{{ job.size }}</span>
                <span v-if="job.quality" class="image-card-tag">{{ qualityLabelOf(job.quality) }}</span>
                <span v-if="job.outputFormat" class="image-card-tag">{{ job.outputFormat.toUpperCase() }}</span>
                <span v-if="job.referenceCount > 0" class="image-card-tag">参考图 ×{{ job.referenceCount }}</span>
              </div>
              <div class="image-card-meta">
                <span class="image-card-time">{{ absoluteTimeText(job.createdAt) }}</span>
                <span v-if="expiryTextOf(job)" class="image-card-expiry">{{ expiryTextOf(job) }}</span>
              </div>
            </div>
          </article>
        </div>
        <div v-if="nextCursor" class="image-load-more">
          <a-button :loading="loadingMore" @click="loadMoreJobs">加载更多</a-button>
        </div>
      </template>

    </div>

    <PlaygroundImageWorkbenchModal
      v-model:open="workbenchOpen"
      :initial-prompt="workbenchInitialPrompt"
      :initial-job="workbenchInitialJob"
      @created="handleJobCreated"
    />
  </a-card>
</template>

<script setup lang="ts">
import { ArrowUpOutlined } from '@ant-design/icons-vue'
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import { message } from '@/lib/antd'
import dayjs from 'dayjs'

import { api } from '@/api/client'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { ApiKeySummary } from '@/types/domain'
import type {
  PlaygroundImageJob,
  PlaygroundImageJobCreateResult,
  PlaygroundImageJobStatus,
  PlaygroundImageJobsParams,
  PlaygroundImageQuality
} from '@/types/domain/playground'
import {
  playgroundImageJobStatusClass,
  playgroundImageJobStatusLabels,
  playgroundImageQualityLabels
} from '@/types/domain/playground'
import { apiKeyOptionLabel, loadPlaygroundKeys } from './usePlaygroundCatalog'
import PlaygroundImageWorkbenchModal from './PlaygroundImageWorkbenchModal.vue'

// 图片生成工作台（演练场设计 §4.2 v1.3 重写）：顶部卡片式快速入口 → 生成设置弹窗
// （异步任务）→ 生成历史卡片网格。生成为真异步任务（§3.4）：任务记录在服务端落库,
// 前端对 queued/running 卡片 3s 轮询详情,单任务连续失败 5 次熔断停轮该任务;
// onMounted 加载历史,onActivated 恢复轮询（刷新后凭历史列表恢复「生成中」并续轮,
// 不依赖前端持有）,onDeactivated/onBeforeUnmount 停止。completed 未删卡片按分钟
// tick 刷新删除倒计时（keepAlive 下激活期管理定时器）。演练场不展示 tokens/费用
// （v1.3）：卡片仅呈现状态、提示词、参数与保留期。2026-11-22 Key 化：历史筛选
// 「全部账户」改「全部 Key」（本人 active Key，任务行记录创建时的 Key）。

const pollIntervalMs = 3000
const pollMaxFailures = 5
const pageSize = 24
/** 筛选提示词防抖:输入停顿后再触发历史重查,避免每键打点。 */
const keywordDebounceMs = 400

// ---- 快速入口与弹窗 ----

const workbenchOpen = ref(false)
const workbenchInitialPrompt = ref('')
const workbenchInitialJob = ref<PlaygroundImageJob | null>(null)

function openWorkbench(): void {
  workbenchInitialJob.value = null
  workbenchInitialPrompt.value = ''
  workbenchOpen.value = true
}

function openWorkbenchWithJob(job: PlaygroundImageJob): void {
  workbenchInitialJob.value = job
  workbenchInitialPrompt.value = ''
  workbenchOpen.value = true
}

// ---- 筛选栏 ----

const filterKeyword = ref('')
const filterApiKeyId = ref<string>()
const filterStatus = ref<'' | PlaygroundImageJobsParams['status']>('')
const filterQuality = ref<'' | PlaygroundImageQuality>('')

const catalogKeys = ref<ApiKeySummary[]>([])
const filterApiKeyOptions = computed(() => catalogKeys.value
  .map((key) => ({ value: key.id, label: apiKeyOptionLabel(key) })))

const statusFilterOptions = [
  { label: '全部', value: '' },
  { label: '生成中', value: 'active' },
  { label: '已完成', value: 'completed' },
  { label: '失败', value: 'failed' },
  { label: '已删除图像', value: 'expired' }
]
const qualityFilterOptions = [
  { label: '全部', value: '' },
  { label: '标准', value: 'standard' },
  { label: '精细', value: 'fine' }
]

// ---- 历史列表（游标分页） ----

const jobs = ref<PlaygroundImageJob[]>([])
const jobsLoading = ref(false)
const loadingMore = ref(false)
const nextCursor = ref<number>()
/** 请求序号守卫:reload 期间迟到的翻页/旧筛选响应直接丢弃。 */
let requestSeq = 0
let keywordDebounceTimer: ReturnType<typeof setTimeout> | null = null

function filterParams(limit: number, before?: number): PlaygroundImageJobsParams {
  const params: PlaygroundImageJobsParams = { limit }
  const keyword = filterKeyword.value.trim()
  if (keyword) params.keyword = keyword
  if (filterApiKeyId.value) params.apiKeyId = filterApiKeyId.value
  if (filterStatus.value) params.status = filterStatus.value
  if (filterQuality.value) params.quality = filterQuality.value
  if (before !== undefined) params.before = before
  return params
}

async function reloadJobs(): Promise<void> {
  const seq = ++requestSeq
  jobsLoading.value = true
  try {
    const page = await api.playground.imagesJobs(filterParams(pageSize))
    if (seq !== requestSeq) return
    jobs.value = page.jobs
    nextCursor.value = page.nextCursor
    resumePolling()
  } catch (error) {
    console.error(error)
    if (seq === requestSeq) message.error(extractApiErrorMessage(error, '加载生成历史失败，请稍后重试'))
  } finally {
    if (seq === requestSeq) jobsLoading.value = false
  }
}

async function loadMoreJobs(): Promise<void> {
  const cursor = nextCursor.value
  if (cursor === undefined || loadingMore.value) return
  const seq = requestSeq
  loadingMore.value = true
  try {
    const page = await api.playground.imagesJobs(filterParams(pageSize, cursor))
    if (seq !== requestSeq) return
    jobs.value.push(...page.jobs)
    nextCursor.value = page.nextCursor
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载更多生成历史失败，请稍后重试'))
  } finally {
    if (seq === requestSeq) loadingMore.value = false
  }
}

watch(filterKeyword, () => {
  if (keywordDebounceTimer) clearTimeout(keywordDebounceTimer)
  keywordDebounceTimer = setTimeout(() => {
    keywordDebounceTimer = null
    void reloadJobs()
  }, keywordDebounceMs)
})

// ---- 轮询引擎（§4.2：queued/running 卡片 3s 一询,单任务连续失败 5 次熔断停轮） ----

const pollTimers = new Map<string, ReturnType<typeof setTimeout>>()
const pollFailures = new Map<string, number>()

function isPendingStatus(status: PlaygroundImageJobStatus): boolean {
  return status === 'queued' || status === 'running'
}

function startPollingJob(jobId: string, options?: { immediate?: boolean }): void {
  stopPollingJob(jobId)
  const job = jobs.value.find((candidate) => candidate.jobId === jobId)
  if (!job || !isPendingStatus(job.status)) return
  pollFailures.set(jobId, 0)
  pollTimers.set(jobId, setTimeout(() => void pollJob(jobId), options?.immediate ? 0 : pollIntervalMs))
}

function stopPollingJob(jobId: string): void {
  const timer = pollTimers.get(jobId)
  if (timer) {
    clearTimeout(timer)
    pollTimers.delete(jobId)
  }
}

function stopAllPolling(): void {
  for (const jobId of [...pollTimers.keys()]) stopPollingJob(jobId)
}

/** 恢复轮询:对当前列表全部未收敛任务续轮（激活/历史加载后调用;重置失败计数）。 */
function resumePolling(): void {
  jobs.value
    .filter((job) => isPendingStatus(job.status))
    .forEach((job) => startPollingJob(job.jobId))
}

async function pollJob(jobId: string): Promise<void> {
  const current = jobs.value.find((candidate) => candidate.jobId === jobId)
  if (!current || !isPendingStatus(current.status)) {
    pollFailures.delete(jobId)
    return
  }
  try {
    const fresh = await api.playground.imagesJob(jobId)
    replaceJob(fresh)
    if (isPendingStatus(fresh.status)) {
      pollFailures.set(jobId, 0)
      pollTimers.set(jobId, setTimeout(() => void pollJob(jobId), pollIntervalMs))
      return
    }
    // 完成/失败/删除判定即时收敛:停轮并清计数。
    pollFailures.delete(jobId)
  } catch (error) {
    console.error(error)
    const failures = (pollFailures.get(jobId) ?? 0) + 1
    if (failures >= pollMaxFailures) {
      // 熔断:停止该任务轮询,卡片停留「生成中」;重新进入页面（onActivated）时重置续轮。
      stopPollingJob(jobId)
      pollFailures.delete(jobId)
      message.error('生成任务状态查询连续失败，已停止该任务的自动刷新；重新进入本页或刷新页面可恢复')
      return
    }
    pollFailures.set(jobId, failures)
    pollTimers.set(jobId, setTimeout(() => void pollJob(jobId), pollIntervalMs))
  }
}

function replaceJob(fresh: PlaygroundImageJob): void {
  const index = jobs.value.findIndex((candidate) => candidate.jobId === fresh.jobId)
  if (index >= 0) jobs.value[index] = fresh
}

// ---- 删除倒计时（每分钟 tick,keepAlive 激活期管理） ----

const nowTick = ref(Date.now())
let tickTimer: ReturnType<typeof setInterval> | null = null

function startTick(): void {
  if (tickTimer) return
  nowTick.value = Date.now()
  tickTimer = setInterval(() => {
    nowTick.value = Date.now()
  }, 60_000)
}

function stopTick(): void {
  if (tickTimer) {
    clearInterval(tickTimer)
    tickTimer = null
  }
}

// ---- 生命周期 ----

onMounted(() => {
  void loadKeys()
  void reloadJobs()
  startTick()
})

onActivated(() => {
  resumePolling()
  startTick()
})

onDeactivated(() => {
  stopAllPolling()
  stopTick()
})

onBeforeUnmount(() => {
  stopAllPolling()
  stopTick()
  if (keywordDebounceTimer) {
    clearTimeout(keywordDebounceTimer)
    keywordDebounceTimer = null
  }
})

async function loadKeys(): Promise<void> {
  try {
    catalogKeys.value = await loadPlaygroundKeys()
  } catch (error) {
    console.error(error)
  }
}

// ---- 任务创建占位 ----

function handleJobCreated(result: PlaygroundImageJobCreateResult): void {
  if (jobs.value.some((candidate) => candidate.jobId === result.jobId)) return
  jobs.value.unshift(makePlaceholderJob(result))
  // 立即首拉真卡片替换占位,随后进入 3s 轮询链。
  startPollingJob(result.jobId, { immediate: true })
}

function makePlaceholderJob(result: PlaygroundImageJobCreateResult): PlaygroundImageJob {
  return {
    jobId: result.jobId,
    status: 'queued',
    prompt: '',
    model: '',
    accountId: '',
    referenceCount: 0,
    references: [],
    images: [],
    imageDeleted: false,
    usage: null,
    costUsd: null,
    error: '',
    createdAt: Math.floor(Date.now() / 1000),
    completedAt: 0,
    expiresAt: 0
  }
}

// ---- 卡片展示判定 ----

function isCardClickable(job: PlaygroundImageJob): boolean {
  // 点击卡片回填弹窗仅对终态开放(§4.2);进行中卡片不可点。
  return job.status === 'completed' || job.status === 'failed'
}

function handleCardClick(job: PlaygroundImageJob): void {
  if (isCardClickable(job)) openWorkbenchWithJob(job)
}

/** 读时派生「已删除图像」:后端标记 × 本地倒计时到期收敛（§3.4.4/§3.4.5）。 */
function isCardDeleted(job: PlaygroundImageJob): boolean {
  return job.imageDeleted
    || (job.status === 'completed' && job.expiresAt !== undefined && job.expiresAt !== null && job.expiresAt * 1000 <= nowTick.value)
}

function statusLabelOf(job: PlaygroundImageJob): string {
  return isCardDeleted(job) ? '已删除图像' : playgroundImageJobStatusLabels[job.status]
}

function statusClassOf(job: PlaygroundImageJob): string {
  return isCardDeleted(job) ? '' : playgroundImageJobStatusClass[job.status]
}

function showImageThumb(job: PlaygroundImageJob): boolean {
  return job.status === 'completed' && !isCardDeleted(job) && job.images.length > 0
}

function qualityLabelOf(quality: PlaygroundImageQuality): string {
  return playgroundImageQualityLabels[quality]
}

function absoluteTimeText(seconds: number): string {
  return dayjs.unix(seconds).format('YYYY-MM-DD HH:mm:ss')
}

/** completed 未删显示「X 天 X 时后删除图像」;已删显示「图像已删除」;其余状态不显示。 */
function expiryTextOf(job: PlaygroundImageJob): string {
  if (job.status !== 'completed') return ''
  if (isCardDeleted(job)) return '图像已删除'
  if (job.expiresAt === undefined || job.expiresAt === null) return ''
  const diffMs = job.expiresAt * 1000 - nowTick.value
  if (diffMs <= 0) return '图像已删除'
  const days = Math.floor(diffMs / 86_400_000)
  const hours = Math.floor((diffMs % 86_400_000) / 3_600_000)
  return `${days} 天 ${hours} 时后删除图像`
}

/** 同域内容地址 + a[download]:浏览器自动带会话 Cookie 拉取;扩展名按输出格式,缺省 png。 */
function downloadImage(job: PlaygroundImageJob): void {
  const image = job.images[0]
  if (!image) return
  const link = document.createElement('a')
  link.href = image.url
  link.download = `playground-image-${job.jobId}.${job.outputFormat ?? 'png'}`
  link.click()
}
</script>

<style scoped>
/* 卡片式快速入口（§4.2 v1.3）：整卡一个 button,左侧 sparkle 图标 + 引导文案 +
   右侧圆形生成按钮;点击/回车打开生成设置弹窗（非真实输入框,不承载输入状态）。 */
.image-quick-entry {
  display: flex;
  align-items: center;
  gap: 14px;
  width: 100%;
  padding: 16px 18px;
  margin-bottom: 24px;
  font: inherit;
  text-align: left;
  cursor: pointer;
  background: var(--juhe-surface);
  border: 1px solid var(--juhe-border);
  border-radius: 14px;
  transition: border-color 0.2s ease, box-shadow 0.2s ease;
}

.image-quick-entry:hover,
.image-quick-entry:focus-visible {
  border-color: var(--juhe-accent-mid);
  box-shadow: 0 2px 10px rgb(83 105 107 / 12%);
  outline: none;
}

.image-quick-spark {
  display: flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 auto;
  width: 40px;
  height: 40px;
  color: var(--juhe-accent);
  background: var(--juhe-accent-soft);
  border-radius: 999px;
}

.image-quick-spark svg {
  width: 20px;
  height: 20px;
}

.image-quick-placeholder {
  flex: 1 1 auto;
  color: var(--juhe-faint);
  font-size: 15px;
}

.image-quick-submit {
  display: flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 auto;
  width: 40px;
  height: 40px;
  color: #fff;
  font-size: 15px;
  background: var(--juhe-accent);
  border-radius: 999px;
}

/* responsive-page-card 的 card body 固定视口高（calc(100dvh - 114px)）+ overflow hidden，
   无全局滚动；快速入口 flex 收缩固定，历史区在此容器内滚动（global.css 布局约定）。 */
.image-history-scroll {
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
}

.image-history-head {
  display: flex;
  align-items: baseline;
  gap: 12px;
  margin-bottom: 12px;
}

.image-history-title {
  margin: 0;
  font-size: 16px;
}

.image-history-count {
  color: var(--juhe-muted);
  font-size: 13px;
}

.image-filter-bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
  margin-bottom: 16px;
}

.image-filter-keyword {
  width: 260px;
}

.image-filter-select {
  width: 160px;
}

.image-history-empty {
  padding: 40px 0;
}

.image-card-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 14px;
}

.image-job-card {
  display: flex;
  flex-direction: column;
  min-width: 0;
  overflow: hidden;
  background: var(--juhe-surface);
  border: 1px solid var(--juhe-border);
  border-radius: var(--juhe-radius);
  transition: border-color 0.2s ease;
}

.image-job-card.is-clickable {
  cursor: pointer;
}

.image-job-card.is-clickable:hover,
.image-job-card.is-clickable:focus-visible {
  border-color: var(--juhe-border-strong);
}

.image-job-card.is-skeleton {
  padding: 14px;
}

/* 缩略图区固定高度（v1.3 卡片一致性）：所有状态共用同一高度,图片按高度裁切,
   占位/失败/已删除同样撑满,杜绝「已完成」与「已删除」卡片高差。 */
.image-card-thumb {
  position: relative;
  height: 160px;
  background: var(--juhe-surface-soft);
}

.image-card-thumb > .playground-status {
  position: absolute;
  top: 8px;
  left: 8px;
  z-index: 2;
  /* 视频页 playground-status 同款 pill 配色(§4.2)。 */
  padding: 2px 10px;
  border-radius: 999px;
  font-size: 12px;
  background: var(--juhe-surface);
  border: 1px solid var(--juhe-border);
  color: var(--juhe-muted);
}

.image-card-thumb > .playground-status.is-running {
  color: var(--juhe-accent);
  border-color: var(--juhe-accent-mid);
  background: var(--juhe-accent-soft);
}

.image-card-thumb > .playground-status.is-completed {
  color: var(--juhe-ok);
  border-color: var(--juhe-ok-line);
  background: var(--juhe-ok-soft);
}

.image-card-thumb > .playground-status.is-failed {
  color: var(--juhe-danger);
  border-color: var(--juhe-danger-line);
  background: var(--juhe-danger-soft);
}

.image-card-thumb-media {
  position: absolute;
  inset: 0;
  line-height: 0;
}

/* a-image 包装 div:结果图按容器定高裁切直显,点击 img 进入大图预览。 */
.image-card-thumb-media :deep(.ant-image) {
  width: 100%;
  height: 100%;
}

.image-card-thumb-media :deep(img) {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: cover;
  cursor: zoom-in;
}

.image-card-download {
  position: absolute;
  right: 8px;
  bottom: 8px;
  z-index: 2;
}

.image-card-thumb-placeholder {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 8px;
  height: 100%;
  color: var(--juhe-muted);
  font-size: 13px;
  text-align: center;
  padding: 0 16px;
}

.image-card-thumb-title {
  font-size: 14px;
}

.image-card-thumb-error {
  display: -webkit-box;
  overflow: hidden;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 1;
  color: var(--juhe-faint);
  font-size: 12px;
  word-break: break-all;
}

/* 信息区三块各自定高（提示词两行 + 标签一行 + 元信息一行）:
   无论标签有无,卡片纵向尺寸一致,网格跨行也等高。 */
.image-card-body {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px;
}

.image-card-prompt {
  display: -webkit-box;
  overflow: hidden;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
  min-height: 39px;
  font-size: 13px;
  line-height: 1.5;
  word-break: break-all;
}

.image-card-prompt.is-placeholder {
  color: var(--juhe-faint);
}

.image-card-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  min-height: 22px;
}

.image-card-tag {
  padding: 1px 8px;
  border: 1px solid var(--juhe-border);
  border-radius: 4px;
  color: var(--juhe-muted);
  font-size: 12px;
}

.image-card-meta {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  color: var(--juhe-faint);
  font-size: 12px;
}

.image-card-expiry {
  text-align: right;
}

.image-load-more {
  display: flex;
  justify-content: center;
  margin-top: 16px;
}

@media (max-width: 768px) {
  .image-filter-keyword {
    width: 100%;
  }

  .image-filter-select {
    flex: 1 1 40%;
    width: auto;
  }
}
</style>

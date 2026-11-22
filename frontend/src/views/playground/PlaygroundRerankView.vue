<template>
  <a-card class="page-card responsive-page-card">
    <div class="playground-media-layout">
      <a-form layout="vertical" class="playground-media-form">
        <a-form-item label="API Key" required>
          <a-select
            v-model:value="apiKeyId"
            :options="keyOptions"
            :loading="keysLoading"
            allow-clear
            show-search
            option-filter-prop="label"
            placeholder="选择要试玩的 API Key"
            @change="handleApiKeyChange"
          />
        </a-form-item>
        <a-form-item label="模型" required>
          <a-select
            v-model:value="modelId"
            :options="modelOptions"
            :loading="modelsLoading"
            :disabled="!apiKeyId || modelsLoading || !modelOptions.length"
            show-search
            option-filter-prop="label"
            :placeholder="apiKeyId ? '选择重排模型' : '先选择 API Key'"
          />
        </a-form-item>
        <a-form-item label="查询语句（Query）" required>
          <a-input v-model:value="query" allow-clear placeholder="要拿文档对照的查询语句" />
        </a-form-item>
        <a-form-item label="文档列表" required>
          <a-textarea
            v-model:value="documentsText"
            :rows="8"
            placeholder="每行一条文档，按行对照排序结果"
          />
          <div class="playground-input-hint">共 {{ documentLineCount }} 条文档</div>
        </a-form-item>
        <a-form-item label="Top N（可选）">
          <a-input-number
            v-model:value="topN"
            :min="1"
            :precision="0"
            placeholder="默认返回全部"
            style="width: 100%"
          />
        </a-form-item>
        <a-form-item>
          <div class="playground-inline-row">
            <span class="playground-toggle-label">回显文档原文</span>
            <a-switch v-model:checked="returnDocuments" />
          </div>
        </a-form-item>
        <a-button
          type="primary"
          :loading="running"
          :disabled="!canRun"
          @click="run"
        >
          执行重排
        </a-button>
        <div v-if="keysLoadError || modelsLoadError" class="playground-form-error">{{ keysLoadError || modelsLoadError }}</div>
      </a-form>

      <section class="playground-media-result">
        <a-empty
          v-if="!results.length"
          class="playground-empty"
          description="选择 API Key 与重排模型，输入查询与文档开始重排"
        />
        <template v-else>
          <div class="playground-result-meta">
            共 {{ results.length }} 条结果<template v-if="resultUsage"> · {{ resultUsage.promptTokens }} prompt / {{ resultUsage.totalTokens }} tokens</template>
            · 前端耗时 {{ elapsedMs }} ms
          </div>
          <div class="playground-rank-list">
            <div v-for="(item, rank) in results" :key="`${item.index}-${rank}`" class="playground-rank-row">
              <span class="playground-rank-badge" :class="rankBadgeClass(rank)">{{ rank + 1 }}</span>
              <div class="playground-rank-body">
                <div class="playground-rank-meta">
                  <span class="playground-rank-line">行 {{ item.index + 1 }}</span>
                  <span class="playground-rank-score">{{ item.relevanceScore.toFixed(3) }}</span>
                </div>
                <div class="playground-rank-track">
                  <div
                    class="playground-rank-fill"
                    :style="{ width: `${rankFillPercent(item.relevanceScore)}%` }"
                  />
                </div>
                <div v-if="item.document?.text" class="playground-rank-text">
                  {{ truncateText(item.document.text, 60) }}
                </div>
              </div>
            </div>
          </div>
        </template>
      </section>
    </div>
  </a-card>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { message } from '@/lib/antd'

import { api } from '@/api/client'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { PlaygroundRerankResultItem, PlaygroundUsage } from '@/types/domain/playground'
import { usePlaygroundCatalog } from './usePlaygroundCatalog'

// 重排测试（演练场设计 §4.4）：API Key × rerank 模型同步重排，results 按分数降序
// （§3.10），条形宽度按本批最高分归一；零持久化——结果只存页内 ref，刷新即空。

/** 后端校验上限（设计 §3.10：documents 1~20 条），前端同口径提前拦截避免必败请求。 */
const maxDocumentCount = 20

const {
  keysLoading,
  keysLoadError,
  modelsLoading,
  modelsLoadError,
  apiKeyId,
  modelId,
  keyOptions,
  modelOptions,
  handleApiKeyChange,
  loadCatalog
} = usePlaygroundCatalog('rerank')

// 默认示例（2026-11-22 用户裁决「造点数据让效果可见」）：query 与六条文档按相关性分层
// （助眠两条高、饮食运动两条中、天气赛事两条无关），排序结果应呈清晰梯度；用户可清空改自拟文本。
const query = ref('如何缓解失眠')
const documentsText = ref('改善睡眠质量的十个方法：固定作息、睡前远离手机\n失眠多梦怎么办？医生给出的实用建议\n睡前喝牛奶真的能助眠吗？科学解读\n规律运动有助于加深睡眠\n本周天气预报：多云转晴，适合晾晒\n本赛季英超争冠形势分析与赛程')
const topN = ref<number>()
const returnDocuments = ref(true)
const running = ref(false)
const results = ref<PlaygroundRerankResultItem[]>([])
const resultUsage = ref<PlaygroundUsage>()
const elapsedMs = ref(0)

const documentLineCount = computed(() => splitDocumentLines(documentsText.value).length)
const canRun = computed(() => Boolean(query.value.trim() && documentLineCount.value && apiKeyId.value && modelId.value && !running.value))

/** 条形归一：本批最高 relevanceScore 为满宽（§4.4）；最高分 ≤0 时全部 0 宽（分数仍由文字展示）。 */
const maxRelevance = computed(() => results.value.reduce((max, item) => Math.max(max, item.relevanceScore), 0))

onMounted(() => {
  void loadCatalog()
})

function splitDocumentLines(value: string): string[] {
  return value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean)
}

/** 条形宽度按分值占本批最高分比例（0~100）；负分值夹取 0。 */
function rankFillPercent(score: number): number {
  if (maxRelevance.value <= 0) return 0
  return Math.max(0, Math.min(100, (score / maxRelevance.value) * 100))
}

/** 名次徽标着色：1/2/3 金银铜，其余中性（数字本身已表达名次，不单靠颜色）。 */
function rankBadgeClass(rank: number): string {
  if (rank === 0) return 'is-first'
  if (rank === 1) return 'is-second'
  if (rank === 2) return 'is-third'
  return ''
}

function truncateText(text: string, max: number): string {
  return text.length > max ? `${text.slice(0, max)}…` : text
}

async function run(): Promise<void> {
  const queryText = query.value.trim()
  const documents = splitDocumentLines(documentsText.value)
  if (!queryText) {
    message.warning('请输入查询语句')
    return
  }
  if (!documents.length) {
    message.warning('请输入至少一条文档')
    return
  }
  if (documents.length > maxDocumentCount) {
    message.warning(`每轮最多 ${maxDocumentCount} 条文档`)
    return
  }
  if (!apiKeyId.value || !modelId.value) {
    message.warning('请先选择 API Key 与模型')
    return
  }
  running.value = true
  const startedAt = performance.now()
  try {
    const result = await api.playground.rerank({
      apiKeyId: apiKeyId.value,
      modelId: modelId.value,
      query: queryText,
      documents,
      topN: topN.value || undefined,
      returnDocuments: returnDocuments.value
    })
    // 后端 results 按分数降序（§3.10）；前端只按归一顺序渲染，不改写排序。
    results.value = result.results
    resultUsage.value = result.usage
    elapsedMs.value = Math.round(performance.now() - startedAt)
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '重排失败，请稍后重试'))
  } finally {
    running.value = false
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

.playground-input-hint {
  margin-top: 4px;
  color: var(--juhe-muted);
  font-size: 12px;
}

.playground-inline-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.playground-toggle-label {
  color: var(--juhe-fg);
  font-size: 14px;
}

.playground-form-error {
  margin-top: 12px;
  color: var(--juhe-danger);
  font-size: 12px;
}

.playground-media-result {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.playground-empty {
  height: 100%;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  min-height: 240px;
}

.playground-result-meta {
  color: var(--juhe-muted);
  font-size: 13px;
}

.playground-rank-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.playground-rank-row {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 10px 12px;
  border: 1px solid var(--juhe-border);
  border-radius: 8px;
  background: var(--juhe-surface-soft);
}

.playground-rank-badge {
  flex-shrink: 0;
  width: 26px;
  height: 26px;
  display: flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--juhe-border-strong);
  border-radius: 50%;
  color: var(--juhe-fg-soft);
  font-size: 13px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
  background: var(--juhe-surface);
}

.playground-rank-badge.is-first {
  border-color: rgba(169, 133, 72, .55);
  background: var(--juhe-warn-soft);
  color: var(--juhe-warn);
}

.playground-rank-badge.is-second {
  border-color: rgba(109, 115, 114, .45);
  background: #f1f1ef;
  color: var(--juhe-muted);
}

.playground-rank-badge.is-third {
  border-color: rgba(166, 117, 94, .5);
  background: #f6ede8;
  color: var(--juhe-coral);
}

.playground-rank-body {
  flex: 1;
  min-width: 0;
}

.playground-rank-meta {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 6px;
}

.playground-rank-line {
  color: var(--juhe-muted);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}

.playground-rank-score {
  color: var(--juhe-fg);
  font-size: 13px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.playground-rank-track {
  height: 8px;
  border-radius: 4px;
  background: rgba(34, 40, 43, .08);
  overflow: hidden;
}

.playground-rank-fill {
  height: 100%;
  border-radius: 4px;
  background: var(--juhe-primary-grad);
  transition: width 0.4s ease;
}

.playground-rank-text {
  margin-top: 6px;
  color: var(--juhe-fg-soft);
  font-size: 13px;
  word-break: break-all;
}

@media (max-width: 992px) {
  .playground-media-layout {
    grid-template-columns: 1fr;
  }
}
</style>

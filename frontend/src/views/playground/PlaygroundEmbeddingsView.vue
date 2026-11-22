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
            :placeholder="apiKeyId ? '选择向量模型' : '先选择 API Key'"
          />
        </a-form-item>
        <a-form-item label="输入文本" required>
          <a-textarea
            v-model:value="inputText"
            :rows="6"
            placeholder="每行一条文本；≥2 行可生成相似度矩阵"
          />
          <div class="playground-input-hint">
            共 {{ inputLineCount }} 条文本<template v-if="inputLineCount >= 2">，将生成相似度矩阵</template>
          </div>
        </a-form-item>
        <a-collapse ghost class="playground-advanced-collapse">
          <a-collapse-panel key="advanced" header="高级参数">
            <a-form-item label="dimensions（可选）">
              <a-input-number
                v-model:value="dimensions"
                :min="1"
                :precision="0"
                placeholder="默认（由上游决定）"
                style="width: 100%"
              />
            </a-form-item>
          </a-collapse-panel>
        </a-collapse>
        <a-button
          type="primary"
          :loading="generating"
          :disabled="!canGenerate"
          @click="generate"
        >
          生成向量
        </a-button>
        <div v-if="keysLoadError || modelsLoadError" class="playground-form-error">{{ keysLoadError || modelsLoadError }}</div>
      </a-form>

      <section class="playground-media-result">
        <a-empty
          v-if="!resultItems.length"
          class="playground-empty"
          description="选择 API Key 与向量模型，输入文本开始生成"
        />
        <template v-else>
          <div class="playground-result-meta">
            <template v-if="resultModel">模型 {{ resultModel }}</template>
            <template v-if="resultUsage"> · {{ resultUsage.promptTokens }} prompt / {{ resultUsage.totalTokens }} tokens</template>
            · 前端耗时 {{ elapsedMs }} ms
          </div>

          <section v-if="hasMatrix" class="playground-matrix-section">
            <div class="playground-section-head">
              <span class="playground-section-title">相似度矩阵</span>
              <span class="playground-section-hint">余弦相似度在前端本地计算，颜色越深越相似</span>
            </div>
            <div class="playground-matrix-scroll">
              <div class="playground-matrix-grid" :style="{ '--matrix-columns': matrixSize }">
                <div class="playground-matrix-corner" />
                <div v-for="row in matrixRows" :key="`head-${row.index}`" class="playground-matrix-head">
                  {{ row.index + 1 }}
                </div>
                <template v-for="row in matrixRows" :key="`row-${row.index}`">
                  <div class="playground-matrix-head">{{ row.index + 1 }}</div>
                  <a-tooltip
                    v-for="cell in row.cells"
                    :key="`cell-${row.index}-${cell.index}`"
                    :title="matrixTooltip(row.index, cell.index)"
                  >
                    <div class="playground-matrix-cell" :style="matrixCellStyle(cell.score)">
                      {{ cell.score.toFixed(3) }}
                    </div>
                  </a-tooltip>
                </template>
              </div>
            </div>
          </section>

          <div class="playground-vector-list">
            <div v-for="item in resultItems" :key="item.index" class="playground-vector-card">
              <div class="playground-vector-head">
                <span class="playground-vector-line">行 {{ item.index + 1 }}</span>
                <span class="playground-vector-text" :title="submittedInputs[item.index]">
                  {{ truncateText(submittedInputs[item.index] ?? '', 40) }}
                </span>
                <a-tag class="playground-vector-dim">{{ item.dimensions }} 维</a-tag>
                <a-button size="small" @click="copyVector(item)">复制向量 JSON</a-button>
              </div>
              <div class="playground-vector-preview">{{ vectorPreview(item.vector) }}</div>
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
import type { PlaygroundEmbeddingItem, PlaygroundUsage } from '@/types/domain/playground'
import { usePlaygroundCatalog } from './usePlaygroundCatalog'

// 向量生成（演练场设计 §4.3）：API Key × embedding 模型同步生成，完整向量随响应回传
// （§3.9），相似度矩阵由前端本地计算余弦相似度；零持久化——结果只存页内 ref，刷新即空。

/** 后端校验上限（设计 §3.9：数组 ≤10 条），前端同口径提前拦截避免必败请求。 */
const maxInputCount = 10

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
} = usePlaygroundCatalog('embedding')

// 默认示例（2026-11-22 用户裁决「造点数据让效果可见」）：前两条语义相近、第三条无关，
// 生成后相似度矩阵应呈「高-高-低」分层，直观展示向量对语义距离的表达；用户可清空改自拟文本。
const inputText = ref('猫咪是一种可爱又独立的宠物\n猫是很受欢迎的家庭伴侣动物\n今天下午股市大跌投资者情绪低迷')
const dimensions = ref<number>()
const generating = ref(false)
const submittedInputs = ref<string[]>([])
const resultItems = ref<PlaygroundEmbeddingItem[]>([])
const resultModel = ref('')
const resultUsage = ref<PlaygroundUsage>()
const elapsedMs = ref(0)

const inputLineCount = computed(() => splitInputLines(inputText.value).length)
const canGenerate = computed(() => Boolean(inputLineCount.value && apiKeyId.value && modelId.value && !generating.value))
const hasMatrix = computed(() => resultItems.value.length >= 2)
const matrixSize = computed(() => resultItems.value.length)

/** 相似度矩阵（行/列均按原始行号标注）：对角线恒 1（同向量余弦为 1，直接取整避免浮点尾差破坏 1.000 显示）。 */
const matrixRows = computed(() => resultItems.value.map((rowItem) => ({
  index: rowItem.index,
  cells: resultItems.value.map((colItem) => ({
    index: colItem.index,
    score: rowItem.index === colItem.index ? 1 : cosineSimilarity(rowItem.vector, colItem.vector)
  }))
})))

onMounted(() => {
  void loadCatalog()
})

/** 多行 textarea 按行拆分：trim 后过滤空行（后端逐条 trim 校验，前端同口径）。 */
function splitInputLines(value: string): string[] {
  return value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean)
}

/** 余弦相似度 dot/(|a||b|)；零向量防护按 0 处理（§4.3）。 */
function cosineSimilarity(a: number[], b: number[]): number {
  let dot = 0
  let normA = 0
  let normB = 0
  for (let i = 0; i < a.length; i += 1) {
    dot += a[i] * b[i]
    normA += a[i] * a[i]
    normB += b[i] * b[i]
  }
  if (!normA || !normB) return 0
  return dot / (Math.sqrt(normA) * Math.sqrt(normB))
}

/**
 * 热力色阶：分值 0→1 映射白到深蓝（hsl 212，亮度 96%→42%）；负分值夹取 0，
 * 对角线 1.000 满色。亮度 ≤60% 起换白字，浅色格保持深字，保证三位小数可读。
 */
function matrixCellStyle(score: number): Record<string, string> {
  const t = Math.max(0, Math.min(1, score))
  const lightness = 96 - 54 * t
  return {
    background: `hsl(212, 84%, ${lightness}%)`,
    color: lightness <= 60 ? '#fff' : 'rgba(34, 40, 43, 0.88)'
  }
}

/** 矩阵单元格 hover 提示：两个文本摘要（截断 24 字）。 */
function matrixTooltip(rowIndex: number, colIndex: number): string {
  const rowText = truncateText(submittedInputs.value[rowIndex] ?? `行 ${rowIndex + 1}`, 24)
  const colText = truncateText(submittedInputs.value[colIndex] ?? `行 ${colIndex + 1}`, 24)
  return `行 ${rowIndex + 1}「${rowText}」 × 行 ${colIndex + 1}「${colText}」`
}

function truncateText(text: string, max: number): string {
  return text.length > max ? `${text.slice(0, max)}…` : text
}

function vectorPreview(vector: number[]): string {
  const head = vector.slice(0, 8).map((value) => value.toFixed(4)).join(', ')
  return vector.length > 8 ? `[${head}, …]` : `[${head}]`
}

async function generate(): Promise<void> {
  const lines = splitInputLines(inputText.value)
  if (!lines.length) {
    message.warning('请输入至少一条文本')
    return
  }
  if (lines.length > maxInputCount) {
    message.warning(`每轮最多 ${maxInputCount} 条文本`)
    return
  }
  if (!apiKeyId.value || !modelId.value) {
    message.warning('请先选择 API Key 与模型')
    return
  }
  generating.value = true
  const startedAt = performance.now()
  try {
    const result = await api.playground.embeddings({
      apiKeyId: apiKeyId.value,
      modelId: modelId.value,
      input: lines,
      dimensions: dimensions.value || undefined
    })
    submittedInputs.value = lines
    resultItems.value = [...result.items].sort((a, b) => a.index - b.index)
    resultModel.value = result.model
    resultUsage.value = result.usage
    elapsedMs.value = Math.round(performance.now() - startedAt)
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '向量生成失败，请稍后重试'))
  } finally {
    generating.value = false
  }
}

async function copyVector(item: PlaygroundEmbeddingItem): Promise<void> {
  try {
    await navigator.clipboard.writeText(JSON.stringify(item.vector))
    message.success('已复制向量 JSON')
  } catch (error) {
    console.error(error)
    message.error('复制失败，请重试')
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

.playground-advanced-collapse {
  margin-bottom: 12px;
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

.playground-matrix-section {
  padding: 12px 14px;
  border: 1px solid var(--juhe-border);
  border-radius: 8px;
  background: var(--juhe-surface-soft);
}

.playground-section-head {
  display: flex;
  align-items: baseline;
  gap: 12px;
  margin-bottom: 10px;
}

.playground-section-title {
  color: var(--juhe-fg);
  font-size: 14px;
  font-weight: 600;
}

.playground-section-hint {
  color: var(--juhe-muted);
  font-size: 12px;
}

.playground-matrix-scroll {
  overflow-x: auto;
}

.playground-matrix-grid {
  display: grid;
  grid-template-columns: 40px repeat(var(--matrix-columns), minmax(64px, 1fr));
  gap: 3px;
  min-width: max-content;
}

.playground-matrix-corner,
.playground-matrix-head {
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--juhe-muted);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}

.playground-matrix-cell {
  min-height: 36px;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 2px 4px;
  border-radius: 4px;
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  cursor: default;
}

.playground-vector-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.playground-vector-card {
  padding: 10px 12px;
  border: 1px solid var(--juhe-border);
  border-radius: 8px;
  background: var(--juhe-surface-soft);
}

.playground-vector-head {
  display: flex;
  align-items: center;
  gap: 10px;
}

.playground-vector-line {
  flex-shrink: 0;
  color: var(--juhe-muted);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}

.playground-vector-text {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--juhe-fg);
  font-size: 13px;
}

.playground-vector-dim {
  flex-shrink: 0;
  margin-inline-end: 0;
}

.playground-vector-preview {
  margin-top: 6px;
  color: var(--juhe-muted);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  word-break: break-all;
}

@media (max-width: 992px) {
  .playground-media-layout {
    grid-template-columns: 1fr;
  }
}
</style>

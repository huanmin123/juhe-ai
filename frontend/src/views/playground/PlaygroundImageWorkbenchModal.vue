<template>
  <a-modal
    :open="open"
    title="生成设置"
    :width="800"
    :confirm-loading="creating"
    :ok-text="initialJob ? '重新生成' : '生成'"
    cancel-text="取消"
    :mask-closable="false"
    :ok-button-props="{ disabled: !canCreate }"
    @update:open="handleOpenChange"
    @ok="createJob"
  >
    <a-form layout="vertical" class="workbench-form">
      <a-form-item label="提示词" required>
        <a-textarea
          v-model:value="prompt"
          :rows="4"
          :maxlength="4000"
          placeholder="描述画面主体、环境、构图、光线、材质与需要保留的文字…"
        />
        <div class="workbench-char-count">{{ prompt.length }}/4000</div>
      </a-form-item>

      <a-form-item label="参考图片（可选，最多 10 张）">
        <!-- 已上传参考图缩略墙:右上角圆形移除钮。 -->
        <div v-if="references.length || uploadingCount > 0" class="workbench-references">
          <div v-for="reference in references" :key="reference.id" class="workbench-reference-item">
            <img class="workbench-reference-thumb" :src="reference.url" alt="参考图预览" />
            <button
              type="button"
              class="workbench-reference-remove"
              aria-label="移除参考图"
              @click="removeReference(reference.id)"
            >
              ×
            </button>
          </div>
          <div v-if="uploadingCount > 0" class="workbench-reference-item is-uploading">
            <a-spin size="small" />
          </div>
        </div>
        <!-- 回填历史卡片时参考图字节已随 7 天保留期删除（§3.8）：占位提示,不阻断重新生成。 -->
        <div v-if="deletedReferenceCount > 0" class="workbench-reference-deleted">参考图已删除</div>
        <a-upload-dragger
          v-if="canUploadMore"
          accept="image/png,image/jpeg,image/webp"
          multiple
          :show-upload-list="false"
          :disabled="uploadingCount > 0"
          :before-upload="handleReferenceUpload"
        >
          <p class="workbench-drag-icon"><PictureOutlined /></p>
          <p class="workbench-drag-text">上传或拖入参考图片</p>
          <p class="workbench-drag-hint">支持 PNG / JPEG / WEBP，单张不超过 10MB</p>
        </a-upload-dragger>
      </a-form-item>

      <div class="workbench-two-column">
        <a-form-item label="API Key" required>
          <a-select
            v-model:value="apiKeyId"
            :options="keyOptions"
            :loading="keysLoading"
            allow-clear
            show-search
            option-filter-prop="label"
            :placeholder="originalKeyDeleted ? '原 Key 已删除，请重新选择' : '选择要试玩的 API Key'"
            @change="handleKeyChange"
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
            :placeholder="apiKeyId ? '选择生图模型' : '先选择 API Key'"
          />
        </a-form-item>
      </div>

      <div class="workbench-two-column">
        <div class="workbench-param-group" role="radiogroup" aria-label="画面比例">
          <div class="workbench-param-label">画面比例</div>
          <div class="workbench-param-options">
            <button
              v-for="opt in aspectRatioOptions"
              :key="opt.value"
              type="button"
              class="workbench-param-option"
              :class="{ 'is-active': aspectRatio === opt.value }"
              role="radio"
              :aria-checked="aspectRatio === opt.value"
              @click="aspectRatio = opt.value"
            >
              <span class="workbench-param-shape" :class="`is-${opt.shape}`" aria-hidden="true" />
              <span>{{ opt.label }}</span>
            </button>
          </div>
        </div>
        <div class="workbench-param-group" role="radiogroup" aria-label="分辨率">
          <div class="workbench-param-label">分辨率</div>
          <div class="workbench-param-options">
            <button
              v-for="opt in resolutionOptions"
              :key="opt.value"
              type="button"
              class="workbench-param-option"
              :class="{ 'is-active': resolution === opt.value }"
              role="radio"
              :aria-checked="resolution === opt.value"
              @click="resolution = opt.value"
            >
              <span>{{ opt.label }}</span>
            </button>
          </div>
        </div>
      </div>
      <div class="workbench-two-column">
        <div class="workbench-param-group" role="radiogroup" aria-label="生成质量">
          <div class="workbench-param-label">生成质量</div>
          <div class="workbench-param-options">
            <button
              v-for="opt in qualityOptions"
              :key="opt.value"
              type="button"
              class="workbench-param-option"
              :class="{ 'is-active': quality === opt.value }"
              role="radio"
              :aria-checked="quality === opt.value"
              @click="quality = opt.value"
            >
              <span>{{ opt.label }}</span>
            </button>
          </div>
        </div>
        <div class="workbench-param-group" role="radiogroup" aria-label="输出格式">
          <div class="workbench-param-label">输出格式</div>
          <div class="workbench-param-options">
            <button
              v-for="opt in outputFormatOptions"
              :key="opt.value"
              type="button"
              class="workbench-param-option"
              :class="{ 'is-active': outputFormat === opt.value }"
              role="radio"
              :aria-checked="outputFormat === opt.value"
              @click="outputFormat = opt.value"
            >
              <span>{{ opt.label }}</span>
            </button>
          </div>
        </div>
      </div>
      <div class="workbench-field-hint">比例与分辨率为近似映射，最终以上游返回结果为准。</div>

      <div v-if="keysLoadError || modelsLoadError" class="workbench-form-error">{{ keysLoadError || modelsLoadError }}</div>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { PictureOutlined } from '@ant-design/icons-vue'
import { computed, ref, watch } from 'vue'
import { message } from '@/lib/antd'

import { api } from '@/api/client'
import { extractApiErrorMessage } from '@/shared/apiError'
import type {
  PlaygroundImageArtifactRef,
  PlaygroundImageAspectRatio,
  PlaygroundImageJob,
  PlaygroundImageJobCreateResult,
  PlaygroundImageOutputFormat,
  PlaygroundImageQuality,
  PlaygroundImageResolution
} from '@/types/domain/playground'
import { firstModelIdOf, usePlaygroundCatalog } from './usePlaygroundCatalog'

// 生成设置弹窗（演练场设计 §4.2 v1.3）：提示词 + 参考图拖拽上传（≤10 张,选中即逐张
// 上传）+ Key→模型两级联动（2026-11-22 Key 化）+ 参数四项（必选,固定默认 16:9/1K/标准/
// PNG,无「默认」档,恒传后端）+ 块状参数选择器;演练场不展示费用（v1.3 裁决,2026-11-22
// Key 化后继续有效）。创建成功即关弹窗,由父页面在历史网格顶部插入「生成中」占位卡片并启动轮询。

const maxReferences = 10
const maxReferenceBytes = 10 * 1024 * 1024

const props = defineProps<{
  open: boolean
  /** 快速入口带入的提示词（无历史回填时使用）。 */
  initialPrompt?: string
  /** 卡片回填来源：携带全部历史配置;queued/running 卡片不打开弹窗。 */
  initialJob?: PlaygroundImageJob | null
}>()

const emit = defineEmits<{
  (event: 'update:open', value: boolean): void
  (event: 'created', result: PlaygroundImageJobCreateResult): void
}>()

const {
  keys,
  keysLoading,
  keysLoadError,
  modelsLoading,
  modelsLoadError,
  apiKeyId,
  modelId,
  models,
  keyOptions,
  modelOptions,
  handleApiKeyChange,
  loadModelsForKey,
  loadCatalog
} = usePlaygroundCatalog('image')

const prompt = ref('')
const references = ref<PlaygroundImageArtifactRef[]>([])
/** 回填任务中参考图字节已到期删除的数量（§3.8 结果图与参考图一并 7 天）,仅作占位展示。 */
const deletedReferenceCount = ref(0)
const uploadingCount = ref(0)
const creating = ref(false)
/** 卡片回填时原 Key 已不在本人 active 列表（已删除/非本人）：占位提示并要求重选（§4.2），不阻断其他项回填。 */
const originalKeyDeleted = ref(false)

/** Key 下拉 change：先清「原 Key 已删除」占位态，再走联动重置。 */
function handleKeyChange(): void {
  originalKeyDeleted.value = false
  handleApiKeyChange()
}

// 参数四项必选（v1.3 无「默认」档）：固定默认 16:9 / 1K / 标准 / PNG,创建时恒传。
const aspectRatio = ref<PlaygroundImageAspectRatio>('16:9')
const resolution = ref<PlaygroundImageResolution>('1k')
const quality = ref<PlaygroundImageQuality>('standard')
const outputFormat = ref<PlaygroundImageOutputFormat>('png')

const aspectRatioOptions: Array<{ label: string; value: PlaygroundImageAspectRatio; shape: string }> = [
  { label: '16:9', value: '16:9', shape: '16-9' },
  { label: '9:16', value: '9:16', shape: '9-16' },
  { label: '1:1', value: '1:1', shape: '1-1' }
]
const resolutionOptions: Array<{ label: string; value: PlaygroundImageResolution }> = [
  { label: '1K', value: '1k' },
  { label: '2K', value: '2k' },
  { label: '4K', value: '4k' }
]
const qualityOptions: Array<{ label: string; value: PlaygroundImageQuality }> = [
  { label: '标准', value: 'standard' },
  { label: '精细', value: 'fine' }
]
const outputFormatOptions: Array<{ label: string; value: PlaygroundImageOutputFormat }> = [
  { label: 'PNG', value: 'png' },
  { label: 'JPEG', value: 'jpeg' },
  { label: 'WEBP', value: 'webp' }
]

const canUploadMore = computed(() => references.value.length + uploadingCount.value < maxReferences)

const canCreate = computed(() => Boolean(
  prompt.value.trim()
  && apiKeyId.value
  && modelId.value
  && uploadingCount.value === 0
  && !creating.value
))

watch(() => props.open, (open) => {
  if (!open) return
  void resetForOpen()
})

/** 打开序：清空上一轮状态并落参数默认值 → 带入快速入口提示词 → 加载 Key/模型目录 → 回填历史卡片配置（Key/模型以卡片 apiKeyId 为准，§4.2）。 */
async function resetForOpen(): Promise<void> {
  creating.value = false
  uploadingCount.value = 0
  references.value = []
  deletedReferenceCount.value = 0
  originalKeyDeleted.value = false
  aspectRatio.value = '16:9'
  resolution.value = '1k'
  quality.value = 'standard'
  outputFormat.value = 'png'
  prompt.value = props.initialPrompt ?? ''
  const job = props.initialJob
  await loadCatalog()
  if (!job) return
  prompt.value = job.prompt
  // Key/模型以卡片 apiKeyId 回填；该 Key 已删除或不在本人列表时占位「原 Key 已删除，
  // 请重新选择」并要求重选（清空当前/默认选择），不阻断提示词/参数/参考图回填。
  if (keys.value.some((key) => key.id === job.apiKeyId)) {
    apiKeyId.value = job.apiKeyId
    const kindModels = await loadModelsForKey(job.apiKeyId)
    modelId.value = kindModels.some((model) => model.modelId === job.model)
      ? job.model
      : firstModelIdOf(kindModels)
  } else {
    originalKeyDeleted.value = true
    apiKeyId.value = undefined
    modelId.value = undefined
    models.value = []
  }
  // 旧任务（默认档时代）缺参时落当前固定默认值,保证四项恒有选中。
  aspectRatio.value = job.aspectRatio ?? '16:9'
  resolution.value = job.resolution ?? '1k'
  quality.value = job.quality ?? 'standard'
  outputFormat.value = job.outputFormat ?? 'png'
  references.value = [...job.references]
  deletedReferenceCount.value = Math.max(0, job.referenceCount - job.references.length)
}

function handleOpenChange(value: boolean): void {
  emit('update:open', value)
}

/** a-upload-dragger beforeUpload：本地校验通过后自行上传（返回 false 阻断 antd 自动上传）;失败 toast 不入列表。 */
function handleReferenceUpload(file: File): boolean {
  if (references.value.length + uploadingCount.value >= maxReferences) {
    message.warning(`参考图最多 ${maxReferences} 张`)
    return false
  }
  if (file.size > maxReferenceBytes) {
    message.warning('单张参考图不能超过 10MB')
    return false
  }
  const mimeOk = file.type
    ? ['image/png', 'image/jpeg', 'image/webp'].includes(file.type)
    : /\.(png|jpe?g|webp)$/i.test(file.name)
  if (!mimeOk) {
    message.warning('仅支持 PNG / JPEG / WEBP 格式的参考图')
    return false
  }
  void uploadReference(file)
  return false
}

async function uploadReference(file: File): Promise<void> {
  uploadingCount.value += 1
  try {
    const dataBase64 = await readAsBase64(file)
    const reference = await api.playground.imagesReferences({ dataBase64 })
    references.value.push(reference)
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '参考图上传失败，请重试'))
  } finally {
    uploadingCount.value -= 1
  }
}

/** FileReader 读为纯 base64（剥 data: 前缀,契约 §3.4.1 要求无前缀）。 */
function readAsBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      if (typeof reader.result !== 'string') {
        reject(new Error('读取参考图失败'))
        return
      }
      const separator = reader.result.indexOf(',')
      resolve(separator >= 0 ? reader.result.slice(separator + 1) : reader.result)
    }
    reader.onerror = () => reject(reader.error ?? new Error('读取参考图失败'))
    reader.readAsDataURL(file)
  })
}

function removeReference(id: string): void {
  references.value = references.value.filter((reference) => reference.id !== id)
}

async function createJob(): Promise<void> {
  const content = prompt.value.trim()
  if (!content) {
    message.warning('请输入提示词')
    return
  }
  if (!apiKeyId.value || !modelId.value) {
    message.warning('请先选择 API Key 与模型')
    return
  }
  creating.value = true
  try {
    const result = await api.playground.imagesCreateJob({
      apiKeyId: apiKeyId.value,
      modelId: modelId.value,
      prompt: content,
      aspectRatio: aspectRatio.value,
      resolution: resolution.value,
      quality: quality.value,
      outputFormat: outputFormat.value,
      referenceIds: references.value.map((reference) => reference.id)
    })
    emit('created', result)
    emit('update:open', false)
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '创建生成任务失败，请稍后重试'))
  } finally {
    creating.value = false
  }
}
</script>

<style scoped>
.workbench-form {
  margin-top: 4px;
}

.workbench-char-count {
  margin-top: 4px;
  text-align: right;
  color: var(--juhe-faint);
  font-size: 12px;
}

/* 已上传参考图缩略墙：64px 方块 + 右上角圆形移除钮;上传中占位同尺寸。 */
.workbench-references {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 8px;
}

.workbench-reference-item {
  position: relative;
  width: 64px;
  height: 64px;
}

.workbench-reference-thumb {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: cover;
  border: 1px solid var(--juhe-border);
  border-radius: 8px;
}

.workbench-reference-remove {
  position: absolute;
  top: -6px;
  right: -6px;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  padding: 0;
  color: var(--juhe-muted);
  font-size: 14px;
  line-height: 1;
  cursor: pointer;
  background: var(--juhe-surface);
  border: 1px solid var(--juhe-border);
  border-radius: 999px;
}

.workbench-reference-remove:hover {
  color: var(--juhe-danger);
  border-color: var(--juhe-danger-line);
}

.workbench-reference-item.is-uploading {
  display: flex;
  align-items: center;
  justify-content: center;
  border: 1px dashed var(--juhe-border);
  border-radius: 8px;
}

.workbench-reference-deleted {
  margin-bottom: 8px;
  padding: 4px 10px;
  color: var(--juhe-muted);
  font-size: 12px;
  border: 1px dashed var(--juhe-border);
  border-radius: 6px;
}

/* 拖拽上传区（v1.3 §4.2）：大虚线区域 + 图片图标 + 主/副文案。 */
.workbench-drag-icon {
  margin-bottom: 4px;
  color: var(--juhe-accent-mid);
  font-size: 26px;
  line-height: 1;
}

.workbench-drag-text {
  margin: 0;
  color: var(--juhe-muted);
  font-size: 13px;
}

.workbench-drag-hint {
  margin: 2px 0 0;
  color: var(--juhe-faint);
  font-size: 12px;
}

.workbench-two-column {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}

/* 块状参数选择器（v1.3 §4.2）：无「默认」档,必选其一;选中态 accent 强调。
   比例选项带 CSS 画的小形状（横/竖/方）。 */
.workbench-param-group {
  margin-bottom: 12px;
}

.workbench-param-label {
  margin-bottom: 8px;
  font-size: 13px;
}

.workbench-param-options {
  display: flex;
  gap: 8px;
}

.workbench-param-option {
  display: flex;
  flex: 1 1 0;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 9px 6px;
  color: var(--juhe-muted);
  font: inherit;
  font-size: 13px;
  cursor: pointer;
  background: var(--juhe-surface);
  border: 1px solid var(--juhe-border);
  border-radius: 8px;
  transition: border-color 0.15s ease, background 0.15s ease, color 0.15s ease;
}

.workbench-param-option:hover {
  border-color: var(--juhe-accent-mid);
}

.workbench-param-option.is-active {
  color: var(--juhe-accent);
  font-weight: 600;
  background: var(--juhe-accent-soft);
  border-color: var(--juhe-accent);
}

.workbench-param-shape {
  display: inline-block;
  border: 1.5px solid currentColor;
  border-radius: 2px;
}

.workbench-param-shape.is-16-9 {
  width: 18px;
  height: 10px;
}

.workbench-param-shape.is-9-16 {
  width: 10px;
  height: 18px;
}

.workbench-param-shape.is-1-1 {
  width: 13px;
  height: 13px;
}

.workbench-field-hint {
  margin-top: -2px;
  margin-bottom: 12px;
  color: var(--juhe-faint);
  font-size: 12px;
}

.workbench-form-error {
  margin-top: 12px;
  color: var(--juhe-danger);
  font-size: 12px;
}
</style>

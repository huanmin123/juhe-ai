<script setup lang="ts">
import { computed, ref } from 'vue'

import { studioAssetContentUrl } from '@/api/domains/studio'
import { extractApiErrorMessage } from '@/shared/apiError'
import { message } from '@/lib/antd'
import type { StudioAccountOption, StudioModelOption } from '@/types/domain'
import { isStudioAccountDispatchable, studioAccountStatusLabel } from '@/types/domain'
import { useStudioUpload } from '../shared/useStudioUpload'
import {
  studioCapabilityFlag,
  studioCapabilityList
} from '../shared/studioCapabilities'
import { studioImageCostEstimate } from './studioImageDisplay'
import { studioImageMaxUploadBytes } from './studioImageOptions'
import StudioCostEstimate from '../shared/StudioCostEstimate.vue'
import type { StudioImageParamsSnapshot } from './studioImageDisplay'

/**
 * 图像参数面板（规划 F2.1）：能力载荷驱动表单——模型目录 capabilities 未声明
 * 的键不渲染（负面提示词/尺寸/参考图），不硬编码参数项（§8 强制点 1）。
 */
const props = defineProps<{
  accounts: StudioAccountOption[]
  models: StudioModelOption[]
  loadingAccounts: boolean
  loadingModels: boolean
  generating: boolean
}>()

const emit = defineEmits<{
  'load-models': []
  generate: [payload: {
    accountId: string
    model: string
    prompt: string
    negativePrompt?: string
    size?: string
    referenceAssetId?: string
  }]
}>()

const accountId = ref<string>()
const model = ref<string>()
const prompt = ref('')
const negativePrompt = ref('')
const size = ref<string>()
const referenceAssetId = ref<string>()
const referencePreviewUrl = ref('')

const selectedModel = computed(() => props.models.find((item) => item.model === model.value))
const sizes = computed(() => studioCapabilityList(selectedModel.value?.capabilities, 'sizes'))
const supportsNegativePrompt = computed(() => studioCapabilityFlag(selectedModel.value?.capabilities, 'supportsNegativePrompt'))
const supportsReferenceImage = computed(() => studioCapabilityFlag(selectedModel.value?.capabilities, 'supportsReferenceImage'))
const costEstimate = computed(() => studioImageCostEstimate(selectedModel.value))
const canGenerate = computed(() => Boolean(accountId.value && model.value && prompt.value.trim()) && !props.generating)

const upload = useStudioUpload()
const uploadInput = ref<HTMLInputElement>()

function openUpload(): void {
  uploadInput.value?.click()
}

async function handleReferenceFile(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  if (file.size > studioImageMaxUploadBytes) {
    message.error('参考图不能超过 25MB')
    return
  }
  try {
    const asset = await upload.upload(file, { source: 'image-reference' })
    if (!asset) return // 已取消：不产生占位
    referenceAssetId.value = asset.id
    referencePreviewUrl.value = studioAssetContentUrl(asset.id)
  } catch (error) {
    message.error(extractApiErrorMessage(error, '参考图上传失败'))
  }
}

function removeReference(): void {
  upload.cancel()
  referenceAssetId.value = undefined
  referencePreviewUrl.value = ''
}

function submit(): void {
  if (!accountId.value || !model.value || !prompt.value.trim()) return
  emit('generate', {
    accountId: accountId.value,
    model: model.value,
    prompt: prompt.value.trim(),
    ...(supportsNegativePrompt.value && negativePrompt.value.trim() ? { negativePrompt: negativePrompt.value.trim() } : {}),
    ...(size.value ? { size: size.value } : {}),
    ...(referenceAssetId.value ? { referenceAssetId: referenceAssetId.value } : {})
  })
}

/** 画廊"回填参数"：把作品参数快照写回表单（缺失字段保持当前值清空语义由调用方决定）。 */
function applyParamsSnapshot(snapshot: StudioImageParamsSnapshot): void {
  if (snapshot.accountId) accountId.value = snapshot.accountId
  if (snapshot.model) model.value = snapshot.model
  prompt.value = snapshot.prompt ?? ''
  negativePrompt.value = snapshot.negativePrompt ?? ''
  size.value = snapshot.size
  referenceAssetId.value = snapshot.referenceAssetId
  referencePreviewUrl.value = snapshot.referenceAssetId ? studioAssetContentUrl(snapshot.referenceAssetId) : ''
}

defineExpose({ applyParamsSnapshot })

defineOptions({ name: 'ImageParamsPanel' })
</script>

<template>
  <div class="image-params-panel">
    <a-form layout="vertical">
      <a-form-item label="执行账户" required>
        <a-select
          v-model:value="accountId"
          placeholder="选择账户"
          :loading="loadingAccounts"
          :options="accounts.map((account) => ({
            value: account.accountId,
            label: `${account.name}（${account.providerCode} · ${studioAccountStatusLabel(account.status)}）`
          }))"
        />
        <div class="image-params-panel__hint">含待检测/停用状态账户；可派发性由受理结果裁决。</div>
      </a-form-item>

      <a-form-item label="模型" required>
        <a-select
          v-model:value="model"
          placeholder="打开下拉按需加载模型目录"
          :loading="loadingModels"
          :options="models.map((item) => ({ value: item.model, label: `${item.model}（${item.providerCode}）` }))"
          @dropdown-visible-change="(open: boolean) => { if (open) emit('load-models') }"
        />
      </a-form-item>

      <a-form-item label="提示词" required>
        <a-textarea
          v-model:value="prompt"
          placeholder="描述想生成的画面，例如：清晨的山谷云海，光线柔和"
          :rows="4"
          :maxlength="4000"
          show-count
        />
      </a-form-item>

      <!-- 能力载荷驱动：supportsNegativePrompt 未声明不渲染 -->
      <a-form-item v-if="supportsNegativePrompt" label="负面提示词">
        <a-textarea
          v-model:value="negativePrompt"
          placeholder="不希望出现的元素"
          :rows="2"
          :maxlength="2000"
        />
      </a-form-item>

      <!-- 能力载荷驱动：sizes 未声明不渲染 -->
      <a-form-item v-if="sizes.length > 0" label="尺寸">
        <a-select
          v-model:value="size"
          placeholder="跟随模型默认"
          allow-clear
          :options="sizes.map((item) => ({ value: item, label: item }))"
        />
      </a-form-item>

      <!-- 能力载荷驱动：supportsReferenceImage 未声明不渲染（图生图走 /v1/images/edits 形态） -->
      <a-form-item v-if="supportsReferenceImage" label="参考图">
        <div v-if="referenceAssetId" class="image-params-panel__reference">
          <img :src="referencePreviewUrl" alt="参考图" />
          <a-button size="small" danger @click="removeReference">移除</a-button>
        </div>
        <div v-else class="image-params-panel__reference-empty">
          <a-button size="small" :loading="upload.uploading.value" @click="openUpload">上传参考图</a-button>
          <span v-if="upload.uploading.value">上传中 {{ upload.progress.value }}%</span>
          <input
            ref="uploadInput"
            type="file"
            accept="image/png,image/jpeg,image/webp"
            style="display: none"
            @change="handleReferenceFile"
          />
        </div>
      </a-form-item>

      <StudioCostEstimate :estimate="costEstimate" />

      <a-button
        type="primary"
        block
        :loading="generating"
        :disabled="!canGenerate"
        @click="submit"
      >生成</a-button>
      <div class="image-params-panel__hint image-params-panel__hint--center">首版单张生成；任务受理后进入右侧队列。</div>
    </a-form>
  </div>
</template>

<style scoped>
.image-params-panel {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.image-params-panel__hint {
  font-size: 12px;
  color: var(--juhe-text-tertiary, #9ca3af);
  margin-top: 4px;
}

.image-params-panel__hint--center {
  text-align: center;
}

.image-params-panel__reference {
  display: flex;
  align-items: center;
  gap: 12px;
}

.image-params-panel__reference img {
  width: 72px;
  height: 72px;
  object-fit: cover;
  border-radius: 6px;
  border: 1px solid var(--juhe-border-color, #e5e7eb);
}

.image-params-panel__reference-empty {
  display: flex;
  align-items: center;
  gap: 12px;
}
</style>

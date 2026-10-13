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
  studioCapabilityList,
  studioCapabilityScalarList
} from '../shared/studioCapabilities'
import StudioCostEstimate from '../shared/StudioCostEstimate.vue'
import { formatSecondsOption, studioVideoCostEstimate } from './studioVideoDisplay'
import { studioVideoFirstFrameMaxBytes } from './studioVideoOptions'

/**
 * 单镜快造表单（规划 F2.3）：模型/提示词/首帧上传（supportsFirstFrame 按能力
 * 显示）/时长与画幅（能力载荷档位，缺项不渲染）+ 成本预估 + 生成按钮。
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
    firstFrameAssetId?: string
    seconds?: string | number
    size?: string
  }]
}>()

const accountId = ref<string>()
const model = ref<string>()
const prompt = ref('')
const seconds = ref<string | number>()
const size = ref<string>()
const firstFrameAssetId = ref<string>()

const selectedModel = computed(() => props.models.find((item) => item.model === model.value))
const secondsOptions = computed(() => studioCapabilityScalarList(selectedModel.value?.capabilities, 'secondsOptions'))
const sizeOptions = computed(() => studioCapabilityList(selectedModel.value?.capabilities, 'sizeOptions'))
const supportsFirstFrame = computed(() => studioCapabilityFlag(selectedModel.value?.capabilities, 'supportsFirstFrame'))
const costEstimate = computed(() => studioVideoCostEstimate(selectedModel.value, seconds.value))
const canGenerate = computed(() => Boolean(accountId.value && model.value && prompt.value.trim()) && !props.generating)

const upload = useStudioUpload()
const uploadInput = ref<HTMLInputElement>()

function openUpload(): void {
  uploadInput.value?.click()
}

async function handleFirstFrame(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  if (file.size > studioVideoFirstFrameMaxBytes) {
    message.error('首帧图片不能超过 25MB')
    return
  }
  try {
    const asset = await upload.upload(file, { source: 'video-first-frame' })
    if (!asset) return
    firstFrameAssetId.value = asset.id
  } catch (error) {
    message.error(extractApiErrorMessage(error, '首帧上传失败'))
  }
}

function removeFirstFrame(): void {
  upload.cancel()
  firstFrameAssetId.value = undefined
}

function submit(): void {
  if (!accountId.value || !model.value || !prompt.value.trim()) return
  emit('generate', {
    accountId: accountId.value,
    model: model.value,
    prompt: prompt.value.trim(),
    ...(firstFrameAssetId.value ? { firstFrameAssetId: firstFrameAssetId.value } : {}),
    ...(seconds.value !== undefined ? { seconds: seconds.value } : {}),
    ...(size.value ? { size: size.value } : {})
  })
}

/** 失败任务重试：回填模型与提示词（换新幂等键重新发起由父级负责）。 */
function applyRetryDraft(draft: { model?: string; prompt?: string }): void {
  if (draft.model) model.value = draft.model
  if (draft.prompt) prompt.value = draft.prompt
}

defineExpose({ applyRetryDraft })

defineOptions({ name: 'VideoQuickForm' })
</script>

<template>
  <a-form layout="vertical">
    <div class="video-quick-form__row">
      <a-form-item label="执行账户" required class="video-quick-form__field">
        <a-select
          v-model:value="accountId"
          placeholder="选择账户"
          :loading="loadingAccounts"
          :options="accounts.map((account) => ({
            value: account.accountId,
            label: `${account.name}（${account.providerCode} · ${studioAccountStatusLabel(account.status)}${isStudioAccountDispatchable(account.status) ? '' : ' · 不可调度'}）`
          }))"
        />
      </a-form-item>
      <a-form-item label="模型" required class="video-quick-form__field">
        <a-select
          v-model:value="model"
          placeholder="打开下拉按需加载模型目录"
          :loading="loadingModels"
          :options="models.map((item) => ({ value: item.model, label: `${item.model}（${item.providerCode}）` }))"
          @dropdown-visible-change="(open: boolean) => { if (open) emit('load-models') }"
        />
      </a-form-item>
    </div>

    <a-form-item label="提示词" required>
      <a-textarea
        v-model:value="prompt"
        placeholder="描述镜头内容，例如：夕阳下的城市天际线，延时摄影"
        :rows="3"
        :maxlength="4000"
        show-count
      />
    </a-form-item>

    <div class="video-quick-form__row">
      <!-- 能力载荷驱动：supportsFirstFrame 未声明不显示首帧上传（图生视频） -->
      <a-form-item v-if="supportsFirstFrame" label="首帧图片" class="video-quick-form__field">
        <div class="video-quick-form__file">
          <template v-if="firstFrameAssetId">
            <img :src="studioAssetContentUrl(firstFrameAssetId)" alt="首帧" class="video-quick-form__thumb" />
            <a-button size="small" danger @click="removeFirstFrame">移除</a-button>
          </template>
          <template v-else>
            <a-button size="small" :loading="upload.uploading.value" @click="openUpload">上传首帧</a-button>
            <span v-if="upload.uploading.value">上传中 {{ upload.progress.value }}%</span>
          </template>
          <input
            ref="uploadInput"
            type="file"
            accept="image/png,image/jpeg,image/webp"
            style="display: none"
            @change="handleFirstFrame"
          />
        </div>
      </a-form-item>

      <!-- 能力载荷驱动：secondsOptions 未声明不渲染 -->
      <a-form-item v-if="secondsOptions.length > 0" label="时长" class="video-quick-form__field">
        <a-select
          v-model:value="seconds"
          placeholder="跟随模型默认"
          allow-clear
          :options="secondsOptions.map((item) => ({ value: item, label: formatSecondsOption(item) }))"
        />
      </a-form-item>

      <!-- 能力载荷驱动：sizeOptions 未声明不渲染 -->
      <a-form-item v-if="sizeOptions.length > 0" label="画幅" class="video-quick-form__field">
        <a-select
          v-model:value="size"
          placeholder="跟随模型默认"
          allow-clear
          :options="sizeOptions.map((item) => ({ value: item, label: item }))"
        />
      </a-form-item>
    </div>

    <div class="video-quick-form__footer">
      <StudioCostEstimate :estimate="costEstimate" />
      <a-button type="primary" :loading="generating" :disabled="!canGenerate" @click="submit">生成视频</a-button>
    </div>
  </a-form>
</template>

<style scoped>
.video-quick-form__row {
  display: flex;
  gap: 16px;
  flex-wrap: wrap;
}

.video-quick-form__field {
  min-width: 200px;
  flex: 1;
}

.video-quick-form__file {
  display: flex;
  align-items: center;
  gap: 12px;
}

.video-quick-form__thumb {
  width: 64px;
  height: 64px;
  object-fit: cover;
  border-radius: 6px;
  border: 1px solid var(--juhe-border-color, #e5e7eb);
}

.video-quick-form__footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
</style>

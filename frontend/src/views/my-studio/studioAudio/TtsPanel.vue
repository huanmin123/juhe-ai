<script setup lang="ts">
import { computed, ref } from 'vue'

import { extractApiErrorMessage } from '@/shared/apiError'
import { message } from '@/lib/antd'
import { studioApi, studioAssetContentUrl } from '@/api/domains/studio'
import type { StudioAccountOption, StudioAsset, StudioModelOption } from '@/types/domain'
import { isStudioAccountDispatchable, studioAccountStatusLabel } from '@/types/domain'
import {
  studioCapabilityList,
  studioCapabilityRange
} from '../shared/studioCapabilities'
import StudioCostEstimate from '../shared/StudioCostEstimate.vue'
import { AUDIO_SPEECH_ENDPOINT_MODE } from './studioAudioOptions'
import { splitStudioSpeechSegments, studioTtsCostEstimate } from './studioAudioDisplay'

/**
 * 文本转语音（规划 F2.2 TTS 页签）：文本 + 音色/语速/情感按能力载荷渲染
 * （缺项不渲染）；合成 = 同步请求，响应返回即已入素材库，无"保存"开关
 * （决策 #9③）；长文本按空行分段逐段调用（§4.3 端点不聚合）。
 */
const props = defineProps<{
  accounts: StudioAccountOption[]
  models: StudioModelOption[]
  loadingAccounts: boolean
  loadingModels: boolean
}>()

const emit = defineEmits<{
  'load-models': []
  created: [asset: StudioAsset]
}>()

const accountId = ref<string>()
const model = ref<string>()
const input = ref('')
const voice = ref<string>()
const speed = ref<number>()
const emotion = ref<string>()
const responseFormat = ref<string>()
const synthesizing = ref(false)
const segmentProgress = ref('')
const results = ref<StudioAsset[]>([])

const speechModels = computed(() => props.models.filter((item) => item.endpointModes?.includes(AUDIO_SPEECH_ENDPOINT_MODE)))
const selectedModel = computed(() => speechModels.value.find((item) => item.model === model.value))
const voices = computed(() => studioCapabilityList(selectedModel.value?.capabilities, 'voices'))
const responseFormats = computed(() => studioCapabilityList(selectedModel.value?.capabilities, 'responseFormats'))
const speedRange = computed(() => studioCapabilityRange(selectedModel.value?.capabilities, 'speedRange'))
const emotionOptions = computed(() => studioCapabilityList(selectedModel.value?.capabilities, 'emotionOptions'))
const costEstimate = computed(() => studioTtsCostEstimate(selectedModel.value))
// voices[] 非空时音色必填（§4.3）。
const canSynthesize = computed(() =>
  Boolean(accountId.value && model.value && input.value.trim() && (!voices.value.length || voice.value))
  && !synthesizing.value)

async function synthesize(): Promise<void> {
  if (!accountId.value || !model.value || !input.value.trim()) return
  const segments = splitStudioSpeechSegments(input.value)
  if (segments.length === 0) return
  synthesizing.value = true
  results.value = []
  try {
    for (let index = 0; index < segments.length; index++) {
      segmentProgress.value = segments.length > 1 ? `第 ${index + 1}/${segments.length} 段` : ''
      const asset = await studioApi.generateSpeech({
        accountId: accountId.value,
        model: model.value,
        input: segments[index],
        ...(voices.value.length && voice.value ? { voice: voice.value } : {}),
        ...(speed.value !== undefined && speedRange.value ? { speed: speed.value } : {}),
        ...(emotionOptions.value.length && emotion.value ? { emotion: emotion.value } : {}),
        ...(responseFormats.value.length && responseFormat.value ? { responseFormat: responseFormat.value } : {}),
        // 每段独立幂等键：重放返回原资产，不重复计费（§3.3 收据）。
        clientOperationId: crypto.randomUUID()
      })
      results.value.push(asset)
      emit('created', asset)
    }
    message.success('合成完成，结果已保存到素材库')
    input.value = ''
  } catch (error) {
    message.error(extractApiErrorMessage(error, '语音合成失败'))
  } finally {
    synthesizing.value = false
    segmentProgress.value = ''
  }
}
</script>

<template>
  <div class="tts-panel">
    <a-form layout="vertical">
      <div class="tts-panel__row">
        <a-form-item label="执行账户" required class="tts-panel__field">
          <a-select
            v-model:value="accountId"
            placeholder="选择账户"
            :loading="loadingAccounts"
            :options="accounts.map((account) => ({
              value: account.accountId,
              label: `${account.name}（${account.providerCode} · ${studioAccountStatusLabel(account.status)}）`
            }))"
          />
        </a-form-item>
        <a-form-item label="模型" required class="tts-panel__field">
          <a-select
            v-model:value="model"
            placeholder="打开下拉按需加载模型目录"
            :loading="loadingModels"
            :options="speechModels.map((item) => ({ value: item.model, label: `${item.model}（${item.providerCode}）` }))"
            @dropdown-visible-change="(open: boolean) => { if (open) emit('load-models') }"
          />
        </a-form-item>
      </div>

      <a-form-item label="合成文本" required>
        <a-textarea
          v-model:value="input"
          placeholder="输入要合成的文本；多个段落用空行分隔，将逐段合成"
          :rows="6"
          :maxlength="20000"
          show-count
        />
      </a-form-item>

      <div class="tts-panel__row">
        <!-- 能力载荷驱动：voices 未声明不渲染；非空时必填 -->
        <a-form-item v-if="voices.length > 0" label="音色" :required="true" class="tts-panel__field">
          <a-select
            v-model:value="voice"
            placeholder="选择音色"
            :options="voices.map((item) => ({ value: item, label: item }))"
          />
        </a-form-item>

        <!-- 能力载荷驱动：speedRange 未声明不渲染 -->
        <a-form-item v-if="speedRange" :label="`语速（${speedRange[0]} - ${speedRange[1]}）`" class="tts-panel__field">
          <a-slider v-model:value="speed" :min="speedRange[0]" :max="speedRange[1]" :step="0.1" />
        </a-form-item>

        <!-- 能力载荷驱动：emotionOptions 未声明不渲染 -->
        <a-form-item v-if="emotionOptions.length > 0" label="情感" class="tts-panel__field">
          <a-select
            v-model:value="emotion"
            placeholder="跟随模型默认"
            allow-clear
            :options="emotionOptions.map((item) => ({ value: item, label: item }))"
          />
        </a-form-item>

        <!-- 能力载荷驱动：responseFormats 未声明不渲染 -->
        <a-form-item v-if="responseFormats.length > 0" label="输出格式" class="tts-panel__field">
          <a-select
            v-model:value="responseFormat"
            placeholder="跟随模型默认"
            allow-clear
            :options="responseFormats.map((item) => ({ value: item, label: item }))"
          />
        </a-form-item>
      </div>

      <StudioCostEstimate :estimate="costEstimate" />

      <a-button
        type="primary"
        :loading="synthesizing"
        :disabled="!canSynthesize"
        @click="synthesize"
      >合成{{ segmentProgress ? `（${segmentProgress}）` : '' }}</a-button>
      <span class="tts-panel__hint">响应返回即已入素材库，无需单独保存。</span>
    </a-form>

    <div v-if="results.length > 0" class="tts-panel__results">
      <div class="tts-panel__results-title">本次合成结果</div>
      <div v-for="asset in results" :key="asset.id" class="tts-panel__result">
        <audio controls preload="none" :src="studioAssetContentUrl(asset.id)" />
        <a :href="studioAssetContentUrl(asset.id, { download: true })">下载</a>
      </div>
    </div>
  </div>
</template>

<style scoped>
.tts-panel {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.tts-panel__row {
  display: flex;
  gap: 16px;
  flex-wrap: wrap;
}

.tts-panel__field {
  min-width: 220px;
  flex: 1;
}

.tts-panel__hint {
  font-size: 12px;
  color: var(--juhe-text-tertiary, #9ca3af);
  margin-left: 12px;
}

.tts-panel__results {
  margin-top: 8px;
  border-top: 1px solid var(--juhe-border-color, #e5e7eb);
  padding-top: 8px;
}

.tts-panel__results-title {
  font-size: 13px;
  color: var(--juhe-text-secondary, #6b7280);
  margin-bottom: 6px;
}

.tts-panel__result {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 6px;
}

.tts-panel__result audio {
  max-width: 420px;
  width: 100%;
  height: 36px;
}
</style>

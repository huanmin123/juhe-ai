<script setup lang="ts">
import axios from 'axios'
import { computed, ref } from 'vue'

import { extractApiErrorMessage } from '@/shared/apiError'
import { message } from '@/lib/antd'
import { copyTextToClipboard } from '@/shared/clipboard'
import { studioApi } from '@/api/domains/studio'
import type { StudioAccountOption, StudioModelOption, StudioTranscriptionResult } from '@/types/domain'
import { isStudioAccountDispatchable, studioAccountStatusLabel } from '@/types/domain'
import { AUDIO_TRANSCRIPTION_ENDPOINT_MODE } from './studioAudioOptions'
import { transcriptionHasSrt } from './studioAudioDisplay'

/**
 * 语音转文本（规划 F2.2 STT 页签）：上传音频 → 文本结果可复制；SRT 仅在
 * 上游返回时展示（§4.3：timestampCapable 不足时不出 SRT，不虚构时间轴）。
 */
const props = defineProps<{
  accounts: StudioAccountOption[]
  models: StudioModelOption[]
  loadingAccounts: boolean
  loadingModels: boolean
}>()

const accountId = ref<string>()
const model = ref<string>()
const language = ref('')
const selectedFile = ref<File>()
const result = ref<StudioTranscriptionResult>()
const transcribing = ref(false)
const progress = ref(0)
let transcribeController: AbortController | undefined

const emit = defineEmits<{
  'load-models': []
}>()

const transcriptionModels = computed(() => props.models.filter((item) => item.endpointModes?.includes(AUDIO_TRANSCRIPTION_ENDPOINT_MODE)))
const canTranscribe = computed(() => Boolean(accountId.value && model.value && selectedFile.value))

const uploadInput = ref<HTMLInputElement>()

function openUpload(): void {
  uploadInput.value?.click()
}

function handleFileChange(event: Event): void {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  if (file.size > 25 * 1024 * 1024) {
    message.error('音频文件不能超过 25MB')
    return
  }
  selectedFile.value = file
  result.value = undefined
}

function clearFile(): void {
  transcribeController?.abort()
  transcribeController = undefined
  selectedFile.value = undefined
}

async function transcribe(): Promise<void> {
  if (!accountId.value || !model.value || !selectedFile.value) return
  transcribeController = new AbortController()
  transcribing.value = true
  progress.value = 0
  try {
    const response = await studioApi.generateTranscription(
      selectedFile.value,
      {
        accountId: accountId.value,
        model: model.value,
        clientOperationId: crypto.randomUUID(),
        ...(language.value.trim() ? { language: language.value.trim() } : {})
      },
      {
        signal: transcribeController.signal,
        onProgress: (percent) => { progress.value = percent }
      }
    )
    result.value = response
    message.success('转写完成')
  } catch (error) {
    if (!axios.isCancel(error)) {
      message.error(extractApiErrorMessage(error, '语音转写失败'))
    }
  } finally {
    transcribing.value = false
    transcribeController = undefined
  }
}

async function copyText(text: string, label: string): Promise<void> {
  await copyTextToClipboard(text, label)
}

function downloadSrt(): void {
  if (!result.value?.srt) return
  const blob = new Blob([result.value.srt], { type: 'application/x-subrip;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = 'transcript.srt'
  anchor.click()
  URL.revokeObjectURL(url)
}

defineOptions({ name: 'TranscribePanel' })
</script>

<template>
  <div class="transcribe-panel">
    <a-form layout="vertical">
      <div class="transcribe-panel__row">
        <a-form-item label="执行账户" required class="transcribe-panel__field">
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
        <a-form-item label="模型" required class="transcribe-panel__field">
          <a-select
            v-model:value="model"
            placeholder="打开下拉按需加载模型目录"
            :loading="loadingModels"
            :options="transcriptionModels.map((item) => ({ value: item.model, label: `${item.model}（${item.providerCode}）` }))"
            @dropdown-visible-change="(open: boolean) => { if (open) emit('load-models') }"
          />
        </a-form-item>
        <a-form-item label="语言（可选）" class="transcribe-panel__field">
          <a-input v-model:value="language" placeholder="如 zh / en，留空跟随模型默认" :maxlength="16" />
        </a-form-item>
      </div>

      <a-form-item label="音频文件" required>
        <div class="transcribe-panel__file">
          <a-button @click="openUpload">选择音频</a-button>
          <span v-if="selectedFile">{{ selectedFile.name }}</span>
          <a-button v-if="selectedFile" size="small" type="link" danger @click="clearFile">移除</a-button>
          <span v-if="transcribing">转写中 {{ progress }}%</span>
          <input
            ref="uploadInput"
            type="file"
            accept="audio/*"
            style="display: none"
            @change="handleFileChange"
          />
        </div>
      </a-form-item>

      <a-button type="primary" :loading="transcribing" :disabled="!canTranscribe" @click="transcribe">开始转写</a-button>
    </a-form>

    <div v-if="result" class="transcribe-panel__result">
      <div class="transcribe-panel__result-head">
        <span>转写结果</span>
        <a-button size="small" type="link" @click="copyText(result.text, '转写文本已复制')">复制文本</a-button>
      </div>
      <a-textarea :value="result.text" readonly :rows="6" />
      <template v-if="transcriptionHasSrt(result)">
        <div class="transcribe-panel__result-head">
          <span>字幕（SRT）</span>
          <a-button size="small" type="link" @click="copyText(result.srt ?? '', 'SRT 已复制')">复制</a-button>
          <a-button size="small" type="link" @click="downloadSrt">下载 .srt</a-button>
        </div>
        <a-textarea :value="result.srt" readonly :rows="6" />
      </template>
    </div>
  </div>
</template>

<style scoped>
.transcribe-panel {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.transcribe-panel__row {
  display: flex;
  gap: 16px;
  flex-wrap: wrap;
}

.transcribe-panel__field {
  min-width: 200px;
  flex: 1;
}

.transcribe-panel__file {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

.transcribe-panel__result {
  margin-top: 8px;
  border-top: 1px solid var(--juhe-border-color, #e5e7eb);
  padding-top: 8px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.transcribe-panel__result-head {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--juhe-text-secondary, #6b7280);
}
</style>

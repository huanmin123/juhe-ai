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
            :placeholder="accountId ? '选择语音模型' : '先选择账户'"
          />
        </a-form-item>
        <a-form-item label="合成文本" required>
          <a-textarea
            v-model:value="inputText"
            :rows="6"
            :maxlength="2000"
            show-count
            placeholder="输入要合成为语音的文本（最多 2000 字）"
          />
        </a-form-item>
        <a-form-item label="音色" required>
          <a-input v-model:value="voice" allow-clear placeholder="账户可用的音色名，如 alloy" />
        </a-form-item>
        <a-button
          type="primary"
          :loading="synthesizing"
          :disabled="!canSynthesize"
          @click="synthesize"
        >
          合成语音
        </a-button>
        <div v-if="accountsLoadError" class="playground-form-error">{{ accountsLoadError }}</div>
      </a-form>

      <section class="playground-media-result">
        <a-empty
          v-if="!audioUrl"
          class="playground-empty"
          description="合成后在这里播放与下载"
        />
        <div v-else class="playground-audio-shell">
          <audio class="playground-audio" :src="audioUrl" controls />
          <a-button block @click="downloadAudio">下载音频</a-button>
        </div>
      </section>
    </div>
  </a-card>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { message } from '@/lib/antd'

import { api } from '@/api/client'
import { extractApiErrorMessage } from '@/shared/apiError'
import { usePlaygroundCatalog } from './usePlaygroundCatalog'

// 语音合成（演练场设计 §4.2）：账户 × audio 模型同步合成，音频落服务端暂存
// （§3.6/§3.8），响应 JSON 引用 url 直播播放与下载；刷新后经 artifacts 恢复最近一条。

const {
  accountsLoading,
  accountsLoadError,
  accountId,
  modelId,
  accountOptions,
  modelOptions,
  handleAccountChange,
  loadCatalog
} = usePlaygroundCatalog('audio')

const inputText = ref('')
const voice = ref('')
const synthesizing = ref(false)
const audioUrl = ref('')
const audioExtension = ref('mp3')

const canSynthesize = computed(() => Boolean(inputText.value.trim() && voice.value.trim() && accountId.value && modelId.value && !synthesizing.value))

onMounted(() => {
  void restoreOnLoad()
})

/** 加载序：先账户目录（默认选中），再拉暂存清单恢复最近一条语音（暂存为展示缓存，恢复失败不阻断合成）。 */
async function restoreOnLoad(): Promise<void> {
  await loadCatalog()
  try {
    const artifacts = await api.playground.artifacts()
    if (artifacts.speech) {
      audioUrl.value = artifacts.speech.url
      audioExtension.value = resolveAudioExtension(artifacts.speech.mimeType)
    }
  } catch (error) {
    console.error(error)
  }
}

async function synthesize(): Promise<void> {
  const content = inputText.value.trim()
  if (!content) {
    message.warning('请输入合成文本')
    return
  }
  if (!voice.value.trim()) {
    message.warning('请输入音色')
    return
  }
  if (!accountId.value || !modelId.value) {
    message.warning('请先选择账户与模型')
    return
  }
  synthesizing.value = true
  try {
    const result = await api.playground.speech({
      accountId: accountId.value,
      modelId: modelId.value,
      input: content,
      voice: voice.value.trim()
    })
    audioUrl.value = result.url
    audioExtension.value = resolveAudioExtension(result.mimeType)
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '语音合成失败，请稍后重试'))
  } finally {
    synthesizing.value = false
  }
}

function downloadAudio(): void {
  const link = document.createElement('a')
  link.href = audioUrl.value
  link.download = `playground-speech.${audioExtension.value}`
  link.click()
}

function resolveAudioExtension(mime: string): string {
  const subtype = /audio\/([a-z0-9.+-]+)/i.exec(mime)?.[1]?.toLowerCase()
  if (!subtype) return 'mp3'
  return ({ mpeg: 'mp3', 'x-wav': 'wav', mp4: 'm4a' })[subtype] ?? subtype.replace(/^x-/, '')
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

.playground-media-result {
  /* 与图片/视频页同款:结果区撑满,产物垂直居中。 */
  height: 100%;
  display: flex;
  flex-direction: column;
  justify-content: center;
}

.playground-audio-shell {
  width: 100%;
}

.playground-audio {
  display: block;
  width: 100%;
  margin-bottom: 8px;
}

@media (max-width: 992px) {
  .playground-media-layout {
    grid-template-columns: 1fr;
  }
}
</style>

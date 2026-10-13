<script setup lang="ts">
import { onActivated, onMounted, ref } from 'vue'

import { extractApiErrorMessage } from '@/shared/apiError'
import { message } from '@/lib/antd'
import { studioApi } from '@/api/domains/studio'
import type { StudioAccountOption, StudioAsset, StudioModelOption } from '@/types/domain'
import { audioWorkbenchDomain } from './studioAudioOptions'
import TtsPanel from './TtsPanel.vue'
import TranscribePanel from './TranscribePanel.vue'
import AudioHistoryList from './AudioHistoryList.vue'

/**
 * 音频工作台容器（规划 F2.2）：a-tabs 两个页签（文本转语音 / 语音转文本）
 * + 下方合成历史列表。TTS/STT 均为同步端点、无轮询；keepAlive 激活时补一轮
 * 历史同步（§8 强制点 4 的同步端点口径——无轮询可暂停）。
 */
const accounts = ref<StudioAccountOption[]>([])
const loadingAccounts = ref(false)
const models = ref<StudioModelOption[]>([])
const modelsLoaded = ref(false)
const loadingModels = ref(false)
const historyRef = ref<{ reload: () => Promise<boolean> | void }>()

async function loadAccounts(): Promise<void> {
  loadingAccounts.value = true
  try {
    const result = await studioApi.listAccounts(audioWorkbenchDomain)
    accounts.value = result.rows
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载账户候选失败'))
  } finally {
    loadingAccounts.value = false
  }
}

async function loadModels(): Promise<void> {
  if (modelsLoaded.value || loadingModels.value) return
  loadingModels.value = true
  try {
    const result = await studioApi.listModels(audioWorkbenchDomain)
    models.value = result.rows
    modelsLoaded.value = true
  } catch (error) {
    message.error(extractApiErrorMessage(error, '加载模型目录失败'))
  } finally {
    loadingModels.value = false
  }
}

function handleCreated(_asset: StudioAsset): void {
  historyRef.value?.reload()
}

onMounted(() => {
  void loadAccounts()
})

onActivated(() => {
  // keepAlive 激活补一轮历史同步（无轮询任务，历史列表自刷新）。
  historyRef.value?.reload()
})
</script>

<template>
  <a-card class="page-card" :body-style="{ padding: '16px' }">
    <div class="studio-audio-view__header">
      <h2 class="studio-audio-view__title">音频生成</h2>
      <p class="studio-audio-view__desc">文本转语音与语音转文本；合成结果自动保存到素材库。</p>
    </div>

    <a-tabs>
      <a-tab-pane key="tts" tab="文本转语音">
        <TtsPanel
          :accounts="accounts"
          :models="models"
          :loading-accounts="loadingAccounts"
          :loading-models="loadingModels"
          @load-models="loadModels"
          @created="handleCreated"
        />
      </a-tab-pane>
      <a-tab-pane key="stt" tab="语音转文本">
        <TranscribePanel
          :accounts="accounts"
          :models="models"
          :loading-accounts="loadingAccounts"
          :loading-models="loadingModels"
          @load-models="loadModels"
        />
      </a-tab-pane>
    </a-tabs>

    <AudioHistoryList ref="historyRef" />
  </a-card>
</template>

<style scoped>
.studio-audio-view__header {
  margin-bottom: 8px;
}

.studio-audio-view__title {
  font-size: 18px;
  margin: 0;
}

.studio-audio-view__desc {
  font-size: 12px;
  color: var(--juhe-text-tertiary, #9ca3af);
  margin: 4px 0 0;
}
</style>

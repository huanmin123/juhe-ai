<template>
  <div class="media-speech-credentials">
    <a-form-item required class="media-speech-field">
      <template #label>
        <div class="media-speech-label">
          <span>语音应用 App ID</span>
          <a-tooltip title="豆包 TTS（openspeech.bytedance.com）的语音应用双值凭据；出站请求不经账户 Base URL，缺一即显式失败。">
            <QuestionCircleOutlined class="media-speech-help" />
          </a-tooltip>
        </div>
      </template>
      <a-input v-model:value="form.speechAppId" autocomplete="off" placeholder="火山语音应用 App ID" />
    </a-form-item>
    <a-form-item required label="语音应用 Access Token">
      <a-input-password v-model:value="form.speechToken" autocomplete="new-password" placeholder="火山语音应用 Access Token" />
    </a-form-item>
  </div>
</template>

<script setup lang="ts">
import { QuestionCircleOutlined } from '@ant-design/icons-vue'
import type { AccountFormModel } from './accountFormTypes'

defineProps<{
  form: AccountFormModel
}>()
</script>

<style scoped>
.media-speech-credentials {
  min-width: 0;
}

.media-speech-label {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.media-speech-help {
  color: rgba(0, 0, 0, 0.45);
  cursor: help;
}

.media-speech-field {
  margin-bottom: 12px;
}
</style>

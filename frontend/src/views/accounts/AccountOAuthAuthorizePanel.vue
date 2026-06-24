<template>
  <a-form-item class="oauth-mode-item" label="授权方式">
    <a-segmented v-model:value="form.oauthMode" :options="oauthModeOptions" block />
  </a-form-item>

  <template v-if="form.oauthMode === 'manual'">
    <div class="oauth-flow-panel">
      <div class="oauth-step-grid">
        <div class="oauth-step-card">
          <span>1</span>
          <div>
            <strong>生成链接</strong>
            <small>获取本次授权地址</small>
          </div>
        </div>
        <div class="oauth-step-card">
          <span>2</span>
          <div>
            <strong>浏览器授权</strong>
            <small>登录 OpenAI 并允许跳转</small>
          </div>
        </div>
        <div class="oauth-step-card">
          <span>3</span>
          <div>
            <strong>粘贴回调 URL</strong>
            <small>保留 code 与 state 参数</small>
          </div>
        </div>
      </div>
    </div>
    <div class="oauth-actions">
      <a-button type="primary" :loading="authLoading" @click="$emit('generate-auth-url')">生成授权链接</a-button>
      <a-button :disabled="!authResult?.authUrl" @click="$emit('open-auth-url')">打开授权链接</a-button>
      <a-button :disabled="!authResult?.authUrl" @click="$emit('copy-auth-url', authResult?.authUrl || '')">复制授权链接</a-button>
    </div>
    <a-form-item v-if="authResult" class="oauth-url-field" label="授权链接">
      <a-textarea :value="authResult.authUrl" :rows="3" readonly />
    </a-form-item>
    <a-form-item class="oauth-callback-field" label="回调 URL" required :tooltip="callbackTooltip">
      <a-textarea v-model:value="form.callbackUrl" :rows="3" placeholder="粘贴浏览器地址栏里的 http://localhost:1455/auth/callback?code=...&state=..." />
    </a-form-item>
  </template>

  <template v-else>
    <div class="oauth-token-panel">
      <a-form-item class="oauth-token-field" label="Refresh Token" required :tooltip="refreshTokenAlertMessage">
        <a-textarea v-model:value="form.refreshToken" :rows="4" :placeholder="refreshTokenPlaceholder" />
      </a-form-item>
    </div>
  </template>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import type { OpenAIAuthURLResult } from '@/types/domain'
import type { AccountOAuthAuthorizeForm } from './accountFormTypes'

const oauthModeOptions = [
  { label: '手动授权', value: 'manual' },
  { label: '粘贴 Refresh Token', value: 'refresh_token' }
]

const props = withDefaults(defineProps<{
  authLoading: boolean
  authResult?: OpenAIAuthURLResult
  form: AccountOAuthAuthorizeForm
  manualAlertMessage?: string
  refreshTokenAlertMessage?: string
  refreshTokenPlaceholder?: string
}>(), {
  manualAlertMessage: '浏览器最终跳转到本地回调地址；如果页面显示连接失败，复制地址栏完整 URL 粘贴回来即可。',
  refreshTokenAlertMessage: '已有 Refresh Token 时可跳过浏览器授权，后端会换取 Access Token 后创建账户。',
  refreshTokenPlaceholder: '粘贴 OpenAI 的 Refresh Token'
})
const callbackTooltip = computed(() => `${props.manualAlertMessage} 需要粘贴完整地址，不能只粘贴 code 或 state。`)

defineEmits<{
  (event: 'copy-auth-url', value: string): void
  (event: 'generate-auth-url'): void
  (event: 'open-auth-url'): void
}>()
</script>

<style scoped>
.oauth-actions {
  display: flex;
  width: 100%;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 12px;
  margin-bottom: 16px;
}

.oauth-mode-item {
  margin-bottom: 12px;
}

.oauth-flow-panel,
.oauth-token-panel {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.oauth-step-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 10px;
}

.oauth-step-card {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
  padding: 12px;
  border: 1px solid #dbeafe;
  border-radius: 14px;
  background: #fff;
}

.oauth-step-card span {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  color: #1d4ed8;
  font-weight: 700;
  border-radius: 999px;
  background: #dbeafe;
}

.oauth-step-card div {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 2px;
}

.oauth-step-card strong {
  color: #0f172a;
  font-size: 13px;
}

.oauth-step-card small {
  overflow: hidden;
  color: #64748b;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.oauth-url-field,
.oauth-callback-field,
.oauth-token-field {
  margin-bottom: 0;
}

@media (max-width: 992px) {
  .oauth-step-grid {
    grid-template-columns: 1fr;
  }
}
</style>

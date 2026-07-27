<template>
  <a-form-item class="oauth-mode-item" label="授权方式">
    <a-segmented v-model:value="form.oauthMode" :options="oauthModeOptions" block />
  </a-form-item>
  <slot name="credentials" />

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
            <small>{{ manualAuthorizeStepText }}</small>
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
      <a-textarea v-model:value="form.callbackUrl" :rows="3" :placeholder="callbackPlaceholder" />
    </a-form-item>
  </template>

  <template v-else-if="form.oauthMode === 'refresh_token'">
    <div class="oauth-token-panel">
      <a-form-item class="oauth-token-field" label="Refresh Token" required :tooltip="refreshTokenAlertMessage">
        <a-textarea v-model:value="form.refreshToken" :rows="4" :placeholder="refreshTokenPlaceholder" />
      </a-form-item>
    </div>
  </template>

  <template v-else-if="form.oauthMode === 'sso_cookie'">
    <div class="oauth-token-panel">
      <a-form-item class="oauth-token-field" label="Grok Web SSO Key" required tooltip="每行一个 SSO key；系统会批量转换为 Grok OAuth 凭据，提交内容不会放入 URL。">
        <a-textarea v-model:value="form.ssoTokens" :rows="6" placeholder="每行粘贴一个 Grok Web SSO key" />
      </a-form-item>
    </div>
  </template>

  <template v-else>
    <div class="oauth-token-panel">
      <a-form-item class="oauth-token-field" label="Access Token" required :tooltip="accessTokenAlertMessage">
        <a-textarea v-model:value="form.accessToken" :rows="4" :placeholder="accessTokenPlaceholder" />
      </a-form-item>
      <a-form-item class="oauth-token-field" label="Refresh Token">
        <a-textarea v-model:value="form.refreshToken" :rows="3" :placeholder="optionalRefreshTokenPlaceholder" />
      </a-form-item>
    </div>
  </template>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import type { OAuthAuthURLResult } from '@/types/domain'
import type { AccountOAuthAuthorizeForm } from './accountFormTypes'

const props = withDefaults(defineProps<{
  authLoading: boolean
  authResult?: OAuthAuthURLResult
  form: AccountOAuthAuthorizeForm
  oauthModeOptions: Array<{ label: string; value: AccountOAuthAuthorizeForm['oauthMode'] }>
  manualAlertMessage?: string
  refreshTokenAlertMessage?: string
  accessTokenAlertMessage?: string
  refreshTokenPlaceholder?: string
  accessTokenPlaceholder?: string
  optionalRefreshTokenPlaceholder?: string
  manualAuthorizeStepText?: string
  callbackPlaceholder?: string
}>(), {
  manualAlertMessage: '浏览器最终跳转到本地回调地址；如果页面显示连接失败，复制地址栏完整 URL 粘贴回来即可。',
  refreshTokenAlertMessage: '已有 Refresh Token 时可跳过浏览器授权，后端会换取 Access Token 后创建账户。',
  accessTokenAlertMessage: '直接录入当前可用的 Access Token；如果同时提供 Refresh Token，仅作为附带凭据保存。',
  refreshTokenPlaceholder: '粘贴 OAuth Refresh Token',
  accessTokenPlaceholder: '粘贴 OAuth Access Token',
  optionalRefreshTokenPlaceholder: '可选：粘贴 Refresh Token',
  manualAuthorizeStepText: '登录供应商并允许跳转',
  callbackPlaceholder: '粘贴浏览器地址栏里的完整回调 URL，例如 http://localhost:1455/auth/callback?code=...&state=...'
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

<template>
  <section class="form-section">
    <div class="form-section-head">
      <div>
        <h4>{{ title }} 配置</h4>
        <p v-if="editing">Access Token 与 Refresh Token 只在编辑弹窗展示和修改，不会出现在列表。</p>
        <p v-else>创建时支持手动授权或直接粘贴 Refresh Token；敏感凭据不会在列表展示。</p>
      </div>
    </div>

    <template v-if="editing">
      <a-form-item label="Access Token">
        <a-textarea v-model:value="form.accessToken" :rows="3" placeholder="可直接查看和修改 Access Token" />
      </a-form-item>
      <a-form-item label="Refresh Token">
        <a-textarea v-model:value="form.refreshToken" :rows="3" placeholder="可直接查看和修改 Refresh Token" />
      </a-form-item>
    </template>

    <template v-else-if="isOpenAI">
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
          <a-alert class="form-alert" type="info" show-icon message="浏览器最终跳转到本地回调地址；如果页面显示连接失败，复制地址栏完整 URL 粘贴回来即可。" />
        </div>
        <div class="oauth-actions">
          <a-button type="primary" :loading="authLoading" @click="$emit('generate-auth-url')">生成授权链接</a-button>
          <a-button :disabled="!authResult?.authUrl" @click="$emit('open-auth-url')">打开授权链接</a-button>
          <a-button :disabled="!authResult?.authUrl" @click="$emit('copy-auth-url', authResult?.authUrl || '')">复制授权链接</a-button>
        </div>
        <a-form-item v-if="authResult" class="oauth-url-field" label="授权链接">
          <a-textarea :value="authResult.authUrl" :rows="3" readonly />
        </a-form-item>
        <a-form-item class="oauth-callback-field" label="回调 URL" required>
          <a-textarea v-model:value="form.callbackUrl" :rows="3" placeholder="粘贴浏览器地址栏里的 http://localhost:1455/auth/callback?code=...&state=..." />
          <div class="form-help">需要粘贴完整地址，不能只粘贴 code 或 state。</div>
        </a-form-item>
      </template>

      <template v-else>
        <div class="oauth-token-panel">
          <a-alert class="form-alert" type="info" show-icon message="已有 Refresh Token 时可跳过浏览器授权，后端会换取 Access Token 后创建账户。" />
          <a-form-item class="oauth-token-field" label="Refresh Token" required>
            <a-textarea v-model:value="form.refreshToken" :rows="4" placeholder="粘贴 OpenAI 的 Refresh Token" />
          </a-form-item>
        </div>
      </template>
    </template>

    <a-alert v-else class="form-alert" type="warning" show-icon message="该供应商的 OAuth 创建流程尚未开放，第一期先支持 OpenAI OAuth。" />
  </section>
</template>

<script setup lang="ts">
import type { OpenAIAuthURLResult } from '@/types/domain'
import type { AccountFormModel } from './accountFormTypes'

const oauthModeOptions = [
  { label: '手动授权', value: 'manual' },
  { label: '粘贴 Refresh Token', value: 'refresh_token' }
]

defineProps<{
  authLoading: boolean
  authResult?: OpenAIAuthURLResult
  editing: boolean
  form: AccountFormModel
  isOpenAI: boolean
  title: string
}>()

defineEmits<{
  (event: 'copy-auth-url', value: string): void
  (event: 'generate-auth-url'): void
  (event: 'open-auth-url'): void
}>()
</script>

<style scoped>
.form-section {
  padding: 16px;
  border: 1px solid #e8edf5;
  border-radius: 16px;
  background: #fff;
}

.form-section-head {
  margin-bottom: 12px;
}

.form-section-head h4 {
  margin: 0;
  color: #0f172a;
  font-size: 16px;
}

.form-section-head p {
  margin: 4px 0 0;
  color: #64748b;
  font-size: 12px;
}

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

.form-help {
  margin-top: 4px;
  color: #64748b;
  font-size: 12px;
}

.form-alert {
  border-radius: 12px;
}

@media (max-width: 992px) {
  .oauth-step-grid {
    grid-template-columns: 1fr;
  }
}
</style>

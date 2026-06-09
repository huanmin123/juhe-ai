<template>
  <section class="form-section" autocomplete="off">
    <div class="form-section-head">
      <div>
        <h4>{{ title }} 配置</h4>
        <p v-if="editing">Access Token 与 Refresh Token 会在编辑时回显；可直接查看或替换。</p>
        <p v-else>创建时支持手动授权或直接粘贴 Refresh Token；敏感凭据不会在列表展示。</p>
      </div>
    </div>

    <template v-if="editing">
      <a-form-item label="Access Token">
        <a-textarea
          v-model:value="form.accessToken"
          :rows="3"
          autocomplete="off"
          data-lpignore="true"
          data-1p-ignore="true"
          data-form-type="other"
          placeholder="粘贴完整 Access Token"
        />
      </a-form-item>
      <a-form-item label="Refresh Token">
        <a-textarea
          v-model:value="form.refreshToken"
          :rows="3"
          autocomplete="off"
          data-lpignore="true"
          data-1p-ignore="true"
          data-form-type="other"
          placeholder="粘贴完整 Refresh Token"
        />
      </a-form-item>
    </template>

    <template v-else-if="isOpenAI">
      <AccountOAuthAuthorizePanel
        :auth-loading="authLoading"
        :auth-result="authResult"
        :form="form"
        @copy-auth-url="$emit('copy-auth-url', $event)"
        @generate-auth-url="$emit('generate-auth-url')"
        @open-auth-url="$emit('open-auth-url')"
      />
    </template>

    <a-alert v-else class="form-alert" type="warning" show-icon message="该供应商的 OAuth 创建流程尚未开放，当前支持 GPT OAuth。" />
  </section>
</template>

<script setup lang="ts">
import type { OpenAIAuthURLResult } from '@/types/domain'
import AccountOAuthAuthorizePanel from './AccountOAuthAuthorizePanel.vue'
import type { AccountFormModel } from './accountFormTypes'

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

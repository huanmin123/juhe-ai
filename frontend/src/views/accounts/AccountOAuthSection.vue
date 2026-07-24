<template>
  <section class="form-section" autocomplete="off">
    <template v-if="isGoogleOAuth">
      <a-alert class="form-alert" type="info" show-icon message="Gemini Google OAuth 使用用户授权 Refresh Token 自动换取 Access Token；可留空 Refresh Token 以使用尚未过期的 Access Token。" />
      <a-form-item label="Access Token"><a-textarea v-model:value="form.accessToken" :rows="3" autocomplete="off" placeholder="粘贴 Google Access Token" /></a-form-item>
      <a-form-item label="Refresh Token"><a-textarea v-model:value="form.refreshToken" :rows="3" autocomplete="off" placeholder="粘贴 Google OAuth Refresh Token" /></a-form-item>
      <a-form-item label="Client ID"><a-input v-model:value="form.googleClientId" autocomplete="off" /></a-form-item>
      <a-form-item label="Client Secret"><a-input-password v-model:value="form.googleClientSecret" autocomplete="off" /></a-form-item>
      <a-form-item label="Quota Project ID"><a-input v-model:value="form.googleQuotaProjectId" placeholder="可选，用于 x-goog-user-project" /></a-form-item>
    </template>

    <template v-else-if="editing">
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

    <template v-else-if="isAnthropicOAuth">
      <a-alert
        class="form-alert"
        type="info"
        show-icon
        message="Anthropic OAuth 使用直接录入 Bearer Token 的方式接入；请粘贴官方 OAuth / Claude Code 体系得到的 Access Token。"
      />
      <a-form-item label="Access Token" required>
        <a-textarea
          v-model:value="form.accessToken"
          :rows="3"
          autocomplete="off"
          data-lpignore="true"
          data-1p-ignore="true"
          data-form-type="other"
          placeholder="粘贴 CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_AUTH_TOKEN"
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
          placeholder="可选；当前项目不主动刷新 Anthropic OAuth Token"
        />
      </a-form-item>
    </template>

    <a-alert v-else class="form-alert" type="warning" show-icon message="该供应商的 OAuth 创建流程尚未开放，当前支持 GPT OAuth。" />

    <a-form-item label="支持模型" required tooltip="声明这个 OAuth 账户实际支持的上游模型；账户必须至少选择一个模型，模型映射右侧只能从这里选择。">
      <a-select
        v-model:value="form.supportedModels"
        allow-clear
        mode="multiple"
        :loading="modelsLoading"
        option-filter-prop="label"
        placeholder="选择这个账户支持的模型"
        :options="modelOptions"
        show-search
        @dropdown-visible-change="$emit('model-options-open', $event)"
        @search="$emit('model-options-search', $event)"
      />
    </a-form-item>
    <AccountHealthCheckModelField
      :form="form"
      :model-options="modelOptions"
      :models-loading="modelsLoading"
      :protocol-code="protocolCode"
      :protocol-version="protocolVersion"
    />
  </section>
</template>

<script setup lang="ts">
import type { OpenAIAuthURLResult } from '@/types/domain'
import AccountHealthCheckModelField from './AccountHealthCheckModelField.vue'
import AccountOAuthAuthorizePanel from './AccountOAuthAuthorizePanel.vue'
import type { AccountFormModel } from './accountFormTypes'
import type { AccountModelSelectOption } from './accountEditFormPayload'

defineProps<{
  authLoading: boolean
  authResult?: OpenAIAuthURLResult
  editing: boolean
  form: AccountFormModel
  isAnthropicOAuth: boolean
  isOpenAI: boolean
  isGoogleOAuth: boolean
  modelOptions: AccountModelSelectOption[]
  modelsLoading: boolean
  protocolCode?: string
  protocolVersion?: string
  title: string
}>()

defineEmits<{
  (event: 'copy-auth-url', value: string): void
  (event: 'generate-auth-url'): void
  (event: 'open-auth-url'): void
  (event: 'model-options-open', open: boolean): void
  (event: 'model-options-search', value: string): void
}>()
</script>

<style scoped>
.form-section {
  padding: 0;
  border: 0;
  background: transparent;
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

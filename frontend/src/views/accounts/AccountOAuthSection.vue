<template>
  <section class="form-section" autocomplete="off">
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
      />
    </a-form-item>
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
  modelOptions: Array<{ label: string; value: string }>
  modelsLoading: boolean
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

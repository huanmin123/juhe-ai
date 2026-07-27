<template>
  <a-modal
    v-model:open="open"
    :title="account?.providerCode === 'anthropic' ? '重新授权 Anthropic OAuth' : '重新授权 OpenAI OAuth'"
    width="760px"
    :confirm-loading="saving"
    ok-text="更新授权"
    cancel-text="取消"
    @ok="$emit('save')"
    @cancel="$emit('cancel')"
  >
    <a-form layout="vertical" class="reauthorize-form">
      <a-alert
        v-if="account"
        class="form-alert"
        type="info"
        show-icon
        :message="`当前账户：${account.name}`"
        description="重新授权只会覆盖该账户的 OAuth Token，不会修改名称、分组、代理、并发或错误策略。"
      />

      <AccountOAuthAuthorizePanel
        :auth-loading="authLoading"
        :auth-result="authResult"
        :form="form"
        :oauth-mode-options="oauthModeOptions"
        :manual-alert-message="account?.providerCode === 'anthropic'
          ? '授权完成后复制浏览器地址栏完整回调 URL，提交后会覆盖当前账户的 Anthropic OAuth Token。'
          : '授权完成后复制浏览器地址栏完整回调 URL，提交后会覆盖当前账户的 OAuth Token。'"
        :manual-authorize-step-text="account?.providerCode === 'anthropic' ? '登录 Claude 并允许跳转' : '登录 OpenAI 并允许跳转'"
        :refresh-token-alert-message="account?.providerCode === 'anthropic'
          ? '已有新的 Anthropic Refresh Token 时可直接粘贴，后端会重新换取 Access Token 并覆盖当前账户的 OAuth Token。'
          : '已有新的 Refresh Token 时可直接粘贴，后端会换取 Access Token 并覆盖当前账户的 OAuth Token。'"
        :access-token-alert-message="account?.providerCode === 'anthropic'
          ? 'Anthropic 重新授权接口不接收裸 Access Token；如仅更换 Token，请直接编辑账户保存。'
          : undefined"
        @copy-auth-url="$emit('copy-auth-url', $event)"
        @generate-auth-url="$emit('generate-auth-url')"
        @open-auth-url="$emit('open-auth-url')"
      />
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { AccountSummary, OAuthAuthURLResult } from '@/types/domain'
import AccountOAuthAuthorizePanel from './AccountOAuthAuthorizePanel.vue'
import type { AccountOAuthAuthorizeForm } from './accountFormTypes'

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{
  account?: AccountSummary
  authLoading: boolean
  authResult?: OAuthAuthURLResult
  form: AccountOAuthAuthorizeForm
  saving: boolean
}>()

const oauthModeOptions = computed(() => {
  if (props.account?.providerCode === 'anthropic') {
    return [
      { label: '官方 OAuth', value: 'manual' as const },
      { label: '粘贴 Refresh Token', value: 'refresh_token' as const }
    ]
  }
  return [
    { label: '手动授权', value: 'manual' as const },
    { label: '粘贴 Refresh Token', value: 'refresh_token' as const }
  ]
})

defineEmits<{
  (event: 'cancel'): void
  (event: 'copy-auth-url', value: string): void
  (event: 'generate-auth-url'): void
  (event: 'open-auth-url'): void
  (event: 'save'): void
}>()
</script>

<style scoped>
.reauthorize-form {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.form-alert {
  border-radius: 12px;
}
</style>

<template>
  <a-form-item
    class="account-proxy-field"
    :required="oauthProxyRequired"
    :label="oauthProxyRequired ? '代理（必选）' : '代理'"
    :tooltip="oauthProxyRequired
      ? '该供应商的 OAuth 账户上游必须经代理访问：不绑定代理时后端会拒绝创建、刷新和重新授权。'
      : '仅影响这个账号访问上游供应商时使用的代理；不使用代理时直接按 Base URL 访问上游。'"
  >
    <ProxySelect
      v-model:value="form.proxyProfileId"
      allow-clear
      :disabled="authorizedEditing"
      :loading="proxyOptionsLoading"
      :placeholder="oauthProxyRequired ? '必须选择出海代理' : '不使用代理'"
      :options="proxyOptions"
      @dropdown-visible-change="$emit('proxyOptionsDropdown', $event)"
      @search="$emit('proxyOptionsSearch', $event)"
    />
  </a-form-item>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import ProxySelect from '@/components/ProxySelect.vue'
import type { SelectOption } from '@/shared/selectLabelCache'
import type { AccountFormModel } from './accountFormTypes'

const props = defineProps<{
  authorizedEditing: boolean
  form: AccountFormModel
  isOAuthForm: boolean
  proxyOptions: SelectOption[]
  proxyOptionsLoading?: boolean
}>()

defineEmits<{
  (event: 'proxyOptionsDropdown', open: boolean): void
  (event: 'proxyOptionsSearch', value: string): void
}>()

// 国外 OAuth 供应商（Grok/GPT/Anthropic/Gemini）的上游必须经代理可达：
// 与后端 oauthmgmt requiresProxy 拦截和 accountSavePayload 校验保持一致。
const oauthProxyRequired = computed(() =>
  props.isOAuthForm
  && ['gpt', 'anthropic', 'gemini', 'xai'].includes(props.form.providerCode))
</script>

<style scoped>
/* 对话域「添加/编辑 AI 账户」弹窗的代理字段（创建向导第 1 步与编辑态常用配置区共用）：
   原渲染位在 AccountStrategySection「策略与代理」卡内，样式语义随迁移内置，
   不依赖父级 scoped 样式。 */
.account-proxy-field {
  min-width: 0;
  margin-bottom: 16px;
}

.account-proxy-field :deep(.ant-select) {
  width: 100%;
  min-width: 0;
  max-width: 100%;
}
</style>

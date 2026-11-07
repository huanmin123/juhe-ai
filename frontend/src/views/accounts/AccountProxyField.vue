<template>
  <a-form-item
    class="account-proxy-field"
    :required="oauthProxyRequired"
    :label="oauthProxyRequired ? '代理（必选）' : '代理'"
    :tooltip="oauthProxyRequired
      ? '该供应商的 OAuth 账户上游必须经代理访问：不绑定代理或代理池时后端会拒绝创建、刷新和重新授权。'
      : '仅影响这个账号访问上游供应商时使用的代理；选择代理池后每次请求按池策略分配成员。不使用代理时直接按 Base URL 访问上游。'"
  >
    <ProxySelect
      v-model:value="proxyTargetValue"
      allow-clear
      :disabled="authorizedEditing"
      :loading="proxyOptionsLoading"
      :placeholder="oauthProxyRequired ? '必须选择出海代理或代理池' : '不使用代理'"
      :options="proxyOptions"
      :pools="proxyPools"
      @dropdown-visible-change="$emit('proxyOptionsDropdown', $event)"
      @search="$emit('proxyOptionsSearch', $event)"
    />
  </a-form-item>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import ProxySelect from '@/components/ProxySelect.vue'
import { joinProxyCompositeValue, splitProxyCompositeValue } from '@/shared/proxyLabelCache'
import type { ProxyPoolOptionSummary } from '@/types/domain'
import type { SelectOption } from '@/shared/selectLabelCache'
import type { AccountFormModel } from './accountFormTypes'

const props = defineProps<{
  authorizedEditing: boolean
  form: AccountFormModel
  isOAuthForm: boolean
  proxyOptions: SelectOption[]
  proxyPools?: ProxyPoolOptionSummary[]
  proxyOptionsLoading?: boolean
}>()

defineEmits<{
  (event: 'proxyOptionsDropdown', open: boolean): void
  (event: 'proxyOptionsSearch', value: string): void
}>()

// kind-aware 复合值绑定（代理池设计 §9.2）：ProxySelect 的内部值是
// `profile:<id>` / `pool:<id>`，get/set 在此拆装成表单的裸 proxyProfileId /
// proxyPoolId，复合值不进入保存 payload。
const proxyTargetValue = computed<string | undefined>({
  get: () => joinProxyCompositeValue(props.form.proxyProfileId, props.form.proxyPoolId),
  set: (value) => {
    const { profileId, poolId } = splitProxyCompositeValue(value)
    props.form.proxyProfileId = profileId
    props.form.proxyPoolId = poolId
  }
})

// 国外 OAuth 供应商（Grok/GPT/Anthropic/Gemini）的上游必须经代理可达：
// 与后端 oauthmgmt requiresProxy 拦截和 accountSavePayload 校验保持一致；
// 代理池与代理等价（profile 或 pool 至少一个）。
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

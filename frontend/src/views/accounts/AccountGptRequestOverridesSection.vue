<template>
  <section v-if="requestOverridesSupported" class="form-section gpt-request-overrides-section">
    <h4>上游请求覆盖</h4>
    <div class="gpt-request-overrides-grid">
      <a-form-item v-if="serviceTierFieldVisible" label="服务等级">
        <a-select
          v-model:value="serviceTierValue"
          :disabled="readonly"
          :options="serviceTierOptions"
        />
      </a-form-item>
      <a-form-item v-if="reasoningEffortFieldVisible" label="思考级别">
        <a-select
          v-model:value="reasoningEffortValue"
          :disabled="readonly"
          :options="reasoningEffortOptions"
        />
      </a-form-item>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import type {
  AccountGptReasoningEffortOverride,
  AccountGptServiceTierOverride
} from '@/types/domain'
import type { AccountModelSelectOption } from './accountEditFormPayload'
import type { AccountFormModel } from './accountFormTypes'
import {
  accountGptRequestOverrideCapabilities,
  availableAccountGptReasoningEffortOptions,
  availableAccountGptServiceTierOptions,
  isAccountRequestOverrideProviderSupported
} from './accountGptRequestOverrides'

const props = withDefaults(defineProps<{
  form: AccountFormModel
  modelOptions: AccountModelSelectOption[]
  modelsLoading?: boolean
  protocolCode?: string
  readonly?: boolean
}>(), {
  modelsLoading: false,
  readonly: false
})

// 值域与选项来自协议族全集（accountRequestOverrideVocab，指向后端
// requestoverride 真源）；模型目录只按“供应商级是否声明过能力”决定字段显示，
// 不再按已选模型过滤。协议码由弹窗的所选协议档案传入，取不到时按供应商码回落
//（与后端 FamilyOf 的回落语义一致）。
const capabilities = computed(() => accountGptRequestOverrideCapabilities({
  providerCode: props.form.providerCode,
  protocolCode: props.protocolCode,
  modelOptions: props.modelOptions,
  supportedEndpointModes: props.form.supportedEndpointModes
}))
const serviceTierFieldVisible = computed(() => Boolean(
  capabilities.value.serviceTiers.length
  && (props.modelsLoading || capabilities.value.declaredServiceTiers || props.form.serviceTierOverride)
))
const reasoningEffortFieldVisible = computed(() => Boolean(
  capabilities.value.reasoningEfforts.length
  && (props.modelsLoading || capabilities.value.declaredReasoningEfforts || props.form.reasoningEffortOverride)
))
// Interactions-only Gemini 守卫是绝对的：即使已有存量覆盖值也不显示（该协议面
// 覆盖不会生效）。其余账户任一字段可见即显示；存量覆盖值会让对应字段保持可见，
// 保留“不覆盖客户端设置”清除入口。
const requestOverridesSupported = computed(() => isAccountRequestOverrideProviderSupported(props.form.providerCode, props.form.supportedEndpointModes)
  && (serviceTierFieldVisible.value || reasoningEffortFieldVisible.value))
const serviceTierOptions = computed(() => availableAccountGptServiceTierOptions(capabilities.value, props.form.serviceTierOverride))
const reasoningEffortOptions = computed(() => availableAccountGptReasoningEffortOptions(capabilities.value, props.form.reasoningEffortOverride))

const serviceTierValue = computed<AccountGptServiceTierOverride>({
  get: () => props.form.serviceTierOverride,
  set: (value) => {
    props.form.serviceTierOverride = value
  }
})

const reasoningEffortValue = computed<AccountGptReasoningEffortOverride>({
  get: () => props.form.reasoningEffortOverride,
  set: (value) => {
    props.form.reasoningEffortOverride = value
  }
})
</script>

<style scoped>
.gpt-request-overrides-section h4 {
  margin: 0 0 12px;
  color: var(--juhe-fg);
  font-size: 14px;
  font-weight: 600;
}

.gpt-request-overrides-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 16px;
}

@media (max-width: 640px) {
  .gpt-request-overrides-grid {
    grid-template-columns: 1fr;
  }
}
</style>

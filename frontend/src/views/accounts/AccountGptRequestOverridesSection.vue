<template>
  <section class="form-section gpt-request-overrides-section">
    <h4>GPT 请求覆盖</h4>
    <div class="gpt-request-overrides-grid">
      <a-form-item label="服务等级">
        <a-select
          v-model:value="serviceTierValue"
          :disabled="readonly || serviceTierOptions.length <= 1"
          :options="serviceTierOptions"
        />
      </a-form-item>
      <a-form-item label="思考级别">
        <a-select
          v-model:value="reasoningEffortValue"
          :disabled="readonly || reasoningEffortOptions.length <= 1"
          :options="reasoningEffortOptions"
        />
      </a-form-item>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'

import { message } from '@/lib/antd'
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
  isAccountGptReasoningEffortOverrideAvailable,
  isAccountGptServiceTierOverrideAvailable
} from './accountGptRequestOverrides'

const props = withDefaults(defineProps<{
  form: AccountFormModel
  modelOptions: AccountModelSelectOption[]
  modelsLoading?: boolean
  readonly?: boolean
}>(), {
  modelsLoading: false,
  readonly: false
})

const capabilities = computed(() => accountGptRequestOverrideCapabilities({
  accountType: props.form.type,
  modelOptions: props.modelOptions,
  supportedModels: props.form.supportedModels
}))
const serviceTierOptions = computed(() => availableAccountGptServiceTierOptions(capabilities.value))
const reasoningEffortOptions = computed(() => availableAccountGptReasoningEffortOptions(capabilities.value))

const serviceTierValue = computed<AccountGptServiceTierOverride>({
  get: () => isAccountGptServiceTierOverrideAvailable(props.form.serviceTierOverride, capabilities.value)
    ? props.form.serviceTierOverride
    : '',
  set: (value) => {
    props.form.serviceTierOverride = value
  }
})

const reasoningEffortValue = computed<AccountGptReasoningEffortOverride>({
  get: () => isAccountGptReasoningEffortOverrideAvailable(props.form.reasoningEffortOverride, capabilities.value)
    ? props.form.reasoningEffortOverride
    : '',
  set: (value) => {
    props.form.reasoningEffortOverride = value
  }
})

watch(
  [
    () => props.modelsLoading,
    () => props.form.providerCode,
    () => props.form.type,
    () => props.form.supportedModels.join('\n'),
    () => capabilities.value.serviceTiers.join('\n'),
    () => capabilities.value.reasoningEfforts.join('\n')
  ],
  () => {
    if (props.readonly || props.modelsLoading || props.form.providerCode !== 'gpt') return
    const cleared: string[] = []
    if (!isAccountGptServiceTierOverrideAvailable(props.form.serviceTierOverride, capabilities.value)) {
      props.form.serviceTierOverride = ''
      cleared.push('服务等级')
    }
    if (!isAccountGptReasoningEffortOverrideAvailable(props.form.reasoningEffortOverride, capabilities.value)) {
      props.form.reasoningEffortOverride = ''
      cleared.push('思考级别')
    }
    if (cleared.length) {
      message.warning(`${cleared.join('和')}已不再被全部支持模型共同支持，已清空对应覆盖`)
    }
  },
  { immediate: true }
)
</script>

<style scoped>
.gpt-request-overrides-section h4 {
  margin: 0 0 12px;
  color: #0f172a;
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

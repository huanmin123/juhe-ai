<template>
  <section v-if="requestOverridesSupported" class="form-section gpt-request-overrides-section">
    <h4>上游请求覆盖</h4>
    <div class="gpt-request-overrides-grid">
      <a-form-item
        label="服务等级"
        :help="serviceTierHelp"
        :validate-status="serviceTierUnavailable ? 'warning' : undefined"
      >
        <a-select
          v-model:value="serviceTierValue"
          :disabled="readonly || modelsLoading || (!capabilities.serviceTiers.length && !form.serviceTierOverride)"
          :options="serviceTierOptions"
        />
      </a-form-item>
      <a-form-item
        label="思考级别"
        :help="reasoningEffortHelp"
        :validate-status="reasoningEffortUnavailable ? 'warning' : undefined"
      >
        <a-select
          v-model:value="reasoningEffortValue"
          :disabled="readonly || modelsLoading || (!capabilities.reasoningEfforts.length && !form.reasoningEffortOverride)"
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
  isAccountRequestOverrideProviderSupported,
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
  providerCode: props.form.providerCode,
  accountType: props.form.type,
  modelOptions: props.modelOptions,
  supportedModels: props.form.supportedModels,
  supportedEndpointModes: props.form.supportedEndpointModes
}))
const requestOverridesSupported = computed(() => isAccountRequestOverrideProviderSupported(props.form.providerCode, props.form.supportedEndpointModes))
const serviceTierOptions = computed(() => availableAccountGptServiceTierOptions(capabilities.value, props.form.serviceTierOverride))
const reasoningEffortOptions = computed(() => availableAccountGptReasoningEffortOptions(capabilities.value, props.form.reasoningEffortOverride))
const serviceTierUnavailable = computed(() => !isAccountGptServiceTierOverrideAvailable(props.form.serviceTierOverride, capabilities.value))
const reasoningEffortUnavailable = computed(() => !isAccountGptReasoningEffortOverrideAvailable(props.form.reasoningEffortOverride, capabilities.value))
const serviceTierHelp = computed(() => {
  if (serviceTierUnavailable.value) return '当前服务等级配置不受已选模型支持，请清除或重新选择'
  if (!props.modelsLoading && !capabilities.value.serviceTiers.length) return '当前已选模型未声明可用服务等级'
  return undefined
})
const reasoningEffortHelp = computed(() => {
  if (reasoningEffortUnavailable.value) return '当前思考级别配置不受已选模型支持，请清除或重新选择'
  if (!props.modelsLoading && !capabilities.value.reasoningEfforts.length) return '当前已选模型未声明可用思考级别'
  return undefined
})

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

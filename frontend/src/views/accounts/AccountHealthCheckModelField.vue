<template>
  <div class="health-check-config-grid">
    <a-form-item
      label="检查模型"
      required
      tooltip="后台激活检查、周期健康检查和恢复探测固定使用这个模型。人工列表测试不受此配置限制。"
    >
      <a-select
        v-model:value="form.healthCheckModel"
        :disabled="!options.length"
        :loading="modelsLoading"
        option-filter-prop="label"
        :options="options"
        placeholder="选择后台检查使用的模型"
        show-search
      />
    </a-form-item>
    <a-form-item
      label="检查协议"
      required
      tooltip="系统检查固定使用所选协议族的非流式 JSON 请求。"
    >
      <a-select
        v-model:value="form.healthCheckEndpointFamily"
        :disabled="!endpointFamilyOptions.length"
        :options="endpointFamilyOptions"
        placeholder="选择后台检查协议"
      />
    </a-form-item>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import type { AccountFormModel } from './accountFormTypes'
import { accountHealthCheckEndpointFamilyOptions } from './accountHealthCheckEndpointFamily'

const props = defineProps<{
  form: AccountFormModel
  modelOptions: Array<{ label: string; value: string }>
  modelsLoading: boolean
}>()

const options = computed(() => {
  const labels = new Map(props.modelOptions.map((option) => [option.value, option.label]))
  return [...new Set(props.form.supportedModels.map((model) => model.trim()).filter(Boolean))]
    .map((model) => ({
      label: labels.get(model) ?? model,
      value: model
    }))
})

const endpointFamilyOptions = computed(() => accountHealthCheckEndpointFamilyOptions(props.form.supportedEndpointModes))
</script>

<style scoped>
.health-check-config-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 16px;
}

@media (max-width: 768px) {
  .health-check-config-grid {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>

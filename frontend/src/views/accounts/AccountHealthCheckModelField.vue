<template>
  <div class="health-check-config-grid">
    <a-form-item :required="checkModelRequired">
      <template #label>
        <span class="check-model-label">
          <span>检查模型</span>
          <span v-if="autoHintText" class="check-model-auto-hint">{{ autoHintText }}</span>
          <a-tooltip :title="checkModelTooltip">
            <QuestionCircleOutlined class="check-model-help" />
          </a-tooltip>
        </span>
      </template>
      <a-select
        v-model:value="form.healthCheckModel"
        :disabled="!options.length"
        :loading="modelsLoading"
        option-filter-prop="label"
        :options="options"
        placeholder="同步上游模型后自动推荐"
        show-search
      />
    </a-form-item>
    <a-form-item
      label="检查请求形态"
      required
      :tooltip="endpointModeTooltip"
    >
      <a-select
        v-if="imageOnlyModel"
        value="images_json"
        disabled
        :options="imageEndpointModeOptions"
      />
      <a-select
        v-else
        v-model:value="form.healthCheckEndpointMode"
        :disabled="!endpointModeOptions.length"
        :options="endpointModeOptions"
        placeholder="选择后台检查请求形态"
      />
    </a-form-item>
  </div>
</template>

<script setup lang="ts">
import { QuestionCircleOutlined } from '@ant-design/icons-vue'
import { computed, watch } from 'vue'

import type { AccountFormModel } from './accountFormTypes'
import { accountHealthCheckEndpointModeOptions } from './accountHealthCheckEndpointMode'
import { accountTestEndpointModesForModel } from './accountEndpointModes'
import type { AccountModelSelectOption } from './accountEditFormPayload'

const props = defineProps<{
  editing: boolean
  form: AccountFormModel
  modelOptions: AccountModelSelectOption[]
  modelsLoading: boolean
  protocolCode?: string
  protocolVersion?: string
}>()

const checkModelTooltip = '推荐由上游目录自动选择；可手动调整。后台激活检查、周期健康检查和恢复探测使用该模型。'
const managedOAuthDefaultsAllowed = computed(() => (
  ['oauth', 'google_oauth'].includes(props.form.type)
  && ['gpt', 'anthropic', 'gemini', 'xai'].includes(props.form.providerCode)
))
const checkModelRequired = computed(() => props.editing || !managedOAuthDefaultsAllowed.value)
const autoHintText = computed(() => {
  if (props.editing) return ''
  return checkModelRequired.value ? '自动推荐' : '可留空'
})

const options = computed(() => {
  const labels = new Map(props.modelOptions.map((option) => [option.value, option.label]))
  return [...new Set(props.form.supportedModels.map((model) => model.trim()).filter(Boolean))]
    .map((model) => ({
      label: labels.get(model) ?? model,
      value: model
    }))
})

const selectedModelOption = computed(() => (
  props.modelOptions.find((option) => option.value === props.form.healthCheckModel)
))
const selectedModelEndpointModes = computed(() => accountTestEndpointModesForModel({
  providerCode: props.form.providerCode,
  providerProtocolProfileId: props.form.providerProtocolProfileId,
  protocolCode: props.protocolCode,
  protocolVersion: props.protocolVersion,
  type: props.form.type,
  clientCompatibility: props.form.clientCompatibility,
  healthCheckEndpointMode: props.form.healthCheckEndpointMode,
  credentials: { supported_endpoint_modes: props.form.supportedEndpointModes }
}, undefined, selectedModelOption.value))
// v8-C 评估结论：保留 imageOnlyModel→images_json 分支。新建对话账户在
// 域 mode + 协议双层过滤后已选不到纯生图模型，但存量对话账户的
// supportedModels 可能仍含图像模型（v8-A「存量共享、新增隔离」历史数据）；
// 删除分支会把这类账户的检查形态错误归位到对话形态，激活检查直接失败。
// 该分支只依据「当前已选模型的能力」收口检查形态，对媒体域账户同样无害。
const imageOnlyModel = computed(() => (
  selectedModelEndpointModes.value.length === 1
  && selectedModelEndpointModes.value[0] === 'images_json'
))
const endpointModeOptions = computed(() => (
  accountHealthCheckEndpointModeOptions(selectedModelEndpointModes.value)
))
const imageEndpointModeOptions = [{ label: 'Images API', value: 'images_json' }]
const endpointModeTooltip = computed(() => imageOnlyModel.value
  ? '所选模型仅支持 Images API，系统检查会自动使用图片生成请求。'
  : '系统检查直接使用所选模型支持的请求形态；GPT 建议使用 Responses API（Streaming）。')

watch(endpointModeOptions, (next) => {
  if (imageOnlyModel.value) {
    props.form.healthCheckEndpointMode = 'images_json'
    return
  }
  if (!next.length) return
  if (!next.some((option) => option.value === props.form.healthCheckEndpointMode)) {
    props.form.healthCheckEndpointMode = next[0].value
  }
}, { immediate: true })
</script>

<style scoped>
.health-check-config-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 16px;
}

.check-model-label {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
}

.check-model-auto-hint {
  color: var(--juhe-faint);
  font-size: 11px;
  font-weight: 400;
}

.check-model-help {
  color: rgba(0, 0, 0, 0.45);
  cursor: help;
}

@media (max-width: 768px) {
  .health-check-config-grid {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>

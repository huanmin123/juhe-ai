<template>
  <section class="form-section">
    <div class="form-section-head">
      <div>
        <h4>请求策略</h4>
      </div>
    </div>
    <a-form-item label="支持模型">
      <a-select
        v-model:value="form.supportedModels"
        allow-clear
        mode="multiple"
        :loading="modelsLoading"
        :disabled="authorizedEditing"
        option-filter-prop="label"
        placeholder="不限制模型"
        :options="modelOptions"
        show-search
      />
    </a-form-item>
    <a-form-item label="模型映射">
      <div v-if="form.modelMappings.length" class="model-mapping-list">
        <div v-for="(mapping, index) in form.modelMappings" :key="index" class="model-mapping-row">
          <a-select
            v-model:value="mapping.sourceModel"
            allow-clear
            :disabled="authorizedEditing"
            option-filter-prop="label"
            :options="mappingSourceModelOptions"
            placeholder="下游模型"
            show-search
          />
          <a-select
            v-model:value="mapping.sourceEndpointFamily"
            :disabled="authorizedEditing"
            :options="endpointFamilyOptions"
            class="model-mapping-endpoint"
            placeholder="下游协议"
          />
          <SwapRightOutlined class="model-mapping-arrow" />
          <a-select
            v-model:value="mapping.upstreamModel"
            allow-clear
            :disabled="authorizedEditing"
            option-filter-prop="label"
            :options="mappingUpstreamModelOptions"
            placeholder="上游模型"
            show-search
          />
          <a-select
            v-model:value="mapping.upstreamEndpointFamily"
            :disabled="authorizedEditing"
            :options="upstreamEndpointFamilyOptions(mapping.sourceEndpointFamily)"
            class="model-mapping-endpoint"
            placeholder="上游协议"
          />
          <a-switch v-model:checked="mapping.enabled" :disabled="authorizedEditing" />
          <a-tooltip title="删除映射">
            <a-button type="text" danger :disabled="authorizedEditing" @click="removeModelMapping(index)">
              <template #icon><DeleteOutlined /></template>
            </a-button>
          </a-tooltip>
        </div>
      </div>
      <a-button v-if="!authorizedEditing" block type="dashed" @click="addModelMapping">
        <template #icon><PlusOutlined /></template>
        新增映射
      </a-button>
    </a-form-item>
    <a-form-item label="客户端兼容">
      <a-radio-group
        v-if="showClientCompatibilityControl"
        v-model:value="form.clientCompatibility"
        :disabled="authorizedEditing"
        button-style="solid"
        @change="syncEndpointModesForClientCompatibility"
      >
        <a-radio-button
          v-for="option in clientCompatibilitySelectOptions"
          :key="option.value"
          :value="option.value"
        >
          {{ option.label }}
        </a-radio-button>
      </a-radio-group>
      <div v-else class="compatibility-capability-list">
        <a-tag
          v-for="capability in clientCompatibilityCapabilities"
          :key="capability"
          color="blue"
        >
          {{ clientCompatibilityCapabilityLabel(capability) }}
        </a-tag>
      </div>
    </a-form-item>
    <a-form-item label="接口能力限制">
      <a-checkbox-group
        v-model:value="form.supportedEndpointModes"
        :disabled="authorizedEditing || isOAuthForm"
      >
        <div class="endpoint-mode-grid">
          <a-checkbox
            v-for="option in endpointModeOptions"
            :key="option.value"
            :value="option.value"
          >
            {{ option.label }}
          </a-checkbox>
        </div>
      </a-checkbox-group>
    </a-form-item>
    <div class="strategy-grid">
      <a-form-item label="并发上限">
        <a-input-number v-model:value="form.concurrencyLimit" :disabled="authorizedEditing" :min="1" style="width: 100%" />
      </a-form-item>
      <a-form-item label="优先级">
        <a-input-number v-model:value="form.priority" :min="0" style="width: 100%" />
      </a-form-item>
    </div>
    <a-form-item class="strategy-proxy-field" label="代理">
      <ProxySelect
        v-model:value="form.proxyProfileId"
        allow-clear
        :disabled="authorizedEditing"
        placeholder="不使用代理"
        :options="proxyOptions"
      />
    </a-form-item>
  </section>
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'
import { DeleteOutlined, PlusOutlined, SwapRightOutlined } from '@ant-design/icons-vue'
import ProxySelect from '@/components/ProxySelect.vue'
import type { SelectOption } from '@/shared/selectLabelCache'
import type { ProviderProtocolProfileDefinition } from '@/types/domain'
import type { AccountFormModel } from './accountFormTypes'
import { accountEndpointModeOptionsForProfile } from './accountEndpointModes'
import {
  accountClientCompatibilityCapabilities,
  clientCompatibilityCapabilityLabel,
  canSelectClientCompatibility,
  defaultEndpointModesForAccount,
  endpointModesForProfile
} from './accountProviderCapabilities'

const props = defineProps<{
  authorizedEditing: boolean
  form: AccountFormModel
  isOAuthForm: boolean
  isManagementView: boolean
  mappingSourceModelOptions: Array<{ label: string; value: string }>
  mappingUpstreamModelOptions: Array<{ label: string; value: string }>
  modelOptions: Array<{ label: string; value: string }>
  modelsLoading: boolean
  proxyOptions: SelectOption[]
  selectedProtocolProfile?: ProviderProtocolProfileDefinition
}>()

const activeProfile = computed(() => props.selectedProtocolProfile ?? props.form)
const clientCompatibilityCapabilities = computed(() => accountClientCompatibilityCapabilities({
  ...activeProfile.value,
  providerCode: activeProfile.value?.providerCode ?? props.form.providerCode,
  providerProtocolProfileId: activeProfileId(),
  type: props.form.type,
  clientCompatibility: props.form.clientCompatibility
}))
const showClientCompatibilityControl = computed(() => canSelectClientCompatibility({
  ...activeProfile.value,
  providerCode: activeProfile.value?.providerCode ?? props.form.providerCode,
  providerProtocolProfileId: activeProfileId(),
  type: props.form.type,
  clientCompatibility: props.form.clientCompatibility
}))
const clientCompatibilitySelectOptions = computed(() => clientCompatibilityCapabilities.value
  .filter((value): value is AccountFormModel['clientCompatibility'] => value === 'openai_standard' || value === 'codex_responses')
  .map((value) => ({
    label: clientCompatibilityCapabilityLabel(value),
    value
  })))

const endpointModeOptions = computed(() => {
  const allowedModes = new Set(endpointModesForProfile(activeProfile.value))
  return accountEndpointModeOptionsForProfile(activeProfile.value).filter((option) => allowedModes.has(option.value))
})
const endpointFamilyOptions = [
  { label: 'Chat Completions', value: 'chat_completions' },
  { label: 'Responses', value: 'responses' }
] as const

watch(() => props.form.modelMappings.map((mapping) => `${mapping.sourceEndpointFamily}:${mapping.upstreamEndpointFamily}`).join('|'), () => {
  for (const mapping of props.form.modelMappings) {
    if (mapping.sourceEndpointFamily === 'chat_completions' && mapping.upstreamEndpointFamily === 'responses') {
      mapping.upstreamEndpointFamily = 'chat_completions'
    }
  }
})

function upstreamEndpointFamilyOptions(sourceEndpointFamily: AccountFormModel['modelMappings'][number]['sourceEndpointFamily']) {
  return endpointFamilyOptions.map((option) => ({
    ...option,
    disabled: sourceEndpointFamily === 'chat_completions' && option.value === 'responses'
  }))
}

function addModelMapping(): void {
  props.form.modelMappings.push({
    sourceModel: '',
    sourceEndpointFamily: 'chat_completions',
    upstreamModel: '',
    upstreamEndpointFamily: 'chat_completions',
    enabled: true
  })
}

function removeModelMapping(index: number): void {
  props.form.modelMappings.splice(index, 1)
}

function syncEndpointModesForClientCompatibility(): void {
  props.form.supportedEndpointModes = defaultEndpointModesForAccount({
    profile: {
      ...activeProfile.value,
      providerCode: activeProfile.value?.providerCode ?? props.form.providerCode,
      providerProtocolProfileId: activeProfileId()
    },
    type: props.form.type,
    clientCompatibility: props.form.clientCompatibility
  })
}

function activeProfileId(): string | undefined {
  const profile = activeProfile.value
  if (profile && 'providerProtocolProfileId' in profile && typeof profile.providerProtocolProfileId === 'string') {
    return profile.providerProtocolProfileId
  }
  if (profile && 'id' in profile && typeof profile.id === 'string') {
    return profile.id
  }
  return props.form.providerProtocolProfileId
}
</script>

<style scoped>
.form-section {
  padding: 0 0 16px;
  border-bottom: 1px solid #eef2f7;
  background: transparent;
}

.form-section-head {
  margin-bottom: 8px;
}

.form-section-head h4 {
  margin: 0;
  color: #0f172a;
  font-size: 14px;
  font-weight: 600;
}

.form-section-head p {
  margin: 4px 0 0;
  color: #64748b;
  font-size: 12px;
}

.strategy-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(120px, 1fr));
  gap: 0 16px;
}

.form-help {
  margin-top: 4px;
  color: #64748b;
  font-size: 12px;
}

.model-mapping-list {
  display: grid;
  gap: 8px;
  margin-bottom: 8px;
}

.endpoint-mode-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 8px 16px;
  align-items: center;
}

.endpoint-mode-grid :deep(.ant-checkbox-wrapper) {
  min-width: 0;
  white-space: normal;
}

.endpoint-mode-grid :deep(.ant-checkbox + span) {
  min-width: 0;
  line-height: 1.5;
}

.compatibility-capability-list {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  min-height: 32px;
  align-items: center;
}

.model-mapping-row {
  display: grid;
  grid-template-columns: minmax(180px, 1fr) 150px 18px minmax(180px, 1fr) 150px auto 32px;
  gap: 8px;
  align-items: center;
}

.model-mapping-endpoint {
  width: 100%;
}

.model-mapping-arrow {
  color: #64748b;
  font-size: 16px;
}

.strategy-help {
  margin-top: -8px;
  margin-bottom: 16px;
}

.strategy-proxy-field {
  margin-bottom: 16px;
}

@media (max-width: 992px) {
  .strategy-grid {
    grid-template-columns: 1fr;
  }

  .model-mapping-row {
    grid-template-columns: minmax(0, 1fr) minmax(132px, 180px);
  }

  .model-mapping-arrow {
    display: none;
  }

  .model-mapping-row :deep(.ant-switch),
  .model-mapping-row :deep(.ant-btn) {
    justify-self: end;
  }
}

@media (max-width: 576px) {
  .endpoint-mode-grid {
    grid-template-columns: 1fr;
    gap: 8px 16px;
  }
}
</style>

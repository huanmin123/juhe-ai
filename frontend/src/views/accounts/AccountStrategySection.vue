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
            :options="modelOptions"
            placeholder="下游模型"
            show-search
          />
          <SwapRightOutlined class="model-mapping-arrow" />
          <a-select
            v-model:value="mapping.upstreamModel"
            allow-clear
            :disabled="authorizedEditing"
            option-filter-prop="label"
            :options="mappingTargetModelOptions"
            placeholder="上游模型"
            show-search
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
      <div class="compatibility-capability-list">
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
import { computed } from 'vue'
import { DeleteOutlined, PlusOutlined, SwapRightOutlined } from '@ant-design/icons-vue'
import ProxySelect from '@/components/ProxySelect.vue'
import type { SelectOption } from '@/shared/selectLabelCache'
import type { ProviderProtocolProfileDefinition } from '@/types/domain'
import type { AccountFormModel } from './accountFormTypes'
import { accountEndpointModeOptions } from './accountEndpointModes'
import {
  accountClientCompatibilityCapabilities,
  clientCompatibilityCapabilityLabel,
  endpointModesForProfile
} from './accountProviderCapabilities'

const props = defineProps<{
  authorizedEditing: boolean
  form: AccountFormModel
  isOAuthForm: boolean
  isManagementView: boolean
  mappingTargetModelOptions: Array<{ label: string; value: string }>
  modelOptions: Array<{ label: string; value: string }>
  modelsLoading: boolean
  proxyOptions: SelectOption[]
  selectedProtocolProfile?: ProviderProtocolProfileDefinition
}>()

const activeProfile = computed(() => props.selectedProtocolProfile ?? props.form)
const clientCompatibilityCapabilities = computed(() => accountClientCompatibilityCapabilities({
  ...activeProfile.value,
  providerCode: activeProfile.value?.providerCode ?? props.form.providerCode,
  type: props.form.type,
  clientCompatibility: props.form.clientCompatibility
}))

const endpointModeOptions = computed(() => {
  const allowedModes = new Set(endpointModesForProfile(activeProfile.value))
  return accountEndpointModeOptions.filter((option) => allowedModes.has(option.value))
})

function addModelMapping(): void {
  props.form.modelMappings.push({
    sourceModel: '',
    upstreamModel: '',
    enabled: true
  })
}

function removeModelMapping(index: number): void {
  props.form.modelMappings.splice(index, 1)
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
  grid-template-columns: repeat(4, max-content);
  gap: 8px 24px;
  align-items: center;
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
  grid-template-columns: minmax(0, 1fr) 18px minmax(0, 1fr) auto 32px;
  gap: 8px;
  align-items: center;
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
    grid-template-columns: minmax(0, 1fr) 18px minmax(0, 1fr);
  }

  .model-mapping-row :deep(.ant-switch),
  .model-mapping-row :deep(.ant-btn) {
    justify-self: end;
  }
}

@media (max-width: 576px) {
  .endpoint-mode-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 8px 16px;
  }
}

@media (max-width: 400px) {
  .endpoint-mode-grid {
    grid-template-columns: 1fr;
  }
}
</style>

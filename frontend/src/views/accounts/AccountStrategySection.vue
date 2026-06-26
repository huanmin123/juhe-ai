<template>
  <section class="form-section">
    <div class="form-section-head">
      <div>
        <h4 class="section-title">
          <span>请求策略</span>
          <a-tooltip title="控制这个账号能接哪些模型、协议形态，以及在分组内怎么参与调度。">
            <QuestionCircleOutlined class="help-icon" />
          </a-tooltip>
        </h4>
      </div>
    </div>
    <a-form-item label="支持模型" tooltip="为空表示不限制模型；填写后，只有客户端请求的 model 命中这里的模型，才会把请求调度到这个账号。">
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
    <a-form-item label="模型映射" tooltip="把客户端请求的下游模型改写为该账号实际请求的上游模型。常用于私有部署名、Codex Responses 到 Chat Completions 桥接等场景。">
      <div v-if="form.modelMappings.length" class="model-mapping-list">
        <div v-for="(mapping, index) in form.modelMappings" :key="index" class="model-mapping-row">
          <div class="model-mapping-side">
            <a-select
              v-model:value="mapping.sourceModel"
              allow-clear
              :disabled="authorizedEditing"
              option-filter-prop="label"
              :options="mappingSourceModelOptionsFor(mapping.sourceEndpointFamily)"
              placeholder="下游模型"
              show-search
            />
            <a-select
              v-model:value="mapping.sourceEndpointFamily"
              :disabled="authorizedEditing"
              :options="sourceEndpointFamilyOptions"
              class="model-mapping-endpoint"
              placeholder="下游协议"
            />
          </div>
          <SwapRightOutlined class="model-mapping-arrow" />
          <div class="model-mapping-side">
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
          </div>
          <div class="model-mapping-actions">
            <a-switch v-model:checked="mapping.enabled" :disabled="authorizedEditing" />
            <a-tooltip title="删除映射">
              <a-button type="text" danger :disabled="authorizedEditing" @click="removeModelMapping(index)">
                <template #icon><DeleteOutlined /></template>
              </a-button>
            </a-tooltip>
          </div>
        </div>
      </div>
      <a-button v-if="!authorizedEditing" block type="dashed" @click="addModelMapping">
        <template #icon><PlusOutlined /></template>
        新增映射
      </a-button>
    </a-form-item>
    <a-form-item label="客户端兼容" tooltip="选择这个账号面向哪类客户端协议工作；切换后会同步调整下方接口能力限制。">
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
    <a-form-item label="接口能力限制" tooltip="限制这个账号可承接的接口形态。未勾选的请求不会进入该账号候选；OAuth 账号按来源能力只读。">
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
      <a-form-item label="并发上限" tooltip="这个账号同一时间最多承接多少个请求。达到上限后，调度会等待或尝试其他可用账号。">
        <a-input-number v-model:value="form.concurrencyLimit" :disabled="authorizedEditing" :min="1" style="width: 100%" />
      </a-form-item>
      <a-form-item label="优先级" tooltip="分组内账号排序使用小值优先；0 会排在 1 前面。授权账号这里表示当前使用方本地分组内的调度优先级。">
        <a-input-number v-model:value="form.priority" :min="0" style="width: 100%" />
      </a-form-item>
    </div>
    <a-form-item class="strategy-proxy-field" label="代理" tooltip="仅影响这个账号访问上游供应商时使用的代理；不使用代理时直接按 Base URL 访问上游。">
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
import { DeleteOutlined, PlusOutlined, QuestionCircleOutlined, SwapRightOutlined } from '@ant-design/icons-vue'
import ProxySelect from '@/components/ProxySelect.vue'
import type { SelectOption } from '@/shared/selectLabelCache'
import {
  GEMINI_GENERATE_CONTENT_FAMILY,
  GEMINI_STREAM_GENERATE_CONTENT_FAMILY
} from '@/shared/providerProtocol'
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
import {
  defaultAccountModelMappingUpstreamEndpointFamily,
  isAccountModelMappingProtocolAllowed,
  isAccountModelMappingSourceEndpointFamilyAllowed,
  isGeminiGenerateContentMappingSource
} from './accountModelMappingProtocolMatrix'

const props = defineProps<{
  authorizedEditing: boolean
  form: AccountFormModel
  isOAuthForm: boolean
  isManagementView: boolean
  mappingAnthropicSourceModelOptions: Array<{ label: string; value: string }>
  mappingGeminiSourceModelOptions: Array<{ label: string; value: string }>
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
const upstreamEndpointFamilyBaseOptions = [
  { label: 'Chat Completions', value: 'chat_completions' },
  { label: 'Responses', value: 'responses' },
  { label: 'Messages', value: 'messages' },
  { label: 'Gemini GenerateContent', value: GEMINI_GENERATE_CONTENT_FAMILY }
] as const
const sourceEndpointFamilyBaseOptions = [
  { label: 'Chat Completions', value: 'chat_completions' },
  { label: 'Responses', value: 'responses' },
  { label: 'Messages', value: 'messages' },
  { label: 'Gemini GenerateContent', value: GEMINI_GENERATE_CONTENT_FAMILY },
  { label: 'Gemini StreamGenerateContent', value: GEMINI_STREAM_GENERATE_CONTENT_FAMILY }
] as const
const sourceEndpointFamilyOptions = computed(() => sourceEndpointFamilyBaseOptions.map((option) => ({
  ...option,
  disabled: !isAccountModelMappingSourceEndpointFamilyAllowed(option.value, modelMappingProtocolContext())
})))

watch(() => [
  props.form.modelMappings.map((mapping) => `${mapping.sourceEndpointFamily}:${mapping.upstreamEndpointFamily}`).join('|'),
  props.selectedProtocolProfile?.protocolCode ?? '',
  props.selectedProtocolProfile?.protocolVersion ?? '',
  props.form.supportedEndpointModes.join(',')
].join('|'), () => {
  for (const mapping of props.form.modelMappings) {
    if (!isAccountModelMappingSourceEndpointFamilyAllowed(mapping.sourceEndpointFamily, modelMappingProtocolContext())) {
      mapping.sourceEndpointFamily = 'chat_completions'
    }
    if (upstreamEndpointFamilyDisabled(mapping.sourceEndpointFamily, mapping.upstreamEndpointFamily)) {
      mapping.upstreamEndpointFamily = defaultUpstreamEndpointFamilyForSource(mapping.sourceEndpointFamily)
    }
  }
})

function mappingSourceModelOptionsFor(sourceEndpointFamily: AccountFormModel['modelMappings'][number]['sourceEndpointFamily']) {
  if (sourceEndpointFamily === 'messages') {
    return props.mappingAnthropicSourceModelOptions
  }
  if (isGeminiGenerateContentMappingSource(sourceEndpointFamily)) {
    return props.mappingGeminiSourceModelOptions
  }
  return props.mappingSourceModelOptions
}

function upstreamEndpointFamilyOptions(sourceEndpointFamily: AccountFormModel['modelMappings'][number]['sourceEndpointFamily']) {
  return upstreamEndpointFamilyBaseOptions.map((option) => ({
    ...option,
    disabled: upstreamEndpointFamilyDisabled(sourceEndpointFamily, option.value)
  }))
}

function upstreamEndpointFamilyDisabled(
  sourceEndpointFamily: AccountFormModel['modelMappings'][number]['sourceEndpointFamily'],
  upstreamEndpointFamily: AccountFormModel['modelMappings'][number]['upstreamEndpointFamily']
): boolean {
  return !isAccountModelMappingProtocolAllowed({
    sourceEndpointFamily,
    upstreamEndpointFamily,
    context: modelMappingProtocolContext()
  })
}

function defaultUpstreamEndpointFamilyForSource(
  sourceEndpointFamily: AccountFormModel['modelMappings'][number]['sourceEndpointFamily']
): AccountFormModel['modelMappings'][number]['upstreamEndpointFamily'] {
  return defaultAccountModelMappingUpstreamEndpointFamily(sourceEndpointFamily, modelMappingProtocolContext())
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

function modelMappingProtocolContext() {
  return {
    providerProfile: props.selectedProtocolProfile ?? props.form,
    supportedEndpointModes: props.form.supportedEndpointModes
  }
}
</script>

<style scoped>
.form-section {
  min-width: 0;
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

.section-title {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.help-icon {
  color: #94a3b8;
  cursor: help;
  font-size: 14px;
}

.help-icon:hover {
  color: #1677ff;
}

.form-section-head p {
  margin: 4px 0 0;
  color: #64748b;
  font-size: 12px;
}

.strategy-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 16px;
  min-width: 0;
}

.strategy-grid :deep(.ant-form-item) {
  min-width: 0;
}

.form-help {
  margin-top: 4px;
  color: #64748b;
  font-size: 12px;
}

.model-mapping-list {
  display: grid;
  gap: 8px;
  min-width: 0;
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
  grid-template-columns: minmax(0, 1fr) 18px minmax(0, 1fr) auto;
  gap: 8px;
  align-items: center;
  min-width: 0;
}

.model-mapping-side {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(126px, 148px);
  gap: 8px;
  min-width: 0;
}

.model-mapping-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 4px;
  min-width: max-content;
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

.form-section :deep(.ant-select),
.form-section :deep(.ant-picker),
.form-section :deep(.ant-input-number) {
  width: 100%;
  min-width: 0;
  max-width: 100%;
}

.form-section :deep(.ant-form-item-control-input-content) {
  min-width: 0;
  max-width: 100%;
}

@media (max-width: 992px) {
  .strategy-grid {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 720px) {
  .model-mapping-row {
    grid-template-columns: minmax(0, 1fr);
  }

  .model-mapping-arrow {
    display: none;
  }

  .model-mapping-actions {
    justify-content: flex-end;
  }
}

@media (max-width: 576px) {
  .endpoint-mode-grid {
    grid-template-columns: 1fr;
    gap: 8px 16px;
  }

  .model-mapping-side {
    grid-template-columns: 1fr;
  }
}
</style>

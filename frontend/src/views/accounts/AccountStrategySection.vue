<template>
  <section class="form-section">
    <a-form-item label="上游接口能力" tooltip="声明这个账号真实上游支持的接口形态。未勾选的上游请求不会进入该账号候选；OAuth 账号按来源能力只读。">
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
    <a-form-item label="账号模型别名" :tooltip="modelMappingTooltip">
      <div v-if="form.modelMappings.length" class="model-mapping-list">
        <div v-for="(mapping, index) in form.modelMappings" :key="index" class="model-mapping-row">
          <div class="model-mapping-side">
            <a-select
              v-model:value="mapping.sourceEndpointFamily"
              :disabled="authorizedEditing"
              :options="sourceEndpointFamilyOptions"
              class="model-mapping-endpoint"
              placeholder="来源协议"
            />
            <a-select
              v-model:value="mapping.sourceModel"
              allow-clear
              :disabled="authorizedEditing"
              option-filter-prop="label"
              :options="mappingSourceModelOptionsFor(mapping)"
              placeholder="来源模型"
              show-search
            />
          </div>
          <SwapRightOutlined class="model-mapping-arrow" />
          <div class="model-mapping-side">
            <a-select
              v-model:value="mapping.upstreamEndpointFamily"
              :disabled="authorizedEditing"
              :options="upstreamEndpointFamilyOptions(mapping)"
              class="model-mapping-endpoint"
              placeholder="目标协议"
            />
            <a-select
              v-model:value="mapping.upstreamModel"
              allow-clear
              :disabled="authorizedEditing"
              option-filter-prop="label"
              :options="mappingUpstreamModelOptionsFor(mapping.upstreamEndpointFamily)"
              placeholder="目标模型"
              show-search
            />
          </div>
          <div class="model-mapping-actions">
            <a-switch v-model:checked="mapping.enabled" :disabled="authorizedEditing" />
            <a-tooltip title="删除别名">
              <a-button type="text" danger :disabled="authorizedEditing" @click="removeModelMapping(index)">
                <template #icon><DeleteOutlined /></template>
              </a-button>
            </a-tooltip>
          </div>
        </div>
      </div>
      <a-button v-if="!authorizedEditing" block type="dashed" @click="addModelMapping">
        <template #icon><PlusOutlined /></template>
        新增别名
      </a-button>
    </a-form-item>
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
import { DeleteOutlined, PlusOutlined, SwapRightOutlined } from '@ant-design/icons-vue'
import ProxySelect from '@/components/ProxySelect.vue'
import type { SelectOption } from '@/shared/selectLabelCache'
import {
  ANTHROPIC_MESSAGES_FAMILY,
  GEMINI_GENERATE_CONTENT_FAMILY,
  GEMINI_STREAM_GENERATE_CONTENT_FAMILY,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_RESPONSES_FAMILY,
  isHybridProviderCode,
  isOpenAIProtocolProfile
} from '@/shared/providerProtocol'
import type { ProviderProtocolProfileDefinition } from '@/types/domain'
import type { AccountFormModel } from './accountFormTypes'
import {
  filterAccountModelMappingOptionsByEndpointFamily,
  type AccountModelMappingModelOption
} from './accountModelMappingModelOptions'
import { accountEndpointModeOptionsForProfile } from './accountEndpointModes'
import {
  endpointModesForProfile
} from './accountProviderCapabilities'
import {
  defaultAccountModelMappingUpstreamEndpointFamily,
  defaultAccountModelMappingSourceEndpointFamily,
  isAccountModelMappingProtocolAllowed,
  isAccountModelMappingSourceEndpointFamilyAllowed,
  shouldResetAccountModelMappingUpstreamEndpointFamily
} from './accountModelMappingProtocolMatrix'

const props = defineProps<{
  authorizedEditing: boolean
  form: AccountFormModel
  isOAuthForm: boolean
  isManagementView: boolean
  mappingAnthropicSourceModelOptions: ModelMappingSourceModelOption[]
  mappingGeminiSourceModelOptions: ModelMappingSourceModelOption[]
  mappingSourceModelOptions: ModelMappingSourceModelOption[]
  mappingUpstreamModelOptions: ModelMappingSourceModelOption[]
  proxyOptions: SelectOption[]
  selectedProtocolProfile?: ProviderProtocolProfileDefinition
}>()

type ModelMappingSourceModelOption = AccountModelMappingModelOption

const activeProfile = computed(() => props.selectedProtocolProfile ?? props.form)
const isHybridAccount = computed(() => isHybridProviderCode(props.form.providerCode))
const modelMappingTooltip = computed(() => (
  isHybridAccount.value
    ? '混合供应商账户在这里配置下游协议和模型到真实上游协议和模型的映射；左侧模型只能选择对应协议支持的模型，右侧上游模型只能选择账户支持模型。'
    : '只在当前供应商和当前协议内做模型名改写；OpenAI v1 可显式配置 Responses 到 Chat Completions；右侧上游模型只能选择账户支持模型。'
))
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
const sourceEndpointFamilyOptions = computed(() => sourceEndpointFamilyBaseOptions.filter((option) => (
  isAccountModelMappingSourceEndpointFamilyAllowed(option.value, modelMappingProtocolContext())
)))

watch(() => [
  props.form.modelMappings.map((mapping) => `${mapping.sourceEndpointFamily}:${mapping.upstreamEndpointFamily}`).join('|'),
  props.selectedProtocolProfile?.protocolCode ?? '',
  props.selectedProtocolProfile?.protocolVersion ?? '',
  props.form.supportedEndpointModes.join(',')
].join('|'), () => {
  for (const mapping of props.form.modelMappings) {
    if (!isAccountModelMappingSourceEndpointFamilyAllowed(mapping.sourceEndpointFamily, modelMappingProtocolContext())) {
      mapping.sourceEndpointFamily = defaultAccountModelMappingSourceEndpointFamily(modelMappingProtocolContext())
    }
    if (shouldResetAccountModelMappingUpstreamEndpointFamily({
      sourceEndpointFamily: mapping.sourceEndpointFamily,
      upstreamEndpointFamily: mapping.upstreamEndpointFamily,
      context: modelMappingProtocolContext()
    })) {
      mapping.upstreamEndpointFamily = defaultUpstreamEndpointFamilyForSource(mapping.sourceEndpointFamily)
    }
  }
})

function mappingSourceModelOptionsFor(mapping: AccountFormModel['modelMappings'][number]) {
  const options = rawMappingSourceModelOptionsFor(mapping.sourceEndpointFamily)
  if (isOpenAIResponsesToChatMapping(mapping)) {
    return options.filter((option) => {
      const protocols = option.supportedApiProtocols ?? []
      return protocols.includes(OPENAI_RESPONSES_FAMILY) || protocols.includes(OPENAI_CHAT_COMPLETIONS_FAMILY)
    })
  }
  return filterAccountModelMappingOptionsByEndpointFamily(options, mapping.sourceEndpointFamily)
}

function rawMappingSourceModelOptionsFor(sourceEndpointFamily: AccountFormModel['modelMappings'][number]['sourceEndpointFamily']) {
  if (sourceEndpointFamily === ANTHROPIC_MESSAGES_FAMILY) return props.mappingAnthropicSourceModelOptions
  if (sourceEndpointFamily === GEMINI_GENERATE_CONTENT_FAMILY || sourceEndpointFamily === GEMINI_STREAM_GENERATE_CONTENT_FAMILY) {
    return props.mappingGeminiSourceModelOptions
  }
  return props.mappingSourceModelOptions
}

function mappingUpstreamModelOptionsFor(upstreamEndpointFamily: AccountFormModel['modelMappings'][number]['upstreamEndpointFamily']) {
  return filterAccountModelMappingOptionsByEndpointFamily(props.mappingUpstreamModelOptions, upstreamEndpointFamily)
}

function upstreamEndpointFamilyOptions(mapping: AccountFormModel['modelMappings'][number]) {
  return upstreamEndpointFamilyBaseOptions.filter((option) => (
    !upstreamEndpointFamilyDisabled(mapping.sourceEndpointFamily, option.value, mapping.enabled)
  ))
}

function upstreamEndpointFamilyDisabled(
  sourceEndpointFamily: AccountFormModel['modelMappings'][number]['sourceEndpointFamily'],
  upstreamEndpointFamily: AccountFormModel['modelMappings'][number]['upstreamEndpointFamily'],
  enabled = true
): boolean {
  return !isAccountModelMappingProtocolAllowed({
    sourceEndpointFamily,
    upstreamEndpointFamily,
    enabled,
    context: modelMappingProtocolContext()
  })
}

function defaultUpstreamEndpointFamilyForSource(
  sourceEndpointFamily: AccountFormModel['modelMappings'][number]['sourceEndpointFamily']
): AccountFormModel['modelMappings'][number]['upstreamEndpointFamily'] {
  return defaultAccountModelMappingUpstreamEndpointFamily(sourceEndpointFamily, modelMappingProtocolContext())
}

function isOpenAIResponsesToChatMapping(mapping: AccountFormModel['modelMappings'][number]): boolean {
  return mapping.sourceEndpointFamily === OPENAI_RESPONSES_FAMILY
    && mapping.upstreamEndpointFamily === OPENAI_CHAT_COMPLETIONS_FAMILY
    && isOpenAIProtocolProfile(props.selectedProtocolProfile)
}

function addModelMapping(): void {
  const sourceEndpointFamily = defaultAccountModelMappingSourceEndpointFamily(modelMappingProtocolContext())
  props.form.modelMappings.push({
    sourceModel: '',
    sourceEndpointFamily,
    upstreamModel: '',
    upstreamEndpointFamily: defaultAccountModelMappingUpstreamEndpointFamily(sourceEndpointFamily, modelMappingProtocolContext()),
    enabled: true
  })
}

function removeModelMapping(index: number): void {
  props.form.modelMappings.splice(index, 1)
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
  grid-template-columns: minmax(126px, 148px) minmax(0, 1fr);
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
.form-section :deep(.ant-input),
.form-section :deep(.ant-input-number) {
  width: 100%;
  min-width: 0;
  max-width: 100%;
}

.form-section :deep(.ant-form-item-control-input-content) {
  min-width: 0;
  max-width: 100%;
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

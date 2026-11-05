<template>
  <section class="form-section">
    <a-form-item label="上游接口能力" tooltip="声明这个账号真实上游支持的接口形态。未勾选的上游请求不会进入该账号候选；OAuth 账号按来源能力只读。">
      <div class="endpoint-mode-grid">
        <a-checkbox
          v-for="option in endpointModeOptions"
          :key="option.value"
          :checked="form.supportedEndpointModes.includes(option.value)"
          :disabled="endpointModesDisabled"
          @change="setModeEnabled(option.value, $event.target.checked)"
        >
          {{ option.label }}
        </a-checkbox>
      </div>
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
              placeholder="客户端协议"
            />
            <a-select
              v-model:value="mapping.sourceModel"
              allow-clear
              :disabled="authorizedEditing"
              option-filter-prop="label"
              :options="mappingSourceModelOptionsFor(mapping)"
              placeholder="客户端模型"
              show-search
              @dropdown-visible-change="handleSourceModelDropdown(mapping.sourceEndpointFamily, $event)"
              @search="handleSourceModelSearch(mapping.sourceEndpointFamily, $event)"
            >
              <template v-if="sourceModelTruncated" #dropdownRender="{ menuNode }">
                <ModelOptionsTruncatedHint :menu-node="menuNode" />
              </template>
            </a-select>
          </div>
          <SwapRightOutlined class="model-mapping-arrow" />
          <div class="model-mapping-side">
            <a-select
              v-model:value="mapping.upstreamEndpointFamily"
              :disabled="authorizedEditing"
              :options="upstreamEndpointFamilyOptions(mapping)"
              class="model-mapping-endpoint"
              placeholder="上游协议"
            />
            <a-select
              v-model:value="mapping.upstreamModel"
              allow-clear
              :disabled="authorizedEditing"
              option-filter-prop="label"
              :options="mappingUpstreamModelOptionsFor(mapping.upstreamEndpointFamily)"
              placeholder="上游模型"
              show-search
              @dropdown-visible-change="emit('current-provider-model-options-open', $event)"
              @search="emit('current-provider-model-options-search', $event)"
            >
              <template v-if="modelsTruncated" #dropdownRender="{ menuNode }">
                <ModelOptionsTruncatedHint :menu-node="menuNode" />
              </template>
            </a-select>
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
  </section>
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'
import { DeleteOutlined, PlusOutlined, SwapRightOutlined } from '@ant-design/icons-vue'
import { ModelOptionsTruncatedHint } from '@/components/ModelOptionsTruncatedHint'
import {
  GEMINI_GENERATE_CONTENT_FAMILY,
  GEMINI_STREAM_GENERATE_CONTENT_FAMILY,
  OPENAI_TTS_FAMILY,
  OPENAI_VIDEO_GENERATION_FAMILY
} from '@/shared/providerProtocol'
import type { AccountSupportedEndpointMode, ProviderProtocolProfileDefinition } from '@/types/domain'
import type { AccountFormModel } from './accountFormTypes'
import {
  filterAccountModelMappingOptionsByEndpointFamily,
  type AccountModelMappingModelOption
} from './accountModelMappingModelOptions'
import { accountEndpointModeOptionsForProfile } from './accountEndpointModes'
import {
  allowedEndpointModesFor
} from './accountCapabilityDomains'
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
  mappingCurrentProviderSourceModelOptions: ModelMappingSourceModelOption[]
  mappingGeminiSourceModelOptions: ModelMappingSourceModelOption[]
  mappingSourceModelOptions: ModelMappingSourceModelOption[]
  /** 映射源模型下拉（协议切片实例）的服务端截断标志（T25 hasMore）。 */
  mappingSourceModelOptionsTruncated?: boolean
  mappingUpstreamModelOptions: ModelMappingSourceModelOption[]
  /** 当前供应商目录（上游模型/源模型）的服务端截断标志（T25 hasMore）。 */
  modelsTruncated?: boolean
  selectedProtocolProfile?: ProviderProtocolProfileDefinition
}>()

const emit = defineEmits<{
  (event: 'current-provider-model-options-open', open: boolean): void
  (event: 'current-provider-model-options-search', value: string): void
  (event: 'mapping-source-model-options-open', protocol: 'openai' | 'anthropic' | 'gemini', open: boolean): void
  (event: 'mapping-source-model-options-search', protocol: 'openai' | 'anthropic' | 'gemini', value: string): void
}>()

type ModelMappingSourceModelOption = AccountModelMappingModelOption

const activeProfile = computed(() => props.selectedProtocolProfile ?? props.form)
const modelMappingTooltip = computed(() => (
  '只在当前供应商和当前协议内做模型名改写；OpenAI v1 可显式配置 Responses 到 Chat Completions；右侧上游模型只能选择账户支持模型。'
))
const endpointModeOptions = computed(() => {
  // M2 能力域收口 + v8-A 对话域纯粹化：chat 域允许集 = 13 值对话锚集合 ∩
  // 供应商写侧词表——媒体任务组与被移出三项（images_json /
  // audio_transcription_json / realtime_session）不再出现在对话表单勾选区
  // （媒体域走独立精简表单）。
  const allowedModes = new Set(allowedEndpointModesFor({
    capabilityDomain: 'chat',
    providerCode: props.form.providerCode,
    profile: activeProfile.value,
    accountType: props.form.type
  }))
  return accountEndpointModeOptionsForProfile(activeProfile.value).filter((option) => allowedModes.has(option.value))
})

const endpointModesDisabled = computed(() => props.authorizedEditing || props.isOAuthForm)
// T23：源模型下拉的截断提示条件——当前供应商目录的 modelsTruncated。
const sourceModelTruncated = computed(() => Boolean(props.modelsTruncated))

function setModeEnabled(mode: AccountSupportedEndpointMode, enabled: boolean): void {
  props.form.supportedEndpointModes = enabled
    ? [...new Set([...props.form.supportedEndpointModes, mode])]
    : props.form.supportedEndpointModes.filter((item) => item !== mode)
}
const upstreamEndpointFamilyBaseOptions = [
  { label: 'Chat Completions', value: 'chat_completions' },
  { label: 'Responses', value: 'responses' },
  { label: 'Messages', value: 'messages' },
  { label: 'Gemini GenerateContent', value: GEMINI_GENERATE_CONTENT_FAMILY },
  // M4b 媒体映射族：同族模型名改写，上游面恒为 OpenAI 形态媒体端点。
  { label: '视频生成', value: OPENAI_VIDEO_GENERATION_FAMILY },
  { label: '语音合成', value: OPENAI_TTS_FAMILY }
] as const
const sourceEndpointFamilyBaseOptions = [
  { label: 'Chat Completions', value: 'chat_completions' },
  { label: 'Responses', value: 'responses' },
  { label: 'Messages', value: 'messages' },
  { label: 'Gemini GenerateContent', value: GEMINI_GENERATE_CONTENT_FAMILY },
  { label: 'Gemini StreamGenerateContent', value: GEMINI_STREAM_GENERATE_CONTENT_FAMILY },
  // M4b 媒体映射族：同族模型名改写，上游面恒为 OpenAI 形态媒体端点。
  { label: '视频生成', value: OPENAI_VIDEO_GENERATION_FAMILY },
  { label: '语音合成', value: OPENAI_TTS_FAMILY }
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

function mappingSourceModelOptionsFor(_mapping: AccountFormModel['modelMappings'][number]) {
  return props.mappingCurrentProviderSourceModelOptions
}

function handleSourceModelDropdown(
  _sourceEndpointFamily: AccountFormModel['modelMappings'][number]['sourceEndpointFamily'],
  open: boolean
): void {
  emit('current-provider-model-options-open', open)
}

function handleSourceModelSearch(
  _sourceEndpointFamily: AccountFormModel['modelMappings'][number]['sourceEndpointFamily'],
  value: string
): void {
  emit('current-provider-model-options-search', value)
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
  border-bottom: 1px solid var(--juhe-border);
  background: transparent;
}

.form-help {
  margin-top: 4px;
  color: var(--juhe-muted);
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
  color: var(--juhe-muted);
  font-size: 16px;
}

.strategy-help {
  margin-top: -8px;
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

.endpoint-mode-group {
  min-width: 0;
}
</style>

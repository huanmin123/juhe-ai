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
          <a-select
            v-model:value="mapping.sourceEndpointFamily"
            :disabled="authorizedEditing"
            :options="protocolOptions"
            class="model-mapping-endpoint"
            placeholder="映射协议"
            @change="handleMappingProtocolChange(mapping)"
          />
          <a-select
            v-model:value="mapping.sourceModel"
            allow-clear
            :disabled="authorizedEditing"
            option-filter-prop="label"
            :options="mappingSourceModelOptionsFor(mapping)"
            placeholder="客户端模型"
            show-search
            @dropdown-visible-change="handleSourceModelDropdown"
            @search="handleSourceModelSearch"
          >
            <template v-if="sourceModelTruncated" #dropdownRender="{ menuNode }">
              <ModelOptionsTruncatedHint :menu-node="menuNode" />
            </template>
          </a-select>
          <SwapRightOutlined class="model-mapping-arrow" />
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
import { computed } from 'vue'
import { DeleteOutlined, PlusOutlined, SwapRightOutlined } from '@ant-design/icons-vue'
import { ModelOptionsTruncatedHint } from '@/components/ModelOptionsTruncatedHint'
import type { ProviderProtocolProfileDefinition } from '@/types/domain'
import type { AccountFormModel } from './accountFormTypes'
import {
  accountModelMappingSourceModelOptions,
  accountModelMappingUpstreamModelOptions,
  type AccountModelMappingModelOption
} from './accountModelMappingModelOptions'
import { accountEndpointModeOptionsForProfile } from './accountEndpointModes'
import type { AccountCapabilityDomain } from './accountCapabilityDomains'
import {
  allowedEndpointModesFor
} from './accountCapabilityDomains'
import {
  accountModelMappingProtocolOptions,
  accountModelMappingTargetForSource,
  defaultAccountModelMappingProtocol,
  isAccountModelMappingProtocolOptionAllowed
} from './accountModelMappingProtocolMatrix'

const props = defineProps<{
  authorizedEditing: boolean
  form: AccountFormModel
  isOAuthForm: boolean
  isManagementView: boolean
  /** 当前场景：工具域展示同族工具协议与对应 endpoint mode 选项。 */
  capabilityDomain: AccountCapabilityDomain
  /** 映射客户端模型候选：跨供应商全量目录（按行协议过滤），由 useAccountEditForm 装载。 */
  mappingSourceModelOptions: ModelMappingSourceModelOption[]
  /** 映射源模型下拉（协议切片实例）的服务端截断标志（T25 hasMore）。 */
  mappingSourceModelOptionsTruncated?: boolean
  mappingUpstreamModelOptions: ModelMappingSourceModelOption[]
  /** 当前供应商目录（上游模型）的服务端截断标志（T25 hasMore）。 */
  modelsTruncated?: boolean
  selectedProtocolProfile?: ProviderProtocolProfileDefinition
}>()

const emit = defineEmits<{
  (event: 'current-provider-model-options-open', open: boolean): void
  (event: 'current-provider-model-options-search', value: string): void
  (event: 'mapping-source-model-options-open', open: boolean): void
  (event: 'mapping-source-model-options-search', value: string): void
}>()

type ModelMappingSourceModelOption = AccountModelMappingModelOption

const activeProfile = computed(() => props.selectedProtocolProfile ?? props.form)
const modelMappingTooltip = computed(() => (
  '客户端协议与上游协议强制一致（含 Gemini 流式归一）；客户端模型可跨供应商选择，但必须支持所选协议；上游模型只能选择账户支持模型。'
))
const endpointModeOptions = computed(() => {
  // 按当前场景取允许集：chat 域为对话锚集合，工具域为同族工具 mode 集合；
  // 再与供应商写侧词表求交。
  const allowedModes = new Set(allowedEndpointModesFor({
    capabilityDomain: props.capabilityDomain,
    providerCode: props.form.providerCode,
    profile: activeProfile.value,
    accountType: props.form.type
  }))
  return accountEndpointModeOptionsForProfile(activeProfile.value).filter((option) => allowedModes.has(option.value))
})

const endpointModesDisabled = computed(() => props.authorizedEditing || props.isOAuthForm)
// T23：源模型下拉的截断提示条件——跨供应商目录的服务端截断标志。
const sourceModelTruncated = computed(() => Boolean(props.mappingSourceModelOptionsTruncated))

function setModeEnabled(mode: AccountFormModel['supportedEndpointModes'][number], enabled: boolean): void {
  props.form.supportedEndpointModes = enabled
    ? [...new Set([...props.form.supportedEndpointModes, mode])]
    : props.form.supportedEndpointModes.filter((item) => item !== mode)
}

// 单协议下拉（7 个协议等价选项）：按协议档案与账户实际 endpoint modes 过滤。
const protocolOptions = computed(() => (
  accountModelMappingProtocolOptions.filter((option) => (
    isAccountModelMappingProtocolOptionAllowed(option, modelMappingProtocolContext())
  ))
))

// 修改协议时同时写入 source/target 两个持久化字段（UI 投影，不新增列）。
function handleMappingProtocolChange(
  mapping: AccountFormModel['modelMappings'][number]
): void {
  mapping.upstreamEndpointFamily = accountModelMappingTargetForSource(mapping.sourceEndpointFamily)
}

function mappingSourceModelOptionsFor(mapping: AccountFormModel['modelMappings'][number]) {
  return accountModelMappingSourceModelOptions({
    sourceEndpointFamily: mapping.sourceEndpointFamily,
    options: props.mappingSourceModelOptions
  })
}

function handleSourceModelDropdown(open: boolean): void {
  emit('mapping-source-model-options-open', open)
}

function handleSourceModelSearch(value: string): void {
  emit('mapping-source-model-options-search', value)
}

function mappingUpstreamModelOptionsFor(upstreamEndpointFamily: AccountFormModel['modelMappings'][number]['upstreamEndpointFamily']) {
  // 上游模型仍只能来自账户支持模型（mappingUpstreamModelOptions 由支持模型构建），
  // 按目标协议过滤。
  return accountModelMappingUpstreamModelOptions(props.mappingUpstreamModelOptions, upstreamEndpointFamily)
}

function addModelMapping(): void {
  const option = defaultAccountModelMappingProtocol(modelMappingProtocolContext())
  props.form.modelMappings.push({
    sourceModel: '',
    sourceEndpointFamily: option.source,
    upstreamModel: '',
    upstreamEndpointFamily: option.upstream,
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
  grid-template-columns: minmax(126px, 148px) minmax(0, 1fr) 18px minmax(0, 1fr) auto;
  gap: 8px;
  align-items: center;
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
}

.endpoint-mode-group {
  min-width: 0;
}
</style>

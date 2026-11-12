<template>
  <a-form-item required class="media-model-field">
    <template #label>
      <div class="supported-models-label">
        <span>支持模型</span>
        <a-tooltip title="声明这个账户实际支持的上游模型；媒体域账户必须至少选择一个模型。">
          <QuestionCircleOutlined class="media-model-help" />
        </a-tooltip>
        <a-tooltip title="从上游同步可新增模型">
          <a-button
            class="supported-models-refresh-button"
            size="small"
            type="text"
            aria-label="从上游同步可新增模型"
            :disabled="authorizedEditing"
            :loading="modelSyncing"
            @click.stop="$emit('refresh-models')"
          >
            <template #icon><SyncOutlined /></template>
          </a-button>
        </a-tooltip>
      </div>
    </template>
    <div class="supported-models-control">
      <a-select
        v-model:value="form.supportedModels"
        mode="multiple"
        :disabled="authorizedEditing"
        :loading="modelsLoading"
        option-filter-prop="label"
        placeholder="选择这个账户支持的模型"
        :options="modelOptions"
        show-search
        @dropdown-visible-change="$emit('model-options-open', $event)"
        @search="$emit('model-options-search', $event)"
      >
        <template v-if="modelsTruncated" #dropdownRender="{ menuNode }">
          <ModelOptionsTruncatedHint :menu-node="menuNode" />
        </template>
      </a-select>
    </div>
    <div v-if="availabilityHint" class="media-domain-model-empty-hint">{{ availabilityHint }}</div>
  </a-form-item>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { QuestionCircleOutlined, SyncOutlined } from '@ant-design/icons-vue'

import { ModelOptionsTruncatedHint } from '@/components/ModelOptionsTruncatedHint'
import type { AccountCapabilityDomain } from './accountCapabilityDomains'
import { mediaDomainModelAvailabilityHint } from './accountMediaFormBlocks'
import type { AccountFormModel } from './accountFormTypes'
import type { AccountModelSelectOption } from './accountEditFormPayload'

const props = withDefaults(defineProps<{
  authorizedEditing?: boolean
  capabilityDomain: AccountCapabilityDomain
  form: AccountFormModel
  modelOptions: AccountModelSelectOption[]
  modelSyncing?: boolean
  modelsLoading: boolean
  /** 当前目录 scope 下候选是否完成过至少一次加载（空态提示判据）。 */
  modelsSettled?: boolean
  /** 最近一次浏览取数命中 50 条窗口时在下拉面板底部提示可搜索。 */
  modelsTruncated?: boolean
}>(), {
  authorizedEditing: false,
  modelSyncing: false
})

defineEmits<{
  (event: 'model-options-open', open: boolean): void
  (event: 'model-options-search', value: string): void
  (event: 'refresh-models'): void
}>()

const availabilityHint = computed(() => mediaDomainModelAvailabilityHint({
  capabilityDomain: props.capabilityDomain,
  optionCount: props.modelOptions.length,
  loading: props.modelsLoading,
  settled: props.modelsSettled === true
}))
</script>

<style scoped>
.supported-models-label {
  display: flex;
  width: 100%;
  align-items: center;
  gap: 4px;
  min-width: 0;
}

.supported-models-label .supported-models-refresh-button {
  margin-left: auto;
}

:deep(.ant-form-item-label > label:has(.supported-models-label)) {
  width: 100%;
}

.media-model-help {
  color: rgba(0, 0, 0, 0.45);
  cursor: help;
}

.supported-models-control {
  display: flex;
  width: 100%;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.supported-models-control :deep(.ant-select) {
  flex: 1;
  min-width: 0;
}

.supported-models-refresh-button {
  width: 24px;
  height: 24px;
  padding: 0;
  color: rgba(0, 0, 0, 0.55);
  border-radius: 6px;
}

.supported-models-refresh-button :deep(.anticon) {
  font-size: 13px;
}

.media-domain-model-empty-hint {
  margin-top: 8px;
  color: var(--juhe-muted);
  font-size: 12px;
  line-height: 1.5;
}
</style>

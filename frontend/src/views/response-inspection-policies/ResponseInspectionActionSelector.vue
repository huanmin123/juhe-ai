<template>
  <div class="response-action-option-grid">
    <button
      v-for="template in responseInspectionActionTemplates"
      :key="template.action"
      class="response-action-option"
      :class="{ active: model === template.action }"
      type="button"
      :disabled="disabled"
      @click="selectAction(template.action)"
    >
      <span class="response-action-option-title">
        <span class="response-action-option-dot" />
        <strong>{{ template.label }}</strong>
        <a-tag :color="actionTagColor(template)">{{ actionTagText(template) }}</a-tag>
      </span>
      <span class="response-action-option-description">{{ template.description }}</span>
    </button>
  </div>
</template>

<script setup lang="ts">
import type { ResponseInspectionPolicyAction } from '@/types/domain'
import {
  responseInspectionActionTemplates,
  type ResponseInspectionActionTemplate
} from './responseInspectionActionTemplates'

const model = defineModel<ResponseInspectionPolicyAction>({ required: true })

const props = withDefaults(defineProps<{
  disabled?: boolean
}>(), {
  disabled: false
})

function selectAction(action: ResponseInspectionPolicyAction): void {
  if (props.disabled) return
  model.value = action
}

function actionTagText(template: ResponseInspectionActionTemplate): string {
  if (template.action === 'observe') return '观察'
  if (template.action === 'drop_event') return '不重试'
  if (template.runtimeAvoidance) return '短期避让'
  return '重试'
}

function actionTagColor(template: ResponseInspectionActionTemplate): string {
  if (template.action === 'observe') return 'gold'
  if (template.action === 'drop_event') return 'default'
  if (template.runtimeAvoidance) return 'orange'
  return 'green'
}
</script>

<style scoped>
.response-action-option-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}

.response-action-option {
  display: flex;
  min-height: 78px;
  flex-direction: column;
  gap: 7px;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  background: #fff;
  padding: 10px 12px;
  color: inherit;
  cursor: pointer;
  text-align: left;
  transition: border-color 0.16s ease, background 0.16s ease, box-shadow 0.16s ease;
}

.response-action-option:hover:not(:disabled) {
  border-color: #91caff;
  background: #f8fbff;
}

.response-action-option.active {
  border-color: #1677ff;
  background: #f0f7ff;
  box-shadow: inset 0 0 0 1px rgba(22, 119, 255, 0.18);
}

.response-action-option:disabled {
  cursor: default;
}

.response-action-option-title {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.response-action-option-title strong {
  overflow: hidden;
  color: #111827;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.response-action-option-dot {
  width: 10px;
  height: 10px;
  flex: 0 0 auto;
  border: 2px solid #cbd5e1;
  border-radius: 50%;
  background: #fff;
}

.response-action-option.active .response-action-option-dot {
  border-color: #1677ff;
  box-shadow: inset 0 0 0 2px #fff;
  background: #1677ff;
}

.response-action-option-description {
  color: #64748b;
  font-size: 12px;
  line-height: 18px;
}

@media (max-width: 720px) {
  .response-action-option-grid {
    grid-template-columns: 1fr;
  }
}
</style>

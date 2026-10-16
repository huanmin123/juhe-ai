<template>
  <a-popover v-model:open="open" trigger="click" placement="bottomRight" :overlay-style="{ width: '360px', maxWidth: 'calc(100vw - 24px)' }">
    <template #content>
      <ModelQualityConfigForm
        :is-management-view="isManagementView"
        :loading="loading"
        :policy="policy"
        :saving="saving"
        :scope-locked="scopeLocked"
        @cancel="open = false"
        @save="emit('save', $event)"
      />
    </template>
    <a-tooltip title="手动检测质量配置">
      <a-button aria-label="手动检测质量配置" :disabled="disabled" @click="emit('open')">
        <template #icon><SettingOutlined /></template>
      </a-button>
    </a-tooltip>
  </a-popover>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { SettingOutlined } from '@ant-design/icons-vue'
import type { ModelQualityPolicyUpdateInput } from '@/types/domain'
import ModelQualityConfigForm from './ModelQualityConfigForm.vue'

defineProps<{
  disabled?: boolean
  isManagementView: boolean
  loading: boolean
  policy: import('@/types/domain').ModelQualityPolicy
  saving: boolean
  scopeLocked?: boolean
}>()
const emit = defineEmits<{
  (event: 'open'): void
  (event: 'save', value: ModelQualityPolicyUpdateInput): void
}>()
const open = ref(false)
</script>

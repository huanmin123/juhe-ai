<template>
  <a-select
    v-model:value="value"
    mode="tags"
    :loading="props.loading"
    :options="selectOptions"
    :max-tag-count="4"
    allow-clear
    placeholder="输入或选择标签"
    @dropdown-visible-change="$emit('dropdown-visible-change', $event)"
  >
    <template #option="option">
      <div class="account-tag-option">
        <span class="account-tag-option-name">{{ option.label }}</span>
        <a-tooltip :title="deleteTooltip(option)">
          <a-button
            class="account-tag-delete"
            danger
            size="small"
            type="text"
            :disabled="option.accountCount > 0"
            :loading="props.deletingTagId === option.tagId"
            @click.stop.prevent="requestDelete(option)"
            @mousedown.stop.prevent
          >
            <template #icon><DeleteOutlined /></template>
          </a-button>
        </a-tooltip>
      </div>
    </template>
  </a-select>
</template>

<script setup lang="ts">
import { DeleteOutlined } from '@ant-design/icons-vue'
import { computed } from 'vue'

import type { AccountTagSummary } from '@/types/domain'

type AccountTagOption = {
  label: string
  value: string
  tagId: string
  accountCount: number
}

const value = defineModel<string[]>('value', { required: true })

const props = defineProps<{
  deletingTagId?: string
  loading: boolean
  options: AccountTagSummary[]
}>()

const emit = defineEmits<{
  (event: 'delete', tagId: string): void
  (event: 'dropdown-visible-change', open: boolean): void
}>()

const selectOptions = computed<AccountTagOption[]>(() => props.options.map((tag) => ({
  label: tag.name,
  value: tag.name,
  tagId: tag.id,
  accountCount: tag.accountCount ?? 0
})))

function requestDelete(option: AccountTagOption): void {
  if (!option.tagId || option.accountCount > 0) return
  emit('delete', option.tagId)
}

function deleteTooltip(option: AccountTagOption): string {
  if (option.accountCount > 0) return `已绑定 ${option.accountCount} 个账户，不能删除`
  return '删除标签'
}
</script>

<style scoped>
.account-tag-option {
  display: flex;
  min-width: 0;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.account-tag-option-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.account-tag-delete {
  flex: none;
}
</style>

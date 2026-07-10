<template>
  <div v-if="selectedCount" class="batch-toolbar">
    <div class="batch-toolbar-info">
      <span>已选择 {{ selectedCount }} 个账户</span>
      <span class="batch-toolbar-hint">批量操作会按当前选择逐个执行</span>
    </div>
    <div class="batch-toolbar-actions">
      <a-button @click="$emit('clear')">清空选择</a-button>
      <a-tooltip :title="editDisabled ? editDisabledReason : '批量覆盖所选账户的公共配置'">
        <span>
          <a-button type="primary" :disabled="editDisabled" @click="$emit('edit')">
            <template #icon><EditOutlined /></template>
            批量编辑
          </a-button>
        </span>
      </a-tooltip>
      <a-button @click="$emit('restore')">批量恢复</a-button>
      <a-button @click="$emit('enable')">批量启用</a-button>
      <a-button danger @click="$emit('disable')">批量停用</a-button>
      <a-button danger :disabled="deletableCount <= 0" @click="$emit('delete')">批量删除</a-button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { EditOutlined } from '@ant-design/icons-vue'

defineProps<{
  deletableCount: number
  editDisabled: boolean
  editDisabledReason: string
  selectedCount: number
}>()

defineEmits<{
  (event: 'clear'): void
  (event: 'delete'): void
  (event: 'disable'): void
  (event: 'edit'): void
  (event: 'enable'): void
  (event: 'restore'): void
}>()
</script>

<style scoped>
.batch-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
  padding: 14px 16px;
  margin-bottom: 16px;
  border: 1px solid #dbeafe;
  border-radius: 8px;
  background: #f8fbff;
}

.batch-toolbar-info {
  display: flex;
  flex-direction: column;
  gap: 4px;
  color: #1d4ed8;
  font-weight: 600;
}

.batch-toolbar-hint {
  color: #64748b;
  font-size: 12px;
  font-weight: 400;
}

.batch-toolbar-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
}
</style>

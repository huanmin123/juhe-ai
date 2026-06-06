<template>
  <div v-if="selectedCount" class="batch-toolbar">
    <div class="batch-toolbar-info">
      <span>已选择 {{ selectedCount }} 个账户</span>
      <span class="batch-toolbar-hint">批量操作会按当前选择逐个执行</span>
    </div>
    <div class="batch-toolbar-actions">
      <a-button @click="$emit('clear')">清空选择</a-button>
      <a-button type="primary" @click="$emit('test')">批量测试</a-button>
      <a-button @click="$emit('restore')">批量恢复</a-button>
      <a-button @click="$emit('enable')">批量启用</a-button>
      <a-button danger @click="$emit('disable')">批量停用</a-button>
      <a-popconfirm
        :title="`确认删除已选择的 ${deletableCount} 个可删除账户？`"
        ok-text="删除"
        cancel-text="取消"
        :disabled="deletableCount <= 0"
        @confirm="$emit('delete')"
      >
        <a-button danger :disabled="deletableCount <= 0">批量删除</a-button>
      </a-popconfirm>
    </div>
  </div>
</template>

<script setup lang="ts">
defineProps<{
  deletableCount: number
  selectedCount: number
}>()

defineEmits<{
  (event: 'clear'): void
  (event: 'delete'): void
  (event: 'disable'): void
  (event: 'enable'): void
  (event: 'restore'): void
  (event: 'test'): void
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
  border-radius: 14px;
  background: linear-gradient(180deg, #eff6ff 0%, #ffffff 100%);
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

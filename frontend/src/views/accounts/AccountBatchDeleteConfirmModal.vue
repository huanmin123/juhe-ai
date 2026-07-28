<template>
  <a-modal
    :open="open"
    title="确认批量删除账户"
    ok-text="删除"
    cancel-text="取消"
    :confirm-loading="loading"
    :ok-button-props="{ danger: true, disabled: !accounts.length }"
    @cancel="emit('cancel')"
    @ok="emit('ok')"
    @update:open="emit('update:open', $event)"
  >
    <div class="batch-delete-confirm">
      <p class="batch-delete-summary">将删除以下 {{ accounts.length }} 个账户：</p>
      <div class="batch-delete-list">
        <div v-for="account in accounts" :key="account.id" class="batch-delete-item">
          <span class="batch-delete-name">{{ accountDisplayName(account) }}</span>
          <span class="batch-delete-meta">
            <span>{{ providerName(account.providerCode) }}</span>
            <span v-if="isManagementView && account.systemAccountName">系统账户：{{ account.systemAccountName }}</span>
          </span>
        </div>
      </div>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import type { AccountListItem } from '@/types/domain'
import { accountDisplayName } from './accountBasicFormatters'

defineProps<{
  accounts: AccountListItem[]
  isManagementView: boolean
  loading: boolean
  open: boolean
  providerName: (providerCode: string) => string
}>()

const emit = defineEmits<{
  (event: 'cancel'): void
  (event: 'ok'): void
  (event: 'update:open', open: boolean): void
}>()
</script>

<style scoped>
.batch-delete-confirm {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.batch-delete-summary {
  margin: 0;
  color: #0f172a;
}

.batch-delete-list {
  max-height: 320px;
  overflow: auto;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
}

.batch-delete-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 12px;
  border-bottom: 1px solid #eef2f7;
}

.batch-delete-item:last-child {
  border-bottom: 0;
}

.batch-delete-name {
  min-width: 0;
  overflow: hidden;
  color: #0f172a;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.batch-delete-meta {
  display: flex;
  flex-shrink: 0;
  gap: 8px;
  color: #64748b;
  font-size: 12px;
  white-space: nowrap;
}

@media (max-width: 900px) {
  .batch-delete-item {
    align-items: flex-start;
    flex-direction: column;
  }

  .batch-delete-meta {
    flex-wrap: wrap;
    white-space: normal;
  }
}
</style>

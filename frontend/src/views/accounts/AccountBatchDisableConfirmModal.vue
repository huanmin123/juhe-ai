<template>
  <a-modal
    :open="open"
    title="确认批量停用账户"
    ok-text="停用"
    cancel-text="取消"
    :confirm-loading="loading"
    :ok-button-props="{ danger: true, disabled: !accounts.length }"
    @cancel="emit('cancel')"
    @ok="emit('ok')"
    @update:open="emit('update:open', $event)"
  >
    <div class="batch-disable-confirm">
      <a-alert
        type="warning"
        show-icon
        :message="`将停用 ${accounts.length} 个账户`"
        description="停用后，这些账户将不再参与请求调度。"
      />
      <div class="batch-disable-list">
        <div v-for="account in accounts" :key="account.id" class="batch-disable-item">
          {{ accountDisplayName(account) }}
        </div>
      </div>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import type { AccountListItem } from '@/types/domain'
import { accountDisplayName } from './accountBasicFormatters'

defineProps<{ accounts: AccountListItem[]; loading: boolean; open: boolean }>()

const emit = defineEmits<{
  (event: 'cancel'): void
  (event: 'ok'): void
  (event: 'update:open', open: boolean): void
}>()
</script>

<style scoped>
.batch-disable-confirm {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.batch-disable-list {
  max-height: 260px;
  overflow: auto;
  border: 1px solid #e5e7eb;
  border-radius: 6px;
}

.batch-disable-item {
  padding: 9px 12px;
  overflow: hidden;
  border-bottom: 1px solid #eef2f7;
  color: #334155;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.batch-disable-item:last-child {
  border-bottom: 0;
}
</style>

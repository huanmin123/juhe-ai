<template>
  <a-modal
    :open="open"
    title="迁移流量"
    width="560px"
    :confirm-loading="saving"
    :ok-button-props="{ type: 'primary', danger: sourceStatus === 'disabled', disabled: !sourceAccount || !targetAccountId || !targetOptions.length }"
    ok-text="确认迁移"
    cancel-text="取消"
    @ok="$emit('save')"
    @update:open="$emit('update:open', $event)"
  >
    <a-form layout="vertical">
      <a-alert
        class="form-alert"
        type="warning"
        show-icon
        message="会把当前命中该账户的客户端会话切到目标账户，并立刻让当前账户退出调度。"
      />
      <a-form-item label="当前账户">
        <a-input :value="sourceAccount?.name || '-'" readonly />
      </a-form-item>
      <a-form-item label="目标账户" required>
        <a-select
          :value="targetAccountId"
          :options="targetOptions"
          placeholder="请选择同供应商的可用账户"
          show-search
          :filter-option="filterOption"
          @update:value="$emit('update:targetAccountId', String($event))"
        />
        <div class="form-help">只显示同一系统账户、同一供应商、同一分组下处于正常状态且可调度的账户。</div>
      </a-form-item>
      <a-form-item label="迁移后原账户状态">
        <a-radio-group :value="sourceStatus" @update:value="handleSourceStatusChange">
          <a-radio value="temporary_unavailable">临时不可调用</a-radio>
          <a-radio value="disabled">停用账户</a-radio>
        </a-radio-group>
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import type { AccountSummary, AccountTrafficMigrationSourceStatus } from '@/types/domain'

defineProps<{
  open: boolean
  saving: boolean
  sourceAccount?: AccountSummary
  targetAccountId: string
  targetOptions: Array<{ label: string; value: string }>
  sourceStatus: AccountTrafficMigrationSourceStatus
}>()

const emit = defineEmits<{
  (event: 'save'): void
  (event: 'update:open', value: boolean): void
  (event: 'update:targetAccountId', value: string): void
  (event: 'update:sourceStatus', value: AccountTrafficMigrationSourceStatus): void
}>()

function filterOption(input: string, option?: { label?: string | number }) {
  return String(option?.label ?? '').toLowerCase().includes(input.trim().toLowerCase())
}

function handleSourceStatusChange(value: unknown) {
  if (value === 'temporary_unavailable' || value === 'disabled') {
    emit('update:sourceStatus', value)
  }
}
</script>

<style scoped>
.form-alert {
  border-radius: 12px;
}

.form-help {
  margin-top: 4px;
  color: #64748b;
  font-size: 12px;
}
</style>

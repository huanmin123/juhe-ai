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
        :message="isAuthorizedSource ? '只影响你自己分组内的授权账户调度；不会修改账户所有者的原账户配置。' : '不会打断当前正在输出的连接；当前请求继续跑完，从下一次请求开始切到目标账户。'"
      />
      <a-form-item label="当前账户">
        <a-input :value="sourceAccount?.name || '-'" readonly />
      </a-form-item>
      <a-form-item label="目标账户" required>
        <AccountSelect
          :value="targetAccountId"
          :selected-account="targetAccount"
          :options="targetOptions"
          placeholder="请选择同供应商的可用账户"
          show-search
          :filter-option="filterOption"
          @update:value="handleTargetAccountIdUpdate"
          @update:selected-account="$emit('update:targetAccount', $event)"
        />
        <div class="form-help">{{ isAuthorizedSource ? '只显示你当前同一分组下处于正常状态且可调度的授权账户。' : '只显示同一系统账户、同一供应商、同一分组下处于正常状态且可调度的账户。' }}</div>
      </a-form-item>
      <a-form-item :label="isAuthorizedSource ? '迁移后当前授权实例状态' : '迁移后原账户状态'">
        <a-radio-group :value="sourceStatus" @update:value="handleSourceStatusChange">
          <a-radio value="temporary_unavailable">临时不可调用</a-radio>
          <a-radio value="disabled">停用账户</a-radio>
        </a-radio-group>
        <div class="form-help">{{ isAuthorizedSource ? '该状态只更新你自己的授权实例账户；不会停用、冷却或修改账户所有者的原账户。' : '迁移只影响后续请求；已经建立的流式输出不会被这次操作中断。' }}</div>
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import AccountSelect from '@/components/AccountSelect.vue'
import type { AccountSelection } from '@/shared/accountLabelCache'
import type { AccountSummary, AccountTrafficMigrationSourceStatus } from '@/types/domain'

const props = defineProps<{
  open: boolean
  saving: boolean
  sourceAccount?: AccountSummary
  targetAccountId: string
  targetAccount?: AccountSelection
  targetOptions: Array<{ label: string; value: string }>
  sourceStatus: AccountTrafficMigrationSourceStatus
}>()

const isAuthorizedSource = computed(() => props.sourceAccount?.accessType === 'authorized')

const emit = defineEmits<{
  (event: 'save'): void
  (event: 'update:open', value: boolean): void
  (event: 'update:targetAccountId', value: string): void
  (event: 'update:targetAccount', value: AccountSelection | undefined): void
  (event: 'update:sourceStatus', value: AccountTrafficMigrationSourceStatus): void
}>()

function filterOption(input: string, option?: { label?: string | number }) {
  const keyword = input.trim().toLowerCase()
  if (!keyword) return true
  const label = String(option?.label ?? '').toLowerCase()
  return label === keyword || label.startsWith(keyword)
}

function handleSourceStatusChange(value: unknown) {
  if (value === 'temporary_unavailable' || value === 'disabled') {
    emit('update:sourceStatus', value)
  }
}

function handleTargetAccountIdUpdate(value: string | string[] | undefined) {
  emit('update:targetAccountId', typeof value === 'string' ? value : '')
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

<template>
  <a-modal
    :open="open"
    title="迁移流量"
    width="560px"
    :confirm-loading="saving"
    :ok-button-props="{ type: 'primary', danger: sourceStatus === 'disabled', disabled: !sourceAccount || !targetAccountId || !targetOptions.length }"
    :ok-text="sourceStatus === 'unchanged' ? '迁移客户端' : '确认迁移'"
    cancel-text="取消"
    @ok="$emit('save')"
    @update:open="$emit('update:open', $event)"
  >
    <a-form layout="vertical">
      <a-alert
        class="form-alert"
        type="warning"
        show-icon
        :message="migrationAlertMessage"
      />
      <a-form-item label="当前账户">
        <a-input :value="sourceAccount?.name || '-'" readonly />
      </a-form-item>
      <a-form-item
        label="目标账户"
        required
        :tooltip="isAuthorizedSource ? '只显示你当前同一分组下处于正常状态且可调度的授权账户。' : '只显示同一系统账户、同一分组下处于正常状态且可调度的账户。'"
      >
        <AccountSelect
          :value="targetAccountId"
          :selected-account="targetAccount"
          :options="targetOptions"
          placeholder="请选择可迁移的可用账户"
          show-search
          :filter-option="filterOption"
          @update:value="handleTargetAccountIdUpdate"
          @update:selected-account="$emit('update:targetAccount', $event)"
        />
      </a-form-item>
      <a-form-item :label="isAuthorizedSource ? '迁移后当前授权实例状态' : '迁移后原账户状态'" :tooltip="sourceStatusHelpText">
        <a-radio-group :value="sourceStatus" @update:value="handleSourceStatusChange">
          <a-radio value="unchanged">不影响原账户</a-radio>
          <a-radio value="temporary_unavailable">临时不可调用</a-radio>
          <a-radio value="disabled">停用账户</a-radio>
        </a-radio-group>
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
const migrationAlertMessage = computed(() => {
  if (props.sourceStatus === 'unchanged') {
    return '不会打断当前正在输出的连接；只把当前已识别的客户端会话迁到目标账户。'
  }
  return isAuthorizedSource.value
    ? '只影响你自己分组内的授权账户调度；从下一次请求开始短期优先切到目标账户。'
    : '不会打断当前正在输出的连接；从下一次请求开始短期优先切到目标账户。'
})
const sourceStatusHelpText = computed(() => {
  if (props.sourceStatus === 'unchanged') {
    return '只把已识别且当前命中该账户的客户端会话迁到目标账户，不修改原账户状态，也不影响新客户端正常调度。'
  }
  if (isAuthorizedSource.value) {
    return '该状态只更新你自己的授权实例账户；不会停用、冷却或修改账户所有者的原账户。'
  }
  return '迁移是短期最高排序覆盖；目标不可用、硬并发已满或不支持本次请求时按候选顺序降级。'
})

const emit = defineEmits<{
  (event: 'save'): void
  (event: 'update:open', value: boolean): void
  (event: 'update:targetAccountId', value: string): void
  (event: 'update:targetAccount', value: AccountSelection | undefined): void
  (event: 'update:sourceStatus', value: AccountTrafficMigrationSourceStatus): void
}>()

function filterOption(input: string, option?: { label?: string | number }) {
  const keyword = input.trim()
  if (!keyword) return true
  const label = String(option?.label ?? '')
  return label === keyword || label.startsWith(keyword)
}

function handleSourceStatusChange(value: unknown) {
  if (value === 'unchanged' || value === 'temporary_unavailable' || value === 'disabled') {
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

</style>

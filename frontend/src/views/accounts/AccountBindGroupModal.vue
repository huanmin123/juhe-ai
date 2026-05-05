<template>
  <a-modal
    :open="open"
    title="绑定分组"
    width="520px"
    :confirm-loading="saving"
    :ok-button-props="{ type: 'primary', disabled: !groupId || !groupOptions.length }"
    @ok="$emit('save')"
    @update:open="$emit('update:open', $event)"
  >
    <a-form layout="vertical">
      <a-alert class="form-alert" type="info" show-icon :message="tip" />
      <a-form-item label="授权账户">
        <a-input :value="account?.name || '-'" readonly />
      </a-form-item>
      <a-form-item label="绑定到我的分组" required>
        <a-select :value="groupId" :options="groupOptions" placeholder="请选择同供应商分组" @update:value="$emit('update:groupId', String($event))" />
        <div class="form-help">API Key 只能调用绑定分组内的账户；授权账户需要先加入你的分组。</div>
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import type { AccountSummary } from '@/types/domain'

defineProps<{
  account?: AccountSummary
  groupId: string
  groupOptions: Array<{ label: string; value: string }>
  open: boolean
  saving: boolean
  tip: string
}>()

defineEmits<{
  (event: 'save'): void
  (event: 'update:groupId', value: string): void
  (event: 'update:open', value: boolean): void
}>()
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

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
        <a-select
          :value="groupId"
          show-search
          :filter-option="false"
          :loading="groupOptionsLoading"
          :options="groupOptions"
          placeholder="输入分组名称或 ID 前缀"
          @dropdown-visible-change="$emit('group-options-dropdown', $event)"
          @search="$emit('group-options-search', $event)"
          @update:value="$emit('update:groupId', String($event))"
        />
        <div class="form-help">同一分组可混合 OAuth / API Key 账户；API Key 只按绑定分组调度，统计、会话亲和和缓存按本地 API Key 与分组连续。</div>
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
  groupOptionsLoading: boolean
  open: boolean
  saving: boolean
  tip: string
}>()

defineEmits<{
  (event: 'save'): void
  (event: 'group-options-dropdown', open: boolean): void
  (event: 'group-options-search', value: string): void
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

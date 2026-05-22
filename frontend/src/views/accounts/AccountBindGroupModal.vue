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
          placeholder="输入分组名称"
          @dropdown-visible-change="$emit('group-options-dropdown', $event)"
          @search="$emit('group-options-search', $event)"
          @update:value="$emit('update:groupId', String($event))"
        />
        <div class="form-help">同一分组可混合 OAuth / API Key 账户；API Key 只按绑定分组调度，统计、会话亲和和缓存按本地 API Key 与分组连续。</div>
      </a-form-item>
      <a-form-item v-if="softConcurrencyVisible" label="绑定权重">
        <a-input-number
          :value="dispatchWeight"
          :min="1"
          :max="1000"
          style="width: 100%"
          @update:value="$emit('update:dispatchWeight', numericOrDefault($event, 1))"
        />
        <div class="form-help">权重越高，当前账户在这个高并发分组中承担的排队份额越多。</div>
      </a-form-item>
      <a-form-item v-if="softConcurrencyVisible" label="绑定单账户排队阈值">
        <a-input-number
          :value="softConcurrencyLimit"
          :min="1"
          :max="account?.concurrencyLimit || 1000000"
          allow-clear
          style="width: 100%"
          placeholder="留空使用分组阈值"
          @update:value="$emit('update:softConcurrencyLimit', numericOrNull($event))"
        />
        <div class="form-help">只影响当前账户在这个高并发分组里的切换阈值，实际值不会超过账户自己的并发上限。</div>
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import type { AccountSummary } from '@/types/domain'

defineProps<{
  account?: AccountSummary
  dispatchWeight: number
  groupId: string
  groupOptions: Array<{ label: string; value: string }>
  groupOptionsLoading: boolean
  open: boolean
  saving: boolean
  softConcurrencyLimit: number | null
  softConcurrencyVisible: boolean
  tip: string
}>()

defineEmits<{
  (event: 'save'): void
  (event: 'group-options-dropdown', open: boolean): void
  (event: 'group-options-search', value: string): void
  (event: 'update:dispatchWeight', value: number): void
  (event: 'update:groupId', value: string): void
  (event: 'update:open', value: boolean): void
  (event: 'update:softConcurrencyLimit', value: number | null): void
}>()

function numericOrDefault(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? Math.trunc(value) : fallback
}

function numericOrNull(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? Math.trunc(value) : null
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

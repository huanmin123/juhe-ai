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
      <a-form-item label="绑定到我的分组" required tooltip="绑定后按目标分组的调度配置执行；账户绑定不再单独配置权重或排队阈值。">
        <GroupSelect
          :value="groupId"
          :selected-group="group"
          :filter-option="false"
          :loading="groupOptionsLoading"
          :options="groupOptions"
          placeholder="输入分组名称"
          @dropdown-visible-change="$emit('group-options-dropdown', $event)"
          @search="$emit('group-options-search', $event)"
          @update:selected-group="$emit('update:groupSelection', $event)"
          @update:value="$emit('update:groupId', String($event))"
        />
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import GroupSelect from '@/components/GroupSelect.vue'
import type { GroupSelection } from '@/shared/groupLabelCache'
import type { AccountListItem } from '@/types/domain'

defineProps<{
  account?: AccountListItem
  groupId: string
  group?: GroupSelection
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
  (event: 'update:groupSelection', value?: GroupSelection): void
  (event: 'update:open', value: boolean): void
}>()
</script>

<style scoped>
.form-alert {
  border-radius: 12px;
}

</style>

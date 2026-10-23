<template>
  <a-form-item label="加入分组" :required="required">
    <div @pointerdown.capture="markGroupDropdownRequested" @keydown.capture="markGroupDropdownRequested">
      <GroupSelect
        v-model:value="form.groupId"
        v-model:selected-group="form.group"
        :filter-option="false"
        :open="groupDropdownOpen"
        :loading="groupOptionsLoading"
        :options="groupOptions"
        placeholder="输入分组名称"
        @dropdown-visible-change="handleGroupDropdownVisibleChange"
        @search="$emit('group-options-search', $event)"
      />
    </div>
  </a-form-item>
</template>

<script setup lang="ts">
// 「加入分组」字段：创建向导第 1 步与编辑态常用配置区（bench 变体）共用。
// 内置防误开守卫：pointerdown/keydown capture 标记用户真实交互意图，程序化触发的
// dropdown-visible-change(open) 未经意图标记时不放行展开；供应商切换时关闭并复位守卫。
import { ref, watch } from 'vue'

import GroupSelect from '@/components/GroupSelect.vue'
import type { AccountFormModel } from './accountFormTypes'

const props = withDefaults(defineProps<{
  form: AccountFormModel
  groupOptions: Array<{ label: string; value: string }>
  groupOptionsLoading: boolean
  /** 分组为创建/编辑均必选项，恒为 true；保留 prop 以显式表达契约。 */
  required?: boolean
}>(), {
  required: true
})

const emit = defineEmits<{
  (event: 'group-options-dropdown', open: boolean): void
  (event: 'group-options-search', value: string): void
}>()

const groupDropdownOpen = ref(false)
const groupDropdownRequested = ref(false)

watch(
  () => props.form.providerCode,
  () => {
    groupDropdownOpen.value = false
    groupDropdownRequested.value = false
  }
)

function markGroupDropdownRequested(): void {
  groupDropdownRequested.value = true
}

function handleGroupDropdownVisibleChange(open: boolean): void {
  if (open && !groupDropdownRequested.value) {
    groupDropdownOpen.value = false
    return
  }
  groupDropdownOpen.value = open
  if (!open) groupDropdownRequested.value = false
  emit('group-options-dropdown', open)
}
</script>

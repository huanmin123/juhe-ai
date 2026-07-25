<template>
  <div class="account-priority-editor" :class="{ 'account-priority-editor-mobile': mobile }">
    <template v-if="editing">
      <a-input-number
        ref="inputRef"
        v-model:value="draftPriority"
        class="account-priority-input"
        :controls="false"
        :min="0"
        :precision="0"
        size="small"
        @keydown.esc.stop="cancel"
        @press-enter="save"
      />
      <a-button
        aria-label="保存优先级"
        class="account-priority-action"
        :disabled="saving"
        :loading="saving"
        size="small"
        type="text"
        @click="save"
      >
        <CheckOutlined />
      </a-button>
      <a-button
        aria-label="取消修改优先级"
        class="account-priority-action"
        :disabled="saving"
        size="small"
        type="text"
        @click="cancel"
      >
        <CloseOutlined />
      </a-button>
    </template>
    <a-tooltip v-else :title="editable ? '点击修改调度优先级；数字越小越优先。' : '调度优先级：数字越小越优先。'">
      <a-button
        v-if="editable"
        class="account-priority-trigger"
        size="small"
        type="link"
        @click="startEditing"
      >
        <span>{{ priority }}</span>
        <EditOutlined class="account-priority-edit-icon" />
      </a-button>
      <span v-else class="account-priority-readonly">{{ priority }}</span>
    </a-tooltip>
  </div>
</template>

<script setup lang="ts">
import { CheckOutlined, CloseOutlined, EditOutlined } from '@ant-design/icons-vue'
import { nextTick, ref, watch } from 'vue'

import { message } from '@/lib/antd'

const props = defineProps<{
  editable: boolean
  mobile?: boolean
  priority: number
  savePriority: (priority: number) => Promise<boolean>
}>()

const inputRef = ref<{ focus?: () => void }>()
const editing = ref(false)
const saving = ref(false)
const draftPriority = ref<number | null>(props.priority)

watch(
  () => props.priority,
  (priority) => {
    if (!editing.value) draftPriority.value = priority
  }
)

function startEditing(): void {
  if (!props.editable || saving.value) return
  draftPriority.value = props.priority
  editing.value = true
  void nextTick(() => inputRef.value?.focus?.())
}

function cancel(): void {
  if (saving.value) return
  draftPriority.value = props.priority
  editing.value = false
}

async function save(): Promise<void> {
  if (saving.value) return
  const priority = Number(draftPriority.value)
  if (!Number.isInteger(priority) || priority < 0) {
    message.warning('优先级必须是大于等于 0 的整数')
    return
  }
  if (priority === props.priority) {
    cancel()
    return
  }

  saving.value = true
  try {
    if (await props.savePriority(priority)) editing.value = false
  } finally {
    saving.value = false
  }
}
</script>

<style scoped>
.account-priority-editor {
  display: inline-flex;
  align-items: center;
  min-height: 24px;
}

.account-priority-trigger {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  height: 24px;
  padding: 0 2px;
  color: inherit;
  font: inherit;
}

.account-priority-trigger:hover,
.account-priority-trigger:focus-visible {
  color: #1677ff;
}

.account-priority-edit-icon {
  color: #94a3b8;
  font-size: 12px;
}

.account-priority-input {
  width: 58px;
}

.account-priority-action {
  width: 24px;
  padding-inline: 3px;
}

.account-priority-readonly {
  color: #475569;
}

.account-priority-editor-mobile {
  min-width: 0;
}

.account-priority-editor-mobile .account-priority-trigger {
  min-width: 0;
  padding: 0;
  font-weight: 600;
}
</style>

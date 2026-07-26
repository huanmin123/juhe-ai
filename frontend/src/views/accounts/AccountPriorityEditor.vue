<template>
  <div ref="editorRef" class="account-priority-editor" :class="{ 'account-priority-editor-mobile': mobile }">
    <div v-if="editing" class="account-priority-control" :class="{ 'is-saving': saving }">
      <a-input-number
        ref="inputRef"
        v-model:value="draftPriority"
        aria-label="账户优先级"
        :bordered="false"
        class="account-priority-input"
        :controls="false"
        :disabled="saving"
        :min="0"
        :precision="0"
        size="small"
        @keydown.esc.stop="cancel"
        @press-enter="save"
      />
      <button
        aria-label="保存优先级"
        class="account-priority-confirm"
        :disabled="saving"
        title="保存优先级"
        type="button"
        @click="save"
      >
        <LoadingOutlined v-if="saving" spin />
        <CheckOutlined v-else />
      </button>
    </div>
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
import { CheckOutlined, EditOutlined, LoadingOutlined } from '@ant-design/icons-vue'
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'

import { message } from '@/lib/antd'

const props = defineProps<{
  editable: boolean
  editing: boolean
  mobile?: boolean
  priority: number
  savePriority: (priority: number) => Promise<boolean>
}>()

const emit = defineEmits<{
  (event: 'cancel-edit'): void
  (event: 'start-edit'): void
}>()

const editorRef = ref<HTMLElement>()
const inputRef = ref<{ focus?: () => void }>()
const saving = ref(false)
const draftPriority = ref<number | null>(props.priority)

watch(
  () => props.priority,
  (priority) => {
    if (!props.editing) draftPriority.value = priority
  }
)

watch(
  () => props.editing,
  (editing) => {
    draftPriority.value = props.priority
    document.removeEventListener('pointerdown', handleDocumentPointerDown, true)
    if (editing) {
      document.addEventListener('pointerdown', handleDocumentPointerDown, true)
      void nextTick(() => inputRef.value?.focus?.())
    }
  }
)

function startEditing(): void {
  if (!props.editable || saving.value) return
  emit('start-edit')
}

function cancel(): void {
  if (saving.value) return
  draftPriority.value = props.priority
  emit('cancel-edit')
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
    if (await props.savePriority(priority)) emit('cancel-edit')
  } finally {
    saving.value = false
  }
}

function handleDocumentPointerDown(event: PointerEvent): void {
  if (!props.editing || saving.value) return
  const target = event.target
  if (target instanceof Node && !editorRef.value?.contains(target)) cancel()
}

onBeforeUnmount(() => document.removeEventListener('pointerdown', handleDocumentPointerDown, true))
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

.account-priority-control {
  position: relative;
  display: inline-flex;
  width: 85px;
  align-items: center;
  overflow: hidden;
  border: 1px solid #91caff;
  border-radius: 8px;
  background: #fff;
  box-shadow: 0 0 0 2px rgb(22 119 255 / 10%);
  transition: border-color 160ms ease, box-shadow 160ms ease;
}

.account-priority-control:focus-within {
  border-color: #1677ff;
  box-shadow: 0 0 0 2px rgb(22 119 255 / 14%);
}

.account-priority-control.is-saving {
  background: #f8fafc;
}

.account-priority-input {
  width: 100%;
  overflow: hidden;
  border-radius: 0;
  background: transparent;
  box-shadow: none !important;
}

.account-priority-input :deep(.ant-input-number-input-wrap) {
  border-radius: 0;
}

.account-priority-input :deep(.ant-input-number-input) {
  height: 26px;
  padding: 0 34px 0 7px;
  border-radius: 0;
  background: transparent;
}

.account-priority-confirm {
  position: absolute;
  z-index: 1;
  inset-block: 0;
  inset-inline-end: 0;
  display: inline-flex;
  width: 27px;
  height: auto;
  padding: 0;
  border: 0;
  border-left: 1px solid #dbeafe;
  border-radius: 0;
  align-items: center;
  justify-content: center;
  background: #eff6ff;
  color: #1677ff;
  cursor: pointer;
  font-size: 13px;
  transition: background-color 160ms ease, color 160ms ease;
}

.account-priority-confirm:hover:not(:disabled),
.account-priority-confirm:focus-visible {
  background: #1677ff;
  color: #fff;
  outline: none;
}

.account-priority-confirm:disabled {
  color: #94a3b8;
  cursor: wait;
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

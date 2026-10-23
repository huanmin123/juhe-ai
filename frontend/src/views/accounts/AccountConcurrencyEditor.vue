<template>
  <div ref="editorRef" class="account-concurrency-editor" :class="{ 'account-concurrency-editor-mobile': mobile }">
    <div v-if="editing" class="account-concurrency-control" :class="{ 'is-saving': saving }">
      <a-input-number
        ref="inputRef"
        v-model:value="draftLimit"
        aria-label="账户并发上限"
        :bordered="false"
        class="account-concurrency-input"
        :controls="false"
        :disabled="saving"
        :min="1"
        :precision="0"
        size="small"
        @keydown.esc.stop="cancel"
        @press-enter="save"
      />
      <button
        aria-label="保存并发上限"
        class="account-concurrency-confirm"
        :disabled="saving"
        title="保存并发上限"
        type="button"
        @click="save"
      >
        <LoadingOutlined v-if="saving" spin />
        <CheckOutlined v-else />
      </button>
    </div>
    <a-tag
      v-if="editable && !mobile"
      class="account-concurrency-trigger"
      color="blue"
      role="button"
      tabindex="0"
      @click="startEditing"
      @keydown.enter.prevent="startEditing"
    >{{ text }}</a-tag>
    <strong
      v-else-if="editable && mobile"
      class="account-concurrency-mobile-trigger"
      role="button"
      tabindex="0"
      @click="startEditing"
      @keydown.enter.prevent="startEditing"
    >{{ text }}</strong>
    <a-tag v-else-if="!mobile" color="blue">{{ text }}</a-tag>
    <strong v-else>{{ text }}</strong>
  </div>
</template>

<script setup lang="ts">
import { CheckOutlined, LoadingOutlined } from '@ant-design/icons-vue'
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'

import { message } from '@/lib/antd'

const props = defineProps<{
  currentConcurrency?: number
  editable: boolean
  editing: boolean
  limit: number
  mobile?: boolean
  saveLimit: (limit: number) => Promise<boolean>
}>()

const emit = defineEmits<{
  (event: 'cancel-edit'): void
  (event: 'start-edit'): void
}>()

const editorRef = ref<HTMLElement>()
const inputRef = ref<{ focus?: () => void }>()
const saving = ref(false)
const draftLimit = ref<number | null>(props.limit)
const currentConcurrencyText = computed(() => `${Math.max(0, props.currentConcurrency ?? 0)}`)
const text = computed(() => `${currentConcurrencyText.value}/${props.limit}`)

watch(
  () => props.limit,
  (limit) => {
    if (!props.editing) draftLimit.value = limit
  }
)

watch(
  () => props.editing,
  (editing) => {
    draftLimit.value = props.limit
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
  draftLimit.value = props.limit
  emit('cancel-edit')
}

async function save(): Promise<void> {
  if (saving.value) return
  const limit = Number(draftLimit.value)
  if (!Number.isInteger(limit) || limit < 1) {
    message.warning('并发上限必须是大于等于 1 的整数')
    return
  }
  if (limit === props.limit) {
    cancel()
    return
  }

  saving.value = true
  try {
    if (await props.saveLimit(limit)) emit('cancel-edit')
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
.account-concurrency-editor {
  display: inline-flex;
  align-items: center;
  min-height: 24px;
}

.account-concurrency-trigger {
  margin-inline-end: 0;
  cursor: pointer;
}

.account-concurrency-trigger:hover,
.account-concurrency-trigger:focus-visible {
  border-color: var(--juhe-accent);
  color: var(--juhe-accent);
  background-color: var(--juhe-accent-soft);
}

.account-concurrency-mobile-trigger {
  overflow: hidden;
  color: var(--juhe-fg);
  font-size: 13px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: pointer;
}

.account-concurrency-mobile-trigger:hover,
.account-concurrency-mobile-trigger:focus-visible {
  color: var(--juhe-accent);
}

.account-concurrency-control {
  position: relative;
  display: inline-flex;
  width: 85px;
  align-items: center;
  overflow: hidden;
  border: 1px solid var(--juhe-border-strong);
  border-radius: 8px;
  background: var(--juhe-surface);
  box-shadow: 0 0 0 2px rgba(83, 105, 107, .10);
  transition: border-color 160ms ease, box-shadow 160ms ease;
}

.account-concurrency-control:focus-within {
  border-color: var(--juhe-accent);
  box-shadow: 0 0 0 2px rgba(83, 105, 107, .14);
}

.account-concurrency-control.is-saving {
  background: var(--juhe-surface-soft);
}

.account-concurrency-input {
  width: 100%;
  overflow: hidden;
  border-radius: 0;
  background: transparent;
  box-shadow: none !important;
}

.account-concurrency-input :deep(.ant-input-number-input-wrap) {
  border-radius: 0;
}

.account-concurrency-input :deep(.ant-input-number-input) {
  height: 26px;
  padding: 0 34px 0 7px;
  border-radius: 0;
  background: transparent;
}

.account-concurrency-confirm {
  position: absolute;
  z-index: 1;
  inset-block: 0;
  inset-inline-end: 0;
  display: inline-flex;
  width: 27px;
  height: auto;
  padding: 0;
  border: 0;
  border-left: 1px solid var(--juhe-border-strong);
  border-radius: 0;
  align-items: center;
  justify-content: center;
  background: var(--juhe-accent-soft);
  color: var(--juhe-accent);
  cursor: pointer;
  font-size: 13px;
  transition: background-color 160ms ease, color 160ms ease;
}

.account-concurrency-confirm:hover:not(:disabled),
.account-concurrency-confirm:focus-visible {
  background: var(--juhe-accent);
  color: var(--juhe-fg-invert);
  outline: none;
}

.account-concurrency-confirm:disabled {
  color: var(--juhe-faint);
  cursor: wait;
}

.account-concurrency-editor-mobile {
  min-width: 0;
}
</style>

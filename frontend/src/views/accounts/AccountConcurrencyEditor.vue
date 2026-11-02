<template>
  <div ref="editorRef" class="account-concurrency-editor" :class="{ 'account-concurrency-editor-mobile': mobile }">
    <!-- 编辑态：两个输入 + 分隔线 + 确认按钮组成一个整体控件（并发上限 | 每分钟请求上限）。 -->
    <div v-if="editing" class="account-concurrency-control" :class="{ 'is-saving': saving }">
      <a-input-number
        ref="concurrencyInputRef"
        v-model:value="draftConcurrencyLimit"
        aria-label="并发上限"
        :bordered="false"
        class="account-concurrency-input"
        :controls="false"
        :disabled="saving"
        :min="1"
        :max="9999"
        :precision="0"
        size="small"
        @keydown.esc.stop="cancel"
        @press-enter="save"
      />
      <span class="account-concurrency-divider" aria-hidden="true"></span>
      <a-input-number
        v-model:value="draftRpmLimit"
        aria-label="每分钟请求上限"
        :bordered="false"
        class="account-concurrency-input"
        :controls="false"
        :disabled="saving"
        :min="0"
        :max="9999"
        :precision="0"
        size="small"
        title="0 = 不限制"
        @keydown.esc.stop="cancel"
        @press-enter="save"
      />
      <button
        aria-label="保存限频"
        class="account-concurrency-confirm"
        :disabled="saving"
        title="保存限频"
        type="button"
        @click="save"
      >
        <LoadingOutlined v-if="saving" spin />
        <CheckOutlined v-else />
      </button>
    </div>
    <!-- 展示态：单行「并发 current/limit, 每分钟 current/limit」；未配置每分钟时只显示前者。
         编辑态只显示输入控件，展示分支必须排除 editing。 -->
    <a-tag
      v-if="!editing && editable && !mobile"
      class="account-concurrency-trigger"
      color="blue"
      role="button"
      tabindex="0"
      :title="displayTooltip"
      @click="startEditing"
      @keydown.enter.prevent="startEditing"
    >{{ text }}</a-tag>
    <strong
      v-else-if="!editing && editable && mobile"
      class="account-concurrency-mobile-trigger"
      role="button"
      tabindex="0"
      :title="displayTooltip"
      @click="startEditing"
      @keydown.enter.prevent="startEditing"
    >{{ text }}</strong>
    <a-tag v-else-if="!editing && !mobile" color="blue" :title="displayTooltip">{{ text }}</a-tag>
    <strong v-else-if="!editing" :title="displayTooltip">{{ text }}</strong>
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
  /** 每分钟请求上限（RPM 设计 §6.2）：0 表示不限制。 */
  rpmCurrent?: number
  rpmLimit?: number
  rpmResetAt?: string
  saveLimits: (limits: { concurrencyLimit: number; rpmLimit: number }) => Promise<boolean>
}>()

const emit = defineEmits<{
  (event: 'cancel-edit'): void
  (event: 'start-edit'): void
}>()

const editorRef = ref<HTMLElement>()
const concurrencyInputRef = ref<{ focus?: () => void }>()
const saving = ref(false)
const draftConcurrencyLimit = ref<number | null>(props.limit)
const draftRpmLimit = ref<number | null>(props.rpmLimit ?? 0)
const concurrencyText = computed(() => `${Math.max(0, props.currentConcurrency ?? 0)}/${props.limit}`)
// 每分钟已用/上限；未配置（0/缺省）时为空，展示态只显示并发部分。
const rpmText = computed(() => {
  if (!Number.isFinite(props.rpmLimit) || (props.rpmLimit ?? 0) <= 0) return ''
  return `${Math.max(0, props.rpmCurrent ?? 0)}/${props.rpmLimit}`
})
// 单行格式：「并发 current/limit, 每分钟 current/limit」。逗号前后各一个数对，
// 列头「限频」+ 原生 title 说明两个数对的含义。
const text = computed(() => (rpmText.value ? `${concurrencyText.value}, ${rpmText.value}` : concurrencyText.value))
const limitTooltip = computed(() => {
  const concurrency = `并发 ${concurrencyText.value}`
  if (!rpmText.value) return concurrency
  const resetSeconds = rpmResetSeconds()
  const resetSuffix = resetSeconds > 0 ? `，约 ${resetSeconds} 秒后重置` : ''
  return `${concurrency}，本分钟已派发 ${Math.max(0, props.rpmCurrent ?? 0)} 次，上限 ${props.rpmLimit} 次/分钟${resetSuffix}`
})
// 仅配置了每分钟上限时才挂原生 title（保留 2026-10-23 并发列不挂悬浮气泡的裁决）。
const displayTooltip = computed(() => (rpmText.value ? limitTooltip.value : undefined))

function rpmResetSeconds(): number {
  const resetAtMs = Date.parse(props.rpmResetAt ?? '')
  if (!Number.isFinite(resetAtMs)) return 0
  return Math.max(0, Math.ceil((resetAtMs - Date.now()) / 1000))
}

watch(
  () => props.limit,
  (limit) => {
    if (!props.editing) draftConcurrencyLimit.value = limit
  }
)

watch(
  () => props.rpmLimit,
  (limit) => {
    if (!props.editing) draftRpmLimit.value = limit ?? 0
  }
)

watch(
  () => props.editing,
  (editing) => {
    draftConcurrencyLimit.value = props.limit
    draftRpmLimit.value = props.rpmLimit ?? 0
    document.removeEventListener('pointerdown', handleDocumentPointerDown, true)
    if (editing) {
      document.addEventListener('pointerdown', handleDocumentPointerDown, true)
      void nextTick(() => concurrencyInputRef.value?.focus?.())
    }
  }
)

function startEditing(): void {
  if (!props.editable || saving.value) return
  emit('start-edit')
}

function cancel(): void {
  if (saving.value) return
  draftConcurrencyLimit.value = props.limit
  draftRpmLimit.value = props.rpmLimit ?? 0
  emit('cancel-edit')
}

async function save(): Promise<void> {
  if (saving.value) return
  const concurrencyLimit = Number(draftConcurrencyLimit.value)
  const rpmLimit = Number(draftRpmLimit.value)
  if (!Number.isInteger(concurrencyLimit) || concurrencyLimit < 1) {
    message.warning('并发上限必须是大于等于 1 的整数')
    return
  }
  if (concurrencyLimit > 9999) {
    message.warning('并发上限最大为 9999')
    return
  }
  if (!Number.isInteger(rpmLimit) || rpmLimit < 0) {
    message.warning('每分钟请求上限必须是大于等于 0 的整数')
    return
  }
  if (rpmLimit > 9999) {
    message.warning('每分钟请求上限最大为 9999')
    return
  }
  if (concurrencyLimit === props.limit && rpmLimit === (props.rpmLimit ?? 0)) {
    cancel()
    return
  }

  saving.value = true
  try {
    if (await props.saveLimits({ concurrencyLimit, rpmLimit })) emit('cancel-edit')
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
  white-space: nowrap;
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

/* 一体化控件：并发输入 | 每分钟输入 | 确认按钮。 */
.account-concurrency-control {
  position: relative;
  display: inline-flex;
  width: 132px;
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
  min-width: 0;
  flex: 1 1 0;
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
  padding: 0 5px;
  border-radius: 0;
  background: transparent;
  text-align: center;
}

.account-concurrency-divider {
  flex: none;
  align-self: stretch;
  border-left: 1px solid var(--juhe-border-strong);
}

.account-concurrency-confirm {
  display: inline-flex;
  width: 27px;
  height: auto;
  align-self: stretch;
  flex: none;
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

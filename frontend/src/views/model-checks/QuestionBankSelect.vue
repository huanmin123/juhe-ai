<template>
  <a-select
    class="question-bank-select"
    :value="value"
    mode="multiple"
    show-search
    allow-clear
    :filter-option="false"
    :loading="loading"
    :options="options"
    :placeholder="placeholder"
    :disabled="disabled"
    @change="handleChange"
    @dropdown-visible-change="handleDropdownVisibleChange"
    @search="handleSearch"
  />
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, toRef, watch } from 'vue'

import { useScopedModelChecksApi } from '@/composables/useScopedDomainApi'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'

interface QuestionBankSelectOption {
  label: string
  value: string
  disabled?: boolean
}

const props = withDefaults(defineProps<{
  isManagementView: boolean
  value?: string[]
  disabled?: boolean
  max?: number
  placeholder?: string
}>(), {
  value: () => [],
  disabled: false,
  max: 3,
  placeholder: '输入题目标题搜索已通过的题目'
})

const emit = defineEmits<{
  (event: 'update:value', value: string[]): void
}>()

const scopedApi = useScopedModelChecksApi(toRef(props, 'isManagementView'))
const loading = ref(false)
const remoteOptions = ref<QuestionBankSelectOption[]>([])
// reactive 使 by-ids 回显标签写入后能触发 options 重算
const optionTitles = reactive(new Map<string, string>())
let searchTimer: ReturnType<typeof setTimeout> | undefined
let optionsAbortController: AbortController | undefined
let optionsRequestId = 0
let labelsAbortController: AbortController | undefined
let labelsRequestId = 0

const options = computed<QuestionBankSelectOption[]>(() => {
  const selectedFull = props.value.length >= props.max
  const selectedOptions = props.value.map((id) => ({ label: optionTitles.get(id) || id, value: id }))
  const unselectedOptions = remoteOptions.value
    .filter((item) => !props.value.includes(item.value))
    .map((item) => ({ ...item, disabled: selectedFull }))
  return [...selectedOptions, ...unselectedOptions]
})

watch(remoteOptions, (items) => {
  for (const item of items) optionTitles.set(item.value, item.label)
})

// 编辑回显：外部 value 非空且标签缓存缺失时，按 id 批量拉取标题；
// 失败静默降级维持原始 id 展示。
watch(() => props.value, (ids) => {
  if (ids.length === 0) return
  void fetchMissingLabels(ids)
}, { immediate: true, deep: true })

async function fetchMissingLabels(ids: string[]) {
  const missing = [...new Set(ids)].filter((id) => !optionTitles.has(id))
  if (missing.length === 0) return
  const requestId = ++labelsRequestId
  labelsAbortController?.abort()
  const controller = new AbortController()
  labelsAbortController = controller
  try {
    const result = await scopedApi.questionBankByIds(missing, { signal: controller.signal })
    if (requestId !== labelsRequestId || controller.signal.aborted) return
    for (const item of result.items) optionTitles.set(item.id, item.title)
  } catch {
    // 回显标签拉取失败保持现状（显示原始 id），不打断编辑
  }
}

function handleChange(value: unknown) {
  const ids = Array.isArray(value) ? value.map((item) => String(item)) : []
  const unique = [...new Set(ids)]
  if (unique.length > props.max) {
    message.warning(`最多选择 ${props.max} 道题目`)
  }
  emit('update:value', unique.slice(0, props.max))
}

function handleDropdownVisibleChange(open: boolean) {
  if (!open) return
  clearTimeout(searchTimer)
  void fetchOptions('')
}

function handleSearch(keyword: string) {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(() => void fetchOptions(keyword), 250)
}

async function fetchOptions(keyword: string) {
  const normalizedKeyword = keyword.trim()
  const requestId = ++optionsRequestId
  optionsAbortController?.abort()
  const controller = new AbortController()
  optionsAbortController = controller
  loading.value = true
  try {
    const result = await scopedApi.questionBankOptions(
      { keyword: normalizedKeyword || undefined, limit: 20 },
      { signal: controller.signal }
    )
    if (requestId !== optionsRequestId || controller.signal.aborted) return
    remoteOptions.value = result.items.map((item) => ({ label: item.title, value: item.id }))
  } catch (error) {
    if (requestId !== optionsRequestId || controller.signal.aborted) return
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载题库题目选项失败'))
  } finally {
    if (requestId === optionsRequestId) loading.value = false
  }
}

onBeforeUnmount(() => {
  clearTimeout(searchTimer)
  optionsRequestId += 1
  optionsAbortController?.abort()
  labelsRequestId += 1
  labelsAbortController?.abort()
})
</script>

<style scoped>
.question-bank-select {
  width: 100%;
}

.question-bank-select :deep(.ant-select-selection-overflow) {
  width: 100%;
  min-width: 0;
}

.question-bank-select :deep(.ant-select-selection-overflow-item:not(.ant-select-selection-overflow-item-suffix)) {
  flex: 0 0 100%;
  max-width: 100%;
}

.question-bank-select :deep(.ant-select-selection-overflow-item-suffix) {
  flex: 1 1 80px;
  min-width: 80px;
}

.question-bank-select :deep(.ant-select-selection-item) {
  width: 100%;
  padding-inline-end: 22px;
}
</style>

<template>
  <a-modal
    :open="open"
    title="导出到 CC Switch"
    width="520px"
    :confirm-loading="exporting"
    :ok-button-props="{ type: 'primary', disabled: !canExport }"
    ok-text="导出"
    @cancel="close"
    @ok="submit"
  >
    <a-form layout="vertical" class="modal-form">
      <a-form-item label="模型参考分组" required>
        <a-select
          v-model:value="selectedGroupId"
          :options="groupOptions"
          placeholder="选择分组"
          @change="applySelectedGroupDefaultModel"
        />
      </a-form-item>
      <a-form-item label="目标客户端" required>
        <a-select
          v-model:value="clientApp"
          :options="ccswitchClientOptions"
          placeholder="选择客户端"
        />
      </a-form-item>
      <a-form-item label="模型">
        <a-select
          v-model:value="model"
          allow-clear
          show-search
          option-filter-prop="label"
          :disabled="!modelsReady"
          :loading="modelsLoading"
          :options="modelOptions"
          placeholder="选择分组供应商的模型"
          @dropdown-visible-change="handleModelOptionsOpen"
          @search="handleModelOptionsSearch"
        />
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'

import {
  canSubmitCcSwitchExport,
  ccswitchClientOptions,
  defaultCcSwitchClientAppForGroups,
  type CcSwitchClientApp,
  type CcSwitchExportGroupOption,
  type CcSwitchExportModelOption
} from './ccswitchExport'

const props = defineProps<{
  open: boolean
  exporting?: boolean
  groups: CcSwitchExportGroupOption[]
  modelOptions: CcSwitchExportModelOption[]
  modelsLoading?: boolean
  modelsReady: boolean
}>()

const emit = defineEmits<{
  (event: 'update:open', value: boolean): void
  (event: 'export', payload: { groupId: string; app: CcSwitchClientApp; model: string }): void
  (event: 'model-options-open', groupId: string, open: boolean): void
  (event: 'model-options-search', groupId: string, keyword: string): void
}>()

const selectedGroupId = ref('')
const clientApp = ref<CcSwitchClientApp>()
const model = ref('')
const defaultModelAppliedGroupId = ref('')

const groupOptions = computed(() => props.groups.map((group) => ({
  value: group.groupId,
  label: group.groupName + ' · ' + group.providerName
})))
const canExport = computed(() => canSubmitCcSwitchExport({
  groupId: selectedGroupId.value,
  app: clientApp.value,
  modelsReady: props.modelsReady,
  modelsLoading: props.modelsLoading
}))

watch(
  () => props.open,
  (open) => {
    if (open) resetForm()
  }
)

watch(
  () => props.groups,
  () => {
    if (props.open) resetForm()
  }
)

watch(
  () => [props.modelsReady, selectedGroupId.value, props.modelOptions] as const,
  () => {
    if (!props.modelsReady || defaultModelAppliedGroupId.value === selectedGroupId.value) return
    const group = props.groups.find((item) => item.groupId === selectedGroupId.value)
    const defaultModel = group?.defaultModel || ''
    model.value = props.modelOptions.some((option) => option.value === defaultModel) ? defaultModel : ''
    defaultModelAppliedGroupId.value = selectedGroupId.value
  }
)

watch(
  () => props.modelsReady,
  (ready, wasReady) => {
    if (ready || !wasReady) return
    model.value = ''
    defaultModelAppliedGroupId.value = ''
  }
)

function resetForm(): void {
  const [firstGroup] = props.groups
  selectedGroupId.value = firstGroup?.groupId || ''
  model.value = ''
  defaultModelAppliedGroupId.value = ''
  clientApp.value = defaultCcSwitchClientAppForGroups(props.groups)
  if (selectedGroupId.value) emit('model-options-open', selectedGroupId.value, true)
}

function applySelectedGroupDefaultModel(): void {
  model.value = ''
  defaultModelAppliedGroupId.value = ''
  if (selectedGroupId.value) emit('model-options-open', selectedGroupId.value, true)
}

function handleModelOptionsOpen(open: boolean): void {
  if (selectedGroupId.value) emit('model-options-open', selectedGroupId.value, open)
}

function handleModelOptionsSearch(keyword: string): void {
  if (selectedGroupId.value) emit('model-options-search', selectedGroupId.value, keyword)
}

function close(): void {
  emit('update:open', false)
}

function submit(): void {
  if (!selectedGroupId.value || !clientApp.value) return
  emit('export', {
    groupId: selectedGroupId.value,
    app: clientApp.value,
    model: model.value.trim()
  })
}
</script>

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
      <a-form-item>
        <a-checkbox v-model:checked="confirmed">我确认将完整 API Key 交给 CC Switch 客户端</a-checkbox>
      </a-form-item>
      <a-form-item label="模型">
        <a-input v-model:value="model" allow-clear placeholder="可按需调整" />
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'

import {
  canSubmitCcSwitchExport,
  ccswitchClientOptions,
  type CcSwitchClientApp,
  type CcSwitchExportGroupOption
} from './ccswitchExport'

const props = defineProps<{
  open: boolean
  exporting?: boolean
  groups: CcSwitchExportGroupOption[]
}>()

const emit = defineEmits<{
  (event: 'update:open', value: boolean): void
  (event: 'export', payload: { groupId: string; app: CcSwitchClientApp; model: string }): void
}>()

const selectedGroupId = ref('')
const clientApp = ref<CcSwitchClientApp>()
const model = ref('')
const confirmed = ref(false)

const groupOptions = computed(() => props.groups.map((group) => ({
  value: group.groupId,
  label: group.groupName + ' · ' + group.providerName
})))
const canExport = computed(() => canSubmitCcSwitchExport({
  groupId: selectedGroupId.value,
  app: clientApp.value,
  confirmed: confirmed.value
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

function resetForm(): void {
  const [firstGroup] = props.groups
  selectedGroupId.value = firstGroup?.groupId || ''
  model.value = firstGroup?.defaultModel || ''
  clientApp.value = undefined
  confirmed.value = false
}

function applySelectedGroupDefaultModel(): void {
  const group = props.groups.find((item) => item.groupId === selectedGroupId.value)
  model.value = group?.defaultModel || ''
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

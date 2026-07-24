<template>
  <section class="form-section credential-section" autocomplete="off">
    <a-form-item required>
      <template #label>
        <div class="api-key-label">
          <span>API Key</span>
          <div class="api-key-label-spacer"></div>
          <a-button v-if="showBatchDeleteApiKeys" type="link" size="small" class="api-key-batch-delete-button" @click="batchDeleteApiKeys">
            批量删除
          </a-button>
          <a-radio-group v-if="showApiKeyStrategy" v-model:value="form.apiKeyStrategy" button-style="solid" size="small">
            <a-radio-button value="round_robin">轮询</a-radio-button>
            <a-radio-button value="weighted_round_robin">权重</a-radio-button>
          </a-radio-group>
        </div>
      </template>
      <div :class="['api-key-input-list', { 'has-runtime': showApiKeyRuntimeDetails }]">
        <div v-for="(_, index) in form.apiKeys" :key="index" class="api-key-input-row">
          <div v-if="showApiKeyRuntimeDetails" class="api-key-runtime-cell">
            <a-tooltip v-if="runtimeErrorReasonText(runtimeDetailForIndex(index))" :title="runtimeErrorReasonText(runtimeDetailForIndex(index))" placement="topLeft">
              <a-tag :color="runtimeStatusColor(runtimeDetailForIndex(index))">
                {{ runtimeStatusText(runtimeDetailForIndex(index)) }}
              </a-tag>
            </a-tooltip>
            <a-tag v-else :color="runtimeStatusColor(runtimeDetailForIndex(index))">
              {{ runtimeStatusText(runtimeDetailForIndex(index)) }}
            </a-tag>
          </div>
          <div :class="['api-key-credential-controls', { 'has-weight': showWeightInputs }]">
            <a-input-password
              v-model:value="form.apiKeys[index]"
              autocomplete="new-password"
              data-lpignore="true"
              data-1p-ignore="true"
              data-form-type="other"
              placeholder="粘贴完整 API Key"
              @paste="handleApiKeyPaste(index, $event)"
            />
            <a-input-number
              v-if="showWeightInputs"
              v-model:value="form.apiKeyWeights[index]"
              :min="1"
              :max="100"
              :precision="0"
              size="small"
              class="api-key-weight-input"
              placeholder="权重"
              @change="normalizeApiKeyWeightAt(index)"
            />
          </div>
          <div class="api-key-row-actions">
            <a-tooltip title="添加 API Key">
              <a-button type="text" @click="addApiKeyInput(index)">
                <template #icon><PlusOutlined /></template>
              </a-button>
            </a-tooltip>
            <a-tooltip v-if="form.apiKeys.length > 1" title="移除 API Key">
              <a-button type="text" danger @click="removeApiKeyInput(index)">
                <template #icon><DeleteOutlined /></template>
              </a-button>
            </a-tooltip>
          </div>
        </div>
      </div>
    </a-form-item>
    <a-alert
      v-if="filledApiKeyCount > 1"
      class="multi-key-balance-notice"
      message="多 Key 账户不支持余额查询，保存后将自动关闭余额查询。"
      show-icon
      type="warning"
    />
    <a-form-item label="Base URL" required :tooltip="baseUrlTooltip">
      <a-input
        v-model:value="form.baseUrl"
        autocomplete="off"
        data-lpignore="true"
        data-1p-ignore="true"
        data-form-type="other"
        :placeholder="baseUrlPlaceholder"
        @paste="suggestAccountNameFromBaseUrl"
      />
    </a-form-item>
    <AccountMetaFields
      :deleting-tag-id="deletingTagId"
      :form="form"
      :tag-options="tagOptions"
      :tag-options-loading="tagOptionsLoading"
      @delete-tag="$emit('delete-tag', $event)"
    />
    <a-form-item label="支持模型" required tooltip="声明这个 Base URL 实际支持的上游模型；账户必须至少选择一个模型，模型映射右侧只能从这里选择。">
      <a-select
        v-model:value="form.supportedModels"
        allow-clear
        mode="multiple"
        :loading="modelsLoading"
        option-filter-prop="label"
        placeholder="选择这个 Base URL 支持的模型"
        :options="modelOptions"
        show-search
        @dropdown-visible-change="$emit('model-options-open', $event)"
        @search="$emit('model-options-search', $event)"
      />
    </a-form-item>
    <AccountHealthCheckModelField
      :form="form"
      :model-options="modelOptions"
      :models-loading="modelsLoading"
      :protocol-code="protocolCode"
      :protocol-version="protocolVersion"
    />
  </section>
</template>

<script setup lang="ts">
import { DeleteOutlined, PlusOutlined } from '@ant-design/icons-vue'
import { computed, watch } from 'vue'

import { formatDateTime } from '@/shared/formatters'
import { isHybridProviderCode } from '@/shared/providerProtocol'
import type { AccountApiKeyRuntimeDetail, AccountApiKeyRuntimeStatus, AccountTagSummary } from '@/types/domain'
import { accountNameFromBaseUrl } from './accountNameSuggestion'
import type { AccountFormModel } from './accountFormTypes'
import type { AccountModelSelectOption } from './accountEditFormPayload'
import { normalizedAccountApiKeys } from './accountCredentials'
import AccountHealthCheckModelField from './AccountHealthCheckModelField.vue'
import AccountMetaFields from './AccountMetaFields.vue'

const props = defineProps<{
  apiKeyRuntimeDetails?: AccountApiKeyRuntimeDetail[]
  apiKeyTestDetails?: AccountApiKeyRuntimeDetail[]
  baseUrlPlaceholder: string
  deletingTagId?: string
  editing: boolean
  form: AccountFormModel
  modelOptions: AccountModelSelectOption[]
  modelsLoading: boolean
  protocolCode?: string
  protocolVersion?: string
  tagOptions: AccountTagSummary[]
  tagOptionsLoading: boolean
  title: string
}>()

defineEmits<{
  (event: 'delete-tag', tagId: string): void
  (event: 'model-options-open', open: boolean): void
  (event: 'model-options-search', value: string): void
}>()

const filledApiKeyCount = computed(() => normalizedAccountApiKeys(props.form).length)
const showApiKeyStrategy = computed(() => filledApiKeyCount.value > 1)
const showWeightInputs = computed(() => showApiKeyStrategy.value && props.form.apiKeyStrategy === 'weighted_round_robin')
const showBatchDeleteApiKeys = computed(() => filledApiKeyCount.value > 1)
const showApiKeyRuntimeDetails = computed(() => (
  filledApiKeyCount.value > 0
  && (
    Boolean(props.apiKeyTestDetails?.length)
    || (props.editing && filledApiKeyCount.value > 1 && Boolean(props.apiKeyRuntimeDetails?.length))
  )
))
const apiKeyRuntimeDetailRows = computed<AccountApiKeyRuntimeDetail[]>(() => {
  if (!showApiKeyRuntimeDetails.value) return []
  return [...(props.apiKeyTestDetails?.length ? props.apiKeyTestDetails : props.apiKeyRuntimeDetails ?? [])].sort((left, right) => left.keyIndex - right.keyIndex)
})
const apiKeyRuntimeDetailByIndex = computed(() => {
  const output = new Map<number, AccountApiKeyRuntimeDetail>()
  for (const detail of apiKeyRuntimeDetailRows.value) {
    output.set(detail.keyIndex, detail)
  }
  return output
})
const baseUrlTooltip = computed(() => (
  isHybridProviderCode(props.form.providerCode)
    ? '填写真实上游服务根地址，不要填写具体接口路径。例如 OpenAI-compatible 可填 https://api.openai.com/v1，Anthropic 可填 https://api.anthropic.com/v1，Gemini native 可填 https://generativelanguage.googleapis.com。本地 CLIProxyAPI sidecar 联调时，可填 http://127.0.0.1:<port>/v1 或 /v1beta，并在后端显式放行对应 loopback Origin。'
    : '填写服务根地址或 /v1 版本根地址，例如 https://api.openai.com/v1 或 https://api.anthropic.com/v1；不要填写 /responses、/messages 等具体接口路径。本地 CLIProxyAPI sidecar 联调时，可填 http://127.0.0.1:<port>/v1（Gemini native 用 /v1beta），并在后端显式放行对应 loopback Origin。'
))

watch(
  [() => props.form.apiKeys.length, () => props.form.apiKeyStrategy],
  () => {
    syncApiKeyWeights()
  },
  { immediate: true },
)

function addApiKeyInput(index: number): void {
  ensureApiKeyInputs()
  props.form.apiKeys.splice(index + 1, 0, '')
  props.form.apiKeyWeights.splice(index + 1, 0, 1)
}

function removeApiKeyInput(index: number): void {
  ensureApiKeyInputs()
  if (props.form.apiKeys.length <= 1) return
  props.form.apiKeys.splice(index, 1)
  props.form.apiKeyWeights.splice(index, 1)
}

function batchDeleteApiKeys(): void {
  props.form.apiKey = ''
  props.form.apiKeys = ['']
  props.form.apiKeyWeights = [1]
}

function handleApiKeyPaste(index: number, event: ClipboardEvent): void {
  const text = event.clipboardData?.getData('text') ?? ''
  const keys = extractOpenAIApiKeys(text)
  if (keys.length <= 1 && !text.includes('\n')) return
  if (!keys.length) return
  event.preventDefault()
  ensureApiKeyInputs()
  const nextKeys = [...props.form.apiKeys]
  nextKeys.splice(index, 1, ...keys)
  props.form.apiKeys = uniqueNonEmptyStrings(nextKeys)
  props.form.apiKeyWeights = props.form.apiKeys.map((_, keyIndex) => props.form.apiKeyWeights[keyIndex] ?? 1)
  if (!props.form.apiKeys.length) props.form.apiKeys = ['']
}

function ensureApiKeyInputs(): void {
  if (props.form.apiKeys.length) return
  props.form.apiKeys = [props.form.apiKey || '']
  props.form.apiKeyWeights = [1]
}

function suggestAccountNameFromBaseUrl(event: ClipboardEvent): void {
  if (props.form.name.trim()) return
  const name = accountNameFromBaseUrl(event.clipboardData?.getData('text') ?? '')
  if (name) props.form.name = name
}

function syncApiKeyWeights(): void {
  props.form.apiKeyWeights = props.form.apiKeys.map((_, index) => normalizeApiKeyWeight(props.form.apiKeyWeights[index]))
}

function normalizeApiKeyWeightAt(index: number): void {
  props.form.apiKeyWeights[index] = normalizeApiKeyWeight(props.form.apiKeyWeights[index])
}

function normalizeApiKeyWeight(value: number | null | undefined): number {
  const numberValue = Number(value ?? 1)
  if (!Number.isInteger(numberValue)) return 1
  return Math.min(100, Math.max(1, numberValue))
}

function runtimeDetailForIndex(index: number): AccountApiKeyRuntimeDetail | undefined {
  return apiKeyRuntimeDetailByIndex.value.get(index)
}

function runtimeStatusText(detail: AccountApiKeyRuntimeDetail | undefined): string {
  return runtimeStatusMeta(detail?.status).label
}

function runtimeStatusColor(detail: AccountApiKeyRuntimeDetail | undefined): string {
  return runtimeStatusMeta(detail?.status).color
}

function runtimeStatusMeta(status: AccountApiKeyRuntimeStatus | undefined): { label: string; color: string } {
  switch (status) {
    case 'active':
      return { label: '可调度', color: 'green' }
    case 'temporary_unavailable':
      return { label: '临时避让', color: 'gold' }
    case 'rate_limited':
      return { label: '限流冷却', color: 'orange' }
    case 'error':
      return { label: '异常', color: 'red' }
    case 'disabled':
      return { label: '已停用', color: 'default' }
    default:
      return { label: '未保存', color: 'default' }
  }
}

function runtimeErrorReasonText(detail: AccountApiKeyRuntimeDetail | undefined): string {
  if (!detail || detail.status === 'active') return ''
  if (detail.lastErrorMessage?.trim()) return detail.lastErrorMessage.trim()
  if (detail.lastErrorCode?.trim()) return detail.lastErrorCode.trim()
  if (detail.cooldownUntil) return `冷却至 ${formatDateTime(detail.cooldownUntil)}`
  if (detail.nextProbeAt) return `下次探测 ${formatDateTime(detail.nextProbeAt)}`
  return ''
}

function extractOpenAIApiKeys(value: string): string[] {
  const matches = value.match(/sk-[^\s,;'"`<>]+/g) ?? []
  return uniqueNonEmptyStrings(matches.map(cleanApiKeyText))
}

function cleanApiKeyText(value: string): string {
  return value.trim().replace(/[)\]}。！？!?.,，;；:：]+$/g, '')
}

function uniqueNonEmptyStrings(values: string[]): string[] {
  const output: string[] = []
  const seen = new Set<string>()
  for (const value of values) {
    const text = value.trim()
    if (!text || seen.has(text)) continue
    seen.add(text)
    output.push(text)
  }
  return output
}

</script>

<style scoped>
.form-section {
  padding: 0;
  border: 0;
  background: transparent;
}

.credential-section {
  padding-top: 2px;
}

.api-key-label {
  display: flex;
  width: 100%;
  gap: 12px;
  align-items: center;
}

.api-key-label :deep(.ant-radio-group) {
  margin-left: 0;
}

.api-key-label-spacer {
  flex: 1;
}

.api-key-batch-delete-button {
  padding-inline: 0;
}

.multi-key-balance-notice {
  margin: -4px 0 16px;
}

:deep(.ant-form-item-label > label:has(.api-key-label)) {
  width: 100%;
}

.api-key-input-list {
  display: grid;
  gap: 8px;
}

.api-key-input-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 8px;
  align-items: center;
}

.api-key-input-list.has-runtime .api-key-input-row {
  grid-template-columns: 72px minmax(0, 1fr) auto;
  gap: 8px;
}

.api-key-credential-controls {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: 8px;
  align-items: center;
}

.api-key-credential-controls.has-weight {
  grid-template-columns: minmax(0, 1fr) 64px;
}

.api-key-weight-input {
  width: 64px;
}

.api-key-weight-input :deep(.ant-input-number-input) {
  height: 30px;
  padding-inline: 8px;
}

.api-key-runtime-cell {
  display: flex;
  box-sizing: border-box;
  width: 72px;
  max-width: 72px;
  height: 32px;
  min-width: 0;
  align-items: center;
  justify-content: flex-start;
}

.api-key-runtime-cell :deep(.ant-tag) {
  margin-inline-end: 0;
  padding-inline: 6px;
  font-size: 12px;
  line-height: 20px;
}

.api-key-row-actions {
  display: inline-flex;
  align-items: center;
  gap: 2px;
}

@media (max-width: 640px) {
  .api-key-input-row {
    grid-template-columns: minmax(0, 1fr);
  }

  .api-key-input-list.has-runtime .api-key-input-row {
    grid-template-columns: minmax(0, 1fr);
  }

  .api-key-runtime-cell {
    width: 100%;
    max-width: none;
  }

  .api-key-credential-controls.has-weight {
    grid-template-columns: minmax(0, 1fr) 58px;
  }

  .api-key-weight-input {
    width: 58px;
  }

  .api-key-row-actions {
    justify-content: flex-end;
  }
}
</style>

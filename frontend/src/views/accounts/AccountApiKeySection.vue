<template>
  <section class="form-section credential-section" autocomplete="off">
    <div class="form-section-head">
      <div>
        <h4>{{ title }} 配置</h4>
      </div>
    </div>
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
      <div class="api-key-input-list">
        <div v-for="(_, index) in form.apiKeys" :key="index" class="api-key-input-row">
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
      <div v-if="showApiKeyRuntimeDetails" class="api-key-runtime-panel">
        <div class="api-key-runtime-title">已保存 Key 状态</div>
        <div v-for="(detail, index) in apiKeyRuntimeDetailRows" :key="runtimeDetailKey(detail, index)" class="api-key-runtime-row">
          <span class="api-key-runtime-index">{{ runtimeIndexText(detail, index) }}</span>
          <a-tag :color="runtimeStatusColor(detail)">
            {{ runtimeStatusText(detail) }}
          </a-tag>
          <span v-if="runtimeKeySuffixText(detail)" class="api-key-runtime-muted">
            {{ runtimeKeySuffixText(detail) }}
          </span>
          <span class="api-key-runtime-muted">权重 {{ runtimeWeightText(detail) }}</span>
          <span v-if="runtimeFailureText(detail)" class="api-key-runtime-muted">
            {{ runtimeFailureText(detail) }}
          </span>
          <span v-if="runtimeScheduleText(detail)" class="api-key-runtime-muted">
            {{ runtimeScheduleText(detail) }}
          </span>
          <a-tooltip v-if="detail.lastErrorMessage" :title="detail.lastErrorMessage">
            <span class="api-key-runtime-error">{{ runtimeLastErrorText(detail) }}</span>
          </a-tooltip>
        </div>
      </div>
    </a-form-item>
    <a-form-item label="Base URL" required :tooltip="baseUrlTooltip">
      <a-input
        v-model:value="form.baseUrl"
        autocomplete="off"
        data-lpignore="true"
        data-1p-ignore="true"
        data-form-type="other"
        :placeholder="baseUrlPlaceholder"
      />
    </a-form-item>
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
      />
    </a-form-item>
  </section>
</template>

<script setup lang="ts">
import { DeleteOutlined, PlusOutlined } from '@ant-design/icons-vue'
import { computed, watch } from 'vue'

import { formatDateTime } from '@/shared/formatters'
import { isHybridProviderCode } from '@/shared/providerProtocol'
import type { AccountApiKeyRuntimeDetail, AccountApiKeyRuntimeStatus } from '@/types/domain'
import type { AccountFormModel } from './accountFormTypes'
import { normalizedAccountApiKeys } from './accountCredentials'

const props = defineProps<{
  apiKeyRuntimeDetails?: AccountApiKeyRuntimeDetail[]
  baseUrlPlaceholder: string
  editing: boolean
  form: AccountFormModel
  modelOptions: Array<{ label: string; value: string }>
  modelsLoading: boolean
  title: string
}>()

const filledApiKeyCount = computed(() => normalizedAccountApiKeys(props.form).length)
const showApiKeyStrategy = computed(() => filledApiKeyCount.value > 1)
const showWeightInputs = computed(() => showApiKeyStrategy.value && props.form.apiKeyStrategy === 'weighted_round_robin')
const showBatchDeleteApiKeys = computed(() => props.form.apiKeys.some((value) => value.trim()))
const showApiKeyRuntimeDetails = computed(() => props.editing && Boolean(props.apiKeyRuntimeDetails?.length))
const apiKeyRuntimeDetailRows = computed<AccountApiKeyRuntimeDetail[]>(() => {
  if (!showApiKeyRuntimeDetails.value) return []
  return [...(props.apiKeyRuntimeDetails ?? [])].sort((left, right) => left.keyIndex - right.keyIndex)
})
const baseUrlTooltip = computed(() => (
  isHybridProviderCode(props.form.providerCode)
    ? '填写真实上游服务根地址，不要填写具体接口路径。例如 OpenAI-compatible 可填 https://api.openai.com/v1，Anthropic 可填 https://api.anthropic.com/v1，Gemini native 可填 https://generativelanguage.googleapis.com。'
    : '填写服务根地址或 /v1 版本根地址，例如 https://api.openai.com/v1 或 https://api.anthropic.com/v1；不要填写 /responses、/messages 等具体接口路径。'
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

function runtimeDetailKey(detail: AccountApiKeyRuntimeDetail, index: number): string {
  return detail.keyFingerprintPrefix || `${detail.keyIndex}-${detail.keySuffix ?? index}`
}

function runtimeIndexText(detail: AccountApiKeyRuntimeDetail, index: number): string {
  return `已保存 Key ${Number.isInteger(detail.keyIndex) ? detail.keyIndex + 1 : index + 1}`
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
      return { label: '未记录', color: 'default' }
  }
}

function runtimeKeySuffixText(detail: AccountApiKeyRuntimeDetail | undefined): string {
  return detail?.keySuffix ? `尾号 ${detail.keySuffix}` : ''
}

function runtimeWeightText(detail: AccountApiKeyRuntimeDetail | undefined): string {
  return String(detail?.weight ?? 1)
}

function runtimeFailureText(detail: AccountApiKeyRuntimeDetail | undefined): string {
  if (!detail) return ''
  if (detail.consecutiveFailures > 0) return `连续失败 ${detail.consecutiveFailures}`
  if (detail.failureCount > 0) return `累计失败 ${detail.failureCount}`
  if (detail.successCount > 0) return `成功 ${detail.successCount}`
  return ''
}

function runtimeScheduleText(detail: AccountApiKeyRuntimeDetail | undefined): string {
  if (!detail) return ''
  if (detail.cooldownUntil) return `冷却至 ${formatDateTime(detail.cooldownUntil)}`
  if (detail.nextProbeAt) return `探测 ${formatDateTime(detail.nextProbeAt)}`
  return ''
}

function runtimeLastErrorText(detail: AccountApiKeyRuntimeDetail | undefined): string {
  if (!detail?.lastErrorMessage) return ''
  return detail.lastErrorCode ? `最近错误 ${detail.lastErrorCode}` : '最近错误'
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

.form-section-head {
  margin-bottom: 8px;
}

.form-section-head h4 {
  margin: 0;
  color: #0f172a;
  font-size: 14px;
  font-weight: 600;
}

.form-section-head p {
  margin: 4px 0 0;
  color: #64748b;
  font-size: 12px;
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

.api-key-runtime-row {
  display: flex;
  min-width: 0;
  flex-wrap: wrap;
  gap: 6px;
  align-items: center;
  color: #64748b;
  font-size: 12px;
  line-height: 22px;
}

.api-key-runtime-panel {
  display: grid;
  gap: 6px;
  margin-top: 10px;
  padding: 8px 10px;
  border: 1px solid #e2e8f0;
  border-radius: 6px;
  background: #f8fafc;
}

.api-key-runtime-title {
  color: #475569;
  font-size: 12px;
  font-weight: 600;
}

.api-key-runtime-index {
  color: #334155;
  font-weight: 600;
}

.api-key-runtime-row :deep(.ant-tag) {
  margin-inline-end: 0;
}

.api-key-runtime-muted {
  overflow-wrap: anywhere;
}

.api-key-runtime-error {
  max-width: 100%;
  overflow: hidden;
  color: #b91c1c;
  text-overflow: ellipsis;
  white-space: nowrap;
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

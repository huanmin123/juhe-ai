<template>
  <section class="form-section credential-section" autocomplete="off">
    <a-form-item required>
      <template #label>
        <div class="api-key-label">
          <span>API Key</span>
          <a-popover placement="bottomLeft" trigger="hover">
            <template #content>
              <div class="api-key-help-content">
                <div><strong>单 Key</strong><span>直接使用当前唯一 Key。</span></div>
                <div><strong>轮询</strong><span>新请求按 Key 顺序轮流选择，持续均衡使用。</span></div>
                <div><strong>权重</strong><span>按每个 Key 的权重选择，权重越高使用越频繁。</span></div>
                <div><strong>主备</strong><span>第一行是主 Key，后续是备用 Key；主 Key 失败时，本次请求按顺序切换备用 Key。</span></div>
                <div><strong>状态</strong><span>输入框前缀显示当前 Key 的运行状态；不可调度的 Key 会被自动跳过。</span></div>
                <div><strong>排序</strong><span>只有主备模式支持拖拽，调整顺序即可调整主备优先级。</span></div>
              </div>
            </template>
            <button type="button" class="api-key-help-button" aria-label="API Key 使用说明">
              <QuestionCircleOutlined />
            </button>
          </a-popover>
          <div class="api-key-label-spacer"></div>
          <a-tooltip v-if="editing && filledApiKeyCount > 1" title="加载多 Key 运行状态">
            <a-button
              type="text"
              size="small"
              :loading="apiKeyRuntimeLoading"
              aria-label="加载多 Key 运行状态"
              @click="$emit('load-api-key-runtime', true)"
            >
              <template #icon><ReloadOutlined /></template>
            </a-button>
          </a-tooltip>
          <a-button v-if="showBatchDeleteApiKeys" type="link" size="small" class="api-key-batch-delete-button" @click="batchDeleteApiKeys">
            批量删除
          </a-button>
          <a-radio-group v-if="showApiKeyStrategy" v-model:value="form.apiKeyStrategy" button-style="solid" size="small">
            <a-radio-button value="failover">主备</a-radio-button>
            <a-radio-button value="round_robin">轮询</a-radio-button>
            <a-radio-button value="weighted_round_robin">权重</a-radio-button>
          </a-radio-group>
        </div>
      </template>
      <div :class="['api-key-input-list', { 'is-failover': isFailoverMode }]">
        <div
          v-for="(_, index) in form.apiKeys"
          :key="index"
          :class="['api-key-input-row', { 'is-dragging': dragSourceIndex === index, 'is-drag-over': dragOverIndex === index, 'is-failover': isFailoverMode }]"
          @dragenter.prevent="handleApiKeyDragEnter(index)"
          @dragover="handleApiKeyDragOver(index, $event)"
          @drop="handleApiKeyDrop(index, $event)"
        >
          <div v-if="isFailoverMode" class="api-key-drag-cell">
            <button
              type="button"
              class="api-key-drag-handle"
              draggable="true"
              :aria-label="`拖动调整第 ${index + 1} 个 Key 顺序，当前为${apiKeyRoleText(index)}`"
              @dragstart="handleApiKeyDragStart(index, $event)"
              @dragend="handleApiKeyDragEnd"
              @keydown.up.prevent="moveApiKeyForMode(index, index - 1)"
              @keydown.down.prevent="moveApiKeyForMode(index, index + 1)"
            >
              <HolderOutlined />
            </button>
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
            >
              <template v-if="showApiKeyRuntimeDetails" #prefix>
                <a-tooltip v-if="runtimeErrorReasonText(runtimeDetailForIndex(index))" :title="runtimeErrorReasonText(runtimeDetailForIndex(index))" placement="topLeft">
                  <a-tag class="api-key-runtime-prefix" :color="runtimeStatusColor(runtimeDetailForIndex(index))">
                    {{ runtimeStatusText(runtimeDetailForIndex(index)) }}
                  </a-tag>
                </a-tooltip>
                <a-tag v-else class="api-key-runtime-prefix" :color="runtimeStatusColor(runtimeDetailForIndex(index))">
                  {{ runtimeStatusText(runtimeDetailForIndex(index)) }}
                </a-tag>
              </template>
            </a-input-password>
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
            <a-tooltip v-if="form.apiKeys.length > 1" title="移除 API Key">
              <a-button type="text" danger @click="removeApiKeyInput(index)">
                <template #icon><DeleteOutlined /></template>
              </a-button>
            </a-tooltip>
            <a-tooltip title="添加 API Key">
              <a-button type="text" aria-label="添加 API Key" @click="addApiKeyInput(index)">
                <template #icon><PlusOutlined /></template>
              </a-button>
            </a-tooltip>
          </div>
        </div>
      </div>
    </a-form-item>
    <a-alert
      v-if="filledApiKeyCount > 1"
      class="multi-key-balance-notice"
      message="多 Key 余额会逐 Key 查询；仅明确属于各 Key 的独立额度才会合计，账户共享余额不会重复相加。"
      show-icon
      type="info"
    />
    <a-form-item required>
      <template #label>
        <span class="base-url-label">Base URL</span>
        <a-popover placement="bottomLeft" trigger="hover">
          <template #content>
            <div class="base-url-help-content">
              <div><strong>填写内容</strong><span>填写上游服务根地址，或带版本号的根地址，例如 <code>https://api.openai.com/v1</code>。</span></div>
              <div><strong>不要填写</strong><span>不要填写具体接口路径，例如 <code>/chat/completions</code>、<code>/responses</code>、<code>/messages</code> 或 <code>:generateContent</code>。</span></div>
              <div><strong>混合供应商</strong><span>填写该账户真实上游的根地址，协议转换由账户配置负责。</span></div>
              <div><strong>本地联调</strong><span>CLIProxyAPI sidecar 可填写 <code>http://127.0.0.1:&lt;port&gt;/v1</code>；Gemini native 使用 <code>/v1beta</code>。</span></div>
            </div>
          </template>
          <button type="button" class="base-url-help-button" aria-label="Base URL 填写说明">
            <QuestionCircleOutlined />
          </button>
        </a-popover>
      </template>
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
      @tag-options-dropdown="$emit('tag-options-dropdown', $event)"
    />
    <a-form-item required>
      <template #label>
        <div class="supported-models-label">
          <span>支持模型</span>
          <a-tooltip title="声明这个 Base URL 实际支持的上游模型；账户必须至少选择一个模型，模型映射右侧只能从这里选择。">
            <QuestionCircleOutlined class="supported-models-help" />
          </a-tooltip>
        </div>
      </template>
      <div class="supported-models-control">
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
        <a-tooltip title="从上游同步可新增模型">
          <a-button
            class="supported-models-refresh-button"
            size="small"
            type="text"
            aria-label="从上游同步可新增模型"
            :loading="modelSyncing"
            @click.stop="$emit('refresh-models')"
          >
            <template #icon><SyncOutlined /></template>
          </a-button>
        </a-tooltip>
      </div>
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
import { DeleteOutlined, HolderOutlined, PlusOutlined, QuestionCircleOutlined, ReloadOutlined, SyncOutlined } from '@ant-design/icons-vue'
import { computed, ref, watch } from 'vue'

import { formatDateTime } from '@/shared/formatters'
import type { AccountApiKeyRuntimeDetail, AccountApiKeyRuntimeStatus, AccountTagSummary } from '@/types/domain'
import { accountNameFromBaseUrl } from './accountNameSuggestion'
import type { AccountFormModel } from './accountFormTypes'
import type { AccountModelSelectOption } from './accountEditFormPayload'
import { normalizedAccountApiKeys } from './accountCredentials'
import AccountHealthCheckModelField from './AccountHealthCheckModelField.vue'
import AccountMetaFields from './AccountMetaFields.vue'

const props = defineProps<{
  apiKeyRuntimeDetails?: AccountApiKeyRuntimeDetail[]
  apiKeyRuntimeLoading?: boolean
  apiKeyTestDetails?: AccountApiKeyRuntimeDetail[]
  baseUrlPlaceholder: string
  deletingTagId?: string
  editing: boolean
  form: AccountFormModel
  modelOptions: AccountModelSelectOption[]
  modelSyncing?: boolean
  modelsLoading: boolean
  protocolCode?: string
  protocolVersion?: string
  tagOptions: AccountTagSummary[]
  tagOptionsLoading: boolean
  title: string
}>()

defineEmits<{
  (event: 'delete-tag', tagId: string): void
  (event: 'load-api-key-runtime', force?: boolean): void
  (event: 'model-options-open', open: boolean): void
  (event: 'model-options-search', value: string): void
  (event: 'refresh-models'): void
  (event: 'tag-options-dropdown', open: boolean): void
}>()

const filledApiKeyCount = computed(() => normalizedAccountApiKeys(props.form).length)
const showApiKeyStrategy = computed(() => filledApiKeyCount.value > 1)
const isFailoverMode = computed(() => showApiKeyStrategy.value && props.form.apiKeyStrategy === 'failover')
const showWeightInputs = computed(() => showApiKeyStrategy.value && props.form.apiKeyStrategy === 'weighted_round_robin')
const showBatchDeleteApiKeys = computed(() => filledApiKeyCount.value > 1)
const showApiKeyRuntimeDetails = computed(() => (
  filledApiKeyCount.value > 0
  && (
    Boolean(props.apiKeyTestDetails?.length)
    || (props.editing && filledApiKeyCount.value > 1 && Boolean(props.apiKeyRuntimeDetails?.length))
  )
))
const activeApiKeyRuntimeDetails = computed<AccountApiKeyRuntimeDetail[]>(() => {
  if (!showApiKeyRuntimeDetails.value) return []
  return [...(props.apiKeyTestDetails?.length ? props.apiKeyTestDetails : props.apiKeyRuntimeDetails ?? [])].sort((left, right) => left.keyIndex - right.keyIndex)
})
const apiKeyRuntimeDetailByKey = ref(new Map<string, AccountApiKeyRuntimeDetail>())
const runtimeKeyOrder = ref<string[]>([])
const dragSourceIndex = ref<number | null>(null)
const dragOverIndex = ref<number | null>(null)

watch(activeApiKeyRuntimeDetails, (details) => {
  const byKey = new Map<string, AccountApiKeyRuntimeDetail>()
  for (const detail of details) {
    const key = props.form.apiKeys[detail.keyIndex]?.trim()
    if (key) byKey.set(key, detail)
  }
  apiKeyRuntimeDetailByKey.value = byKey
  runtimeKeyOrder.value = props.form.apiKeys.map((key, index) => key.trim() || `draft:${index}`)
}, { deep: true, immediate: true })
watch(
  [() => props.form.apiKeys.length, () => props.form.apiKeyStrategy],
  () => {
    syncApiKeyWeights()
  },
  { immediate: true },
)

function addApiKeyInput(index: number): void {
  ensureApiKeyInputs()
  const insertIndex = index + 1
  props.form.apiKeys.splice(insertIndex, 0, '')
  props.form.apiKeyWeights.splice(insertIndex, 0, 1)
  runtimeKeyOrder.value.splice(insertIndex, 0, `draft:${Date.now()}-${insertIndex}`)
}

function removeApiKeyInput(index: number): void {
  ensureApiKeyInputs()
  if (props.form.apiKeys.length <= 1) return
  props.form.apiKeys.splice(index, 1)
  props.form.apiKeyWeights.splice(index, 1)
  runtimeKeyOrder.value.splice(index, 1)
}

function batchDeleteApiKeys(): void {
  props.form.apiKey = ''
  props.form.apiKeys = ['']
  props.form.apiKeyWeights = [1]
  runtimeKeyOrder.value = ['draft:0']
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
  runtimeKeyOrder.value = props.form.apiKeys.map((key, keyIndex) => key.trim() || `draft:${keyIndex}`)
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
  return apiKeyRuntimeDetailByKey.value.get(runtimeKeyOrder.value[index] ?? props.form.apiKeys[index]?.trim())
}

function apiKeyRoleText(index: number): string {
  return index === 0 ? '主 Key' : `备用 Key ${index}`
}

function moveApiKeyForMode(fromIndex: number, toIndex: number): void {
  if (!isFailoverMode.value || fromIndex === toIndex || fromIndex < 0 || toIndex < 0 || toIndex >= props.form.apiKeys.length) return
  const [key] = props.form.apiKeys.splice(fromIndex, 1)
  const [weight] = props.form.apiKeyWeights.splice(fromIndex, 1)
  const [runtimeKey] = runtimeKeyOrder.value.splice(fromIndex, 1)
  if (key === undefined) return
  props.form.apiKeys.splice(toIndex, 0, key)
  props.form.apiKeyWeights.splice(toIndex, 0, weight ?? 1)
  runtimeKeyOrder.value.splice(toIndex, 0, runtimeKey ?? (key.trim() || `draft:${toIndex}`))
}

function handleApiKeyDragStart(index: number, event: DragEvent): void {
  if (!isFailoverMode.value) {
    event.preventDefault()
    return
  }
  dragSourceIndex.value = index
  dragOverIndex.value = index
  event.dataTransfer?.setData('text/plain', String(index))
  if (event.dataTransfer) event.dataTransfer.effectAllowed = 'move'
}

function handleApiKeyDragEnter(index: number): void {
  if (dragSourceIndex.value === null) return
  dragOverIndex.value = index
}

function handleApiKeyDragOver(index: number, event: DragEvent): void {
  if (dragSourceIndex.value === null) return
  event.preventDefault()
  dragOverIndex.value = index
  if (event.dataTransfer) event.dataTransfer.dropEffect = 'move'
}

function handleApiKeyDrop(index: number, event: DragEvent): void {
  event.preventDefault()
  const sourceIndex = dragSourceIndex.value
  handleApiKeyDragEnd()
  if (sourceIndex === null) return
  moveApiKeyForMode(sourceIndex, index)
}

function handleApiKeyDragEnd(): void {
  dragSourceIndex.value = null
  dragOverIndex.value = null
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
    case 'unverified':
      return { label: '待验证', color: 'blue' }
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

.api-key-help-button {
  display: inline-flex;
  width: 24px;
  height: 24px;
  align-items: center;
  justify-content: center;
  margin-inline-start: 2px;
  padding: 0;
  border: 0;
  background: transparent;
  color: rgba(0, 0, 0, 0.45);
  cursor: pointer;
}

.api-key-help-button:hover,
.api-key-help-button:focus-visible {
  color: #1677ff;
  outline: none;
}

.api-key-help-content {
  width: min(420px, calc(100vw - 48px));
  display: grid;
  gap: 8px;
  color: rgba(0, 0, 0, 0.65);
  font-size: 12px;
  line-height: 1.55;
}

.api-key-help-content > div {
  display: grid;
  grid-template-columns: 42px minmax(0, 1fr);
  gap: 8px;
}

.api-key-help-content strong {
  color: rgba(0, 0, 0, 0.88);
  font-weight: 600;
}

.base-url-label {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.base-url-help-button {
  display: inline-flex;
  width: 20px;
  height: 20px;
  align-items: center;
  justify-content: center;
  margin-inline-start: 4px;
  padding: 0;
  border: 0;
  background: transparent;
  color: rgba(0, 0, 0, 0.45);
  cursor: help;
}

.base-url-help-button:hover,
.base-url-help-button:focus-visible {
  color: #1677ff;
  outline: none;
}

.base-url-help-content {
  width: min(440px, calc(100vw - 48px));
  display: grid;
  gap: 8px;
  color: rgba(0, 0, 0, 0.65);
  font-size: 12px;
  line-height: 1.55;
}

.base-url-help-content > div {
  display: grid;
  grid-template-columns: 56px minmax(0, 1fr);
  gap: 8px;
}

.base-url-help-content strong {
  color: rgba(0, 0, 0, 0.88);
  font-weight: 600;
}

.base-url-help-content code {
  padding: 1px 4px;
  border-radius: 3px;
  background: #f5f5f5;
  color: rgba(0, 0, 0, 0.78);
  font-size: 11px;
}

.api-key-batch-delete-button {
  padding-inline: 0;
}

.supported-models-label {
  display: flex;
  flex: 1;
  align-items: center;
  min-width: 0;
}

.supported-models-control {
  display: flex;
  width: 100%;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.supported-models-control :deep(.ant-select) {
  flex: 1;
  min-width: 0;
}

.supported-models-refresh-button {
  width: 24px;
  height: 24px;
  padding: 0;
  color: rgba(0, 0, 0, 0.55);
  border-radius: 6px;
}

.supported-models-refresh-button :deep(.anticon) {
  font-size: 13px;
}

.supported-models-help {
  color: rgba(0, 0, 0, 0.45);
  cursor: help;
}

:deep(.ant-form-item-label > label:has(.supported-models-label)) {
  width: 100%;
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

.api-key-input-row.is-failover {
  grid-template-columns: 36px minmax(0, 1fr) auto;
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

.api-key-drag-cell {
  display: flex;
  align-items: center;
  justify-content: center;
}

.api-key-drag-handle {
  display: inline-flex;
  width: 32px;
  height: 32px;
  align-items: center;
  justify-content: center;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: #64748b;
  cursor: grab;
}

.api-key-drag-handle:active {
  cursor: grabbing;
}

.api-key-drag-handle:hover,
.api-key-drag-handle:focus-visible {
  background: #f1f5f9;
  color: #1677ff;
  outline: none;
}

.api-key-input-row.is-dragging {
  opacity: 0.55;
}

.api-key-input-row.is-drag-over {
  outline: 1px dashed #1677ff;
  outline-offset: 2px;
}

.api-key-runtime-prefix {
  display: inline-flex;
  width: 64px;
  height: 20px;
  box-sizing: border-box;
  align-items: center;
  justify-content: center;
  margin-inline-end: 8px;
  padding: 0 8px 0 0;
  border: 0;
  border-inline-end: 1px solid #d9d9d9;
  border-radius: 0;
  background: transparent !important;
  overflow: hidden;
  text-align: center;
  text-overflow: ellipsis;
  white-space: nowrap;
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

  .api-key-input-row.is-failover {
    grid-template-columns: 36px minmax(0, 1fr) auto;
  }

  .api-key-runtime-prefix {
    width: 58px;
    margin-inline-end: 6px;
    padding-inline-end: 6px;
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

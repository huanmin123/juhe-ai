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
    </a-form-item>
    <a-form-item label="Base URL" required extra="填写服务根地址或 /v1 版本根地址，例如 https://api.openai.com/v1 或 https://api.anthropic.com/v1；不要填写 /responses、/messages 等具体接口路径。">
      <a-input
        v-model:value="form.baseUrl"
        autocomplete="off"
        data-lpignore="true"
        data-1p-ignore="true"
        data-form-type="other"
        :placeholder="baseUrlPlaceholder"
      />
    </a-form-item>
    <a-alert
      v-if="showAnthropicBaseUrlNotice"
      class="anthropic-base-url-notice"
      type="warning"
      show-icon
      message="当前 Base URL 不是 Anthropic 官方 API 地址。兼容入口可用于本地测试或后续独立供应商接入，但不要把它标记为官方 Claude 直连账号。"
    />
    <template v-if="isAnthropicForm">
      <a-form-item label="Anthropic-Version" extra="客户端未传 anthropic-version 时使用该值；通常保持 2023-06-01。">
        <a-input
          v-model:value="form.anthropicVersion"
          autocomplete="off"
          placeholder="2023-06-01"
        />
      </a-form-item>
      <a-form-item label="Anthropic-Beta" extra="可填写账号级 beta，多项用英文逗号分隔；系统不会默认注入 Claude Code 专属 beta。">
        <a-input
          v-model:value="form.anthropicBeta"
          autocomplete="off"
          placeholder="例如 fine-grained-tool-streaming-2025-05-14"
        />
      </a-form-item>
    </template>
  </section>
</template>

<script setup lang="ts">
import { DeleteOutlined, PlusOutlined } from '@ant-design/icons-vue'
import { computed, watch } from 'vue'

import type { AccountFormModel } from './accountFormTypes'
import { normalizedAccountApiKeys } from './accountCredentials'
import { ANTHROPIC_PROVIDER_CODE, normalizeProviderToken } from '@/shared/providerProtocol'

const props = defineProps<{
  baseUrlPlaceholder: string
  editing: boolean
  form: AccountFormModel
  title: string
}>()

const filledApiKeyCount = computed(() => normalizedAccountApiKeys(props.form).length)
const showApiKeyStrategy = computed(() => filledApiKeyCount.value > 1)
const showWeightInputs = computed(() => showApiKeyStrategy.value && props.form.apiKeyStrategy === 'weighted_round_robin')
const showBatchDeleteApiKeys = computed(() => props.form.apiKeys.some((value) => value.trim()))
const isAnthropicForm = computed(() => normalizeProviderToken(props.form.providerCode) === ANTHROPIC_PROVIDER_CODE)
const showAnthropicBaseUrlNotice = computed(() => isAnthropicForm.value && isNonOfficialAnthropicBaseUrl(props.form.baseUrl))

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

function isNonOfficialAnthropicBaseUrl(value: string): boolean {
  const text = value.trim()
  if (!text) return false
  try {
    const url = new URL(text)
    return url.hostname.toLowerCase() !== 'api.anthropic.com'
  } catch {
    return false
  }
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

.anthropic-base-url-notice {
  margin: -8px 0 16px;
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

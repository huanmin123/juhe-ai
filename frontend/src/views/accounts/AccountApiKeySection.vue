<template>
  <section class="form-section credential-section" autocomplete="off">
    <div class="form-section-head">
      <div>
        <h4>{{ title }} 配置</h4>
      </div>
    </div>
    <a-form-item :required="!editing">
      <template #label>
        <div class="api-key-label">
          <span>API Key</span>
          <a-radio-group
            v-if="showApiKeyStrategy"
            v-model:value="form.apiKeyStrategy"
            button-style="solid"
            size="small"
          >
            <a-radio-button value="round_robin">轮询</a-radio-button>
            <a-radio-button value="weighted_round_robin">权重</a-radio-button>
          </a-radio-group>
        </div>
      </template>
      <a-input-password
        v-if="editing"
        :value="form.apiKey"
        autocomplete="new-password"
        data-lpignore="true"
        data-1p-ignore="true"
        data-form-type="other"
        placeholder="留空保留原 API Key"
        @update:value="updateSingleApiKey"
      />
      <div v-else class="api-key-input-list">
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
        <div v-if="filledApiKeyCount > 1" class="api-key-batch-summary">将保存为 1 个账户，账户内 {{ filledApiKeyCount }} 个 API Key 按策略分配请求</div>
      </div>
    </a-form-item>
    <a-form-item label="Base URL" required extra="填写服务根地址或 /v1 版本根地址，例如 https://api.openai.com/v1；不要填写 /responses 等具体接口路径。">
      <a-input
        v-model:value="form.baseUrl"
        autocomplete="off"
        data-lpignore="true"
        data-1p-ignore="true"
        data-form-type="other"
        :placeholder="baseUrlPlaceholder"
      />
    </a-form-item>
  </section>
</template>

<script setup lang="ts">
import { DeleteOutlined, PlusOutlined } from '@ant-design/icons-vue'
import { computed, watch } from 'vue'

import type { AccountFormModel } from './accountFormTypes'
import { normalizedAccountApiKeys } from './accountCredentials'

const props = defineProps<{
  baseUrlPlaceholder: string
  editing: boolean
  form: AccountFormModel
  title: string
}>()

const filledApiKeyCount = computed(() => normalizedAccountApiKeys(props.form).length)
const showApiKeyStrategy = computed(() => !props.editing && filledApiKeyCount.value > 1)
const showWeightInputs = computed(() => showApiKeyStrategy.value && props.form.apiKeyStrategy === 'weighted_round_robin')

watch(
  [() => props.form.apiKeys.length, () => props.form.apiKeyStrategy],
  () => {
    syncApiKeyWeights()
  },
  { immediate: true },
)

function updateSingleApiKey(value: string): void {
  props.form.apiKey = value
  props.form.apiKeys = value.trim() ? [value] : []
  props.form.apiKeyWeights = value.trim() ? [1] : []
}

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
  display: inline-flex;
  flex-wrap: wrap;
  gap: 10px;
  align-items: center;
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
  grid-template-columns: minmax(0, 1fr) 96px;
}

.api-key-weight-input {
  width: 96px;
}

.api-key-row-actions {
  display: inline-flex;
  align-items: center;
  gap: 2px;
}

.api-key-batch-summary {
  color: #64748b;
  font-size: 12px;
}

@media (max-width: 640px) {
  .api-key-input-row {
    grid-template-columns: minmax(0, 1fr);
  }

  .api-key-credential-controls.has-weight {
    grid-template-columns: minmax(0, 1fr) 88px;
  }

  .api-key-weight-input {
    width: 88px;
  }

  .api-key-row-actions {
    justify-content: flex-end;
  }
}
</style>

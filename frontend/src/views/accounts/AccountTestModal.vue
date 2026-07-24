<template>
  <a-modal
    :open="open"
    title="测试账号连接"
    width="620px"
    :footer="null"
    :closable="true"
    :keyboard="true"
    :mask-closable="true"
    @cancel="close"
    @update:open="handleOpenUpdate"
  >
    <div v-if="account" class="test-modal">
      <div class="test-account-card">
        <div class="test-account-main">
          <div class="test-account-icon">▶</div>
          <div class="test-account-detail">
            <div class="test-account-name">{{ account.name }}</div>
            <div class="test-account-meta">
              <a-tag color="processing">{{ accountTypeText(account.type) }}</a-tag>
              <a-tag :color="proxyTagColor">{{ proxyTagText }}</a-tag>
              <a-tag color="geekblue">{{ currentProviderName }}</a-tag>
            </div>
          </div>
        </div>
        <a-tag :color="accountStatusColor(account)">{{ accountStatusText(account) }}</a-tag>
      </div>

      <a-form layout="vertical" class="test-form">
        <div class="test-config-row">
          <a-form-item class="test-config-field" :label="modelReadonly ? '检查模型' : '选择测试模型'">
            <a-input
              v-if="modelReadonly"
              class="readonly-model-input"
              :value="model"
              readonly
            />
            <a-select
              v-else
              :value="model"
              show-search
              :filter-option="false"
              :loading="modelsLoading"
              :disabled="running"
              :options="modelOptions"
              placeholder="选择测试模型"
              @dropdown-visible-change="$emit('load-model-options', $event)"
              @search="$emit('search-model-options', $event)"
              @update:value="$emit('update:model', String($event))"
            />
            <div v-if="modelsError" class="test-field-error">{{ modelsError }}</div>
          </a-form-item>
          <a-form-item class="test-config-field" :label="imageTest ? '检查方式' : '测试请求形态'">
            <a-select
              class="test-endpoint-select"
              :value="selectedEndpointModeSelectValue"
              :disabled="running || !canSelectEndpointMode"
              :options="testEndpointModeOptions"
              placeholder="无可测试请求形态"
              @update:value="handleTestEndpointModeUpdate"
            />
          </a-form-item>
        </div>
      </a-form>

      <div class="test-terminal">
        <div v-if="!outputLines.length" class="test-output-line muted">准备开始测试</div>
        <div
          v-for="(line, index) in outputLines"
          :key="index"
          class="test-output-line"
          :class="line.tone"
        >
          {{ line.text }}
        </div>
      </div>

      <div v-if="result && !imageTest" class="test-result-meta">
        <a-collapse class="test-result-collapse" ghost>
          <a-collapse-panel key="result" header="完整测试结果 JSON">
            <a-textarea :value="resultJson" :rows="8" readonly />
          </a-collapse-panel>
        </a-collapse>
      </div>

      <div class="test-modal-footer">
        <div class="test-footer-hint">
          <span>{{ modelReadonly ? '当前表单检查模型' : '本次人工测试配置' }}</span>
        </div>
        <a-space>
          <a-button v-if="!imageTest" :disabled="!result" @click="$emit('copy-result', resultJson)">复制完整结果</a-button>
          <a-button v-if="running" danger @click="$emit('stop')">停止测试</a-button>
          <a-button @click="close">关闭</a-button>
          <a-button
            v-if="!running"
            type="primary"
            :disabled="runDisabled"
            @click="$emit('run')"
          >
            {{ result ? '重试' : '开始测试' }}
          </a-button>
        </a-space>
      </div>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import type {
  AccountSummary,
  AccountSupportedEndpointMode,
  AccountTestResult,
  AccountTestTask
} from '@/types/domain'
import type { AccountTestEndpointMode } from './accountTestFlow'
import {
  accountTestSingleOutputLines,
  type AccountTestOutputLine
} from './accountTestDisplayFormatters'
import { accountTypeText } from './accountBasicFormatters'
import {
  accountStatusColor,
  accountStatusText
} from './accountFormatters'
import {
  accountEndpointModeLabel
} from './accountEndpointModes'

const props = defineProps<{
  account?: AccountSummary
  activeTask?: AccountTestTask
  model: string
  modelOptions: Array<{ label: string; value: string }>
  modelReadonly: boolean
  modelsError: string
  modelsLoading: boolean
  open: boolean
  providerName?: (providerCode?: string) => string
  result?: AccountTestResult
  running: boolean
  testEndpointMode: AccountTestEndpointMode
  testEndpointModes: AccountSupportedEndpointMode[]
}>()

const emit = defineEmits<{
  (event: 'close'): void
  (event: 'copy-result', value: string): void
  (event: 'load-model-options', open: boolean): void
  (event: 'search-model-options', keyword: string): void
  (event: 'run'): void
  (event: 'stop'): void
  (event: 'update:model', value: string): void
  (event: 'update:open', value: boolean): void
  (event: 'update:testEndpointMode', value: AccountTestEndpointMode): void
}>()

const testEndpointModeOptions = computed(() => props.testEndpointModes.map((value) => ({
  label: value === 'images_json' ? '图像模型可用性（Models API）' : accountEndpointModeLabel(value, props.account),
  value
})))
const canSelectEndpointMode = computed(() => testEndpointModeOptions.value.length > 1)
const selectedEndpointModeSelectValue = computed<AccountSupportedEndpointMode | undefined>(() => {
  if (props.testEndpointMode !== 'account_default' && props.testEndpointModes.includes(props.testEndpointMode)) {
    return props.testEndpointMode
  }
  return props.testEndpointModes[0]
})
const selectedEndpointModeText = computed(() => {
  const selected = selectedEndpointModeSelectValue.value
  return testEndpointModeOptions.value.find((item) => item.value === selected)?.label
    ?? '无可测试请求形态'
})
const resultJson = computed(() => props.result ? JSON.stringify(props.result, null, 2) : '')
const imageTest = computed(() => props.result?.testEndpointMode === 'images_json'
  || selectedEndpointModeSelectValue.value === 'images_json'
)
const runDisabled = computed(() => (
  props.modelsLoading
  || !props.model.trim()
  || !selectedEndpointModeSelectValue.value
))
const currentProviderName = computed(() => props.account
  ? props.providerName?.(props.account.providerCode) ?? '未知供应商'
  : ''
)
const proxyTagText = computed(() => props.account?.proxyProfileId ? '有代理' : '无代理')
const proxyTagColor = computed(() => {
  if (props.account?.proxyProfileUnavailable) return 'red'
  return props.account?.proxyProfileId ? 'cyan' : 'default'
})
const outputLines = computed<AccountTestOutputLine[]>(() => accountTestSingleOutputLines({
  account: props.account,
  activeTask: props.activeTask,
  testEndpointMode: props.testEndpointMode,
  selectedEndpointModeText: selectedEndpointModeText.value,
  model: props.model,
  providerLabel: (account) => props.providerName?.(account.providerCode) ?? '未知供应商',
  result: props.result,
  running: props.running
}))

function close() {
  emit('close')
}

function handleOpenUpdate(value: boolean) {
  if (!value) {
    close()
    return
  }
  emit('update:open', value)
}

function handleTestEndpointModeUpdate(value: string | number | undefined): void {
  const option = testEndpointModeOptions.value.find((item) => item.value === value)
  if (option) {
    emit('update:testEndpointMode', option.value)
  }
}
</script>

<style scoped>
.test-modal {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.test-account-card {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 14px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #f8fafc;
}

.test-account-main {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
}

.test-account-icon {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 40px;
  height: 40px;
  color: #fff;
  border-radius: 8px;
  background: #1677ff;
}

.test-account-name {
  overflow: hidden;
  color: #0f172a;
  font-size: 16px;
  font-weight: 700;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.test-account-detail {
  min-width: 0;
}

.test-account-meta {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 4px;
  color: #64748b;
  font-size: 12px;
}

.test-form :deep(.ant-form-item) {
  margin-bottom: 0;
}

.test-config-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 12px;
}

.test-config-field {
  min-width: 0;
}

.test-endpoint-select {
  width: 100%;
}

.readonly-model-input {
  font-family: Consolas, 'Courier New', monospace;
}

.test-field-error {
  margin-top: 6px;
  color: #ff4d4f;
  font-size: 12px;
  line-height: 1.5;
}

.test-terminal {
  min-height: 112px;
  max-height: 300px;
  overflow: auto;
  padding: 14px 16px;
  color: #dbeafe;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 13px;
  line-height: 1.65;
  white-space: pre-wrap;
  border: 1px solid #334155;
  border-radius: 8px;
  background: #0f172a;
}

.test-output-line.muted {
  color: #94a3b8;
}

.test-output-line.info {
  color: #60a5fa;
}

.test-output-line.success {
  color: #34d399;
}

.test-output-line.warning {
  color: #facc15;
}

.test-output-line.error {
  color: #f87171;
}

.test-output-line.label {
  color: #facc15;
  font-weight: 700;
}

.test-output-line.divider {
  height: 1px;
  padding: 0;
  margin: 10px 0;
  overflow: hidden;
  background: #334155;
}

.test-result-meta {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.test-result-collapse {
  border-radius: 8px;
  background: #f8fafc;
}

.test-modal-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  padding-top: 12px;
  border-top: 1px solid #e2e8f0;
}

.test-footer-hint {
  display: flex;
  gap: 16px;
  color: #64748b;
  font-size: 12px;
}

@media (max-width: 720px) {
  .test-account-card,
  .test-modal-footer {
    align-items: stretch;
    flex-direction: column;
  }

  .test-account-meta {
    flex-wrap: wrap;
  }

  .test-config-row {
    grid-template-columns: 1fr;
  }
}
</style>

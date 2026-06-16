<template>
  <a-modal
    :open="open"
    :title="modalTitle"
    :width="modalWidth"
    :footer="null"
    :closable="true"
    :keyboard="true"
    :mask-closable="!running"
    @cancel="close"
    @update:open="handleOpenUpdate"
  >
    <div v-if="hasTestTarget" class="test-modal">
      <div class="test-account-card">
        <div class="test-account-main">
          <div class="test-account-icon">▶</div>
          <div class="test-account-detail">
            <div class="test-account-name">{{ headerTitle }}</div>
            <div v-if="!isBatchMode && account" class="test-account-meta">
              <a-tag color="processing">{{ accountTypeText(account.type) }}</a-tag>
              <a-tag :color="proxyTagColor">{{ proxyTagText }}</a-tag>
              <a-tag color="geekblue">{{ currentProviderName }}</a-tag>
            </div>
            <div v-else class="test-account-meta">
              <a-tag color="processing">{{ batchItems.length }} 个账户</a-tag>
              <a-tag color="geekblue">{{ batchSelectedCompatibilityText }}</a-tag>
              <a-tag color="cyan">优先模型 {{ model }}</a-tag>
            </div>
          </div>
        </div>
        <a-tag v-if="!isBatchMode && account" :color="accountStatusColor(account)">{{ accountStatusText(account) }}</a-tag>
        <a-tag v-else :color="batchStatusColor">{{ batchStatusText }}</a-tag>
      </div>

      <a-form layout="vertical" class="test-form">
        <div class="test-config-row">
          <a-form-item class="test-config-field" :label="isBatchMode ? '优先测试模型' : '选择测试模型'">
            <a-select
              :value="model"
              show-search
              :loading="modelsLoading"
              :disabled="running"
              :options="modelOptions"
              placeholder="选择测试模型"
              @update:value="$emit('update:model', String($event))"
            />
          </a-form-item>
          <a-form-item v-if="showClientCompatibilityControl" class="test-config-field" label="客户端兼容">
            <a-select
              :value="clientCompatibility"
              :disabled="running"
              :options="clientCompatibilityOptions"
              @update:value="handleCompatibilityUpdate"
            />
          </a-form-item>
          <a-form-item v-else class="test-config-field" label="客户端兼容">
            <a-input :value="fixedOAuthCompatibilityText" disabled />
          </a-form-item>
        </div>
      </a-form>

      <div class="test-terminal">
        <div v-if="!outputLines.length" class="test-output-line muted">准备开始测试</div>
        <div v-for="(line, index) in outputLines" :key="index" class="test-output-line" :class="line.tone">{{ line.text }}</div>
      </div>

      <div v-if="isBatchMode" class="batch-test-results">
        <div class="batch-test-results-header">
          <div>
            <div class="batch-test-results-title">测试结果</div>
            <div class="batch-test-results-summary">
              已完成 {{ batchCompletedCount }} / {{ batchItems.length }}，成功 {{ batchSuccessCount }}，失败 {{ batchFailedCount }}<span v-if="batchStoppedCount">，已停止 {{ batchStoppedCount }}</span>
            </div>
          </div>
        </div>
        <div class="batch-test-result-list">
          <div v-for="item in batchItems" :key="item.account.id" class="batch-test-result-row">
            <div class="batch-test-result-main">
              <div class="batch-test-result-title">
                <a-tag :color="batchItemStatusColor(item)">{{ batchItemStatusText(item) }}</a-tag>
                <span class="batch-test-result-name">{{ item.account.name }}</span>
              </div>
              <div class="batch-test-result-meta">
                <span>{{ accountTypeText(item.account.type) }}</span>
                <span>{{ providerLabel(item.account) }}</span>
                <span>模型：{{ batchItemModelText(item) }}</span>
                <span v-if="batchItemDurationText(item)">耗时：{{ batchItemDurationText(item) }}</span>
                <span v-if="item.result?.statusCode">HTTP {{ item.result.statusCode }}</span>
              </div>
              <div class="batch-test-result-message" :class="{ failed: item.status === 'failed', success: item.status === 'success' }">
                {{ batchItemMessage(item) }}
              </div>
            </div>
            <a-button size="small" :disabled="!item.result" @click="$emit('copy-result', batchItemJson(item))">复制结果</a-button>
          </div>
        </div>
      </div>

      <div v-if="showResultJson" class="test-result-meta">
        <a-collapse class="test-result-collapse" ghost>
          <a-collapse-panel key="result" :header="isBatchMode ? '完整批量测试结果 JSON' : '完整测试结果 JSON'">
            <a-textarea :value="resultJson" :rows="8" readonly />
          </a-collapse-panel>
        </a-collapse>
      </div>

      <div class="test-modal-footer">
        <div class="test-footer-hint">
          <span>当前测试配置</span>
        </div>
        <a-space>
          <a-button :disabled="!showResultJson" @click="$emit('copy-result', resultJson)">复制完整结果</a-button>
          <a-button danger v-if="running" @click="$emit('stop')">停止测试</a-button>
          <a-button @click="close">{{ running ? '停止并关闭' : '关闭' }}</a-button>
          <a-button v-if="!running" type="primary" @click="$emit('run')">{{ runButtonText }}</a-button>
        </a-space>
      </div>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import type { AccountSummary, AccountTestResult, AccountTestTask } from '@/types/domain'
import type { AccountBatchTestItem, AccountTestClientCompatibility, AccountTestMode } from './accountTestFlow'
import {
  accountTestBatchCounts,
  accountTestBatchItemDurationText as batchItemDurationText,
  accountTestBatchItemJson as batchItemJson,
  accountTestBatchItemMessage as batchItemMessage,
  accountTestBatchItemModelText,
  accountTestBatchItemStatusColor as batchItemStatusColor,
  accountTestBatchItemStatusText as batchItemStatusText,
  accountTestBatchOutputLines,
  accountTestBatchResultSnapshot,
  accountTestBatchSelectedCompatibilityText,
  accountTestBatchStatusColor,
  accountTestBatchStatusText,
  accountTestSingleOutputLines,
  type AccountTestOutputLine
} from './accountTestDisplayFormatters'
import {
  accountTypeText
} from './accountBasicFormatters'
import {
  accountStatusColor,
  accountStatusText
} from './accountFormatters'

const props = defineProps<{
  account?: AccountSummary
  accounts: AccountSummary[]
  activeTask?: AccountTestTask
  batchItems: AccountBatchTestItem[]
  clientCompatibility: AccountTestClientCompatibility
  mode: AccountTestMode
  model: string
  modelOptions: Array<{ label: string; value: string }>
  modelsLoading: boolean
  open: boolean
  providerName?: (providerCode?: string) => string
  result?: AccountTestResult
  running: boolean
}>()

const emit = defineEmits<{
  (event: 'close'): void
  (event: 'copy-result', value: string): void
  (event: 'run'): void
  (event: 'stop'): void
  (event: 'update:clientCompatibility', value: AccountTestClientCompatibility): void
  (event: 'update:model', value: string): void
  (event: 'update:open', value: boolean): void
}>()

const isBatchMode = computed(() => props.mode === 'batch')
const hasTestTarget = computed(() => isBatchMode.value ? props.accounts.length > 0 : Boolean(props.account))
const modalTitle = computed(() => isBatchMode.value ? '批量测试账号连接' : '测试账号连接')
const modalWidth = computed(() => isBatchMode.value ? '860px' : '620px')
const headerTitle = computed(() => isBatchMode.value ? '批量测试账号连接' : props.account?.name ?? '')
const batchCounts = computed(() => accountTestBatchCounts(props.batchItems))
const batchSuccessCount = computed(() => batchCounts.value.success)
const batchFailedCount = computed(() => batchCounts.value.failed)
const batchStoppedCount = computed(() => batchCounts.value.stopped)
const batchCompletedCount = computed(() => batchCounts.value.completed)
const testTargetAccounts = computed(() => isBatchMode.value ? props.accounts : props.account ? [props.account] : [])
const showClientCompatibilityControl = computed(() => testTargetAccounts.value.some((account) => account.type === 'api_key'))
const fixedOAuthCompatibilityText = computed(() => 'Codex Responses（OAuth 固定）')
const batchSelectedCompatibilityText = computed(() => accountTestBatchSelectedCompatibilityText({
  clientCompatibility: props.clientCompatibility,
  fixedOAuthCompatibilityText: fixedOAuthCompatibilityText.value,
  showClientCompatibilityControl: showClientCompatibilityControl.value
}))
const batchStatusColor = computed(() => accountTestBatchStatusColor(batchCounts.value, props.running))
const batchStatusText = computed(() => accountTestBatchStatusText(batchCounts.value, props.running))
const showResultJson = computed(() => isBatchMode.value ? batchCompletedCount.value > 0 : Boolean(props.result))
const resultJson = computed(() => {
  if (isBatchMode.value) {
    return JSON.stringify(accountTestBatchResultSnapshot({
      batchItems: props.batchItems,
      clientCompatibility: props.clientCompatibility,
      model: props.model
    }), null, 2)
  }
  return props.result ? JSON.stringify(props.result, null, 2) : ''
})
const runButtonText = computed(() => {
  if (isBatchMode.value) return batchCompletedCount.value ? '重新批量测试' : '开始批量测试'
  return props.result ? '重试' : '开始测试'
})
const currentProviderName = computed(() => props.account ? providerLabel(props.account) : '')
const clientCompatibilityOptions: Array<{ label: string; value: AccountTestClientCompatibility }> = [
  { label: '跟随账户配置', value: 'account_default' },
  { label: 'OpenAI 标准', value: 'openai_standard' },
  { label: 'Codex Responses', value: 'codex_responses' }
]
const proxyTagText = computed(() => props.account?.proxyProfileId ? '有代理' : '无代理')
const proxyTagColor = computed(() => {
  if (props.account?.proxyProfileUnavailable) return 'red'
  return props.account?.proxyProfileId ? 'cyan' : 'default'
})
const outputLines = computed<AccountTestOutputLine[]>(() => {
  if (isBatchMode.value) {
    return accountTestBatchOutputLines({
      batchItems: props.batchItems,
      counts: batchCounts.value,
      model: props.model,
      running: props.running,
      selectedCompatibilityText: batchSelectedCompatibilityText.value
    })
  }
  return accountTestSingleOutputLines({
    account: props.account,
    activeTask: props.activeTask,
    clientCompatibility: props.clientCompatibility,
    fixedOAuthCompatibilityText: fixedOAuthCompatibilityText.value,
    model: props.model,
    providerLabel,
    result: props.result,
    running: props.running
  })
})

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

function providerLabel(account: AccountSummary): string {
  return props.providerName?.(account.providerCode) ?? '未知供应商'
}

function handleCompatibilityUpdate(value: string): void {
  if (!showClientCompatibilityControl.value) return
  if (value === 'codex_responses' || value === 'openai_standard' || value === 'account_default') {
    emit('update:clientCompatibility', value)
  }
}

function batchItemModelText(item: AccountBatchTestItem): string {
  return accountTestBatchItemModelText(item, props.model)
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
  border-radius: 14px;
  background: linear-gradient(135deg, #f8fafc 0%, #ffffff 100%);
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
  border-radius: 10px;
  background: #14b8a6;
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
  border-radius: 14px;
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

.batch-test-results {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.batch-test-results-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.batch-test-results-title {
  color: #0f172a;
  font-weight: 700;
}

.batch-test-results-summary {
  margin-top: 2px;
  color: #64748b;
  font-size: 12px;
}

.batch-test-result-list {
  display: flex;
  flex-direction: column;
  max-height: 320px;
  overflow: auto;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
}

.batch-test-result-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  padding: 12px;
  border-bottom: 1px solid #e2e8f0;
}

.batch-test-result-row:last-child {
  border-bottom: 0;
}

.batch-test-result-main {
  min-width: 0;
}

.batch-test-result-title {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.batch-test-result-name {
  overflow: hidden;
  color: #0f172a;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.batch-test-result-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 12px;
  margin-top: 6px;
  color: #64748b;
  font-size: 12px;
}

.batch-test-result-message {
  display: -webkit-box;
  max-width: 680px;
  margin-top: 6px;
  overflow: hidden;
  color: #64748b;
  font-size: 12px;
  line-height: 1.5;
  overflow-wrap: anywhere;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
}

.batch-test-result-message.success {
  color: #047857;
}

.batch-test-result-message.failed {
  color: #b91c1c;
}

.test-result-collapse {
  border-radius: 12px;
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
  .batch-test-result-row,
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

  .batch-test-result-row :deep(.ant-btn) {
    align-self: flex-start;
  }
}
</style>

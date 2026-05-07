<template>
  <a-modal
    :open="open"
    title="测试账号连接"
    width="620px"
    :footer="null"
    :closable="true"
    :keyboard="true"
    :mask-closable="!running"
    @cancel="close"
    @update:open="handleOpenUpdate"
  >
    <div v-if="account" class="test-modal">
      <div class="test-account-card">
        <div class="test-account-main">
          <div class="test-account-icon">▶</div>
          <div>
            <div class="test-account-name">{{ account.name }}</div>
            <div class="test-account-meta">
              <a-tag color="processing">{{ accountTypeText(account.type) }}</a-tag>
              <span>账号</span>
            </div>
          </div>
        </div>
        <a-tag :color="accountStatusColor(account)">{{ accountStatusText(account) }}</a-tag>
      </div>

      <a-form layout="vertical" class="test-form">
        <a-form-item label="选择测试模型">
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
      </a-form>

      <div class="test-terminal">
        <div v-if="!outputLines.length" class="test-output-line muted">准备开始测试</div>
        <div v-for="(line, index) in outputLines" :key="index" class="test-output-line" :class="line.tone">{{ line.text }}</div>
      </div>

      <div v-if="result" class="test-result-meta">
        <a-collapse class="test-result-collapse" ghost>
          <a-collapse-panel key="result" header="完整测试结果 JSON">
            <a-textarea :value="resultJson" :rows="8" readonly />
          </a-collapse-panel>
        </a-collapse>
      </div>

      <div class="test-modal-footer">
        <div class="test-footer-hint">
          <span>测试模型</span>
          <span>提示词："{{ prompt }}"</span>
        </div>
        <a-space>
          <a-button :disabled="!result" @click="$emit('copy-result', resultJson)">复制完整结果</a-button>
          <a-button danger v-if="running" @click="$emit('stop')">停止测试</a-button>
          <a-button @click="close">{{ running ? '停止并关闭' : '关闭' }}</a-button>
          <a-button v-if="!running" type="primary" @click="$emit('run')">{{ result ? '重试' : '开始测试' }}</a-button>
        </a-space>
      </div>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import type { AccountSummary, AccountTestResult } from '@/types/domain'
import {
  accountStatusColor,
  accountStatusText,
  accountTypeText,
  formatAccountTestDuration,
  formatErrorPolicyAction,
  formatTestTerminalResult,
  statusText
} from './accountFormatters'

interface TestOutputLine {
  text: string
  tone?: 'muted' | 'info' | 'success' | 'warning' | 'error' | 'label' | 'divider'
}

const props = defineProps<{
  account?: AccountSummary
  model: string
  modelOptions: Array<{ label: string; value: string }>
  modelsLoading: boolean
  open: boolean
  prompt: string
  result?: AccountTestResult
  running: boolean
}>()

const emit = defineEmits<{
  (event: 'close'): void
  (event: 'copy-result', value: string): void
  (event: 'run'): void
  (event: 'stop'): void
  (event: 'update:model', value: string): void
  (event: 'update:open', value: boolean): void
}>()

const resultJson = computed(() => props.result ? JSON.stringify(props.result, null, 2) : '')
const outputLines = computed<TestOutputLine[]>(() => {
  const account = props.account
  if (!account || (!props.running && !props.result)) return []
  const lines: TestOutputLine[] = [
    { text: `开始测试账号：${account.name}`, tone: 'info' },
    { text: `账号类型：${accountTypeText(account.type)}`, tone: 'muted' }
  ]

  if (props.running) {
    lines.push({ text: '正在连接 OpenAI API...', tone: 'warning' })
    lines.push({ text: `使用模型：${props.model}`, tone: 'success' })
    lines.push({ text: `发送测试消息："${props.prompt}"`, tone: 'muted' })
    return lines
  }

  if (!props.result) {
    lines.push({ text: '点击「开始测试」后会显示完整返回结果。', tone: 'muted' })
    return lines
  }

  lines.push({ text: props.result.statusCode && props.result.statusCode >= 200 && props.result.statusCode < 300 ? '已连接到 API' : 'API 返回错误', tone: props.result.success ? 'success' : 'error' })
  lines.push({ text: `使用模型：${props.result.model || props.model}`, tone: 'success' })
  lines.push({ text: `发送测试消息："${props.prompt}"`, tone: 'muted' })
  lines.push({ text: '响应：', tone: 'label' })
  const outputText = formatTestTerminalResult(props.result)
  if (outputText) {
    lines.push({ text: outputText, tone: props.result.success ? 'success' : 'error' })
  } else {
    lines.push({ text: props.result.message, tone: props.result.success ? 'success' : 'error' })
  }
  if (props.result.errorPolicyAction && props.result.errorPolicyAction !== 'none') {
    const reason = props.result.errorPolicyReason ? `，原因：${props.result.errorPolicyReason}` : ''
    lines.push({ text: `错误处理策略：${formatErrorPolicyAction(props.result.errorPolicyAction)}${reason}`, tone: 'warning' })
  }
  if (props.result.accountStatusChanged || props.result.accountStatus) {
    const status = props.result.accountStatus ? statusText(props.result.accountStatus) : '未变化'
    lines.push({ text: `账号状态：${status}`, tone: props.result.accountStatusChanged ? 'warning' : 'muted' })
  }
  lines.push({ text: '', tone: 'divider' })
  const completionText = props.result.success ? '✓ 测试完成！' : '✕ 测试失败！'
  const firstTokenText = props.result.firstTokenMs !== undefined ? `，首 token：${formatAccountTestDuration(props.result.firstTokenMs)}` : ''
  lines.push({ text: `${completionText}  总耗时：${formatAccountTestDuration(props.result.durationMs)}${firstTokenText}`, tone: props.result.success ? 'success' : 'error' })
  return lines
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
</style>

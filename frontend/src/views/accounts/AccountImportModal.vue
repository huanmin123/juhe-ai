<template>
  <a-modal
    :open="open"
    title="导入 AI 账户"
    width="1080px"
    :footer="null"
    destroy-on-close
    @update:open="emit('update:open', $event)"
    @cancel="emit('update:open', false)"
  >
    <div class="account-import">
      <a-alert
        class="import-alert"
        type="info"
        show-icon
        :message="targetSystemAccountLabel ? `目标系统账户：${targetSystemAccountLabel}` : '导入目标为当前系统账户'"
        description="只支持 juhe-ai-account-import v1 JSON。其他系统的数据请先用 AI 或脚本整理为本协议后再导入。"
      />

      <div class="import-options">
        <a-checkbox v-model:checked="options.createMissingGroups">自动创建缺失分组</a-checkbox>
        <a-checkbox v-model:checked="options.createMissingProxies">自动创建缺失代理</a-checkbox>
        <a-checkbox v-model:checked="options.skipDuplicates">导入时跳过重复账户</a-checkbox>
      </div>

      <div class="import-layout">
        <section class="import-editor">
          <div class="import-section-head">
            <h4>导入 JSON</h4>
            <div class="import-head-actions">
              <a-button size="small" @click="fillTemplate">填入模板</a-button>
              <a-button size="small" @click="copyTemplate">复制模板</a-button>
            </div>
          </div>
          <a-textarea
            v-model:value="importText"
            class="import-textarea"
            :auto-size="{ minRows: 18, maxRows: 26 }"
            placeholder="粘贴 juhe-ai-account-import v1 JSON"
          />
          <div class="import-actions">
            <a-button :disabled="!importText.trim()" :loading="previewLoading" @click="runPreview">解析预览</a-button>
            <a-button type="primary" :disabled="!previewResult?.canImport" :loading="importLoading" @click="confirmImport">确认导入</a-button>
          </div>
        </section>

        <section class="protocol-panel">
          <div class="import-section-head">
            <h4>AI 提示词</h4>
            <a-button size="small" @click="downloadProtocolMarkdown">
              <template #icon>
                <DownloadOutlined />
              </template>
              导出协议 Markdown
            </a-button>
          </div>
          <a-typography-paragraph class="ai-prompt" :copyable="{ text: aiConversionPrompt }">
            {{ aiConversionPrompt }}
          </a-typography-paragraph>
        </section>
      </div>

      <div v-if="previewResult" class="preview-panel">
        <div class="summary-grid">
          <div class="summary-item">
            <span>账户</span>
            <strong>{{ previewResult.summary.accounts.create }}</strong>
            <em>将创建 / 共 {{ previewResult.summary.accounts.total }}</em>
          </div>
          <div class="summary-item">
            <span>代理</span>
            <strong>{{ previewResult.summary.proxies.create }}</strong>
            <em>将创建 / 复用 {{ previewResult.summary.proxies.reuse }}</em>
          </div>
          <div class="summary-item">
            <span>分组</span>
            <strong>{{ previewResult.summary.groups.create }}</strong>
            <em>将自动创建</em>
          </div>
          <div class="summary-item" :class="{ danger: previewResult.summary.accounts.failed || previewResult.summary.proxies.failed }">
            <span>错误</span>
            <strong>{{ previewResult.summary.accounts.failed + previewResult.summary.proxies.failed }}</strong>
            <em>修正后再导入</em>
          </div>
        </div>

        <a-alert
          v-if="previewResult.messages.length"
          class="preview-message"
          type="error"
          show-icon
          :message="previewResult.messages.join('；')"
        />

        <a-table
          size="small"
          :columns="accountColumns"
          :data-source="previewResult.accounts"
          :pagination="{ pageSize: 8, size: 'small' }"
          row-key="index"
          :scroll="{ x: 1000 }"
        >
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'providerCode'">
              <span>{{ providerDisplayName(record.providerCode) }}</span>
            </template>
            <template v-else-if="column.key === 'action'">
              <a-tag :color="actionColor(record.action)">{{ actionText(record.action) }}</a-tag>
            </template>
            <template v-else-if="column.key === 'message'">
              <span :class="{ 'row-error': record.messages.length }">{{ itemMessage(record) }}</span>
            </template>
          </template>
        </a-table>

        <a-table
          v-if="previewResult.proxies.length"
          class="proxy-preview-table"
          size="small"
          :columns="proxyColumns"
          :data-source="previewResult.proxies"
          :pagination="false"
          row-key="index"
        >
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'action'">
              <a-tag :color="actionColor(record.action)">{{ actionText(record.action) }}</a-tag>
            </template>
            <template v-else-if="column.key === 'message'">
              <span :class="{ 'row-error': record.messages.length }">{{ itemMessage(record) }}</span>
            </template>
          </template>
        </a-table>
      </div>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { DownloadOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'
import { reactive, ref, watch } from 'vue'

import { api } from '@/api/client'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import { providerDisplayName } from '@/shared/providerDisplay'
import type { AccountImportItem, AccountImportOptions, AccountImportProxyItem, AccountImportResult } from '@/types/domain'
import { accountImportProtocolMarkdown, aiConversionPrompt, importTemplate } from './accountImportProtocol'

const props = defineProps<{
  isManagementView: boolean
  open: boolean
  scopeParams?: { systemAccountId: string }
  targetSystemAccountLabel?: string
}>()

const emit = defineEmits<{
  (event: 'update:open', value: boolean): void
  (event: 'imported'): void
}>()

const importText = ref('')
const previewResult = ref<AccountImportResult>()
const previewLoading = ref(false)
const importLoading = ref(false)
const options = reactive<Required<AccountImportOptions>>({
  createMissingGroups: true,
  createMissingProxies: true,
  skipDuplicates: true
})

const accountColumns = [
  { title: '#', dataIndex: 'index', key: 'index', width: 64 },
  { title: '账户名称', dataIndex: 'name', key: 'name', width: 180 },
  { title: '供应商', dataIndex: 'providerCode', key: 'providerCode', width: 100 },
  { title: '类型', dataIndex: 'accountType', key: 'accountType', width: 90 },
  { title: '分组', dataIndex: 'groupName', key: 'groupName', width: 140 },
  { title: '动作', key: 'action', width: 100 },
  { title: '提示', key: 'message', width: 260 }
]

const proxyColumns = [
  { title: '#', dataIndex: 'index', key: 'index', width: 64 },
  { title: '代理 ref', dataIndex: 'ref', key: 'ref', width: 160 },
  { title: '代理名称', dataIndex: 'name', key: 'name', width: 180 },
  { title: '动作', key: 'action', width: 100 },
  { title: '提示', key: 'message' }
]

watch(() => props.open, (open) => {
  if (!open) return
  previewResult.value = undefined
})

watch([importText, () => options.createMissingGroups, () => options.createMissingProxies, () => options.skipDuplicates], () => {
  previewResult.value = undefined
})

function fillTemplate() {
  importText.value = importTemplate
}

async function copyTemplate() {
  await copyTextToClipboard(importTemplate)
}

function downloadProtocolMarkdown() {
  const timestamp = new Date().toISOString().slice(0, 10)
  downloadTextFile(`juhe-ai-account-import-v1-${timestamp}.md`, accountImportProtocolMarkdown, 'text/markdown;charset=utf-8')
  message.success('协议说明 Markdown 已导出')
}

async function runPreview() {
  const data = parseImportJson()
  if (!data) return
  previewLoading.value = true
  try {
    const payload = { data, options: { ...options } }
    previewResult.value = props.isManagementView
      ? await api.accounts.importPreview(payload, props.scopeParams)
      : await api.myAccounts.importPreview(payload)
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '解析导入 JSON 失败'))
  } finally {
    previewLoading.value = false
  }
}

async function confirmImport() {
  const data = parseImportJson()
  if (!data) return
  importLoading.value = true
  try {
    const payload = { data, options: { ...options } }
    previewResult.value = props.isManagementView
      ? await api.accounts.importConfirm(payload, props.scopeParams)
      : await api.myAccounts.importConfirm(payload)
    const created = previewResult.value.summary.accounts.create
    const skipped = previewResult.value.summary.accounts.skip
    message.success(`导入完成：创建 ${created} 个账户，跳过 ${skipped} 个`)
    emit('imported')
    if (previewResult.value.summary.accounts.failed === 0) {
      emit('update:open', false)
    }
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '导入账户失败'))
  } finally {
    importLoading.value = false
  }
}

function parseImportJson(): unknown | undefined {
  try {
    return JSON.parse(importText.value)
  } catch {
    message.error('JSON 解析失败，请检查格式是否完整')
    return undefined
  }
}

function actionText(action: AccountImportItem['action']): string {
  if (action === 'create') return '创建'
  if (action === 'reuse') return '复用'
  if (action === 'skip') return '跳过'
  return '失败'
}

function actionColor(action: AccountImportItem['action']): string {
  if (action === 'create') return 'blue'
  if (action === 'reuse') return 'green'
  if (action === 'skip') return 'default'
  return 'red'
}

function itemMessage(item: AccountImportItem | AccountImportProxyItem): string {
  if (item.messages.length) return item.messages.join('；')
  if (item.warnings.length) return item.warnings.join('；')
  return '校验通过'
}

function downloadTextFile(filename: string, content: string, type: string): void {
  const blob = new Blob([content], { type })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  document.body.appendChild(link)
  link.click()
  link.remove()
  URL.revokeObjectURL(url)
}
</script>

<style scoped>
.account-import {
  display: grid;
  gap: 16px;
}

.import-alert {
  border-radius: 10px;
}

.import-options {
  display: flex;
  flex-wrap: wrap;
  gap: 12px 18px;
}

.import-layout {
  display: grid;
  grid-template-columns: minmax(0, 1.5fr) minmax(280px, 0.85fr);
  gap: 16px;
}

.import-editor,
.protocol-panel,
.preview-panel {
  padding: 14px;
  border: 1px solid #e8edf5;
  border-radius: 12px;
  background: #fff;
}

.import-section-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 10px;
}

.import-section-head h4 {
  margin: 0;
  color: #0f172a;
  font-size: 15px;
}

.import-head-actions,
.import-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.import-textarea {
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

.import-actions {
  justify-content: flex-end;
  margin-top: 10px;
}

.ai-prompt {
  margin: 0;
  padding: 10px 12px;
  border: 1px dashed #cbd5e1;
  border-radius: 10px;
  color: #475569;
  background: #f8fafc;
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
}

.summary-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 10px;
  margin-bottom: 12px;
}

.summary-item {
  padding: 10px 12px;
  border: 1px solid #e2e8f0;
  border-radius: 10px;
  background: #f8fafc;
}

.summary-item span,
.summary-item em {
  display: block;
  color: #64748b;
  font-size: 12px;
  font-style: normal;
}

.summary-item strong {
  display: block;
  margin: 2px 0;
  color: #0f172a;
  font-size: 22px;
  line-height: 1.2;
}

.summary-item.danger strong {
  color: #dc2626;
}

.preview-message,
.proxy-preview-table {
  margin-top: 12px;
}

.row-error {
  color: #dc2626;
}

@media (max-width: 900px) {
  .import-layout,
  .summary-grid {
    grid-template-columns: 1fr;
  }
}
</style>

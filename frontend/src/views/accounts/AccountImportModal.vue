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
        :description="sourceDescription"
      />

      <div class="import-options">
        <label class="import-mode-field">
          <span>导入模式</span>
          <a-select v-model:value="sourceMode" class="import-mode-select" :options="sourceModeOptions" />
        </label>
        <a-checkbox v-model:checked="options.createMissingGroups">自动创建缺失分组</a-checkbox>
        <a-checkbox v-model:checked="options.createMissingProxies">自动创建缺失代理</a-checkbox>
        <a-checkbox v-model:checked="options.skipDuplicates">导入时跳过重复账户</a-checkbox>
      </div>

      <div class="import-layout">
        <section class="import-editor">
          <div class="import-section-head">
            <h4>{{ sourceEditorTitle }}</h4>
            <div v-if="sourceMode === 'native'" class="import-head-actions">
              <a-button size="small" @click="fillTemplate">填入模板</a-button>
              <a-button size="small" @click="copyTemplate">复制模板</a-button>
            </div>
          </div>
          <a-textarea
            v-model:value="importText"
            class="import-textarea"
            :auto-size="{ minRows: 18, maxRows: 26 }"
            :placeholder="sourcePlaceholder"
          />
          <div class="import-actions">
            <a-button :disabled="!importText.trim()" :loading="previewLoading" @click="runPreview">解析预览</a-button>
            <a-button type="primary" :disabled="!previewResult?.canImport" :loading="importLoading" @click="confirmImport">确认导入</a-button>
          </div>
        </section>

        <section class="protocol-panel">
          <div class="import-section-head">
            <h4>{{ sourceMode === 'native' ? 'AI 提示词' : '来源格式' }}</h4>
            <a-button v-if="sourceMode === 'native'" size="small" @click="downloadProtocolMarkdown">
              <template #icon>
                <DownloadOutlined />
              </template>
              导出协议 Markdown
            </a-button>
          </div>
          <a-typography-paragraph class="ai-prompt" :copyable="sourceMode === 'native' ? { text: aiConversionPrompt } : false">
            {{ sourceGuide }}
          </a-typography-paragraph>
          <template v-if="sourceExample">
            <div class="source-example-head">
              <span>格式示例</span>
              <a-button size="small" @click="copySourceExample">复制示例</a-button>
            </div>
            <pre class="source-example">{{ sourceExample }}</pre>
          </template>
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

        <div v-if="previewResult.source.mode !== 'native'" class="source-summary">
          <span>来源处理</span>
          <strong>{{ previewResult.source.accepted }} / {{ previewResult.source.records }}</strong>
          <em>接受 / 读取，跳过 {{ previewResult.source.skipped }} 条，忽略 {{ previewResult.source.ignoredFields }} 个非核心字段</em>
        </div>

        <a-alert
          v-if="previewResult.messages.length"
          class="preview-message"
          type="error"
          show-icon
          :message="previewResult.messages.join('；')"
        />

        <a-alert
          v-if="previewResult.source.messages.length"
          class="preview-message"
          type="warning"
          show-icon
          message="来源记录处理提示"
          :description="previewResult.source.messages.join('；')"
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
import { computed, reactive, ref, watch } from 'vue'

import { api } from '@/api/client'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import { providerDisplayName } from '@/shared/providerDisplay'
import type { AccountImportItem, AccountImportOptions, AccountImportProxyItem, AccountImportResult, AccountImportSourceMode } from '@/types/domain'
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
const sourceMode = ref<AccountImportSourceMode>('native')
const previewResult = ref<AccountImportResult>()
const previewLoading = ref(false)
const importLoading = ref(false)
const options = reactive<Required<AccountImportOptions>>({
  createMissingGroups: true,
  createMissingProxies: true,
  skipDuplicates: true
})

const sourceModeOptions = [
  { label: '原生', value: 'native' },
  { label: 'Sub2API', value: 'sub2api' },
  { label: 'NewAPI', value: 'newapi' },
  { label: 'CLIProxyAPI', value: 'cpa' },
  { label: 'One-API', value: 'oneapi' }
] satisfies Array<{ label: string; value: AccountImportSourceMode }>

const sourceEditorTitle = computed(() => sourceMode.value === 'native' ? '导入 JSON' : `${sourceModeLabel(sourceMode.value)} 数据`)
const sourcePlaceholder = computed(() => sourceMode.value === 'cpa'
  ? '粘贴 CLIProxyAPI config.yaml 或 Codex auth JSON'
  : sourceMode.value === 'native'
    ? '粘贴 juhe-ai-account-import v1 JSON'
    : `粘贴 ${sourceModeLabel(sourceMode.value)} 的 JSON 数据`)
const sourceDescription = computed(() => sourceMode.value === 'native'
  ? '原生模式只接受 juhe-ai-account-import v1 JSON，并保持严格字段校验。'
  : `${sourceModeLabel(sourceMode.value)} 模式只提取可确认的 OpenAI 账户；不支持的供应商和非核心字段会在预览中跳过或计数忽略。`)
const sourceGuide = computed(() => {
  if (sourceMode.value === 'native') return aiConversionPrompt
  if (sourceMode.value === 'sub2api') return '支持 Sub2API sub2api-data / sub2api-bundle v1 的 accounts 与 proxies。仅导入 OpenAI API Key/OAuth。'
  if (sourceMode.value === 'newapi') return '支持 NewAPI Channel JSON。仅识别来源定义中明确的 OpenAI Channel，key 映射为 API Key。'
  if (sourceMode.value === 'oneapi') return '支持 One-API Channel JSON。仅识别 OpenAI Channel，数字 type 不做跨项目猜测。'
  return '支持 CLIProxyAPI config.yaml 中 codex-api-key、openai-compatibility，以及 type=codex 的 auth JSON。'
})

const sourceExample = computed(() => {
  if (sourceMode.value === 'native') return ''
  if (sourceMode.value === 'sub2api') return `{
  "type": "sub2api-data",
  "version": 1,
  "accounts": [{
    "name": "OpenAI API Key",
    "platform": "openai",
    "type": "apikey",
    "credentials": {
      "api_key": "<API_KEY>",
      "base_url": "https://api.openai.com/v1"
    }
  }]
}`
  if (sourceMode.value === 'newapi') return `[
  {
    "type": 1,
    "name": "OpenAI Channel",
    "key": "<API_KEY>",
    "base_url": "https://api.openai.com/v1",
    "group": "默认分组",
    "status": 1
  }
]`
  if (sourceMode.value === 'oneapi') return `[
  {
    "type": 1,
    "name": "OpenAI Channel",
    "key": "<API_KEY>",
    "base_url": "https://api.openai.com/v1",
    "group": "默认分组",
    "status": 1
  }
]`
  return `openai-compatibility:
  - name: OpenAI 上游
    base-url: https://api.openai.com/v1
    api-key-entries:
      - api-key: <API_KEY>`
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

watch([importText, sourceMode, () => options.createMissingGroups, () => options.createMissingProxies, () => options.skipDuplicates], () => {
  previewResult.value = undefined
})

function fillTemplate() {
  importText.value = importTemplate
}

async function copyTemplate() {
  await copyTextToClipboard(importTemplate)
}

async function copySourceExample() {
  await copyTextToClipboard(sourceExample.value)
  message.success('来源格式示例已复制')
}

function downloadProtocolMarkdown() {
  const timestamp = new Date().toISOString().slice(0, 10)
  downloadTextFile(`juhe-ai-account-import-v1-${timestamp}.md`, accountImportProtocolMarkdown, 'text/markdown;charset=utf-8')
  message.success('协议说明 Markdown 已导出')
}

async function runPreview() {
  const data = parseImportInput()
  if (!data) return
  previewLoading.value = true
  try {
    const payload = { data, sourceMode: sourceMode.value, options: { ...options } }
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
  const data = parseImportInput()
  if (!data) return
  importLoading.value = true
  try {
    const payload = { data, sourceMode: sourceMode.value, options: { ...options } }
    previewResult.value = props.isManagementView
      ? await api.accounts.importConfirm(payload, props.scopeParams)
      : await api.myAccounts.importConfirm(payload)
    const created = previewResult.value.summary.accounts.create
    const skipped = previewResult.value.summary.accounts.skip
    const sourceSkipped = previewResult.value.source.skipped
    message.success(`导入完成：创建 ${created} 个账户，跳过 ${skipped + sourceSkipped} 个`)
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

function parseImportInput(): unknown | undefined {
  if (sourceMode.value === 'cpa') return importText.value
  try {
    return JSON.parse(importText.value)
  } catch {
    message.error('JSON 解析失败，请检查格式是否完整')
    return undefined
  }
}

function sourceModeLabel(mode: AccountImportSourceMode): string {
  if (mode === 'native') return '原生'
  if (mode === 'sub2api') return 'Sub2API'
  if (mode === 'newapi') return 'NewAPI'
  if (mode === 'oneapi') return 'One-API'
  return 'CLIProxyAPI'
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

.import-mode-field {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  color: #334155;
  font-size: 13px;
  font-weight: 600;
}

.import-mode-select {
  min-width: 190px;
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

.source-example-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin: 14px 0 6px;
  color: #334155;
  font-size: 12px;
  font-weight: 600;
}

.source-example {
  max-height: 304px;
  margin: 0;
  padding: 10px 12px;
  overflow: auto;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  color: #334155;
  background: #f8fafc;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 1.55;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
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

.source-summary {
  display: grid;
  gap: 2px;
  margin: 0 0 12px;
  padding: 10px 12px;
  border: 1px solid #dbeafe;
  border-radius: 10px;
  background: #eff6ff;
}

.source-summary span,
.source-summary em {
  color: #475569;
  font-size: 12px;
  font-style: normal;
}

.source-summary strong {
  color: #0f172a;
  font-size: 18px;
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

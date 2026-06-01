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
            <h4>协议说明</h4>
          </div>
          <dl class="protocol-list">
            <div>
              <dt>type</dt>
              <dd>固定为 juhe-ai-account-import</dd>
            </div>
            <div>
              <dt>version</dt>
              <dd>当前固定为 1</dd>
            </div>
            <div>
              <dt>proxies</dt>
              <dd>可选代理数组，账户通过 proxyRef 引用代理 ref</dd>
            </div>
            <div>
              <dt>accounts</dt>
              <dd>必填账户数组，API Key 填 api_key，OAuth 填 refresh_token 或 access_token</dd>
            </div>
          </dl>
          <a-typography-paragraph class="ai-prompt" copyable>
            请把我提供的账号数据转换为 juhe-ai-account-import v1 JSON。只输出合法 JSON，不要解释；每个账户都必须显式写 providerCode、type、status 和 groupName 或 groupId；API Key 账号写入 credentials.api_key 和 credentials.base_url；OAuth 账号保留 refresh_token、access_token、id_token、account_id、email 和 credentials.base_url；代理放入 proxies 并用 proxyRef 引用；不确定的信息写入 notes。
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
          :scroll="{ x: 820 }"
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
import { message } from '@/lib/antd'
import { reactive, ref, watch } from 'vue'

import { api } from '@/api/client'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import type { AccountImportItem, AccountImportOptions, AccountImportProxyItem, AccountImportResult } from '@/types/domain'

const props = defineProps<{
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

const importTemplate = JSON.stringify({
  type: 'juhe-ai-account-import',
  version: 1,
  proxies: [
    {
      ref: 'proxy-hk-1',
      name: '香港代理 1',
      type: 'http',
      host: '127.0.0.1',
      port: 7890,
      username: '',
      password: '',
      enabled: true
    }
  ],
  accounts: [
    {
      ref: 'openai-key-001',
      name: 'OpenAI API Key 账号 1',
      providerCode: 'openai',
      type: 'api_key',
      status: 'active',
      groupName: '默认 OpenAI 分组',
      concurrencyLimit: 3,
      priority: 50,
      proxyRef: 'proxy-hk-1',
      credentials: {
        api_key: 'sk-xxx',
        base_url: 'https://api.openai.com/v1'
      },
      notes: 'API Key 账号'
    },
    {
      ref: 'openai-oauth-001',
      name: 'OpenAI OAuth 账号 1',
      providerCode: 'openai',
      type: 'oauth',
      status: 'active',
      groupName: '默认 OpenAI 分组',
      concurrencyLimit: 3,
      priority: 50,
      credentials: {
        refresh_token: 'refresh-token-xxx',
        access_token: 'access-token-xxx',
        id_token: 'id-token-xxx',
        base_url: 'https://api.openai.com/v1',
        account_id: 'acct_xxx',
        email: 'user@example.com'
      },
      notes: 'OAuth 账号'
    }
  ]
}, null, 2)

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

async function runPreview() {
  const data = parseImportJson()
  if (!data) return
  previewLoading.value = true
  try {
    previewResult.value = await api.accounts.importPreview({ data, options: { ...options } }, props.scopeParams)
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
    previewResult.value = await api.accounts.importConfirm({ data, options: { ...options } }, props.scopeParams)
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

.protocol-list {
  display: grid;
  gap: 10px;
  margin: 0;
}

.protocol-list div {
  display: grid;
  grid-template-columns: 88px minmax(0, 1fr);
  gap: 10px;
}

.protocol-list dt {
  color: #334155;
  font-family: Consolas, 'Courier New', monospace;
  font-weight: 600;
}

.protocol-list dd {
  margin: 0;
  color: #64748b;
  line-height: 1.55;
}

.ai-prompt {
  margin: 14px 0 0;
  padding: 10px 12px;
  border: 1px dashed #cbd5e1;
  border-radius: 10px;
  color: #475569;
  background: #f8fafc;
  font-size: 12px;
  line-height: 1.6;
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

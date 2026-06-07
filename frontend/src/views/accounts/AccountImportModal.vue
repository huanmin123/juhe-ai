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
import { DownloadOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'
import { reactive, ref, watch } from 'vue'

import { api } from '@/api/client'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import type { AccountImportItem, AccountImportOptions, AccountImportProxyItem, AccountImportResult } from '@/types/domain'

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

const aiConversionPrompt = [
  '请根据我附上的《juhe-ai AI 账户导入协议 v1》Markdown，把下面账号数据转换为 juhe-ai-account-import v1 JSON。',
  '',
  '要求：',
  '1. 协议文件优先于你的默认认知；字段名、必填项、枚举值和示例都按协议文件执行。',
  '2. 只输出合法 JSON，不要输出解释、Markdown 代码块或注释。',
  '3. 顶层 type 固定为 juhe-ai-account-import，version 固定为数字 1。',
  '4. 每个账户必须显式填写 name、providerCode、type、status、credentials，以及 groupName 或 groupId。',
  '5. API Key 账号使用 type: api_key；OAuth 账号使用 type: oauth。',
  '6. 不要编造来源数据里不存在的 token、API Key、邮箱、账号 ID、代理密码或模型列表。',
  '7. 如果有代理，请放到 proxies 数组，并用账号的 proxyRef 引用代理 ref。',
  '8. 不确定的信息放到 notes；无法判断是否可立即调度时 status 填 disabled。'
].join('\n')

const accountImportProtocolMarkdown = [
  '# juhe-ai AI 账户导入协议 v1',
  '',
  '## 用途',
  '',
  '把其他系统、表格、文本或人工整理的 OpenAI 账户数据转换为 juhe-ai 可导入的 JSON。导入接口只接受 JSON，不接受 Markdown、注释、JSONL、CSV 或外部系统原始格式。',
  '',
  '## 转换约束',
  '',
  '- 输出必须是一个 JSON 对象，不能包在 Markdown 代码块里，不能附带解释文字。',
  '- 字段名严格使用本协议定义，不要把 `providerCode` 改成 `provider_code`，也不要把 `api_key` 改成 `apiKey`。',
  '- 顶层 `type` 固定为 `juhe-ai-account-import`，`version` 固定为数字 `1`。',
  '- `accounts` 至少 1 条；每个账户必须显式填写 `name`、`providerCode`、`type`、`status`、`credentials`，以及 `groupId` 或 `groupName`。',
  '- 当前 `providerCode` 填 `openai`；当前账户类型只填 `api_key` 或 `oauth`。',
  '- 不确定是否可立即调度时，`status` 填 `disabled`，不要默认填 `active`。',
  '- 不要编造缺失的 token、API Key、邮箱、账号 ID、代理密码或模型列表；不确定的信息写入 `notes`。',
  '- 外部来源字段如果没有本协议对应字段，不要塞进 `credentials`，可以整理到 `notes`。',
  '',
  '## 顶层结构',
  '',
  '| 字段 | 必填 | 类型 | 说明 |',
  '| --- | --- | --- | --- |',
  '| `type` | 是 | string | 固定为 `juhe-ai-account-import`。 |',
  '| `version` | 是 | number | 当前固定为 `1`。 |',
  '| `accounts` | 是 | array | 账户数组，至少 1 条，单次最多 50 条。 |',
  '| `proxies` | 否 | array | 代理数组，单次最多 20 条；账户通过 `proxyRef` 引用 `proxies[].ref`。 |',
  '',
  '结构骨架：',
  '',
  '```json',
  '{',
  '  "type": "juhe-ai-account-import",',
  '  "version": 1,',
  '  "accounts": []',
  '}',
  '```',
  '',
  '## accounts 字段',
  '',
  '| 字段 | 必填 | 类型 | 说明 |',
  '| --- | --- | --- | --- |',
  '| `ref` | 否 | string | 导入预览和错误定位用，不写入数据库。 |',
  '| `name` | 是 | string | 账户名称，同一系统账户下不能重复。 |',
  '| `providerCode` | 是 | string | 当前填写 `openai`。 |',
  '| `type` | 是 | string | `api_key` 或 `oauth`。 |',
  '| `status` | 是 | string | `active` 或 `disabled`。不确定时用 `disabled`。 |',
  '| `groupId` | 二选一 | string | 绑定已有分组 ID；同时存在 `groupName` 时优先使用 `groupId`。 |',
  '| `groupName` | 二选一 | string | 绑定或自动创建同名分组。 |',
  '| `proxyRef` | 否 | string | 引用 `proxies[].ref` 或已有代理 ID。 |',
  '| `proxyProfileId` | 否 | string | 直接引用已有代理 ID；不能和 `proxyRef` 同时填写。 |',
  '| `concurrencyLimit` | 否 | number | 正整数，并发上限。 |',
  '| `priority` | 否 | number | 非负整数，调度优先级。 |',
  '| `superPriorityEnabled` | 否 | boolean | 超级优先开关。 |',
  '| `fallbackEnabled` | 否 | boolean | 降级备用开关。 |',
  '| `supportedModels` | 否 | string[] | 支持模型列表；不确定时省略。 |',
  '| `modelMappings` | 否 | object[] | 模型映射列表，条目包含 `sourceModel`、`upstreamModel`、`enabled`。 |',
  '| `accountExpiresAt` | 否 | string | ISO 时间字符串。 |',
  '| `availabilitySchedule` | 否 | object | 可用时段计划；不确定时省略。 |',
  '| `credentials` | 是 | object | 按账户类型填写凭据。 |',
  '| `notes` | 否 | string | 备注；无法确定的信息写这里。 |',
  '',
  '字段规则：',
  '',
  '- `groupId` 和 `groupName` 同时存在时优先使用 `groupId`。',
  '- `proxyRef` 和 `proxyProfileId` 不能同时填写。',
  '- `concurrencyLimit` 必须是正整数；`priority` 必须是非负整数。',
  '- `supportedModels` 只填明确支持的模型名称；不确定时省略。',
  '- `modelMappings` 的 sourceModel 是下游请求模型，upstreamModel 是该账户实际转发模型。',
  '- `accountExpiresAt` 使用 ISO 时间字符串，例如 `2027-12-31T00:00:00.000Z`。',
  '',
  '## credentials 字段',
  '',
  'API Key 账户：',
  '',
  '```json',
  '{',
  '  "api_key": "sk-xxx",',
  '  "base_url": "https://api.openai.com/v1"',
  '}',
  '```',
  '',
  'OAuth 账户：',
  '',
  '```json',
  '{',
  '  "refresh_token": "refresh-token-xxx",',
  '  "access_token": "access-token-xxx",',
  '  "id_token": "id-token-xxx",',
  '  "base_url": "https://api.openai.com/v1",',
  '  "account_id": "acct_xxx",',
  '  "email": "user@example.com"',
  '}',
  '```',
  '',
  '凭据规则：',
  '',
  '- `api_key` 账户必须有 `credentials.api_key`。',
  '- `oauth` 账户必须有 `credentials.refresh_token` 或 `credentials.access_token`。',
  '- `credentials.base_url` 必须显式填写，通常为 `https://api.openai.com/v1`。',
  '- 字段名保持 snake_case，不要改成 camelCase。',
  '- 不要编造缺失 token，不确定的信息写入 `notes`。',
  '',
  '## proxies 字段',
  '',
  '| 字段 | 必填 | 类型 | 说明 |',
  '| --- | --- | --- | --- |',
  '| `ref` | 是 | string | 代理引用标识，供账户 `proxyRef` 使用。 |',
  '| `name` | 是 | string | 代理名称；已有同名启用代理时复用。 |',
  '| `type` | 是 | string | `http`、`https`、`socks5`、`socks5h`。 |',
  '| `host` | 是 | string | 代理主机。 |',
  '| `port` | 是 | number | 1 到 65535。 |',
  '| `username` | 否 | string | 代理用户名。 |',
  '| `password` | 否 | string | 代理密码。 |',
  '| `enabled` | 否 | boolean | 是否启用，默认 `true`。 |',
  '',
  '## 完整示例',
  '',
  '```json',
  importTemplate,
  '```',
  '',
  '## 常见失败原因',
  '',
  '- 顶层不是 JSON 对象，或者带了 Markdown 代码块。',
  '- `type` / `version` 不匹配当前协议。',
  '- `accounts` 为空、超过 50 条，或账户名称在同一系统账户下重复。',
  '- 账户缺少 `groupId` / `groupName`，或分组供应商和账户供应商不一致。',
  '- API Key 账户缺少 `credentials.api_key`。',
  '- OAuth 账户同时缺少 `credentials.refresh_token` 和 `credentials.access_token`。',
  '- `proxyRef` 指向的代理不存在、未在 `proxies` 中声明，或普通用户尝试创建新代理。',
  '- 未知字段写入了错误层级，例如把外部系统字段直接放进 `credentials`。'
].join('\n')

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

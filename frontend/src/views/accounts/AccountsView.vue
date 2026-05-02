<template>
  <a-card class="page-card accounts-page-card">
    <div class="page-toolbar account-toolbar">
      <div class="account-filters">
        <a-input-search v-model:value="filters.keyword" allow-clear placeholder="搜索账户名、备注、Base URL" class="toolbar-search" @search="applyFilters" />
        <a-select v-model:value="filters.type" class="toolbar-select" :options="typeOptions" />
        <a-select v-model:value="filters.status" class="toolbar-select" :options="statusOptions" />
        <a-select v-model:value="filters.schedulable" class="toolbar-select" :options="schedulableOptions" />
        <a-button @click="resetFilters">重置筛选</a-button>
      </div>
      <div class="page-toolbar-actions">
        <a-button type="primary" @click="openCreate">添加账户</a-button>
      </div>
    </div>

    <div v-if="selectedAccounts.length" class="batch-toolbar">
      <div class="batch-toolbar-info">
        <span>已选择 {{ selectedAccounts.length }} 个账户</span>
        <span class="batch-toolbar-hint">批量操作会按当前选择逐个执行</span>
      </div>
      <div class="batch-toolbar-actions">
        <a-button @click="clearSelection">清空选择</a-button>
        <a-button type="primary" @click="batchTestSelected">批量测试</a-button>
        <a-button @click="batchSetStatus('active')">批量恢复正常</a-button>
        <a-button danger @click="batchSetStatus('disabled')">批量停用</a-button>
        <a-button @click="batchSetSchedulable(true)">恢复调度</a-button>
        <a-button @click="batchSetSchedulable(false)">暂停调度</a-button>
      </div>
    </div>

    <a-table class="account-table" size="middle" :columns="columns" :data-source="filteredAccounts" row-key="id" :loading="loading" :scroll="{ x: 1840 }" :row-selection="rowSelection">
      <template #emptyText>
        <a-empty class="page-empty-card" description="还没有账户。点击「添加账户」，再选择供应商和账户类型。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'type'">
          <a-tag color="processing">{{ accountTypeText(record.type) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'providerCode'">
          <a-tag color="geekblue">{{ providerName(record.providerCode) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'group'">
          <a-tooltip v-if="groupNameForAccount(record.id)" :title="groupNameForAccount(record.id)">
            <a-tag class="account-group-tag" color="purple">{{ groupNameForAccount(record.id) }}</a-tag>
          </a-tooltip>
          <span v-else class="muted-cell">未归属</span>
        </template>
        <template v-else-if="column.key === 'status'">
          <div class="status-cell">
            <a-tooltip v-if="accountStatusTooltipLines(record).length" placement="topLeft">
              <template #title>
                <div class="status-tooltip">
                  <div v-for="line in accountStatusTooltipLines(record)" :key="line">{{ line }}</div>
                </div>
              </template>
              <a-tag class="status-tag" :color="accountStatusColor(record)">{{ accountStatusText(record) }}</a-tag>
            </a-tooltip>
            <a-tag v-else class="status-tag" :color="accountStatusColor(record)">{{ accountStatusText(record) }}</a-tag>
          </div>
        </template>
        <template v-else-if="column.key === 'concurrency'">
          <a-tag color="blue">{{ record.currentConcurrency }}/{{ record.concurrencyLimit }}</a-tag>
        </template>
        <template v-else-if="column.key === 'usage'">
          <div class="usage-cell">
            <span class="usage-summary">{{ `${record.usage.requestCount}req/${formatUsageAmount(record.usage.totalTokens)}/${formatCost(record.usage.totalCost)}` }}</span>
          </div>
        </template>
        <template v-else-if="column.key === 'lastUsedAt'">
          {{ formatDateTime(record.lastUsedAt || record.usage.lastUsedAt) }}
        </template>
        <template v-else-if="column.key === 'actions'">
          <a-space class="row-actions" :size="8">
            <a-button type="link" size="small" @click="openEdit(record)">编辑</a-button>
            <a-popconfirm title="确认删除这个账户？" @confirm="removeAccount(record.id)">
              <a-button type="link" size="small" danger>删除</a-button>
            </a-popconfirm>
            <a-dropdown>
              <a-button type="link" size="small">更多</a-button>
              <template #overlay>
                <a-menu @click="handleAccountMenuClick($event, record)">
                  <a-menu-item v-for="item in accountMenuItems(record)" :key="item.key" :danger="item.danger">{{ item.label }}</a-menu-item>
                </a-menu>
              </template>
            </a-dropdown>
          </a-space>
        </template>
      </template>
    </a-table>

    <a-modal v-model:open="testModalOpen" title="测试账号连接" width="620px" :footer="null" :closable="!testRunning" :keyboard="!testRunning" :mask-closable="!testRunning" @cancel="closeTestModal">
      <div v-if="testingAccount" class="test-modal">
        <div class="test-account-card">
          <div class="test-account-main">
            <div class="test-account-icon">▶</div>
            <div>
              <div class="test-account-name">{{ testingAccount.name }}</div>
              <div class="test-account-meta">
                <a-tag color="processing">{{ accountTypeText(testingAccount.type) }}</a-tag>
                <span>账号</span>
              </div>
            </div>
          </div>
          <a-tag :color="accountStatusColor(testingAccount)">{{ testingAccount.status }}</a-tag>
        </div>

        <a-form layout="vertical" class="test-form">
          <a-form-item label="选择测试模型">
            <a-select
              v-model:value="testForm.model"
              show-search
              :loading="testModelsLoading"
              :disabled="testRunning"
              :options="testModelOptions"
              placeholder="选择测试模型"
            />
          </a-form-item>
        </a-form>

        <div class="test-terminal">
          <div v-if="!testOutputLines.length" class="test-output-line muted">准备开始测试</div>
          <div v-for="(line, index) in testOutputLines" :key="index" class="test-output-line" :class="line.tone">{{ line.text }}</div>
        </div>

        <div v-if="testResult" class="test-result-meta">
          <a-descriptions size="small" bordered :column="2">
            <a-descriptions-item label="状态码">{{ testResult.statusCode ?? '-' }}</a-descriptions-item>
            <a-descriptions-item label="耗时">{{ formatDuration(testResult.durationMs) }}</a-descriptions-item>
            <a-descriptions-item label="模型">{{ testResult.model || testForm.model }}</a-descriptions-item>
            <a-descriptions-item label="Token 刷新">{{ testResult.tokenRefreshed ? '已刷新' : '未刷新' }}</a-descriptions-item>
            <a-descriptions-item label="请求 URL" :span="2">{{ testResult.requestUrl || '-' }}</a-descriptions-item>
            <a-descriptions-item label="代理" :span="2">{{ testResult.proxyUrl || '未使用' }}</a-descriptions-item>
          </a-descriptions>
          <a-collapse class="test-result-collapse" ghost>
            <a-collapse-panel key="result" header="完整测试结果 JSON">
              <a-textarea :value="testResultJson" :rows="8" readonly />
            </a-collapse-panel>
          </a-collapse>
        </div>

        <div class="test-modal-footer">
          <div class="test-footer-hint">
            <span>⌘ 测试模型</span>
            <span>提示词："{{ testForm.prompt }}"</span>
          </div>
          <a-space>
            <a-button :disabled="!testResult" @click="copyTestResult">复制完整结果</a-button>
            <a-button @click="closeTestModal">关闭</a-button>
            <a-button type="primary" :loading="testRunning" @click="runAccountTest">{{ testResult ? '重试' : '开始测试' }}</a-button>
          </a-space>
        </div>
      </div>
    </a-modal>

    <a-modal v-model:open="modalOpen" :title="modalTitle" width="920px" :confirm-loading="modalConfirmLoading" :ok-button-props="modalOkButtonProps" @ok="saveAccount" @cancel="handleModalCancel">
      <a-form layout="vertical" class="account-form">
        <div v-if="!editingId" class="setup-progress">
          <div class="setup-step" :class="{ active: !form.providerCode, done: Boolean(form.providerCode) }">
            <span>1</span>
            <strong>选择供应商</strong>
          </div>
          <div class="setup-step" :class="{ active: Boolean(form.providerCode) && !form.type, done: Boolean(form.type) }">
            <span>2</span>
            <strong>选择类型</strong>
          </div>
          <div class="setup-step" :class="{ active: Boolean(form.providerCode && form.type) }">
            <span>3</span>
            <strong>填写配置</strong>
          </div>
        </div>

        <a-alert v-if="editingId" class="form-alert" type="info" show-icon message="编辑账户时不修改供应商和账户类型；Access/API Key 与 Refresh Token 只在这里展示和修改。" />

        <section class="form-section selector-section">
          <div class="form-section-head">
            <div>
              <h4>选择供应商</h4>
              <p>未来接入 Claude Code、Gemini 等供应商时，也会从这里进入。</p>
            </div>
          </div>
          <div class="choice-grid provider-choice-grid">
            <button
              v-for="provider in availableProviders"
              :key="provider.code"
              type="button"
              class="choice-card provider-choice-card"
              :class="{ active: form.providerCode === provider.code, disabled: editingId || !provider.enabled }"
              :disabled="Boolean(editingId) || !provider.enabled"
              @click="selectProvider(provider.code)"
            >
              <span class="choice-card-icon">{{ provider.name.slice(0, 1).toUpperCase() }}</span>
              <span class="choice-card-content">
                <strong>{{ provider.name }}</strong>
                <small>{{ provider.baseUrl }}</small>
              </span>
              <a-tag :color="provider.enabled ? 'green' : 'default'">{{ provider.enabled ? '可用' : '停用' }}</a-tag>
            </button>
          </div>
        </section>

        <section v-if="selectedProvider" class="form-section selector-section">
          <div class="form-section-head">
            <div>
              <h4>选择账户类型</h4>
              <p>{{ selectedProvider.name }} 当前支持 {{ accountTypeChoices.length }} 种账户创建方式。</p>
            </div>
          </div>
          <div class="choice-grid type-choice-grid">
            <button
              v-for="item in accountTypeChoices"
              :key="item.value"
              type="button"
              class="choice-card type-choice-card"
              :class="{ active: form.type === item.value, disabled: Boolean(editingId) }"
              :disabled="Boolean(editingId)"
              @click="selectAccountType(item.value)"
            >
              <span class="choice-card-content">
                <strong>{{ item.label }}</strong>
                <small>{{ item.description }}</small>
              </span>
              <a-tag color="blue">{{ item.tag }}</a-tag>
            </button>
          </div>
        </section>

        <section v-if="hasAccountType" class="form-section">
          <div class="form-section-head">
            <div>
              <h4>基础信息</h4>
              <p>账户主动选择归属分组；API Key 再绑定分组来统一调度该组账户。</p>
            </div>
          </div>
          <div class="form-grid">
            <a-form-item label="账户名称" :required="form.type === 'api_key' || Boolean(editingId)">
              <a-input v-model:value="form.name" :placeholder="form.type === 'oauth' ? 'OAuth 可留空，默认使用授权信息' : '例如 openai-main'" />
            </a-form-item>
            <a-form-item label="归属分组">
              <a-select v-model:value="form.groupId" allow-clear :options="groupOptions" placeholder="可选，只显示同供应商分组" />
            </a-form-item>
          </div>
          <a-form-item label="备注">
            <a-textarea v-model:value="form.notes" :rows="2" placeholder="可填写来源、用途或额度说明" />
          </a-form-item>
        </section>

        <section v-if="isApiKeyForm" class="form-section credential-section">
          <div class="form-section-head">
            <div>
              <h4>{{ accountTypeTitle(form.providerCode, form.type) }} 配置</h4>
              <p>API Key 会完整保存在本地；列表不展示，编辑弹窗可直接查看和修改。</p>
            </div>
          </div>
          <a-form-item label="API Key" required>
            <a-input v-model:value="form.apiKey" placeholder="粘贴完整 API Key" />
          </a-form-item>
          <div class="form-grid">
            <a-form-item label="Base URL">
              <a-input v-model:value="form.baseUrl" :placeholder="selectedProvider?.baseUrl || 'https://api.openai.com/v1'" />
            </a-form-item>
            <a-form-item label="Organization ID">
              <a-input v-model:value="form.organizationId" placeholder="可选" />
            </a-form-item>
          </div>
        </section>

        <section v-else-if="isOAuthForm" class="form-section credential-section">
          <div class="form-section-head">
            <div>
              <h4>{{ accountTypeTitle(form.providerCode, form.type) }} 配置</h4>
              <p>Refresh Token 不在列表展示；创建时可手动授权，也可直接粘贴 Refresh Token。</p>
            </div>
          </div>

          <template v-if="editingId">
            <a-form-item label="Access Token">
              <a-textarea v-model:value="form.accessToken" :rows="3" placeholder="可直接查看和修改 Access Token" />
            </a-form-item>
            <a-form-item label="Refresh Token">
              <a-textarea v-model:value="form.refreshToken" :rows="3" placeholder="可直接查看和修改 Refresh Token" />
            </a-form-item>
            <div class="form-grid">
              <a-form-item label="Client ID">
                <a-input v-model:value="form.clientId" placeholder="默认使用 Codex CLI Client ID" />
              </a-form-item>
              <a-form-item label="过期时间">
                <a-date-picker v-model:value="form.expiresAt" show-time style="width: 100%" />
              </a-form-item>
            </div>
            <div class="form-grid">
              <a-form-item label="Account ID">
                <a-input v-model:value="form.accountId" />
              </a-form-item>
              <a-form-item label="Organization ID">
                <a-input v-model:value="form.organizationId" />
              </a-form-item>
            </div>
          </template>

          <template v-else-if="isOpenAIOAuthForm">
            <a-form-item label="授权方式">
              <a-segmented v-model:value="form.oauthMode" :options="[{ label: '手动授权', value: 'manual' }, { label: 'Refresh Token', value: 'refresh_token' }]" block />
            </a-form-item>
            <div class="form-grid">
              <a-form-item label="Client ID">
                <a-input v-model:value="form.clientId" placeholder="默认使用 Codex CLI Client ID" />
              </a-form-item>
              <a-form-item label="Redirect URI" v-if="form.oauthMode === 'manual'">
                <a-input v-model:value="form.redirectUri" />
              </a-form-item>
            </div>

            <template v-if="form.oauthMode === 'manual'">
              <a-alert class="form-alert" type="info" show-icon message="先生成授权链接；浏览器跳转 localhost 失败后，复制地址栏完整回调 URL 粘贴回来即可。" />
              <a-space class="oauth-actions" wrap>
                <a-button :loading="authLoading" @click="generateOAuthUrl">生成授权链接</a-button>
                <a-button :disabled="!authResult?.authUrl" @click="openAuthUrl">打开授权链接</a-button>
                <a-button :disabled="!authResult?.authUrl" @click="copyText(authResult?.authUrl || '')">复制授权链接</a-button>
              </a-space>
              <a-form-item v-if="authResult" label="授权链接">
                <a-textarea :value="authResult.authUrl" :rows="3" readonly />
              </a-form-item>
              <a-form-item label="回调 URL" required>
                <a-textarea v-model:value="form.callbackUrl" :rows="3" placeholder="粘贴浏览器地址栏里的 http://localhost:1455/auth/callback?code=...&state=..." />
              </a-form-item>
            </template>

            <template v-else>
              <a-form-item label="Refresh Token" required>
                <a-textarea v-model:value="form.refreshToken" :rows="4" placeholder="粘贴 OpenAI refresh_token" />
              </a-form-item>
            </template>
          </template>

          <a-alert v-else class="form-alert" type="warning" show-icon message="该供应商的 OAuth 创建流程尚未开放，第一期先支持 OpenAI OAuth。" />
        </section>

        <section v-if="hasAccountType" class="form-section">
          <div class="form-section-head">
            <div>
              <h4>调度与策略</h4>
              <p>并发、优先级、代理和透传会影响后续请求转发与账户选择。</p>
            </div>
          </div>
          <div class="form-grid">
            <a-form-item label="状态">
              <a-select v-model:value="form.status" :options="statusEditOptions" />
            </a-form-item>
            <a-form-item label="并发上限">
              <a-input-number v-model:value="form.concurrencyLimit" :min="1" style="width: 100%" />
            </a-form-item>
            <a-form-item label="优先级" extra="数字越小越优先；当前账号失败后会切换到下一个可用账号。">
              <a-input-number v-model:value="form.priority" :min="0" style="width: 100%" />
            </a-form-item>
            <a-form-item label="代理">
              <a-select v-model:value="form.proxyProfileId" allow-clear placeholder="不使用代理" :options="proxyOptions" />
            </a-form-item>
          </div>
          <div class="form-toggle-grid">
            <a-form-item label="调度">
              <a-switch v-model:checked="form.schedulable" checked-children="可调度" un-checked-children="停用" />
            </a-form-item>
            <a-form-item label="透传">
              <a-switch v-model:checked="form.passthroughEnabled" checked-children="启用" un-checked-children="关闭" />
            </a-form-item>
          </div>
        </section>

        <AccountErrorPolicyCard v-if="hasAccountType" v-model:rules="accountErrorPolicyRules" />
      </a-form>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import type { Dayjs } from 'dayjs'
import { message } from 'ant-design-vue'
import { computed, onMounted, reactive, ref } from 'vue'

import { api } from '@/api/client'
import type { AccountStatus, AccountSummary, AccountTestResult, AccountType, GroupSummary, OpenAIAuthURLResult, ProviderDefinition, ProviderModelPricing, ProxyProfileSummary, SystemSettings } from '@/types/domain'
import AccountErrorPolicyCard from './AccountErrorPolicyCard.vue'
import {
  loadAccountErrorPolicyRules,
  validateAccountErrorPolicyRules,
  writeAccountErrorPolicyToCredentials,
  type AccountErrorPolicyRuleForm
} from './accountErrorPolicy'

type SchedulableFilter = 'all' | 'schedulable' | 'paused' | 'cooling'

interface AccountFilters {
  keyword: string
  type: 'all' | AccountType
  status: 'all' | AccountStatus
  schedulable: SchedulableFilter
}

interface AccountMenuItem {
  key: string
  label: string
  danger?: boolean
}

interface TestOutputLine {
  text: string
  tone?: 'muted' | 'info' | 'success' | 'warning' | 'error' | 'label' | 'divider'
}

interface AccountForm {
  providerCode: string
  name: string
  type: AccountType
  groupId?: string
  apiKey: string
  baseUrl: string
  accessToken: string
  refreshToken: string
  clientId: string
  expiresAt?: Dayjs
  accountId: string
  organizationId: string
  oauthMode: 'manual' | 'refresh_token'
  redirectUri: string
  callbackUrl: string
  status: AccountStatus
  concurrencyLimit: number
  priority: number
  proxyProfileId?: string
  passthroughEnabled: boolean
  schedulable: boolean
  notes: string
}

const FALLBACK_PROVIDER: ProviderDefinition = {
  id: 'openai',
  code: 'openai',
  name: 'OpenAI',
  enabled: true,
  baseUrl: 'https://api.openai.com/v1',
  accountTypes: ['oauth', 'api_key'],
  capabilities: ['models', 'responses', 'stream', 'passthrough']
}

const loading = ref(false)
const saving = ref(false)
const authLoading = ref(false)
const testModalOpen = ref(false)
const testRunning = ref(false)
const testModelsLoading = ref(false)
const modalOpen = ref(false)
const authResult = ref<OpenAIAuthURLResult>()
const editingId = ref<string>()
const testingAccount = ref<AccountSummary>()
const testResult = ref<AccountTestResult>()
const selectedAccountIds = ref<string[]>([])
const accounts = ref<AccountSummary[]>([])
const providers = ref<ProviderDefinition[]>([])
const providerModels = ref<ProviderModelPricing[]>([])
const proxies = ref<ProxyProfileSummary[]>([])
const groups = ref<GroupSummary[]>([])
const systemSettings = ref<SystemSettings>({ defaultAccountConcurrencyLimit: 3 })
const filters = reactive<AccountFilters>({ keyword: '', type: 'all', status: 'all', schedulable: 'all' })
const testForm = reactive({ model: 'gpt-5.5', prompt: 'hi' })

const form = reactive<AccountForm>(defaultForm())
const accountErrorPolicyRules = ref<AccountErrorPolicyRuleForm[]>(loadAccountErrorPolicyRules())

const typeOptions = [
  { label: '全部类型', value: 'all' },
  { label: 'OAuth', value: 'oauth' },
  { label: 'API Key', value: 'api_key' }
]

const schedulableOptions = [
  { label: '全部调度', value: 'all' },
  { label: '可调度', value: 'schedulable' },
  { label: '已暂停', value: 'paused' },
  { label: '临时不可调用', value: 'cooling' }
] as const

const statusOptions = [
  { label: '全部状态', value: 'all' },
  { label: '正常', value: 'active' },
  { label: '停用', value: 'disabled' },
  { label: '错误', value: 'error' },
  { label: '限流中', value: 'rate_limited' },
  { label: '临时不可调用', value: 'temporary_unavailable' }
]

const statusEditOptions = statusOptions.filter((item) => item.value !== 'all')

const columns = [
  { title: '名称', dataIndex: 'name', key: 'name', width: 230 },
  { title: '账户类型', dataIndex: 'type', key: 'type', width: 120 },
  { title: '供应商', dataIndex: 'providerCode', key: 'providerCode', width: 110 },
  { title: '归属分组', key: 'group', width: 240, className: 'account-group-column' },
  { title: '并发数', key: 'concurrency', width: 100, align: 'center' },
  { title: '状态', key: 'status', width: 190 },
  { title: '用量情况', key: 'usage', width: 280 },
  { title: '优先级', dataIndex: 'priority', key: 'priority', width: 90 },
  { title: '最近使用时间', key: 'lastUsedAt', width: 180 },
  { title: '操作', key: 'actions', width: 220, fixed: 'right' }
]

const filteredAccounts = computed(() => accounts.value.filter((account) => {
  const keyword = normalizeKeyword(filters.keyword)
  const keywordMatched = !keyword || [
    account.name,
    account.notes ?? '',
    account.providerCode,
    groupNameForAccount(account.id) ?? '',
    account.type,
    accountBaseUrl(account),
    account.id
  ].some((value) => normalizeKeyword(value).includes(keyword))
  const typeMatched = filters.type === 'all' || account.type === filters.type
  const statusMatched = filters.status === 'all' || account.status === filters.status
  const schedulableMatched = matchesSchedulableFilter(account, filters.schedulable)
  return keywordMatched && typeMatched && statusMatched && schedulableMatched
}))

const defaultTestModelOptions = [
  'gpt-5.5',
  'gpt-5.4',
  'gpt-5.4-mini',
  'gpt-5.3-codex',
  'gpt-5.2',
  'gpt-4.1',
  'gpt-4.1-mini'
]

const testModelOptions = computed(() => {
  const models = providerModels.value.length ? providerModels.value.map((item) => item.model) : defaultTestModelOptions
  return [...new Set(models)].map((model) => ({ label: model, value: model }))
})

const testResultJson = computed(() => testResult.value ? JSON.stringify(testResult.value, null, 2) : '')

const testOutputLines = computed<TestOutputLine[]>(() => {
  const account = testingAccount.value
  if (!account || (!testRunning.value && !testResult.value)) return []
  const lines: TestOutputLine[] = [
    { text: `开始测试账号：${account.name}`, tone: 'info' },
    { text: `账号类型：${accountTypeText(account.type)}`, tone: 'muted' }
  ]

  if (testRunning.value) {
    lines.push({ text: '正在连接 OpenAI API...', tone: 'warning' })
    lines.push({ text: `使用模型：${testForm.model}`, tone: 'success' })
    lines.push({ text: `发送测试消息："${testForm.prompt}"`, tone: 'muted' })
    return lines
  }

  if (!testResult.value) {
    lines.push({ text: '点击「开始测试」后会显示完整返回结果。', tone: 'muted' })
    return lines
  }

  lines.push({ text: testResult.value.statusCode && testResult.value.statusCode >= 200 && testResult.value.statusCode < 300 ? '已连接到 API' : 'API 返回错误', tone: testResult.value.success ? 'success' : 'error' })
  lines.push({ text: `使用模型：${testResult.value.model || testForm.model}`, tone: 'success' })
  lines.push({ text: `发送测试消息："${testForm.prompt}"`, tone: 'muted' })
  lines.push({ text: '响应：', tone: 'label' })
  const outputText = formatTestTerminalResult(testResult.value)
  if (outputText) {
    lines.push({ text: outputText, tone: testResult.value.success ? 'success' : 'error' })
  } else {
    lines.push({ text: testResult.value.message, tone: testResult.value.success ? 'success' : 'error' })
  }
  if (testResult.value.errorPolicyAction && testResult.value.errorPolicyAction !== 'none') {
    const reason = testResult.value.errorPolicyReason ? `，原因：${testResult.value.errorPolicyReason}` : ''
    lines.push({ text: `错误处理策略：${formatErrorPolicyAction(testResult.value.errorPolicyAction)}${reason}`, tone: 'warning' })
  }
  if (testResult.value.accountStatusChanged || testResult.value.accountStatus) {
    const status = testResult.value.accountStatus ? statusText(testResult.value.accountStatus) : '未变化'
    lines.push({ text: `账号状态：${status}`, tone: testResult.value.accountStatusChanged ? 'warning' : 'muted' })
  }
  lines.push({ text: '', tone: 'divider' })
  lines.push({ text: testResult.value.success ? '✓ 测试完成！' : '✕ 测试失败！', tone: testResult.value.success ? 'success' : 'error' })
  return lines
})

const selectedAccounts = computed(() => accounts.value.filter((account) => selectedAccountIds.value.includes(account.id)))

const rowSelection = computed(() => ({
  selectedRowKeys: selectedAccountIds.value,
  onChange: (selectedRowKeys: Array<string | number>) => {
    selectedAccountIds.value = selectedRowKeys.map((key) => String(key))
  }
}))

const proxyOptions = computed(() => proxies.value.map((proxy) => ({ label: `${proxy.name} (${proxy.type})`, value: proxy.id })))
const groupOptions = computed(() => groups.value
  .filter((group) => !form.providerCode || group.providerCode === form.providerCode)
  .map((group) => ({ label: group.name, value: group.id })))
const availableProviders = computed(() => providers.value.length ? providers.value : [FALLBACK_PROVIDER])
const selectedProvider = computed(() => availableProviders.value.find((provider) => provider.code === form.providerCode))
const accountTypeChoices = computed(() => (selectedProvider.value?.accountTypes ?? []).map((type) => ({
  value: type,
  label: accountTypeTitle(selectedProvider.value?.code ?? form.providerCode, type),
  description: accountTypeDescription(selectedProvider.value?.code ?? form.providerCode, type),
  tag: accountTypeText(type)
})))
const hasAccountType = computed(() => Boolean(form.providerCode && form.type))
const isApiKeyForm = computed(() => hasAccountType.value && form.type === 'api_key')
const isOAuthForm = computed(() => hasAccountType.value && form.type === 'oauth')
const isOpenAIOAuthForm = computed(() => form.providerCode === 'openai' && form.type === 'oauth')
const modalTitle = computed(() => {
  if (editingId.value) return '编辑账户'
  if (!form.providerCode) return '添加账户'
  if (!form.type) return `添加 ${providerName(form.providerCode)} 账户`
  return `添加 ${accountTypeTitle(form.providerCode, form.type)} 账户`
})
const modalConfirmLoading = computed(() => saving.value)
const modalOkButtonProps = computed(() => ({
  type: 'primary' as const,
  disabled: !hasAccountType.value || (!editingId.value && isOAuthForm.value && !isOpenAIOAuthForm.value)
}))

function defaultForm(providerCode = '', type: AccountType = ''): AccountForm {
  const providerList = providers.value.length ? providers.value : [FALLBACK_PROVIDER]
  const provider = providerList.find((item) => item.code === providerCode) ?? (providerCode ? FALLBACK_PROVIDER : undefined)
  return {
    providerCode,
    name: '',
    type,
    groupId: undefined,
    apiKey: '',
    baseUrl: provider?.baseUrl ?? 'https://api.openai.com/v1',
    accessToken: '',
    refreshToken: '',
    clientId: '',
    accountId: '',
    organizationId: '',
    oauthMode: 'manual',
    redirectUri: 'http://localhost:1455/auth/callback',
    callbackUrl: '',
    status: 'active',
    concurrencyLimit: defaultAccountConcurrencyLimit(),
    priority: 0,
    proxyProfileId: defaultProxyProfileId(),
    passthroughEnabled: true,
    schedulable: true,
    notes: ''
  }
}

function resetForm(providerCode = '', type: AccountType = '') {
  Object.assign(form, defaultForm(providerCode, type))
  accountErrorPolicyRules.value = loadAccountErrorPolicyRules()
  authResult.value = undefined
}

function statusColor(status: AccountStatus) {
  if (status === 'active') return 'green'
  if (status === 'error') return 'red'
  if (status === 'rate_limited') return 'orange'
  if (status === 'temporary_unavailable') return 'gold'
  return 'default'
}

function statusText(status: AccountStatus) {
  if (status === 'active') return '正常'
  if (status === 'error') return '错误'
  if (status === 'rate_limited') return '限流中'
  if (status === 'temporary_unavailable') return '临时不可调用'
  return '停用'
}

function formatErrorPolicyAction(action: NonNullable<AccountTestResult['errorPolicyAction']>): string {
  if (action === 'retry_next') return '切换下一个账号'
  if (action === 'cooldown') return '账号冷却'
  if (action === 'disable') return '标记错误'
  if (action === 'default_cooldown') return '默认临时不可调用'
  return '无'
}

function accountStatusColor(account: AccountSummary) {
  return statusColor(account.status)
}

function accountStatusText(account: AccountSummary) {
  return statusText(account.status)
}

function accountCooldownText(account: AccountSummary) {
  if (!isCoolingDown(account)) return ''
  return `暂停至 ${formatDateTime(account.cooldownUntil)}`
}

function accountStatusTooltipLines(account: AccountSummary): string[] {
  const lines: string[] = []
  const cooldownText = accountCooldownText(account)
  if (cooldownText) {
    lines.push(cooldownText)
  } else if (isTemporaryAccountStatus(account) && account.cooldownUntil) {
    lines.push(`已到期：${formatDateTime(account.cooldownUntil)}`)
    lines.push('等待下一次真实请求验证，成功后恢复正常')
  }
  if (account.lastErrorMessage) {
    lines.push(`原因：${account.lastErrorMessage}`)
  }
  return lines
}

function isTemporaryAccountStatus(account: AccountSummary) {
  return account.status === 'rate_limited' || account.status === 'temporary_unavailable'
}

function isCoolingDown(account: AccountSummary) {
  if (!account.cooldownUntil) return false
  const time = new Date(account.cooldownUntil).getTime()
  return Number.isFinite(time) && time > Date.now()
}

function accountTypeText(type: AccountType) {
  if (type === 'oauth') return 'OAuth'
  if (type === 'api_key') return 'API Key'
  return type || '-'
}

function accountTypeTitle(providerCode: string, type: AccountType) {
  const provider = providerName(providerCode)
  if (type === 'oauth') return `${provider} OAuth`
  if (type === 'api_key') return `${provider} API Key`
  return `${provider} ${type}`.trim()
}

function accountTypeDescription(providerCode: string, type: AccountType) {
  if (providerCode === 'openai' && type === 'oauth') return '适合 Codex / ChatGPT OAuth 授权账户，支持手动授权或 Refresh Token。'
  if (providerCode === 'openai' && type === 'api_key') return '适合直接粘贴 OpenAI API Key，可配置 Base URL 和组织 ID。'
  return '该账户类型会使用供应商定义的创建流程。'
}

function providerName(providerCode?: string) {
  if (!providerCode) return '未知供应商'
  return availableProviders.value.find((provider) => provider.code === providerCode)?.name ?? providerCode
}

function groupIdForAccount(accountId: string) {
  return groups.value.find((group) => group.accountIds.includes(accountId))?.id
}

function groupNameForAccount(accountId: string) {
  return groups.value.find((group) => group.accountIds.includes(accountId))?.name
}

function defaultProxyProfileId() {
  return proxies.value.find((proxy) => proxy.type === 'socks5h' && proxy.host === '127.0.0.1' && proxy.port === 7897)?.id
}

function defaultAccountConcurrencyLimit() {
  const value = systemSettings.value.defaultAccountConcurrencyLimit
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  if (!Number.isFinite(number)) return 3
  return Math.min(Math.max(Math.trunc(number), 1), 999)
}

function asString(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function normalizeKeyword(value: unknown): string {
  return String(value ?? '').trim().toLowerCase()
}

function matchesSchedulableFilter(account: AccountSummary, filter: SchedulableFilter): boolean {
  if (filter === 'all') return true
  if (filter === 'cooling') return isTemporaryAccountStatus(account) || isCoolingDown(account)
  if (filter === 'schedulable') return account.schedulable && !isTemporaryAccountStatus(account) && !isCoolingDown(account)
  return !account.schedulable
}

function accountBaseUrl(account: AccountSummary): string {
  return asString(account.credentials.base_url)
}

function accountMenuItems(account: AccountSummary): AccountMenuItem[] {
  return [
    { key: 'test', label: '测试' },
    { key: 'toggle-status', label: account.status === 'active' ? '停用账户' : '恢复正常', danger: account.status === 'active' },
    ...(!isTemporaryAccountStatus(account)
      ? [{ key: 'toggle-schedulable', label: account.schedulable ? '暂停调度' : '恢复调度' }]
      : []),
    ...(account.type === 'oauth' ? [{ key: 'refresh-oauth', label: '刷新授权' }] : [])
  ]
}

function formatUsageAmount(value?: number): string {
  const amount = value ?? 0
  const absoluteValue = Math.abs(amount)
  if (absoluteValue >= 1_000_000_000) {
    return `${(amount / 1_000_000_000).toFixed(1)}B`
  }
  if (absoluteValue >= 1_000_000) {
    return `${(amount / 1_000_000).toFixed(1)}M`
  }
  if (absoluteValue >= 1_000) {
    return `${(amount / 1_000).toFixed(1)}K`
  }
  return new Intl.NumberFormat('zh-CN').format(amount)
}

function formatCost(value?: number): string {
  return `$${(value ?? 0).toFixed(2)}`
}

function formatDateTime(value?: string): string {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString('zh-CN', { hour12: false })
}

function formatDuration(value?: number): string {
  return typeof value === 'number' ? `${value} ms` : '-'
}

function formatTestTerminalResult(result: AccountTestResult): string {
  if (result.outputText?.trim()) return result.outputText.trim()
  if (result.success) return ''
  const rawText = result.responseText?.trim()
  if (!rawText || rawText === result.message.trim()) return ''
  return rawText
}

async function copyText(value: string) {
  if (!value) return
  await navigator.clipboard.writeText(value)
  message.success('已复制')
}

async function loadData() {
  loading.value = true
  try {
    const [accountList, providerList, proxyList, groupList, settings] = await Promise.all([
      api.accounts.list(),
      api.providers.list(),
      api.proxies.list(),
      api.groups.list(),
      api.settings.get()
    ])
    accounts.value = accountList
    providers.value = providerList.length ? providerList : [FALLBACK_PROVIDER]
    proxies.value = proxyList
    groups.value = groupList
    systemSettings.value = settings
    selectedAccountIds.value = selectedAccountIds.value.filter((id) => accountList.some((account) => account.id === id))
  } catch (error) {
    console.error(error)
    message.error('加载账户失败')
  } finally {
    loading.value = false
  }
}

function applyFilters() {
  filters.keyword = filters.keyword.trim()
}

function resetFilters() {
  Object.assign(filters, {
    keyword: '',
    type: 'all',
    status: 'all',
    schedulable: 'all'
  })
}

function clearSelection() {
  selectedAccountIds.value = []
}

function openCreate() {
  editingId.value = undefined
  resetForm('', '')
  modalOpen.value = true
}

function handleModalCancel() {
  authResult.value = undefined
}

function selectProvider(providerCode: string) {
  if (editingId.value || form.providerCode === providerCode) return
  resetForm(providerCode, '')
}

function selectAccountType(type: AccountType) {
  if (editingId.value || form.type === type) return
  const providerCode = form.providerCode
  Object.assign(form, {
    ...defaultForm(providerCode, type),
    groupId: form.groupId,
    proxyProfileId: form.proxyProfileId,
    notes: form.notes,
    concurrencyLimit: form.concurrencyLimit,
    priority: form.priority,
    passthroughEnabled: form.passthroughEnabled,
    schedulable: form.schedulable
  })
  authResult.value = undefined
}

function openEdit(account: AccountSummary) {
  editingId.value = account.id
  Object.assign(form, defaultForm(account.providerCode, account.type), {
    providerCode: account.providerCode,
    name: account.name,
    type: account.type,
    status: account.status,
    concurrencyLimit: account.concurrencyLimit,
    priority: account.priority,
    proxyProfileId: account.proxyProfileId,
    passthroughEnabled: account.passthroughEnabled,
    schedulable: account.schedulable,
    groupId: groupIdForAccount(account.id),
    apiKey: asString(account.credentials.api_key),
    baseUrl: asString(account.credentials.base_url) || 'https://api.openai.com/v1',
    accessToken: asString(account.credentials.access_token),
    refreshToken: asString(account.credentials.refresh_token),
    clientId: asString(account.credentials.client_id),
    accountId: asString(account.credentials.account_id),
    organizationId: asString(account.credentials.organization_id),
    expiresAt: undefined,
    notes: account.notes ?? ''
  })
  accountErrorPolicyRules.value = loadAccountErrorPolicyRules(account.credentials)
  authResult.value = undefined
  modalOpen.value = true
}

function buildCredentials() {
  const credentials: Record<string, unknown> = form.type === 'api_key'
    ? {
        api_key: form.apiKey,
        base_url: form.baseUrl,
        organization_id: form.organizationId
      }
    : {
        access_token: form.accessToken,
        refresh_token: form.refreshToken,
        client_id: form.clientId,
        expires_at: form.expiresAt?.toISOString(),
        account_id: form.accountId,
        organization_id: form.organizationId
      }
  writeAccountErrorPolicyToCredentials(credentials, accountErrorPolicyRules.value)
  return credentials
}

async function saveAccount() {
  if (!form.providerCode) {
    message.warning('请先选择供应商')
    return
  }
  if (!form.type) {
    message.warning('请先选择账户类型')
    return
  }
  if ((editingId.value || form.type === 'api_key') && !form.name.trim()) {
    message.warning('请填写账户名称')
    return
  }
  if (form.type === 'api_key' && !form.apiKey.trim()) {
    message.warning('请填写 API Key')
    return
  }
  if (editingId.value && form.type === 'oauth' && !form.accessToken.trim() && !form.refreshToken.trim()) {
    message.warning('请至少填写 Access Token 或 Refresh Token')
    return
  }
  if (!editingId.value && form.type === 'oauth' && form.providerCode !== 'openai') {
    message.warning('第一期只支持创建 OpenAI OAuth 账户')
    return
  }
  if (!editingId.value && form.type === 'oauth' && form.oauthMode === 'manual' && !authResult.value?.sessionId) {
    message.warning('请先生成授权链接')
    return
  }
  if (!editingId.value && form.type === 'oauth' && form.oauthMode === 'manual' && !form.callbackUrl.trim()) {
    message.warning('请粘贴回调 URL')
    return
  }
  if (!editingId.value && form.type === 'oauth' && form.oauthMode === 'refresh_token' && !form.refreshToken.trim()) {
    message.warning('请填写 Refresh Token')
    return
  }
  const errorPolicyValidation = validateAccountErrorPolicyRules(accountErrorPolicyRules.value)
  if (!errorPolicyValidation.valid) {
    message.warning(errorPolicyValidation.message || '错误处理策略配置不完整')
    return
  }

  const payload = {
    providerCode: form.providerCode,
    name: form.name.trim() || undefined,
    type: form.type,
    credentials: buildCredentials(),
    status: form.status,
    concurrencyLimit: form.concurrencyLimit,
    priority: form.priority,
    proxyProfileId: form.proxyProfileId,
    passthroughEnabled: form.passthroughEnabled,
    schedulable: form.schedulable,
    groupId: form.groupId ?? null,
    notes: form.notes
  }

  saving.value = true
  try {
    if (editingId.value) {
      await api.accounts.update(editingId.value, payload)
      message.success('账户已更新')
    } else if (form.type === 'oauth') {
      await createOAuthAccountFromUnifiedForm()
      message.success('OAuth 账户已创建')
    } else {
      await api.accounts.create(payload)
      message.success('账户已创建')
    }
    modalOpen.value = false
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('保存账户失败')
  } finally {
    saving.value = false
  }
}

async function generateOAuthUrl() {
  authLoading.value = true
  try {
    authResult.value = await api.openaiOAuth.authUrl({
      redirectUri: form.redirectUri,
      clientId: form.clientId || undefined
    })
    message.success('授权链接已生成')
  } catch (error) {
    console.error(error)
    message.error('生成授权链接失败')
  } finally {
    authLoading.value = false
  }
}

function openAuthUrl() {
  if (!authResult.value?.authUrl) return
  window.open(authResult.value.authUrl, '_blank', 'noopener,noreferrer')
}

async function createOAuthAccountFromUnifiedForm() {
  const commonPayload = {
    clientId: form.clientId || undefined,
    name: form.name.trim() || undefined,
    groupId: form.groupId || undefined,
    concurrencyLimit: form.concurrencyLimit,
    proxyProfileId: form.proxyProfileId,
    credentialsPatch: { error_handling_rules: buildCredentials().error_handling_rules },
    notes: form.notes || undefined
  }

  if (form.oauthMode === 'manual') {
    await api.openaiOAuth.createFromCode({
      ...commonPayload,
      sessionId: authResult.value?.sessionId,
      callbackUrl: form.callbackUrl,
      redirectUri: form.redirectUri
    })
    return
  }

  await api.openaiOAuth.createFromRefreshToken({
    ...commonPayload,
    refreshToken: form.refreshToken
  })
}

async function refreshOAuthAccount(id: string) {
  try {
    await api.openaiOAuth.refreshAccount(id)
    message.success('OAuth 授权已刷新')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('刷新 OAuth 授权失败')
  }
}

async function loadTestModels() {
  if (providerModels.value.length || testModelsLoading.value) return
  testModelsLoading.value = true
  try {
    providerModels.value = await api.providers.models('openai')
  } catch (error) {
    console.error(error)
    message.warning('测试模型列表加载失败，已使用默认模型')
  } finally {
    testModelsLoading.value = false
  }
}

async function openTestModal(account: AccountSummary) {
  testingAccount.value = account
  testResult.value = undefined
  testForm.model = testForm.model || 'gpt-5.5'
  testModalOpen.value = true
  void loadTestModels()
}

async function runAccountTest() {
  if (!testingAccount.value || testRunning.value) return
  testResult.value = undefined
  testRunning.value = true
  try {
    const result = await api.accounts.test(testingAccount.value.id, {
      model: testForm.model,
      prompt: testForm.prompt
    })
    testResult.value = result
    if (result.success) {
      message.success(`${testingAccount.value.name}: ${result.message}${result.tokenRefreshed ? '，并已刷新 token' : ''}`)
    } else {
      message.error(`${testingAccount.value.name}: ${result.message}`)
    }
    await loadData()
  } catch (error) {
    console.error(error)
    const fallbackMessage = error instanceof Error ? error.message : '测试失败'
    testResult.value = {
      accountId: testingAccount.value.id,
      accountName: testingAccount.value.name,
      providerCode: testingAccount.value.providerCode,
      type: testingAccount.value.type,
      success: false,
      message: fallbackMessage,
      model: testForm.model,
      responseText: fallbackMessage
    }
    message.error(`${testingAccount.value.name}: 测试失败`)
  } finally {
    testRunning.value = false
  }
}

function closeTestModal() {
  if (testRunning.value) return
  testModalOpen.value = false
}

async function copyTestResult() {
  await copyText(testResultJson.value)
}

async function testAccount(account: AccountSummary) {
  await openTestModal(account)
}

async function testAccountSilently(account: AccountSummary) {
  try {
    return await api.accounts.test(account.id, { model: testForm.model, prompt: testForm.prompt })
  } catch (error) {
    console.error(error)
    return undefined
  }
}

async function batchUpdateAccounts(
  payloadBuilder: (account: AccountSummary) => Record<string, unknown>,
  loadingLabel: string,
  successLabel: string
) {
  const selected = selectedAccounts.value
  if (!selected.length) {
    message.warning('请先选择账户')
    return
  }
  const hide = message.loading(`${loadingLabel}（${selected.length} 个）...`, 0)
  try {
    const results = await Promise.allSettled(selected.map((account) => api.accounts.update(account.id, payloadBuilder(account))))
    const failedCount = results.filter((result) => result.status === 'rejected').length
    if (failedCount === 0) {
      message.success(successLabel)
      clearSelection()
    } else {
      message.warning(`${successLabel}，成功 ${selected.length - failedCount} 个，失败 ${failedCount} 个`)
    }
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(`${loadingLabel}失败`)
  } finally {
    hide()
  }
}

async function batchTestSelected() {
  const selected = selectedAccounts.value
  if (!selected.length) {
    message.warning('请先选择账户')
    return
  }
  const hide = message.loading(`正在批量测试 ${selected.length} 个账户...`, 0)
  try {
    const results = await Promise.all(selected.map((account) => testAccountSilently(account)))
    const successCount = results.filter((result) => result?.success).length
    const failedCount = results.length - successCount
    if (failedCount === 0) {
      message.success(`批量测试完成，${successCount} 个账户全部通过`)
      clearSelection()
    } else {
      message.warning(`批量测试完成，成功 ${successCount} 个，失败 ${failedCount} 个`)
    }
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('批量测试失败')
  } finally {
    hide()
  }
}

async function batchSetStatus(status: 'active' | 'disabled') {
  await batchUpdateAccounts(
    () => ({ status }),
    status === 'active' ? '正在批量恢复正常' : '正在批量停用账户',
    status === 'active' ? '账户已批量恢复正常' : '账户已批量停用'
  )
}

async function batchSetSchedulable(schedulable: boolean) {
  await batchUpdateAccounts(
    () => ({ schedulable }),
    schedulable ? '正在批量恢复调度' : '正在批量暂停调度',
    schedulable ? '账户已批量恢复调度' : '账户已批量暂停调度'
  )
}

async function updateAccountState(account: AccountSummary, payload: Record<string, unknown>, successText: string) {
  try {
    await api.accounts.update(account.id, payload)
    message.success(successText)
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('账户状态更新失败')
  }
}

async function handleAccountMenu(key: string, account: AccountSummary) {
  if (key === 'test') {
    await testAccount(account)
    return
  }
  if (key === 'toggle-status') {
    const nextStatus = account.status === 'active' ? 'disabled' : 'active'
    await updateAccountState(account, { status: nextStatus }, nextStatus === 'active' ? '账户已恢复正常' : '账户已停用')
    return
  }
  if (key === 'toggle-schedulable') {
    await updateAccountState(account, { schedulable: !account.schedulable }, account.schedulable ? '账户已暂停调度' : '账户已恢复调度')
    return
  }
  if (key === 'refresh-oauth') {
    await refreshOAuthAccount(account.id)
    return
  }
}

function handleAccountMenuClick(event: { key: string | number }, account: AccountSummary) {
  void handleAccountMenu(String(event.key), account)
}

async function removeAccount(id: string) {
  try {
    await api.accounts.delete(id)
    message.success('账户已删除')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('删除账户失败')
  }
}

onMounted(loadData)
</script>

<style scoped>
.accounts-page-card {
  border: 1px solid #e8edf5;
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.04);
}

.account-toolbar {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
  margin-bottom: 16px;
}

.batch-toolbar {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
  padding: 14px 16px;
  margin-bottom: 16px;
  border: 1px solid #dbeafe;
  border-radius: 14px;
  background: linear-gradient(180deg, #eff6ff 0%, #ffffff 100%);
}

.batch-toolbar-info {
  display: flex;
  flex-direction: column;
  gap: 4px;
  color: #1d4ed8;
  font-weight: 600;
}

.batch-toolbar-hint {
  color: #64748b;
  font-size: 12px;
  font-weight: 400;
}

.batch-toolbar-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
}

.account-filters {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  align-items: center;
  flex: 1 1 520px;
}

.toolbar-search {
  width: min(340px, 100%);
}

.toolbar-select {
  min-width: 150px;
}

.account-table {
  overflow: hidden;
  border: 1px solid #e8edf5;
  border-radius: 14px;
}

.credential-cell {
  display: inline-block;
  max-width: 240px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: bottom;
}

.account-table :deep(.ant-table-tbody > tr > td) {
  vertical-align: middle;
}

.account-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.account-group-tag {
  display: inline-block;
  max-width: 210px;
  overflow: hidden;
  text-overflow: ellipsis;
  vertical-align: bottom;
  white-space: nowrap;
  margin-inline-end: 0;
}

.usage-cell {
  display: block;
  line-height: 1.4;
  white-space: nowrap;
}

.usage-summary {
  color: #0f172a;
  font-family: Consolas, 'Courier New', monospace;
  font-weight: 700;
}

.status-cell {
  display: inline-flex;
  align-items: center;
}

.status-tag {
  width: max-content;
  max-width: 100%;
  margin-inline-end: 0;
  white-space: nowrap;
}

.status-tooltip {
  max-width: 320px;
  line-height: 1.7;
  white-space: pre-wrap;
}

.row-actions :deep(.ant-btn-link) {
  padding-inline: 2px;
}

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

.secret-cell {
  width: 100%;
}

.secret-input {
  width: calc(100% - 64px);
  font-family: Consolas, 'Courier New', monospace;
}

.oauth-actions {
  margin-bottom: 16px;
}

.setup-progress {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 10px;
}

.setup-step {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 14px;
  color: #64748b;
  border: 1px solid #e8edf5;
  border-radius: 14px;
  background: #f8fafc;
}

.setup-step span {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  color: #64748b;
  font-weight: 700;
  border-radius: 999px;
  background: #e2e8f0;
}

.setup-step.active {
  color: #1d4ed8;
  border-color: #bfdbfe;
  background: linear-gradient(135deg, #eff6ff 0%, #ffffff 100%);
}

.setup-step.active span,
.setup-step.done span {
  color: #fff;
  background: #2563eb;
}

.setup-step.done {
  color: #0f172a;
  border-color: #bbf7d0;
  background: #f0fdf4;
}

.selector-section {
  background: linear-gradient(180deg, #ffffff 0%, #f8fafc 100%);
}

.choice-grid {
  display: grid;
  gap: 12px;
}

.provider-choice-grid {
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
}

.type-choice-grid {
  grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
}

.choice-card {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  width: 100%;
  min-height: 82px;
  padding: 14px;
  text-align: left;
  cursor: pointer;
  border: 1px solid #dbe3ef;
  border-radius: 16px;
  background: rgba(255, 255, 255, 0.92);
  box-shadow: 0 8px 22px rgba(15, 23, 42, 0.04);
  transition: border-color 0.18s ease, box-shadow 0.18s ease, transform 0.18s ease;
}

.choice-card:hover:not(.disabled) {
  border-color: #93c5fd;
  box-shadow: 0 14px 32px rgba(37, 99, 235, 0.12);
  transform: translateY(-1px);
}

.choice-card.active {
  border-color: #2563eb;
  background: linear-gradient(135deg, #eff6ff 0%, #ffffff 78%);
  box-shadow: 0 16px 34px rgba(37, 99, 235, 0.14);
}

.choice-card.disabled {
  cursor: not-allowed;
  opacity: 0.68;
}

.choice-card-icon {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 42px;
  height: 42px;
  color: #fff;
  font-size: 18px;
  font-weight: 800;
  border-radius: 14px;
  background: linear-gradient(135deg, #2563eb 0%, #7c3aed 100%);
}

.choice-card-content {
  display: flex;
  flex: 1 1 auto;
  min-width: 0;
  flex-direction: column;
  gap: 4px;
}

.choice-card-content strong {
  color: #0f172a;
  font-size: 15px;
}

.choice-card-content small {
  overflow: hidden;
  color: #64748b;
  font-size: 12px;
  line-height: 18px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.credential-section {
  border-color: #dbeafe;
  background: #f8fbff;
}

.account-table :deep(.ant-empty) {
  margin: 12px 0;
}

.notes-cell {
  display: inline-block;
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: bottom;
}

.account-form {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.form-section {
  padding: 16px;
  border: 1px solid #e8edf5;
  border-radius: 16px;
  background: #fff;
}

.form-section-head {
  margin-bottom: 12px;
}

.form-section-head h4 {
  margin: 0;
  color: #0f172a;
  font-size: 16px;
}

.form-section-head p {
  margin: 4px 0 0;
  color: #64748b;
  font-size: 12px;
}

.form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 16px;
}


.form-toggle-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 16px;
}

.form-alert {
  border-radius: 12px;
}

@media (max-width: 992px) {
  .setup-progress {
    grid-template-columns: 1fr;
  }

  .form-grid,
  .form-toggle-grid {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 768px) {
  .choice-card {
    align-items: flex-start;
  }

  .provider-choice-card {
    flex-wrap: wrap;
  }

  .form-grid,
  .form-toggle-grid {
    grid-template-columns: 1fr;
  }

  .account-toolbar {
    flex-direction: column;
  }

  .account-filters {
    width: 100%;
  }

  .toolbar-search,
  .toolbar-select {
    width: 100%;
    min-width: 0;
  }
}
</style>

<template>
  <a-card class="page-card accounts-page-card">
    <div class="page-toolbar account-toolbar">
      <div class="account-filters">
        <a-input-search v-model:value="filters.keyword" allow-clear placeholder="搜索账户名、备注、Base URL" class="toolbar-search" @search="applyFilters" />
        <a-select v-model:value="filters.type" class="toolbar-select" :options="typeOptions" />
        <a-select v-model:value="filters.status" class="toolbar-select" :options="statusOptions" />
        <a-select v-model:value="filters.schedulable" class="toolbar-select" :options="schedulableOptions" />
        <a-select v-if="isAdmin" v-model:value="filters.systemAccountId" show-search option-filter-prop="label" class="toolbar-select" :options="systemAccountOptions" @change="handleSystemAccountFilterChange" />
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
        <a-button @click="batchSetStatus('active')">批量启用</a-button>
        <a-button danger @click="batchSetStatus('disabled')">批量停用</a-button>
      </div>
    </div>

    <a-table class="account-table" size="middle" :columns="columns" :data-source="filteredAccounts" row-key="id" :loading="loading" :scroll="{ x: tableScrollX }" :row-selection="rowSelection">
      <template #emptyText>
        <a-empty class="page-empty-card" description="还没有账户。点击「添加账户」，再选择供应商和账户类型。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'name'">
          <div class="resource-name-cell">
            <span class="resource-name-line">
              <span>{{ record.name }}</span>
              <a-tooltip v-if="isAuthorizedAccount(record)" :title="authorizedAccountTooltip(record)">
                <InfoCircleOutlined class="authorized-account-icon" :class="{ 'owner-disabled': isOwnerDisabledAuthorizedAccount(record) }" />
              </a-tooltip>
            </span>
          </div>
        </template>
        <template v-else-if="column.key === 'type'">
          <a-tag color="processing">{{ accountTypeText(record.type) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'providerCode'">
          <a-tag color="geekblue">{{ providerName(record.providerCode) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'systemAccount'">
          <span :class="record.systemAccountName ? 'name-cell' : 'muted-cell'">{{ record.systemAccountName || record.systemAccountId || '-' }}</span>
        </template>
        <template v-else-if="column.key === 'group'">
          <a-tooltip v-if="groupNameForAccount(record.id)" :title="groupNameForAccount(record.id)">
            <span class="account-group-text">{{ groupNameForAccount(record.id) }}</span>
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
            <div class="usage-summary-tags">
              <a-tag class="usage-summary-tag">{{ `${record.usage.requestCount}req` }}</a-tag>
              <a-tag class="usage-summary-tag">{{ formatUsageAmount(record.usage.totalTokens) }}</a-tag>
              <a-tag class="usage-summary-tag">{{ formatCost(record.usage.totalCost) }}</a-tag>
            </div>
            <div v-if="oauthUsageBars(record).length" class="oauth-usage-bars">
              <div v-for="bar in oauthUsageBars(record)" :key="bar.key" class="oauth-usage-row">
                <span class="oauth-usage-label">{{ bar.label }}</span>
                <a-progress class="oauth-usage-progress" size="small" :percent="bar.percent" :stroke-color="bar.color" :show-info="false" />
                <span class="oauth-usage-percent" :class="bar.tone">{{ bar.displayPercent }}</span>
                <span class="oauth-usage-reset">{{ bar.resetText }}</span>
              </div>
            </div>
          </div>
        </template>
        <template v-else-if="column.key === 'lastUsedAt'">
          {{ formatDateTime(accountLastUsedAt(record)) }}
        </template>
        <template v-else-if="column.key === 'accountExpiresAt'">
          <span :class="isAccountPackageExpired(record) ? 'expired-cell' : 'muted-cell'">{{ formatDateTime(record.accountExpiresAt) }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <a-space class="row-actions" :size="8">
            <template v-if="isAuthorizedAccount(record)">
              <a-button type="link" size="small" @click="openTestModal(record)">测试</a-button>
              <a-tooltip v-if="canToggleGrantedAuthorization(record)" :title="authorizedAccountToggleTooltip(record)">
                <a-button type="link" size="small" :danger="record.accountAuthorizationSchedulable !== false" @click="updateGrantedAuthorizationSchedulable(record)">{{ record.accountAuthorizationSchedulable === false ? '启用账户' : '停用账户' }}</a-button>
              </a-tooltip>
              <a-popconfirm title="确认归还这个授权账户？" ok-text="确认归还" cancel-text="取消" @confirm="returnGrantedAuthorization(record)">
                <a-button type="link" size="small" danger>归还</a-button>
              </a-popconfirm>
            </template>
            <template v-else>
              <a-button v-if="canEditAccount(record)" type="link" size="small" @click="openEdit(record)">编辑</a-button>
              <a-popconfirm v-if="canDeleteAccount(record)" title="确认删除这个账户？" @confirm="removeAccount(record.id)">
                <a-button type="link" size="small" danger>删除</a-button>
              </a-popconfirm>
              <a-dropdown v-if="accountMenuItems(record).length">
                <a-button type="link" size="small">更多</a-button>
                <template #overlay>
                  <a-menu @click="handleAccountMenuClick($event, record)">
                    <a-menu-item v-for="item in accountMenuItems(record)" :key="item.key" :danger="item.danger">{{ item.label }}</a-menu-item>
                  </a-menu>
                </template>
              </a-dropdown>
            </template>
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
          <a-tag :color="accountStatusColor(testingAccount)">{{ accountStatusText(testingAccount) }}</a-tag>
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
            <a-form-item label="归属分组" required>
              <a-select v-model:value="form.groupId" :options="groupOptions" placeholder="请选择同供应商分组" />
              <div class="form-help">添加账户时会根据供应商默认选择默认分组。</div>
            </a-form-item>
            <a-form-item label="账户到期时间">
              <a-date-picker v-model:value="form.accountExpiresAt" show-time allow-clear style="width: 100%" />
              <div class="form-help">可选，表示套餐/账号购买到期时间；到期后后端会自动停用账户。</div>
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
          </div>
        </section>

        <section v-else-if="isOAuthForm" class="form-section">
          <div class="form-section-head">
            <div>
              <h4>{{ accountTypeTitle(form.providerCode, form.type) }} 配置</h4>
              <p v-if="editingId">Access Token 与 Refresh Token 只在编辑弹窗展示和修改，不会出现在列表。</p>
              <p v-else>创建时支持手动授权或直接粘贴 Refresh Token；敏感凭据不会在列表展示。</p>
            </div>
          </div>

          <template v-if="editingId">
            <a-form-item label="Access Token">
              <a-textarea v-model:value="form.accessToken" :rows="3" placeholder="可直接查看和修改 Access Token" />
            </a-form-item>
            <a-form-item label="Refresh Token">
              <a-textarea v-model:value="form.refreshToken" :rows="3" placeholder="可直接查看和修改 Refresh Token" />
            </a-form-item>
          </template>

          <template v-else-if="isOpenAIOAuthForm">
            <a-form-item class="oauth-mode-item" label="授权方式">
              <a-segmented v-model:value="form.oauthMode" :options="[{ label: '手动授权', value: 'manual' }, { label: '粘贴 Refresh Token', value: 'refresh_token' }]" block />
            </a-form-item>

            <template v-if="form.oauthMode === 'manual'">
              <div class="oauth-flow-panel">
                <div class="oauth-step-grid">
                  <div class="oauth-step-card">
                    <span>1</span>
                    <div>
                      <strong>生成链接</strong>
                      <small>获取本次授权地址</small>
                    </div>
                  </div>
                  <div class="oauth-step-card">
                    <span>2</span>
                    <div>
                      <strong>浏览器授权</strong>
                      <small>登录 OpenAI 并允许跳转</small>
                    </div>
                  </div>
                  <div class="oauth-step-card">
                    <span>3</span>
                    <div>
                      <strong>粘贴回调 URL</strong>
                      <small>保留 code 与 state 参数</small>
                    </div>
                  </div>
                </div>
                <a-alert class="form-alert" type="info" show-icon message="浏览器最终跳转到本地回调地址；如果页面显示连接失败，复制地址栏完整 URL 粘贴回来即可。" />
              </div>
              <div class="oauth-actions">
                <a-button type="primary" :loading="authLoading" @click="generateOAuthUrl">生成授权链接</a-button>
                <a-button :disabled="!authResult?.authUrl" @click="openAuthUrl">打开授权链接</a-button>
                <a-button :disabled="!authResult?.authUrl" @click="copyText(authResult?.authUrl || '')">复制授权链接</a-button>
              </div>
              <a-form-item v-if="authResult" class="oauth-url-field" label="授权链接">
                <a-textarea :value="authResult.authUrl" :rows="3" readonly />
              </a-form-item>
              <a-form-item class="oauth-callback-field" label="回调 URL" required>
                <a-textarea v-model:value="form.callbackUrl" :rows="3" placeholder="粘贴浏览器地址栏里的 http://localhost:1455/auth/callback?code=...&state=..." />
                <div class="form-help">需要粘贴完整地址，不能只粘贴 code 或 state。</div>
              </a-form-item>
            </template>

            <template v-else>
              <div class="oauth-token-panel">
                <a-alert class="form-alert" type="info" show-icon message="已有 Refresh Token 时可跳过浏览器授权，后端会换取 Access Token 后创建账户。" />
                <a-form-item class="oauth-token-field" label="Refresh Token" required>
                  <a-textarea v-model:value="form.refreshToken" :rows="4" placeholder="粘贴 OpenAI 的 Refresh Token" />
                </a-form-item>
              </div>
            </template>
          </template>

          <a-alert v-else class="form-alert" type="warning" show-icon message="该供应商的 OAuth 创建流程尚未开放，第一期先支持 OpenAI OAuth。" />
        </section>

        <section v-if="hasAccountType" class="form-section">
          <div class="form-section-head">
            <div>
              <h4>请求策略</h4>
              <p>并发、优先级和代理会影响后续请求转发与账户选择。</p>
            </div>
          </div>
          <div class="strategy-grid">
            <a-form-item label="状态">
              <a-select v-model:value="form.status" :options="statusEditOptions" />
            </a-form-item>
            <a-form-item label="并发上限">
              <a-input-number v-model:value="form.concurrencyLimit" :min="1" style="width: 100%" />
            </a-form-item>
            <a-form-item label="优先级">
              <a-input-number v-model:value="form.priority" :min="0" style="width: 100%" />
            </a-form-item>
          </div>
          <div class="form-help strategy-help">优先级数字越小越优先；当前账号失败后会切换到下一个可用账号。</div>
          <a-form-item v-if="isAdmin" class="strategy-proxy-field" label="代理">
            <a-select v-model:value="form.proxyProfileId" allow-clear placeholder="不使用代理" :options="proxyOptions" />
          </a-form-item>
        </section>

        <AccountErrorPolicyCard v-if="hasAccountType" v-model:rules="accountErrorPolicyRules" />
      </a-form>
    </a-modal>

    <a-modal v-model:open="authorizationModalOpen" :title="authorizationModalTitle" width="920px" :footer="null" @cancel="closeAuthorizationModal">
      <div v-if="authorizationAccount" class="authorization-modal">
        <a-alert class="form-alert" type="info" show-icon message="授权后对方仅可使用账户，不能编辑、删除、测试或查看凭据；用量统一累计到账户，日志仍按实际调用者记录。" />
        <div class="authorization-create-row">
          <a-select v-model:value="authorizationForm.granteeSystemAccountId" show-search option-filter-prop="label" class="authorization-user-select" :options="authorizationUserOptions" placeholder="选择被授权用户" />
          <a-input v-model:value="authorizationForm.remark" allow-clear placeholder="备注（可选）" />
          <a-button type="primary" :loading="authorizationSaving" @click="createAuthorization">新增授权</a-button>
        </div>
        <a-table size="small" :columns="authorizationColumns" :data-source="accountAuthorizations" row-key="id" :loading="authorizationLoading" :pagination="false">
          <template #emptyText>
            <a-empty description="还没有授权记录" />
          </template>
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'grantee'">
              {{ record.granteeSystemAccountName || record.granteeSystemAccountId }}
            </template>
            <template v-else-if="column.key === 'status'">
              <a-tag :color="record.status === 'active' ? 'green' : 'default'">{{ record.status === 'active' ? '生效中' : '已收回' }}</a-tag>
            </template>
            <template v-else-if="column.key === 'usage'">
              <span class="usage-summary">{{ formatAuthorizationUsage(record.usage) }}</span>
            </template>
            <template v-else-if="column.key === 'createdAt'">
              {{ formatDateTime(record.createdAt) }}
            </template>
            <template v-else-if="column.key === 'actions'">
              <a-popconfirm v-if="record.status === 'active'" title="确认收回这个授权？" @confirm="revokeAuthorization(record.id)">
                <a-button type="link" size="small" danger>收回</a-button>
              </a-popconfirm>
              <span v-else class="muted-cell">-</span>
            </template>
          </template>
        </a-table>
      </div>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import axios from 'axios'
import dayjs, { type Dayjs } from 'dayjs'
import { message } from 'ant-design-vue'
import { InfoCircleOutlined } from '@ant-design/icons-vue'
import { computed, onMounted, reactive, ref } from 'vue'

import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import type { AccountAuthorizationSummary, AccountStatus, AccountSummary, AccountTestResult, AccountType, AccountUsageSummary, GroupSummary, OpenAIAuthURLResult, ProviderDefinition, ProviderModelPricing, ProxyProfileSummary, SystemAccountSummary } from '@/types/domain'
import { allSystemAccountsValue, buildSystemAccountOptions, matchesSystemAccountFilter, selectedSystemAccountId } from '@/utils/systemAccountFilter'
import AccountErrorPolicyCard from './AccountErrorPolicyCard.vue'
import {
  loadAccountErrorPolicyRules,
  validateAccountErrorPolicyRules,
  writeAccountErrorPolicyToCredentials,
  type AccountErrorPolicyRuleForm
} from './accountErrorPolicy'

type SchedulableFilter = 'all' | 'enabled' | 'disabled' | 'cooling'

interface AccountFilters {
  keyword: string
  type: 'all' | AccountType
  status: 'all' | AccountStatus
  schedulable: SchedulableFilter
  systemAccountId: string
}

interface AccountMenuItem {
  key: string
  label: string
  danger?: boolean
}

interface OAuthUsageBar {
  key: string
  label: string
  percent: number
  displayPercent: string
  resetText: string
  color: string
  tone: string
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
  oauthMode: 'manual' | 'refresh_token'
  callbackUrl: string
  accountExpiresAt?: Dayjs | null
  status: AccountStatus
  concurrencyLimit: number
  priority: number
  proxyProfileId?: string
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

const DEFAULT_ACCOUNT_CONCURRENCY_LIMIT = 20

const loading = ref(false)
const saving = ref(false)
const authLoading = ref(false)
const testModalOpen = ref(false)
const testRunning = ref(false)
const testModelsLoading = ref(false)
const modalOpen = ref(false)
const authorizationModalOpen = ref(false)
const authorizationLoading = ref(false)
const authorizationSaving = ref(false)
const authResult = ref<OpenAIAuthURLResult>()
const editingId = ref<string>()
const authorizationAccount = ref<AccountSummary>()
const accountAuthorizations = ref<AccountAuthorizationSummary[]>([])
const testingAccount = ref<AccountSummary>()
const testResult = ref<AccountTestResult>()
const selectedAccountIds = ref<string[]>([])
const accounts = ref<AccountSummary[]>([])
const providers = ref<ProviderDefinition[]>([])
const providerModels = ref<ProviderModelPricing[]>([])
const proxies = ref<ProxyProfileSummary[]>([])
const groups = ref<GroupSummary[]>([])
const systemAccounts = ref<SystemAccountSummary[]>([])
const filters = reactive<AccountFilters>({ keyword: '', type: 'all', status: 'all', schedulable: 'all', systemAccountId: allSystemAccountsValue })
const testForm = reactive({ model: 'gpt-5.5', prompt: 'hi' })
const authorizationForm = reactive({ granteeSystemAccountId: undefined as string | undefined, remark: '' })
const isAdmin = authState.isAdmin

const form = reactive<AccountForm>(defaultForm())
const accountErrorPolicyRules = ref<AccountErrorPolicyRuleForm[]>(loadAccountErrorPolicyRules())

const typeOptions = [
  { label: '全部类型', value: 'all' },
  { label: 'OAuth', value: 'oauth' },
  { label: 'API Key', value: 'api_key' }
]

const schedulableOptions = [
  { label: '全部启停', value: 'all' },
  { label: '已启用', value: 'enabled' },
  { label: '已停用', value: 'disabled' },
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

const currentEditingAccount = computed(() => editingId.value ? accounts.value.find((account) => account.id === editingId.value) : undefined)

const statusEditOptions = computed(() => {
  const options = statusOptions.filter((item) => item.value !== 'all')
  if (currentEditingAccount.value && isTemporaryAccountStatus(currentEditingAccount.value)) {
    return options.filter((item) => item.value !== 'active')
  }
  return options
})

const columns = computed(() => {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: '名称', dataIndex: 'name', key: 'name', width: 230 },
    { title: '账户类型', dataIndex: 'type', key: 'type', width: 120 },
    { title: '供应商', dataIndex: 'providerCode', key: 'providerCode', width: 110 }
  ]
  if (isAdmin.value) {
    baseColumns.push({ title: '系统账户', key: 'systemAccount', width: 180 })
  }
  baseColumns.push(
    { title: '并发数', key: 'concurrency', width: 100, align: 'center', sorter: compareAccountConcurrency },
    { title: '状态', key: 'status', width: 190 },
    { title: '用量情况', key: 'usage', width: 380 },
    { title: '归属分组', key: 'group', width: 240, className: 'account-group-column' },
    { title: '优先级', dataIndex: 'priority', key: 'priority', width: 90 },
    { title: '账户到期时间', key: 'accountExpiresAt', width: 180, sorter: compareAccountExpiresAt },
    { title: '最近使用时间', key: 'lastUsedAt', width: 180, sorter: compareAccountLastUsedAt },
    { title: '操作', key: 'actions', width: 160, fixed: 'right' }
  )
  return baseColumns
})
const tableScrollX = computed(() => (isAdmin.value ? 2240 : 2060))

const authorizationModalTitle = computed(() => authorizationAccount.value ? `授权管理：${authorizationAccount.value.name}` : '授权管理')

const authorizationColumns = [
  { title: '被授权用户', key: 'grantee', width: 180 },
  { title: '状态', key: 'status', width: 100 },
  { title: '授权后用量', key: 'usage', width: 260 },
  { title: '备注', dataIndex: 'remark', key: 'remark', width: 180 },
  { title: '授权时间', key: 'createdAt', width: 180 },
  { title: '操作', key: 'actions', width: 100 }
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
  const systemAccountMatched = matchesSystemAccountFilter(account, filters.systemAccountId, isAdmin.value)
  return keywordMatched && typeMatched && statusMatched && schedulableMatched && systemAccountMatched
}))

const systemAccountOptions = computed(() => buildSystemAccountOptions(systemAccounts.value))
const authorizationUserOptions = computed(() => systemAccounts.value
  .filter((account) => account.status === 'active' && account.id !== authorizationAccount.value?.ownerSystemAccountId)
  .map((account) => ({ label: `${account.displayName || account.username}（${account.username}）`, value: account.id })))

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
  },
  getCheckboxProps: (account: AccountSummary) => ({ disabled: !canEditAccount(account) })
}))

const proxyOptions = computed(() => (isAdmin.value ? proxies.value : []).map((proxy) => ({ label: `${proxy.name} (${proxy.type})`, value: proxy.id })))
const providerGroups = computed(() => groups.value.filter((group) => canManageGroupAccounts(group) && (!form.providerCode || group.providerCode === form.providerCode)))
const groupOptions = computed(() => providerGroups.value.map((group) => ({ label: group.isDefault ? `${group.name}（默认）` : group.name, value: group.id })))
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
    oauthMode: 'manual',
    callbackUrl: '',
    accountExpiresAt: undefined,
    status: 'active',
    concurrencyLimit: DEFAULT_ACCOUNT_CONCURRENCY_LIMIT,
    priority: 0,
    proxyProfileId: undefined,
    notes: ''
  }
}

function resetForm(providerCode = '', type: AccountType = '') {
  Object.assign(form, defaultForm(providerCode, type))
  ensureDefaultGroupSelected(providerCode)
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
  if (isOwnerDisabledAuthorizedAccount(account)) return 'default'
  if (isLocallyDisabledAuthorizedAccount(account)) return 'default'
  return statusColor(account.status)
}

function accountStatusText(account: AccountSummary) {
  if (isOwnerDisabledAuthorizedAccount(account)) return '停用'
  if (isLocallyDisabledAuthorizedAccount(account)) return '停用'
  return statusText(account.status)
}

function accountCooldownText(account: AccountSummary) {
  if (!isCoolingDown(account)) return ''
  return `暂停至 ${formatDateTime(account.cooldownUntil)}`
}

function accountStatusTooltipLines(account: AccountSummary): string[] {
  const lines: string[] = []
  if (isAuthorizedAccount(account) && account.accountAuthorizationSchedulable === false) {
    lines.push('你已停用这份授权账户，不影响授权方和其他用户')
  }
  if (account.accountExpiresAt) {
    lines.push(`账户到期时间：${formatDateTime(account.accountExpiresAt)}`)
  }
  const cooldownText = accountCooldownText(account)
  if (cooldownText) {
    lines.push(cooldownText)
  } else if (isTemporaryAccountStatus(account) && account.cooldownUntil) {
    lines.push(`已到期：${formatDateTime(account.cooldownUntil)}`)
    lines.push('等待后台复测；也可手动测试，成功后恢复正常')
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

function isAccountPackageExpired(account: AccountSummary) {
  if (!account.accountExpiresAt) return false
  const time = new Date(account.accountExpiresAt).getTime()
  return Number.isFinite(time) && time <= Date.now()
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
  if (providerCode === 'openai' && type === 'api_key') return '适合直接粘贴 OpenAI API Key，可配置 Base URL。'
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

function isAuthorizedAccount(account: AccountSummary): boolean {
  return account.accessType === 'authorized'
}

function isLocallyDisabledAuthorizedAccount(account: AccountSummary): boolean {
  return isAuthorizedAccount(account) && account.accountAuthorizationSchedulable === false
}

function isOwnerDisabledAuthorizedAccount(account: AccountSummary): boolean {
  return isAuthorizedAccount(account) && account.status === 'disabled'
}

function authorizedAccountTooltip(account: AccountSummary): string {
  const ownerName = account.ownerSystemAccountName || '其他用户'
  if (isOwnerDisabledAuthorizedAccount(account)) {
    return `授权自 ${ownerName}。账户所有者已停用该账户，你暂时无法启用或调用；请联系对方启用后再使用。`
  }
  if (isLocallyDisabledAuthorizedAccount(account)) {
    return `授权自 ${ownerName}。你已停用这份授权账户；重新启用只影响你自己，不影响账户所有者和其他用户。`
  }
  return `授权自 ${ownerName}，仅可使用`
}

function canToggleGrantedAuthorization(account: AccountSummary): boolean {
  return !isOwnerDisabledAuthorizedAccount(account)
}

function authorizedAccountToggleTooltip(account: AccountSummary): string {
  return account.accountAuthorizationSchedulable === false
    ? '只为你自己启用这份授权账户'
    : '只为你自己停用这份授权账户'
}

function canEditAccount(account: AccountSummary): boolean {
  return account.permissions?.canEdit !== false
}

function canDeleteAccount(account: AccountSummary): boolean {
  return account.permissions?.canDelete !== false
}

function canAuthorizeAccount(account: AccountSummary): boolean {
  return account.permissions?.canAuthorize !== false && !isAuthorizedAccount(account)
}

function canUseAccountActions(account: AccountSummary): boolean {
  return canEditAccount(account) && account.permissions?.canViewCredentials !== false
}

function canTestAccount(account: AccountSummary): boolean {
  return account.permissions?.canUse !== false
}

function canManageGroupAccounts(group: GroupSummary): boolean {
  return group.permissions?.canManageAccounts !== false && group.accessType !== 'authorized'
}

function defaultGroupForProvider(providerCode: string) {
  const candidates = groups.value.filter((group) => group.providerCode === providerCode && canManageGroupAccounts(group))
  return candidates.find((group) => group.isDefault) ?? candidates[0]
}

function ensureDefaultGroupSelected(providerCode = form.providerCode) {
  if (!providerCode) {
    form.groupId = undefined
    return
  }
  const currentGroup = groups.value.find((group) => group.id === form.groupId)
  if (currentGroup?.providerCode === providerCode && canManageGroupAccounts(currentGroup)) {
    return
  }
  form.groupId = defaultGroupForProvider(providerCode)?.id
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
  if (filter === 'enabled') return account.status === 'active' && account.schedulable && !isTemporaryAccountStatus(account) && !isCoolingDown(account)
  return account.status === 'disabled' || !account.schedulable
}

function accountBaseUrl(account: AccountSummary): string {
  return asString(account.credentials.base_url)
}

function accountMenuItems(account: AccountSummary): AccountMenuItem[] {
  if (!canUseAccountActions(account)) return []
  return [
    { key: 'test', label: '测试' },
    { key: 'toggle-status', label: account.status === 'disabled' ? '启用账户' : '停用账户', danger: account.status !== 'disabled' },
    ...(canAuthorizeAccount(account) ? [{ key: 'authorizations', label: '授权管理' }] : [])
  ]
}

function formatAuthorizationUsage(usage: AccountUsageSummary): string {
  return `${formatNumber(usage.requestCount)}req / ${formatUsageAmount(usage.totalTokens)} / ${formatCost(usage.totalCost)}`
}

function formatNumber(value?: number): string {
  return new Intl.NumberFormat('zh-CN').format(value ?? 0)
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

function oauthUsageBars(account: AccountSummary): OAuthUsageBar[] {
  if (account.providerCode !== 'openai' || account.type !== 'oauth') return []
  return [
    oauthUsageBar('5h', '5h', account.oauthUsage?.fiveHour) ?? oauthUsagePlaceholder('5h'),
    oauthUsageBar('7d', '7d', account.oauthUsage?.sevenDay) ?? oauthUsagePlaceholder('7d')
  ]
}

function oauthUsageBar(key: string, label: string, window?: { utilization: number; resetsAt?: string; remainingSeconds: number }): OAuthUsageBar | undefined {
  if (!window) return undefined
  const rawPercent = Math.max(0, window.utilization)
  const percent = Math.min(Math.round(rawPercent), 100)
  return {
    key,
    label,
    percent,
    displayPercent: rawPercent > 999 ? '>999%' : `${Math.round(rawPercent)}%`,
    resetText: window.resetsAt ? formatRelativeReset(window.resetsAt) : '现在',
    color: rawPercent >= 100 ? '#ef4444' : rawPercent >= 80 ? '#f59e0b' : '#22c55e',
    tone: rawPercent >= 100 ? 'danger' : rawPercent >= 80 ? 'warning' : 'normal'
  }
}

function oauthUsagePlaceholder(key: string): OAuthUsageBar {
  return {
    key,
    label: key,
    percent: 0,
    displayPercent: '--',
    resetText: '未获取',
    color: '#d1d5db',
    tone: 'normal'
  }
}

function formatRelativeReset(value: string): string {
  const time = Date.parse(value)
  if (!Number.isFinite(time)) return value
  const diffMs = time - Date.now()
  if (diffMs <= 0) return '现在'
  const totalMinutes = Math.ceil(diffMs / 60_000)
  const days = Math.floor(totalMinutes / 1440)
  const hours = Math.floor((totalMinutes % 1440) / 60)
  const minutes = totalMinutes % 60
  if (days > 0) return `${days}d ${hours}h`
  if (hours > 0) return `${hours}h ${minutes}m`
  return `${minutes}m`
}

function formatDateTime(value?: string): string {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString('zh-CN', { hour12: false })
}

function parseDatePickerValue(value?: string): Dayjs | undefined {
  if (!value) return undefined
  const parsed = dayjs(value)
  return parsed.isValid() ? parsed : undefined
}

function accountLastUsedAt(account: AccountSummary): string | undefined {
  return account.lastUsedAt || account.usage.lastUsedAt
}

function compareAccountLastUsedAt(left: AccountSummary, right: AccountSummary): number {
  return timestampOf(accountLastUsedAt(left)) - timestampOf(accountLastUsedAt(right))
}

function compareAccountExpiresAt(left: AccountSummary, right: AccountSummary): number {
  return timestampOf(left.accountExpiresAt) - timestampOf(right.accountExpiresAt)
}

function compareAccountConcurrency(left: AccountSummary, right: AccountSummary): number {
  return left.concurrencyLimit - right.concurrencyLimit || left.currentConcurrency - right.currentConcurrency || left.name.localeCompare(right.name, 'zh-CN')
}

function timestampOf(value?: string): number {
  if (!value) return 0
  const timestamp = Date.parse(value)
  return Number.isFinite(timestamp) ? timestamp : 0
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
    const systemAccountId = selectedSystemAccountId(filters.systemAccountId, isAdmin.value)
    const [accountList, providerList, proxyList, groupList, systemAccountList] = await Promise.all([
      api.accounts.list({ systemAccountId }),
      isAdmin.value ? api.providers.list() : Promise.resolve([] as ProviderDefinition[]),
      isAdmin.value ? api.proxies.list() : Promise.resolve([] as ProxyProfileSummary[]),
      api.groups.list({ systemAccountId }),
      api.systemAccounts.list()
    ])
    accounts.value = accountList
    providers.value = providerList.length ? providerList : [FALLBACK_PROVIDER]
    proxies.value = proxyList
    groups.value = groupList
    systemAccounts.value = systemAccountList
    selectedAccountIds.value = selectedAccountIds.value.filter((id) => accountList.some((account) => account.id === id && canEditAccount(account)))
    if (modalOpen.value && !editingId.value) {
      ensureDefaultGroupSelected()
    }
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
    schedulable: 'all',
    systemAccountId: allSystemAccountsValue
  })
  void loadData()
}

function extractApiErrorMessage(error: unknown, fallback: string): string {
  if (axios.isAxiosError<{ message?: string }>(error)) {
    return error.response?.data?.message ?? fallback
  }
  return error instanceof Error ? error.message : fallback
}

function handleSystemAccountFilterChange() {
  selectedAccountIds.value = []
  void loadData()
}

function clearSelection() {
  selectedAccountIds.value = []
}

function currentListParams() {
  return { systemAccountId: selectedSystemAccountId(filters.systemAccountId, isAdmin.value) }
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
    accountExpiresAt: form.accountExpiresAt
  })
  ensureDefaultGroupSelected(providerCode)
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
    accountExpiresAt: parseDatePickerValue(account.accountExpiresAt),
    groupId: groupIdForAccount(account.id),
    apiKey: asString(account.credentials.api_key),
    baseUrl: asString(account.credentials.base_url) || 'https://api.openai.com/v1',
    accessToken: asString(account.credentials.access_token),
    refreshToken: asString(account.credentials.refresh_token),
    notes: account.notes ?? ''
  })
  accountErrorPolicyRules.value = loadAccountErrorPolicyRules(account.credentials)
  authResult.value = undefined
  modalOpen.value = true
}

async function openAuthorizationModal(account: AccountSummary) {
  if (!canAuthorizeAccount(account)) {
    message.warning('当前账户没有授权管理权限')
    return
  }
  authorizationAccount.value = account
  authorizationForm.granteeSystemAccountId = undefined
  authorizationForm.remark = ''
  authorizationModalOpen.value = true
  authorizationLoading.value = true
  try {
    accountAuthorizations.value = await api.accounts.authorizations(account.id)
  } catch (error) {
    console.error(error)
    message.error('加载授权记录失败')
  } finally {
    authorizationLoading.value = false
  }
}

function closeAuthorizationModal() {
  authorizationModalOpen.value = false
  authorizationAccount.value = undefined
  accountAuthorizations.value = []
  authorizationForm.granteeSystemAccountId = undefined
  authorizationForm.remark = ''
}

async function refreshAuthorizationList() {
  if (!authorizationAccount.value) return
  authorizationLoading.value = true
  try {
    accountAuthorizations.value = await api.accounts.authorizations(authorizationAccount.value.id)
  } catch (error) {
    console.error(error)
    message.error('刷新授权记录失败')
  } finally {
    authorizationLoading.value = false
  }
}

async function createAuthorization() {
  if (!authorizationAccount.value) return
  if (!authorizationForm.granteeSystemAccountId) {
    message.warning('请选择被授权用户')
    return
  }
  authorizationSaving.value = true
  try {
    await api.accounts.createAuthorization(authorizationAccount.value.id, {
      granteeSystemAccountId: authorizationForm.granteeSystemAccountId,
      remark: authorizationForm.remark.trim() || undefined
    })
    message.success('授权已创建')
    authorizationForm.granteeSystemAccountId = undefined
    authorizationForm.remark = ''
    await refreshAuthorizationList()
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '创建授权失败'))
  } finally {
    authorizationSaving.value = false
  }
}

async function revokeAuthorization(authorizationId: string) {
  if (!authorizationAccount.value) return
  try {
    await api.accounts.revokeAuthorization(authorizationAccount.value.id, authorizationId)
    message.success('授权已收回')
    await refreshAuthorizationList()
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('收回授权失败')
  }
}

function buildCredentials() {
  const credentials: Record<string, unknown> = form.type === 'api_key'
    ? buildApiKeyCredentials()
    : buildOAuthCredentials()
  writeAccountErrorPolicyToCredentials(credentials, accountErrorPolicyRules.value)
  return credentials
}

function buildApiKeyCredentials(): Record<string, unknown> {
  return {
    api_key: form.apiKey,
    base_url: form.baseUrl
  }
}

function buildOAuthCredentials(): Record<string, unknown> {
  const currentCredentials = editingId.value
    ? accounts.value.find((account) => account.id === editingId.value)?.credentials ?? {}
    : {}
  return compactCredentials({
    ...currentCredentials,
    access_token: form.accessToken,
    refresh_token: form.refreshToken,
    expires_at: currentCredentials.expires_at,
    base_url: currentCredentials.base_url ?? 'https://api.openai.com/v1'
  })
}

function compactCredentials(credentials: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(credentials).filter(([, value]) => value !== undefined && value !== ''))
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
  if (!form.groupId) {
    message.warning('请选择归属分组')
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
    accountExpiresAt: form.accountExpiresAt?.toISOString() ?? null,
    groupId: form.groupId,
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
    message.error(extractApiErrorMessage(error, '保存账户失败'))
  } finally {
    saving.value = false
  }
}

async function generateOAuthUrl() {
  authLoading.value = true
  try {
    authResult.value = await api.openaiOAuth.authUrl({})
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
    name: form.name.trim() || undefined,
    groupId: form.groupId,
    concurrencyLimit: form.concurrencyLimit,
    proxyProfileId: form.proxyProfileId,
    accountExpiresAt: form.accountExpiresAt?.toISOString() ?? null,
    credentialsPatch: { error_handling_rules: buildCredentials().error_handling_rules },
    notes: form.notes || undefined
  }

  if (form.oauthMode === 'manual') {
    await api.openaiOAuth.createFromCode({
      ...commonPayload,
      sessionId: authResult.value?.sessionId,
      callbackUrl: form.callbackUrl
    })
    return
  }

  await api.openaiOAuth.createFromRefreshToken({
    ...commonPayload,
    refreshToken: form.refreshToken
  })
}

async function loadTestModels() {
  if (!isAdmin.value || providerModels.value.length || testModelsLoading.value) return
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
  if (!canTestAccount(account)) {
    message.warning('当前账户不能测试')
    return
  }
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
  if (!canTestAccount(account)) return undefined
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
  successLabel: string,
  selected = selectedAccounts.value.filter(canEditAccount)
) {
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
  const selected = selectedAccounts.value.filter(canTestAccount)
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
  const selected = selectedAccounts.value.filter(canEditAccount)
  const eligible = status === 'active'
    ? selected.filter((account) => account.status === 'disabled')
    : selected.filter((account) => account.status !== 'disabled')
  if (!eligible.length) {
    message.warning(status === 'active' ? '所选账户里没有可手动启用的停用账户' : '所选账户里没有可停用的账户')
    return
  }
  if (eligible.length !== selected.length) {
    message.warning(status === 'active' ? '已跳过临时状态或错误状态的账户，只启用手动停用的账户' : '已跳过已停用的账户')
  }
  await batchUpdateAccounts(
    (account) => ({ status: account.status === 'disabled' ? 'active' : 'disabled' }),
    status === 'active' ? '正在批量启用账户' : '正在批量停用账户',
    status === 'active' ? '账户已批量启用' : '账户已批量停用',
    eligible
  )
}

async function updateAccountState(account: AccountSummary, payload: Record<string, unknown>, successText: string) {
  if (!canEditAccount(account)) {
    message.warning('授权账户不能修改状态')
    return
  }
  try {
    await api.accounts.update(account.id, payload)
    message.success(successText)
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '账户状态更新失败'))
  }
}

async function updateGrantedAuthorizationSchedulable(account: AccountSummary) {
  if (!account.accountAuthorizationId) {
    message.warning('授权关系不存在')
    return
  }
  const nextSchedulable = account.accountAuthorizationSchedulable === false
  try {
    await api.accounts.updateGrantedAuthorizationSchedulable(account.id, account.accountAuthorizationId, nextSchedulable, currentListParams())
    message.success(nextSchedulable ? '已启用这份授权账户' : '已停用这份授权账户')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '授权账户启停失败'))
  }
}

async function returnGrantedAuthorization(account: AccountSummary) {
  if (!account.accountAuthorizationId) {
    message.warning('授权关系不存在')
    return
  }
  try {
    await api.accounts.returnGrantedAuthorization(account.id, account.accountAuthorizationId, currentListParams())
    message.success('已归还授权账户')
    selectedAccountIds.value = selectedAccountIds.value.filter((id) => id !== account.id)
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '归还授权账户失败'))
  }
}

async function handleAccountMenu(key: string, account: AccountSummary) {
  if (!canUseAccountActions(account)) {
    message.warning('授权账户仅可使用，不能执行管理操作')
    return
  }
  if (key === 'test') {
    await testAccount(account)
    return
  }
  if (key === 'toggle-status') {
    const nextStatus = account.status === 'disabled' ? 'active' : 'disabled'
    await updateAccountState(account, { status: nextStatus }, nextStatus === 'active' ? '账户已启用' : '账户已停用')
    return
  }
  if (key === 'authorizations') {
    await openAuthorizationModal(account)
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

.account-group-text {
  display: block;
  width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.usage-cell {
  --usage-meter-width: 150px;
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 5px;
  line-height: 1.4;
  white-space: normal;
}

.usage-summary-tags {
  display: inline-grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 4px;
  width: var(--usage-meter-width);
}

.usage-summary-tag {
  min-width: 0;
  margin-inline-end: 0;
  padding-inline: 5px;
  color: #0f172a;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  text-align: center;
  white-space: nowrap;
}

.oauth-usage-bars {
  display: flex;
  flex-direction: column;
  gap: 3px;
  width: var(--usage-meter-width);
}

.oauth-usage-row {
  display: grid;
  grid-template-columns: 24px minmax(0, 1fr) 36px 44px;
  align-items: center;
  column-gap: 4px;
}

.oauth-usage-label {
  display: inline-flex;
  justify-content: center;
  border-radius: 999px;
  background: #eef2ff;
  color: #4338ca;
  font-size: 11px;
  font-weight: 600;
}

.oauth-usage-progress {
  line-height: 1;
  min-width: 0;
}

.oauth-usage-percent {
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  text-align: right;
}

.oauth-usage-percent.normal {
  color: #475569;
}

.oauth-usage-percent.warning {
  color: #d97706;
}

.oauth-usage-percent.danger {
  color: #dc2626;
}

.oauth-usage-reset,
.oauth-usage-updated {
  color: #64748b;
  font-size: 12px;
  white-space: nowrap;
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

.expired-cell {
  color: #dc2626;
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
  display: flex;
  width: 100%;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 12px;
  margin-bottom: 16px;
}

.oauth-mode-item {
  margin-bottom: 12px;
}

.oauth-flow-panel,
.oauth-token-panel {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.oauth-step-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 10px;
}

.oauth-step-card {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
  padding: 12px;
  border: 1px solid #dbeafe;
  border-radius: 14px;
  background: #fff;
}

.oauth-step-card span {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  color: #1d4ed8;
  font-weight: 700;
  border-radius: 999px;
  background: #dbeafe;
}

.oauth-step-card div {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 2px;
}

.oauth-step-card strong {
  color: #0f172a;
  font-size: 13px;
}

.oauth-step-card small {
  overflow: hidden;
  color: #64748b;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.oauth-url-field,
.oauth-callback-field,
.oauth-token-field {
  margin-bottom: 0;
}

.form-help {
  margin-top: 4px;
  color: #64748b;
  font-size: 12px;
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

.resource-name-cell,
.authorization-modal {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.resource-name-line {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.authorized-account-icon {
  color: #08979c;
  cursor: help;
  font-size: 14px;
}

.authorized-account-icon.owner-disabled {
  color: #d48806;
}

.authorization-create-row {
  display: grid;
  grid-template-columns: minmax(180px, 240px) minmax(180px, 1fr) auto;
  gap: 10px;
  align-items: center;
}

.authorization-user-select {
  width: 100%;
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

.strategy-grid {
  display: grid;
  grid-template-columns: minmax(160px, 1.3fr) minmax(120px, 1fr) minmax(120px, 1fr);
  gap: 0 16px;
}

.strategy-help {
  margin-top: -8px;
  margin-bottom: 16px;
}

.strategy-proxy-field {
  margin-bottom: 16px;
}

.form-alert {
  border-radius: 12px;
}

@media (max-width: 992px) {
  .setup-progress,
  .oauth-step-grid {
    grid-template-columns: 1fr;
  }

  .form-grid,
  .strategy-grid {
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
  .strategy-grid {
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

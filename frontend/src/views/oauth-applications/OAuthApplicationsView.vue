<template>
  <a-card class="page-card responsive-page-card oauth-applications-card">
    <ResponsiveListToolbar
      v-model:keyword="keyword"
      search-placeholder="搜索应用名称或 Client ID"
      :refresh-loading="loading"
      :show-filters="false"
      @reset="resetFilters"
      @refresh="loadClients"
    >
      <template #actions>
        <a-button type="primary" @click="openCreateModal">新建第三方应用</a-button>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList
      table-class="page-table oauth-client-table"
      :columns="columns"
      :data-source="filteredClients"
      row-key="clientId"
      :loading="loading"
      :scroll-x="1280"
      pull-refresh-enabled
      :refreshing="loading"
      @mobile-refresh="loadClients"
    >
      <template #emptyText>
        <a-empty class="page-empty-card" description="暂无第三方应用。创建 Client 后可向外部应用分发 Client ID。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'displayName'">
          <div class="application-name-cell">
            <strong>{{ record.displayName }}</strong>
            <span class="application-client-id" :title="record.clientId">{{ record.clientId }}</span>
          </div>
        </template>
        <template v-else-if="column.key === 'clientType'">
          <a-tag :color="record.clientType === 'confidential' ? 'blue' : 'purple'">
            {{ clientTypeLabel(record.clientType) }}
          </a-tag>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag :color="record.status === 'active' ? 'green' : 'red'">
            {{ record.status === 'active' ? '启用' : '停用' }}
          </a-tag>
        </template>
        <template v-else-if="column.key === 'redirectUris'">
          <div class="uri-list" :title="record.redirectUris.join('\n')">
            <code v-for="uri in record.redirectUris" :key="uri">{{ uri }}</code>
          </div>
        </template>
        <template v-else-if="column.key === 'allowedScopes'">
          <div class="scope-list">
            <a-tag v-for="scope in record.allowedScopes" :key="scope">{{ scope }}</a-tag>
          </div>
        </template>
        <template v-else-if="column.key === 'createdAt'">
          {{ formatDateTime(record.createdAt) }}
        </template>
        <template v-else-if="column.key === 'actions'">
          <a-space :size="4">
            <a-switch
              :checked="record.status === 'active'"
              :loading="updatingClientIds.has(record.clientId)"
              checked-children="启用"
              un-checked-children="停用"
              @change="(checked: boolean) => void updateClientStatus(record, checked)"
            />
            <a-tooltip title="下载该应用的对接文档">
              <a-button
                type="text"
                :loading="downloadingClientIds.has(record.clientId)"
                :aria-label="`下载${record.displayName}的对接文档`"
                @click="downloadIntegrationGuide(record)"
              >
                <template #icon><DownloadOutlined /></template>
              </a-button>
            </a-tooltip>
            <a-popconfirm
              v-if="record.clientType === 'confidential'"
              title="重新签发 Client Secret？"
              description="旧 secret 会立即失效。请下载新的完整对接文档并交给客户服务端。"
              ok-text="重新签发"
              cancel-text="取消"
              @confirm="reissueClientSecret(record)"
            >
              <a-tooltip title="重新签发 Client Secret 并下载完整对接文档">
                <a-button
                  type="text"
                  :loading="reissuingSecretClientIds.has(record.clientId)"
                  :aria-label="`重新签发${record.displayName}的 Client Secret`"
                >
                  <template #icon><KeyOutlined /></template>
                </a-button>
              </a-tooltip>
            </a-popconfirm>
          </a-space>
        </template>
      </template>
      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">{{ record.displayName }}</div>
            <a-tag :color="record.status === 'active' ? 'green' : 'red'">
              {{ record.status === 'active' ? '启用' : '停用' }}
            </a-tag>
          </div>
          <div class="mobile-list-meta-grid">
            <div class="mobile-list-meta-item">
              <span>Client 类型</span>
              <strong>{{ clientTypeLabel(record.clientType) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>创建时间</span>
              <strong>{{ formatDateTime(record.createdAt) }}</strong>
            </div>
          </div>
          <div class="mobile-list-note">
            <span>Client ID</span>
            <code>{{ record.clientId }}</code>
          </div>
          <div class="mobile-list-note">
            <span>已允许 Scope</span>
            <div class="scope-list">
              <a-tag v-for="scope in record.allowedScopes" :key="scope">{{ scope }}</a-tag>
            </div>
          </div>
          <div class="mobile-list-actions">
            <a-switch
              :checked="record.status === 'active'"
              :loading="updatingClientIds.has(record.clientId)"
              checked-children="启用"
              un-checked-children="停用"
              @change="(checked: boolean) => void updateClientStatus(record, checked)"
            />
            <a-button :loading="downloadingClientIds.has(record.clientId)" @click="downloadIntegrationGuide(record)">
              <template #icon><DownloadOutlined /></template>
              下载对接文档
            </a-button>
            <a-popconfirm
              v-if="record.clientType === 'confidential'"
              title="重新签发 Client Secret？"
              description="旧 secret 会立即失效。"
              ok-text="重新签发"
              cancel-text="取消"
              @confirm="reissueClientSecret(record)"
            >
              <a-button :loading="reissuingSecretClientIds.has(record.clientId)">
                <template #icon><KeyOutlined /></template>
                重新签发密钥
              </a-button>
            </a-popconfirm>
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <a-modal
      v-model:open="createModalOpen"
      title="新建第三方应用"
      width="680px"
      :confirm-loading="creating"
      :ok-button-props="{ disabled: creating }"
      ok-text="创建"
      cancel-text="取消"
      @ok="createClient"
    >
      <a-form layout="vertical">
        <a-form-item label="应用名称" required>
          <a-input v-model:value="createForm.displayName" :maxlength="120" placeholder="例如 运营数据看板" />
        </a-form-item>
        <a-form-item label="Client 类型" required>
          <a-radio-group v-model:value="createForm.clientType">
            <a-radio value="public">公开 Client</a-radio>
            <a-radio value="confidential">机密 Client</a-radio>
          </a-radio-group>
          <div class="form-help-text">
            {{ createForm.clientType === 'confidential' ? '机密 Client 的当前 client_secret 会写入应用专属对接文档，可随时从应用操作中重新下载；只能由第三方服务端或 BFF 保存。' : '公开 Client 不签发 client_secret，必须配合 PKCE 使用。' }}
          </div>
        </a-form-item>
        <a-form-item label="回调地址" required>
          <a-textarea
            v-model:value="createForm.redirectUrisText"
            :rows="4"
            placeholder="每行一个精确回调地址，例如 https://example.com/oauth/callback"
          />
          <div class="form-help-text">回调地址按逐字符精确匹配，不支持通配符。</div>
        </a-form-item>
        <a-form-item label="允许申请的 Scope" required>
          <a-checkbox-group v-model:value="createForm.allowedScopes" :options="scopeOptions" />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal
      :open="createdSecretOpen"
      title="Client Secret 已就绪"
      width="620px"
      :footer="null"
      :mask-closable="false"
      @cancel="closeCreatedSecret"
    >
      <div class="created-secret-content">
        <a-alert
          type="warning"
          show-icon
          message="完整对接文档可随时重新下载"
          description="机密 Client 的当前 client_secret 会自动写入该应用的对接文档。后续在应用操作中再次下载即可取得当前值；重新签发后旧 secret 会立即失效。"
        />
        <div class="created-secret-actions">
          <a-button @click="downloadCreatedClientGuide">下载完整对接文档</a-button>
          <a-button type="primary" @click="closeCreatedSecret">知道了</a-button>
        </div>
      </div>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { DownloadOutlined, KeyOutlined } from '@ant-design/icons-vue'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime } from '@/shared/formatters'
import type { OAuthClientCreatePayload, OAuthClientSummary, OAuthClientType } from '@/types/domain'
import { buildOAuthIntegrationGuide, oauthIntegrationGuideFilename } from './oauthIntegrationGuide'

const scopeOptions = [
  { label: 'OIDC 登录', value: 'openid' },
  { label: '基础身份资料', value: 'profile' },
  { label: '个人资料读取', value: 'juhe:profile.read' },
  { label: '个人资料写入', value: 'juhe:profile.write' },
  { label: '分组读取', value: 'juhe:groups.read' },
  { label: '分组写入', value: 'juhe:groups.write' },
  { label: '路由策略读取', value: 'juhe:route_strategies.read' },
  { label: '路由策略写入', value: 'juhe:route_strategies.write' },
  { label: 'API Key 读取', value: 'juhe:api_keys.read' },
  { label: 'API Key 写入', value: 'juhe:api_keys.write' },
  { label: 'AI 账户读取', value: 'juhe:ai_accounts.read' },
  { label: 'AI 账户写入', value: 'juhe:ai_accounts.write' },
  { label: '请求限额读取', value: 'juhe:request_limits.read' }
]

const requiredReadScopeByWriteScope: Record<string, string> = {
  'juhe:profile.write': 'juhe:profile.read',
  'juhe:groups.write': 'juhe:groups.read',
  'juhe:route_strategies.write': 'juhe:route_strategies.read',
  'juhe:api_keys.write': 'juhe:api_keys.read',
  'juhe:ai_accounts.write': 'juhe:ai_accounts.read'
}

const columns = [
  { title: '第三方应用', key: 'displayName', width: 260, fixed: 'left', align: 'left' },
  { title: '类型', key: 'clientType', width: 110, align: 'left' },
  { title: '状态', key: 'status', width: 90, align: 'left' },
  { title: '精确回调地址', key: 'redirectUris', width: 320, align: 'left' },
  { title: '允许 Scope', key: 'allowedScopes', width: 340, align: 'left' },
  { title: '创建时间', key: 'createdAt', width: 180, align: 'left' },
  { title: '操作', key: 'actions', width: 210, align: 'center' }
]

const loading = ref(false)
const creating = ref(false)
const keyword = ref('')
const clients = ref<OAuthClientSummary[]>([])
const updatingClientIds = ref(new Set<string>())
const downloadingClientIds = ref(new Set<string>())
const reissuingSecretClientIds = ref(new Set<string>())
const createModalOpen = ref(false)
const createdSecretOpen = ref(false)
const createdClient = ref<OAuthClientSummary>()
const createForm = reactive(createEmptyClientForm())
let listRequestId = 0

const filteredClients = computed(() => {
  const text = keyword.value.trim().toLowerCase()
  if (!text) return clients.value
  return clients.value.filter((client) => (
    client.displayName.toLowerCase().includes(text)
    || client.clientId.toLowerCase().includes(text)
  ))
})

onMounted(() => {
  void loadClients()
})

async function loadClients(): Promise<void> {
  const requestId = ++listRequestId
  loading.value = true
  try {
    const result = await api.oauthApplications.listClients()
    if (requestId !== listRequestId) return
    clients.value = result
  } catch (error) {
    if (requestId !== listRequestId) return
    message.error(extractApiErrorMessage(error, '加载第三方应用失败'))
  } finally {
    if (requestId === listRequestId) loading.value = false
  }
}

function resetFilters(): void {
  keyword.value = ''
}

function openCreateModal(): void {
  Object.assign(createForm, createEmptyClientForm())
  createModalOpen.value = true
}

async function createClient(): Promise<void> {
  const payload = buildCreatePayload()
  if (!payload) return
  creating.value = true
  try {
    const { clientSecret: _clientSecret, ...client } = await api.oauthApplications.createClient(payload)
    clients.value = [client, ...clients.value]
    createModalOpen.value = false
    if (client.clientType === 'confidential') {
      createdClient.value = client
      createdSecretOpen.value = true
    }
    message.success('第三方应用已创建')
  } catch (error) {
    message.error(extractApiErrorMessage(error, '创建第三方应用失败'))
  } finally {
    creating.value = false
  }
}

async function updateClientStatus(client: OAuthClientSummary, enabled: boolean): Promise<void> {
  const status = enabled ? 'active' : 'disabled'
  if (status === client.status) return
  updatingClientIds.value = new Set(updatingClientIds.value).add(client.clientId)
  try {
    const updated = await api.oauthApplications.updateClientStatus(client.clientId, status)
    clients.value = clients.value.map((item) => item.clientId === updated.clientId ? updated : item)
    message.success(status === 'active' ? '第三方应用已启用' : '第三方应用已停用，现有 token 已立即失效')
  } catch (error) {
    message.error(extractApiErrorMessage(error, '更新第三方应用状态失败'))
  } finally {
    const next = new Set(updatingClientIds.value)
    next.delete(client.clientId)
    updatingClientIds.value = next
  }
}

async function reissueClientSecret(client: OAuthClientSummary): Promise<void> {
  reissuingSecretClientIds.value = new Set(reissuingSecretClientIds.value).add(client.clientId)
  try {
    const { clientSecret: _clientSecret, ...updated } = await api.oauthApplications.reissueClientSecret(client.clientId)
    clients.value = clients.value.map((item) => item.clientId === updated.clientId ? updated : item)
    createdClient.value = updated
    createdSecretOpen.value = true
    message.success('新的 Client Secret 已签发，请下载完整对接文档')
  } catch (error) {
    message.error(extractApiErrorMessage(error, '重新签发 Client Secret 失败'))
  } finally {
    const next = new Set(reissuingSecretClientIds.value)
    next.delete(client.clientId)
    reissuingSecretClientIds.value = next
  }
}

async function downloadIntegrationGuide(client: OAuthClientSummary): Promise<void> {
  downloadingClientIds.value = new Set(downloadingClientIds.value).add(client.clientId)
  try {
    const [integration, integrationPackage] = await Promise.all([
      api.oauthApplications.integrationInfo(),
      api.oauthApplications.integrationPackage(client.clientId)
    ])
    const guideClient = integrationPackage.client
    const blob = new Blob([buildOAuthIntegrationGuide({
      client: guideClient,
      integration,
      clientSecret: integrationPackage.clientSecret
    })], { type: 'text/markdown;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = oauthIntegrationGuideFilename(guideClient.clientId)
    document.body.appendChild(link)
    link.click()
    link.remove()
    window.setTimeout(() => URL.revokeObjectURL(url), 0)
    message.success(`“${guideClient.displayName}”的对接文档已下载`)
  } catch (error) {
    message.error(extractApiErrorMessage(error, '下载 OIDC 对接文档失败'))
  } finally {
    const next = new Set(downloadingClientIds.value)
    next.delete(client.clientId)
    downloadingClientIds.value = next
  }
}

function downloadCreatedClientGuide(): void {
  if (!createdClient.value) return
  void downloadIntegrationGuide(createdClient.value)
}

function buildCreatePayload(): OAuthClientCreatePayload | undefined {
  const displayName = createForm.displayName.trim()
  const redirectUris = Array.from(new Set(
    createForm.redirectUrisText.split(/\r?\n/).map((value) => value.trim()).filter(Boolean)
  ))
  const allowedScopes = Array.from(new Set(createForm.allowedScopes))
  if (!displayName) {
    message.error('请输入应用名称')
    return undefined
  }
  if (!redirectUris.length) {
    message.error('请至少填写一个回调地址')
    return undefined
  }
  if (!allowedScopes.length) {
    message.error('请至少选择一个允许申请的 Scope')
    return undefined
  }
  if (allowedScopes.includes('profile') && !allowedScopes.includes('openid')) {
    message.error('基础身份资料需要同时选择 OIDC 登录')
    return undefined
  }
  const missingReadScope = Object.entries(requiredReadScopeByWriteScope).find(([writeScope, readScope]) => (
    allowedScopes.includes(writeScope) && !allowedScopes.includes(readScope)
  ))
  if (missingReadScope) {
    message.error(`${missingReadScope[0]} 需要同时选择 ${missingReadScope[1]}`)
    return undefined
  }
  return { displayName, clientType: createForm.clientType, redirectUris, allowedScopes }
}

function closeCreatedSecret(): void {
  createdSecretOpen.value = false
  createdClient.value = undefined
}

function clientTypeLabel(type: OAuthClientType): string {
  return type === 'confidential' ? '机密 Client' : '公开 Client'
}

function createEmptyClientForm(): {
  displayName: string
  clientType: OAuthClientType
  redirectUrisText: string
  allowedScopes: string[]
} {
  return {
    displayName: '',
    clientType: 'public',
    redirectUrisText: '',
    allowedScopes: ['openid', 'profile']
  }
}
</script>

<style scoped>
.application-name-cell,
.created-secret-content,
.mobile-list-note {
  display: flex;
  min-width: 0;
  flex-direction: column;
}

.application-name-cell {
  gap: 4px;
}

.application-client-id,
.mobile-list-note code {
  overflow: hidden;
  color: #0f766e;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.uri-list,
.scope-list {
  display: flex;
  max-width: 100%;
  flex-wrap: wrap;
  gap: 4px;
}

.uri-list {
  flex-direction: column;
}

.uri-list code {
  overflow: hidden;
  color: #475569;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.scope-list :deep(.ant-tag) {
  margin-inline-end: 0;
  overflow-wrap: anywhere;
  white-space: normal;
}

.form-help-text {
  margin-top: 6px;
  color: #64748b;
  font-size: 12px;
  line-height: 1.5;
}

.created-secret-content {
  gap: 14px;
}

.mobile-list-note > span {
  color: #64748b;
  font-size: 12px;
}

.created-secret-actions {
  display: flex;
  gap: 8px;
  justify-content: flex-end;
}

.mobile-list-actions {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  justify-content: space-between;
  gap: 4px;
}
</style>

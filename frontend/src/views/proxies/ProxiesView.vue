<template>
  <a-card class="page-card responsive-page-card">
    <ResponsiveListToolbar :show-search="false" :show-reset="false" :refresh-loading="loading" @refresh="loadData">
      <template #actions>
        <a-button type="primary" @click="openCreate">新建代理</a-button>
      </template>
    </ResponsiveListToolbar>
    <ResponsiveDataList table-class="page-table proxy-table" :columns="columns" :data-source="proxies" row-key="id" :loading="loading" :scroll-x="1160" pull-refresh-enabled :refreshing="loading" @mobile-refresh="loadData">
      <template #emptyText>
        <a-empty class="page-empty-card" description="先创建代理，再在 OAuth 账户里选择绑定。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'type'">
          <a-tag :color="proxyTypeColor(record.type)">{{ record.type.toUpperCase() }}</a-tag>
        </template>
        <template v-else-if="column.key === 'host'">
          <span class="mono-cell">{{ record.host }}</span>
        </template>
        <template v-else-if="column.key === 'port'">
          <a-tag>{{ record.port }}</a-tag>
        </template>
        <template v-else-if="column.key === 'username'">
          <span :class="record.username ? 'mono-cell' : 'muted-cell'">{{ record.username || '-' }}</span>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-space :size="6">
            <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
            <a-tag :color="testStatusColor(record.testStatus)">{{ testStatusText(record.testStatus) }}</a-tag>
          </a-space>
        </template>
        <template v-else-if="column.key === 'latency'">
          <a-tooltip :title="latencyTooltip(record)">
            <span :class="record.latencyMs === undefined ? 'muted-cell' : 'latency-value'">{{ formatLatency(record.latencyMs) }}</span>
          </a-tooltip>
        </template>
        <template v-else-if="column.key === 'outboundIp'">
          <span :class="record.outboundIp ? 'mono-cell' : 'muted-cell'">{{ record.outboundIp || '-' }}</span>
        </template>
        <template v-else-if="column.key === 'outboundRegion'">
          <span :class="record.outboundRegion ? '' : 'muted-cell'">{{ record.outboundRegion || '-' }}</span>
        </template>
        <template v-else-if="column.key === 'description'">
          <span>{{ record.description || '-' }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <RowActions :actions="proxyActions" @action-click="handleProxyAction($event, record)" />
        </template>
      </template>
      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">{{ record.name }}</div>
            <div class="mobile-list-card-tags">
              <a-tag :color="proxyTypeColor(record.type)">{{ record.type.toUpperCase() }}</a-tag>
              <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
            </div>
          </div>
          <div class="mobile-list-meta-grid">
            <div class="mobile-list-meta-item">
              <span>地址</span>
              <strong class="mono-cell">{{ record.host }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>端口</span>
              <strong>{{ record.port }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>延迟</span>
              <strong>{{ formatLatency(record.latencyMs) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>出口 IP</span>
              <strong :class="record.outboundIp ? 'mono-cell' : 'muted-cell'">{{ record.outboundIp || '-' }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>地区</span>
              <strong :class="record.outboundRegion ? '' : 'muted-cell'">{{ record.outboundRegion || '-' }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>检测</span>
              <strong>
                <a-tag :color="testStatusColor(record.testStatus)">{{ testStatusText(record.testStatus) }}</a-tag>
              </strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>用户</span>
              <strong :class="record.username ? 'mono-cell' : 'muted-cell'">{{ record.username || '-' }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>说明</span>
              <strong>{{ record.description || '-' }}</strong>
            </div>
          </div>
          <div class="mobile-list-card-actions">
            <RowActions variant="button" :actions="proxyActions" @action-click="handleProxyAction($event, record)" />
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <a-modal v-model:open="modalOpen" :title="editingId ? '编辑代理' : '新建代理'" width="720px" :ok-button-props="{ type: 'primary' }" @ok="saveProxy">
      <a-form layout="vertical" class="modal-form" autocomplete="off">
        <a-form-item label="名称" required>
          <a-input v-model:value="form.name" placeholder="例如 OpenAI OAuth 本地代理" />
        </a-form-item>
        <a-row :gutter="16">
          <a-col :span="12">
            <a-form-item label="类型">
              <a-select v-model:value="form.type" :options="typeOptions" />
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item label="端口" required>
              <a-input-number v-model:value="form.port" :min="1" :max="65535" style="width: 100%" />
            </a-form-item>
          </a-col>
        </a-row>
        <a-form-item label="Host" required>
          <a-input v-model:value="form.host" placeholder="127.0.0.1" />
        </a-form-item>
        <a-form-item label="说明">
          <a-textarea v-model:value="form.description" :rows="3" placeholder="可选，填写用途或绑定场景" />
        </a-form-item>
        <a-row :gutter="16">
          <a-col :span="12">
            <a-form-item label="用户名">
              <a-input v-model:value="form.username" placeholder="可选" autocomplete="new-password" name="proxy-username" />
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item label="密码">
              <a-input-password v-model:value="form.password" placeholder="编辑时留空表示不修改" autocomplete="new-password" name="proxy-password" />
            </a-form-item>
          </a-col>
        </a-row>
        <a-form-item label="状态">
          <a-switch v-model:checked="form.enabled" checked-children="启用" un-checked-children="停用" />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal
      v-model:open="testReportOpen"
      title="代理质量检测报告"
      width="680px"
      :footer="null"
      class="proxy-test-modal"
    >
      <template v-if="selectedTestProxy && !testReport">
        <section class="proxy-test-start">
          <h3>{{ selectedTestProxy.name }}</h3>
          <div class="proxy-report-meta">
            <span>检测目标: 供应商默认地址</span>
            <span>当前延迟: {{ formatLatency(selectedTestProxy.latencyMs) }}</span>
            <span>当前状态: {{ testStatusText(selectedTestProxy.testStatus) }}</span>
            <span>最近检测: {{ formatDateTime(selectedTestProxy.lastTestedAt) }}</span>
          </div>
        </section>

        <div class="proxy-report-footer">
          <a-space>
            <a-button :disabled="Boolean(testingProxyId)" @click="testReportOpen = false">关闭</a-button>
            <a-button type="primary" :loading="testingProxyId === selectedTestProxy.id" @click="runProxyTest">开始测试</a-button>
          </a-space>
        </div>
      </template>

      <template v-else-if="testReport">
        <section class="proxy-report-summary">
          <div class="proxy-report-main">
            <h3>{{ testReport.proxyName }}</h3>
            <p>通过 {{ testReport.passedCount }} 项，告警 {{ testReport.warningCount }} 项，失败 {{ testReport.failedCount }} 项</p>
            <div class="proxy-report-meta">
              <span>检测目标: 供应商默认地址</span>
              <span>出口 IP: {{ testReport.outboundIp || '-' }}</span>
              <span>出口地区: {{ testReport.outboundRegion || '-' }}</span>
              <span>基础延迟: {{ formatLatency(testReport.baseLatencyMs) }}</span>
              <span>检测时间: {{ formatDateTime(testReport.testedAt) }}</span>
            </div>
          </div>
          <div class="proxy-score">
            <strong>{{ testReport.score }}</strong>
            <span>等级 {{ testReport.grade }}</span>
          </div>
        </section>

        <a-table
          class="proxy-report-table"
          size="small"
          :columns="reportColumns"
          :data-source="testReport.items"
          :pagination="false"
          row-key="name"
        >
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'status'">
              <a-tag :color="testItemStatusColor(record.status)">{{ testItemStatusText(record.status) }}</a-tag>
            </template>
            <template v-else-if="column.key === 'httpStatus'">
              <span>{{ record.httpStatus ?? '-' }}</span>
            </template>
            <template v-else-if="column.key === 'latencyMs'">
              <span>{{ formatLatency(record.latencyMs) }}</span>
            </template>
            <template v-else-if="column.key === 'message'">
              <span>{{ record.message }}</span>
            </template>
          </template>
        </a-table>

        <div class="proxy-report-footer">
          <a-space>
            <a-button :disabled="Boolean(testingProxyId)" @click="testReportOpen = false">关闭</a-button>
            <a-button type="primary" :loading="testingProxyId === testReport.proxyId" @click="runProxyTest">重新测试</a-button>
          </a-space>
        </div>
      </template>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import axios from 'axios'
import { message } from '@/lib/antd'
import { onMounted, reactive, ref } from 'vue'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { formatDateTime } from '@/shared/formatters'
import type { ProxyProfileSummary, ProxyTestItemStatus, ProxyTestReport } from '@/types/domain'

const loading = ref(false)
const modalOpen = ref(false)
const editingId = ref<string>()
const proxies = ref<ProxyProfileSummary[]>([])
const testingProxyId = ref<string>()
const testReportOpen = ref(false)
const selectedTestProxy = ref<ProxyProfileSummary>()
const testReport = ref<ProxyTestReport>()
const DEFAULT_PROXY_TYPE = 'socks5h'

const form = reactive({ name: '', description: '', type: DEFAULT_PROXY_TYPE, host: '', port: 7890, username: '', password: '', enabled: true })

const columns = [
  { title: '名称', dataIndex: 'name', key: 'name', width: 180 },
  { title: '类型', dataIndex: 'type', key: 'type', width: 100 },
  { title: '地址', dataIndex: 'host', key: 'host', width: 140 },
  { title: '端口', dataIndex: 'port', key: 'port', width: 80 },
  { title: '用户', dataIndex: 'username', key: 'username', width: 130 },
  { title: '状态', key: 'status', width: 160 },
  { title: '延迟', key: 'latency', width: 100 },
  { title: '出口 IP', key: 'outboundIp', width: 140 },
  { title: '地区', key: 'outboundRegion', width: 100 },
  { title: '说明', dataIndex: 'description', key: 'description', width: 200 },
  { title: '操作', key: 'actions', width: 110, fixed: 'right', customRender: () => '' }
]

const proxyActions: RowActionItem[] = [
  { key: 'test', label: '测试', icon: 'test', tone: 'info' },
  { key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' },
  {
    key: 'delete',
    label: '删除',
    icon: 'delete',
    tone: 'danger',
    confirmTitle: '确认删除这个代理？',
    confirmOkText: '删除'
  }
]

const reportColumns = [
  { title: '检测项', dataIndex: 'name', key: 'name', width: 120 },
  { title: '状态', dataIndex: 'status', key: 'status', width: 90 },
  { title: 'HTTP', dataIndex: 'httpStatus', key: 'httpStatus', width: 80 },
  { title: '延迟', dataIndex: 'latencyMs', key: 'latencyMs', width: 90 },
  { title: '说明', dataIndex: 'message', key: 'message' }
]

const typeOptions = [
  { label: 'HTTP', value: 'http' },
  { label: 'HTTPS', value: 'https' },
  { label: 'SOCKS5', value: 'socks5' },
  { label: 'SOCKS5H', value: 'socks5h' }
]

function proxyTypeColor(type: string) {
  if (type === 'socks5' || type === 'socks5h') return 'purple'
  if (type === 'https') return 'green'
  return 'blue'
}

function testStatusColor(status: string) {
  if (status === 'passed') return 'green'
  if (status === 'warning') return 'gold'
  if (status === 'failed') return 'red'
  return 'default'
}

function testStatusText(status: string) {
  if (status === 'passed') return '检测通过'
  if (status === 'warning') return '有告警'
  if (status === 'failed') return '检测失败'
  return '未检测'
}

function testItemStatusColor(status: ProxyTestItemStatus) {
  return status === 'passed' ? 'green' : status === 'warning' ? 'gold' : 'red'
}

function testItemStatusText(status: ProxyTestItemStatus) {
  return status === 'passed' ? '通过' : status === 'warning' ? '告警' : '失败'
}

function formatLatency(value?: number) {
  return value === undefined ? '-' : `${Math.round(value)}ms`
}

function latencyTooltip(proxy: ProxyProfileSummary) {
  const parts = [
    proxy.lastTestMessage || testStatusText(proxy.testStatus),
    proxy.lastTestedAt ? `检测时间：${formatDateTime(proxy.lastTestedAt)}` : ''
  ].filter(Boolean)
  return parts.join('\n') || '尚未检测'
}

async function loadData() {
  loading.value = true
  try {
    proxies.value = await api.proxies.list()
  } catch (error) {
    console.error(error)
    message.error('加载代理失败')
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = undefined
  Object.assign(form, { name: '', description: '', type: DEFAULT_PROXY_TYPE, host: '', port: 7890, username: '', password: '', enabled: true })
  modalOpen.value = true
}

function openEdit(proxy: ProxyProfileSummary) {
  editingId.value = proxy.id
  Object.assign(form, { name: proxy.name, description: proxy.description ?? '', type: proxy.type, host: proxy.host, port: proxy.port, username: proxy.username ?? '', password: '', enabled: proxy.enabled })
  modalOpen.value = true
}

function openTestReport(proxy: ProxyProfileSummary) {
  selectedTestProxy.value = proxy
  testReport.value = undefined
  testReportOpen.value = true
}

function handleProxyAction(key: string, proxy: ProxyProfileSummary) {
  if (key === 'test') {
    openTestReport(proxy)
    return
  }
  if (key === 'edit') {
    openEdit(proxy)
    return
  }
  if (key === 'delete') {
    void removeProxy(proxy.id)
  }
}

async function saveProxy() {
  if (!form.name.trim() || !form.host.trim() || !form.port) {
    message.warning('请填写代理名称、Host 和端口')
    return
  }
  try {
    if (editingId.value) {
      await api.proxies.update(editingId.value, { ...form })
      message.success('代理已更新')
    } else {
      await api.proxies.create({ ...form })
      message.success('代理已创建')
    }
    modalOpen.value = false
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存代理失败'))
  }
}

async function runProxyTest() {
  const id = selectedTestProxy.value?.id ?? testReport.value?.proxyId
  if (!id) return
  testingProxyId.value = id
  try {
    testReport.value = await api.proxies.test(id)
    testReportOpen.value = true
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '代理检测失败'))
  } finally {
    testingProxyId.value = undefined
  }
}

async function removeProxy(id: string) {
  try {
    await api.proxies.delete(id)
    message.success('代理已删除')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '删除代理失败'))
  }
}

function extractApiErrorMessage(error: unknown, fallback: string): string {
  if (axios.isAxiosError<{ message?: string }>(error)) {
    return error.response?.data?.message ?? fallback
  }
  return error instanceof Error ? error.message : fallback
}

onMounted(loadData)
</script>

<style scoped>
.proxy-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.proxy-table :deep(.ant-empty) {
  margin: 12px 0;
}

.latency-value {
  color: #0f172a;
  font-variant-numeric: tabular-nums;
}

.mobile-list-card :deep(.mobile-list-meta-item strong) {
  font-weight: 400;
}

.proxy-report-summary {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 20px;
  margin-bottom: 20px;
  padding: 22px;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  background: #f8fafc;
}

.proxy-report-main {
  min-width: 0;
}

.proxy-report-main h3 {
  margin: 0 0 8px;
  color: #0f172a;
  font-size: 16px;
  font-weight: 700;
}

.proxy-report-main p {
  margin: 0 0 14px;
  color: #475569;
  font-size: 14px;
}

.proxy-report-meta {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px 28px;
  color: #475569;
  font-size: 13px;
}

.proxy-report-meta span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.proxy-score {
  display: flex;
  flex: 0 0 auto;
  flex-direction: column;
  align-items: center;
  min-width: 74px;
  color: #0f172a;
}

.proxy-score strong {
  font-size: 32px;
  line-height: 1;
}

.proxy-score span {
  margin-top: 6px;
  color: #64748b;
  font-size: 13px;
}

.proxy-report-table {
  margin-top: 8px;
}

.proxy-report-table :deep(.ant-table-cell) {
  white-space: normal;
}

.proxy-report-footer {
  display: flex;
  justify-content: flex-end;
  margin-top: 20px;
}

@media (max-width: 640px) {
  .proxy-report-summary {
    flex-direction: column;
  }

  .proxy-report-meta {
    grid-template-columns: 1fr;
  }
}

</style>

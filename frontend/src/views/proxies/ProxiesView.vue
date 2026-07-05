<template>
  <a-card class="page-card responsive-page-card">
    <ResponsiveListToolbar v-model:keyword="keyword" search-placeholder="搜索代理名称" :show-reset="Boolean(keyword.trim())" :refresh-loading="loading" @search="searchProxies" @reset="resetSearch" @refresh="loadData">
      <template #actions>
        <a-button type="primary" @click="openCreate">新建代理</a-button>
      </template>
    </ResponsiveListToolbar>
    <ResponsiveDataList table-class="page-table proxy-table" :columns="proxyColumns" :data-source="proxies" row-key="id" :loading="loading" :loading-more="mobileLoadingMore" :mobile-has-more="mobileHasMore" :pagination="tablePagination" :scroll-x="1160" mobile-pagination pull-refresh-enabled :refreshing="loading" @change="handleTableChange" @mobile-load-more="loadMoreMobileProxies" @mobile-refresh="refreshMobileProxies">
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

    <a-modal v-model:open="modalOpen" :title="editingId ? '编辑代理' : '新建代理'" width="720px" :confirm-loading="proxySaving" :ok-button-props="{ type: 'primary', disabled: proxySaving }" @ok="saveProxy">
      <a-form layout="vertical" class="modal-form" autocomplete="off">
        <a-form-item label="名称" required>
          <a-input v-model:value="form.name" placeholder="例如 OpenAI OAuth 本地代理" />
        </a-form-item>
        <a-row :gutter="16">
          <a-col :span="12">
            <a-form-item label="类型">
              <a-select v-model:value="form.type" :options="proxyTypeOptions" />
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

    <ProxyTestReportModal
      v-model:open="testReportOpen"
      :report="testReport"
      :selected-proxy="selectedTestProxy"
      :testing-proxy-id="testingProxyId"
      @run-test="runProxyTest"
    />
  </a-card>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { onMounted, reactive, ref, watch } from 'vue'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useSubmitAction } from '@/composables/useSubmitAction'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatNumber } from '@/shared/formatters'
import { sanitizePaginationState, stringOrFallback, type PagePaginationState } from '@/shared/pageStateSanitizers'
import type { ProxyProfileSummary, ProxyTestReport } from '@/types/domain'
import ProxyTestReportModal from './ProxyTestReportModal.vue'
import {
  formatLatency,
  latencyTooltip,
  proxyActions,
  proxyColumns,
  proxyTypeColor,
  proxyTypeOptions,
  testStatusColor,
  testStatusText
} from './proxyDisplay'

interface ProxiesPageState {
  keyword: string
  pagination: PagePaginationState
}

const pageSize = 20
const pageStateCache = usePageStateCache<ProxiesPageState>(undefined, defaultProxiesPageState, {
  sanitize: sanitizeProxiesPageState,
  version: 1
})
const initialPageState = pageStateCache.read()
const modalOpen = ref(false)
const editingId = ref<string>()
const { submitAction, submittingRef } = useSubmitAction('proxies')
const proxySaving = submittingRef('proxies.save')
const keyword = ref(initialPageState.keyword)
const testingProxyId = ref<string>()
const testReportOpen = ref(false)
const selectedTestProxy = ref<ProxyProfileSummary>()
const testReport = ref<ProxyTestReport>()
const DEFAULT_PROXY_TYPE = 'socks5h'

const form = reactive({ name: '', description: '', type: DEFAULT_PROXY_TYPE, host: '', port: 7890, username: '', password: '', enabled: true })

function proxySavePayload(): Record<string, unknown> {
  const payload: Record<string, unknown> = { ...form }
  if (!form.password.trim()) {
    delete payload.password
  }
  return payload
}

const {
  items: proxies,
  loading,
  mobileHasMore,
  mobileLoadingMore,
  pagination,
  tablePagination,
  handleTableChange,
  loadData,
  loadMoreMobile: loadMoreMobileProxies,
  removeItems: removeProxyItems,
  refreshMobile: refreshMobileProxies,
  resetPagination,
  updateItems: updateProxyItems
} = useResponsivePagedList<ProxyProfileSummary>({
  pageSize,
  initialPagination: initialPageState.pagination,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${formatNumber(range?.[1] ?? total - 1)} 个代理，还有更多`
    : `共 ${formatNumber(total)} 个代理`,
  fetchPage: (_options, pageState) => api.proxies.list({
    keyword: keyword.value.trim() || undefined,
    page: pageState.current,
    pageSize: pageState.pageSize
  }),
  onError: (error) => {
    console.error(error)
    message.error('加载代理失败')
  }
})

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

const saveProxy = submitAction('proxies.save', async () => {
  if (!form.name.trim() || !form.host.trim() || !form.port) {
    message.warning('请填写代理名称、Host 和端口')
    return
  }
  try {
    const targetId = editingId.value
    const payload = proxySavePayload()
    if (targetId) {
      const updated = await api.proxies.update(targetId, payload)
      updateProxyItems((item) => item.id === targetId, () => updated)
      message.success('代理已更新')
      void loadData({ quiet: true })
    } else {
      await api.proxies.create(payload)
      message.success('代理已创建')
      resetPagination()
      await loadData()
    }
    modalOpen.value = false
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存代理失败'))
  }
})

async function runProxyTest() {
  const id = selectedTestProxy.value?.id ?? testReport.value?.proxyId
  if (!id) return
  testingProxyId.value = id
  try {
    testReport.value = await api.proxies.test(id)
    testReportOpen.value = true
    void loadData({ quiet: true })
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
    removeProxyItems((item) => item.id === id)
    message.success('代理已删除')
    void loadData({ quiet: true })
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '删除代理失败'))
  }
}

function searchProxies() {
  resetPagination()
  void loadData()
}

function resetSearch() {
  keyword.value = ''
  pageStateCache.clear()
  searchProxies()
}

function defaultProxiesPageState(): ProxiesPageState {
  return {
    keyword: '',
    pagination: { current: 1, pageSize }
  }
}

function sanitizeProxiesPageState(value: unknown, fallback: ProxiesPageState): ProxiesPageState {
  const source = value && typeof value === 'object' ? value as Partial<ProxiesPageState> : {}
  return {
    keyword: stringOrFallback(source.keyword, fallback.keyword),
    pagination: sanitizePaginationState(source.pagination, fallback.pagination)
  }
}

function snapshotPageState(): ProxiesPageState {
  return {
    keyword: keyword.value,
    pagination: { current: pagination.current, pageSize: pagination.pageSize }
  }
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })

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

</style>

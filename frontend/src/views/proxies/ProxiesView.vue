<template>
  <a-card class="page-card responsive-page-card">
    <ResponsiveListToolbar :show-search="false" :show-reset="false" :refresh-loading="loading" @refresh="loadData">
      <template #actions>
        <a-button type="primary" @click="openCreate">新建代理</a-button>
      </template>
    </ResponsiveListToolbar>
    <ResponsiveDataList table-class="page-table proxy-table" :columns="columns" :data-source="proxies" row-key="id" :loading="loading" :scroll-x="1120" pull-refresh-enabled :refreshing="loading" @mobile-refresh="loadData">
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
          <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'actions'">
          <a-space class="row-actions" :size="8">
            <a-button type="link" size="small" @click="openEdit(record)">编辑</a-button>
            <a-popconfirm title="确认删除这个代理？" @confirm="removeProxy(record.id)">
              <a-button type="link" size="small" danger>删除</a-button>
            </a-popconfirm>
          </a-space>
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
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>用户</span>
              <strong :class="record.username ? 'mono-cell' : 'muted-cell'">{{ record.username || '-' }}</strong>
            </div>
          </div>
          <div class="mobile-list-card-actions two-actions">
            <a-button type="primary" @click="openEdit(record)">编辑</a-button>
            <a-popconfirm title="确认删除这个代理？" @confirm="removeProxy(record.id)">
              <a-button danger>删除</a-button>
            </a-popconfirm>
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <a-modal v-model:open="modalOpen" :title="editingId ? '编辑代理' : '新建代理'" width="720px" :ok-button-props="{ type: 'primary' }" @ok="saveProxy">
      <a-form layout="vertical" class="modal-form">
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
        <a-row :gutter="16">
          <a-col :span="12">
            <a-form-item label="用户名">
              <a-input v-model:value="form.username" placeholder="可选" />
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item label="密码">
              <a-input-password v-model:value="form.password" placeholder="编辑时留空表示不修改" />
            </a-form-item>
          </a-col>
        </a-row>
        <a-form-item label="状态">
          <a-switch v-model:checked="form.enabled" checked-children="启用" un-checked-children="停用" />
        </a-form-item>
      </a-form>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { message } from 'ant-design-vue'
import { onMounted, reactive, ref } from 'vue'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import type { ProxyProfileSummary } from '@/types/domain'

const loading = ref(false)
const modalOpen = ref(false)
const editingId = ref<string>()
const proxies = ref<ProxyProfileSummary[]>([])
const form = reactive({ name: '', type: 'http', host: '', port: 7890, username: '', password: '', enabled: true })

const columns = [
  { title: '名称', dataIndex: 'name', key: 'name', width: 240 },
  { title: '类型', dataIndex: 'type', key: 'type', width: 110 },
  { title: '地址', dataIndex: 'host', key: 'host', width: 200 },
  { title: '端口', dataIndex: 'port', key: 'port', width: 90 },
  { title: '用户', dataIndex: 'username', key: 'username', width: 160 },
  { title: '状态', key: 'status', width: 100 },
  { title: '操作', key: 'actions', width: 140, fixed: 'right' }
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
  Object.assign(form, { name: '', type: 'http', host: '', port: 7890, username: '', password: '', enabled: true })
  modalOpen.value = true
}

function openEdit(proxy: ProxyProfileSummary) {
  editingId.value = proxy.id
  Object.assign(form, { name: proxy.name, type: proxy.type, host: proxy.host, port: proxy.port, username: proxy.username ?? '', password: '', enabled: proxy.enabled })
  modalOpen.value = true
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
    message.error('保存代理失败')
  }
}

async function removeProxy(id: string) {
  try {
    await api.proxies.delete(id)
    message.success('代理已删除')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('删除代理失败')
  }
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

</style>

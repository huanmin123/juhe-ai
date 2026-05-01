<template>
  <a-card class="page-card" title="供应商">
    <a-alert class="provider-alert" message="第一期只启用 OpenAI，账户创建方式支持 OAuth 和 API Key。" type="info" show-icon />
    <a-table class="page-table provider-table" size="middle" :columns="columns" :data-source="providers" row-key="code" :loading="loading" :pagination="false" :scroll="{ x: 1080 }">
      <template #emptyText>
        <a-empty class="page-empty-card" description="当前仅内置 OpenAI 供应商，后续新供应商会在这里扩展。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'code'">
          <span class="mono-cell">{{ record.code }}</span>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'accountTypes'">
          <a-space wrap>
            <a-tag v-for="type in record.accountTypes" :key="type" color="processing">{{ type }}</a-tag>
          </a-space>
        </template>
        <template v-else-if="column.key === 'capabilities'">
          <a-space wrap>
            <a-tag v-for="capability in record.capabilities" :key="capability" color="blue">{{ capability }}</a-tag>
          </a-space>
        </template>
        <template v-else-if="column.key === 'baseUrl'">
          <span class="mono-cell">{{ record.baseUrl }}</span>
        </template>
      </template>
    </a-table>
  </a-card>
</template>

<script setup lang="ts">
import { message } from 'ant-design-vue'
import { onMounted, ref } from 'vue'

import { api } from '@/api/client'
import type { ProviderDefinition } from '@/types/domain'

const loading = ref(false)
const providers = ref<ProviderDefinition[]>([])

const columns = [
  { title: '编码', dataIndex: 'code', key: 'code', width: 120 },
  { title: '名称', dataIndex: 'name', key: 'name', width: 160 },
  { title: '状态', key: 'status', width: 90 },
  { title: '账户类型', key: 'accountTypes', width: 180 },
  { title: '能力', key: 'capabilities', width: 360 },
  { title: '默认 Base URL', dataIndex: 'baseUrl', key: 'baseUrl', width: 260 }
]

async function loadProviders() {
  loading.value = true
  try {
    providers.value = await api.providers.list()
  } catch (error) {
    console.error(error)
    message.error('加载供应商失败')
  } finally {
    loading.value = false
  }
}

onMounted(loadProviders)
</script>

<style scoped>
.provider-alert {
  margin-bottom: 18px;
  border-radius: 10px;
}

.provider-table :deep(.ant-empty) {
  margin: 12px 0;
}

.provider-table :deep(.ant-table-cell) {
  white-space: nowrap;
}
</style>

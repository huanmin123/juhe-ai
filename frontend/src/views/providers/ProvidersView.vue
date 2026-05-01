<template>
  <a-card class="page-card" title="供应商">
    <a-alert class="provider-alert" message="第一期只启用 OpenAI，账户创建方式支持 OAuth 和 API Key；模型价格来自供应商模型目录，网关计费复用同一份数据。" type="info" show-icon />
    <a-table class="page-table provider-table" size="middle" :columns="columns" :data-source="providers" row-key="code" :loading="loading" :pagination="false" :scroll="{ x: 1200 }">
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
        <template v-else-if="column.key === 'actions'">
          <a-button type="link" size="small" @click="openModelModal(record)">查看模型</a-button>
        </template>
      </template>
    </a-table>

    <a-modal v-model:open="modelModalOpen" :title="modelModalTitle" width="1180px" :footer="null" @cancel="resetModelModal">
      <div class="model-toolbar">
        <a-input-search v-model:value="modelKeyword" allow-clear placeholder="搜索模型名称或类型" class="model-search" />
        <a-space wrap>
          <a-tag color="blue">{{ filteredModels.length }} / {{ providerModels.length }} 个模型</a-tag>
          <a-tag color="purple">价格单位：USD / 1M tokens</a-tag>
        </a-space>
      </div>
      <a-table
        class="model-table"
        size="small"
        :columns="modelColumns"
        :data-source="filteredModels"
        row-key="model"
        :loading="modelLoading"
        :pagination="{ pageSize: 12, showSizeChanger: true }"
        :scroll="{ x: 1360 }"
      >
        <template #emptyText>
          <a-empty class="page-empty-card" description="这个供应商暂未配置模型价格。" />
        </template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'model'">
            <span class="mono-cell">{{ record.model }}</span>
          </template>
          <template v-else-if="column.key === 'releaseDate'">
            <span>{{ record.releaseDate || '-' }}</span>
          </template>
          <template v-else-if="column.key === 'mode'">
            <a-tag>{{ record.mode || '-' }}</a-tag>
          </template>
          <template v-else-if="column.key === 'prices'">
            <div class="price-cell">
              <span>输入 {{ formatPrice(record.inputUsdPer1M) }}</span>
              <span>输出 {{ formatPrice(record.outputUsdPer1M) }}</span>
              <span>缓存读 {{ formatPrice(record.cachedInputUsdPer1M) }}</span>
            </div>
          </template>
          <template v-else-if="column.key === 'cacheWrite'">
            <div class="price-cell">
              <span>写入 {{ formatPrice(record.cacheWriteUsdPer1M) }}</span>
              <span>1h {{ formatPrice(record.cacheWrite1hUsdPer1M) }}</span>
            </div>
          </template>
          <template v-else-if="column.key === 'image'">
            <div class="price-cell">
              <span>图片 token {{ formatPrice(record.imageOutputUsdPer1M) }}</span>
              <span>每张 {{ formatUnitPrice(record.outputUsdPerImage) }}</span>
            </div>
          </template>
          <template v-else-if="column.key === 'context'">
            <div class="price-cell">
              <span>输入 {{ formatTokens(record.maxInputTokens) }}</span>
              <span>输出 {{ formatTokens(record.maxOutputTokens) }}</span>
            </div>
          </template>
          <template v-else-if="column.key === 'features'">
            <a-space wrap>
              <a-tag v-if="record.supportsPromptCaching" color="green">缓存</a-tag>
              <a-tag v-if="record.supportsServiceTier" color="gold">service tier</a-tag>
              <span v-if="!record.supportsPromptCaching && !record.supportsServiceTier" class="muted-text">-</span>
            </a-space>
          </template>
        </template>
      </a-table>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { message } from 'ant-design-vue'
import { computed, onMounted, ref } from 'vue'

import { api } from '@/api/client'
import type { ProviderDefinition, ProviderModelPricing } from '@/types/domain'

const loading = ref(false)
const modelLoading = ref(false)
const providers = ref<ProviderDefinition[]>([])
const providerModels = ref<ProviderModelPricing[]>([])
const modelKeyword = ref('')
const modelModalOpen = ref(false)
const activeProvider = ref<ProviderDefinition | null>(null)

const columns = [
  { title: '编码', dataIndex: 'code', key: 'code', width: 120 },
  { title: '名称', dataIndex: 'name', key: 'name', width: 160 },
  { title: '状态', key: 'status', width: 90 },
  { title: '账户类型', key: 'accountTypes', width: 180 },
  { title: '能力', key: 'capabilities', width: 360 },
  { title: '默认 Base URL', dataIndex: 'baseUrl', key: 'baseUrl', width: 260 },
  { title: '操作', key: 'actions', fixed: 'right', width: 120 }
]

const modelColumns = [
  { title: '模型', key: 'model', width: 260 },
  { title: '版本日期', key: 'releaseDate', width: 120 },
  { title: '类型', key: 'mode', width: 110 },
  { title: 'Token 价格', key: 'prices', width: 230 },
  { title: '缓存写入', key: 'cacheWrite', width: 180 },
  { title: '图片价格', key: 'image', width: 210 },
  { title: '上下文', key: 'context', width: 180 },
  { title: '能力', key: 'features', width: 180 }
]

const modelModalTitle = computed(() => activeProvider.value ? `${activeProvider.value.name} 模型价格` : '模型价格')

const filteredModels = computed(() => {
  const keyword = modelKeyword.value.trim().toLowerCase()
  if (!keyword) return providerModels.value
  return providerModels.value.filter((item) => {
    return item.model.toLowerCase().includes(keyword) || (item.mode ?? '').toLowerCase().includes(keyword)
  })
})

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

async function openModelModal(provider: ProviderDefinition) {
  activeProvider.value = provider
  modelModalOpen.value = true
  modelKeyword.value = ''
  modelLoading.value = true
  try {
    providerModels.value = await api.providers.models(provider.code)
  } catch (error) {
    console.error(error)
    providerModels.value = []
    message.error('加载模型价格失败')
  } finally {
    modelLoading.value = false
  }
}

function resetModelModal() {
  activeProvider.value = null
  modelKeyword.value = ''
  providerModels.value = []
}

function formatPrice(value?: number) {
  return typeof value === 'number' ? `$${trimNumber(value)}` : '-'
}

function formatUnitPrice(value?: number) {
  return typeof value === 'number' ? `$${trimNumber(value)}` : '-'
}

function formatTokens(value?: number) {
  if (typeof value !== 'number') return '-'
  if (value >= 1_000_000) return `${trimNumber(value / 1_000_000)}M`
  if (value >= 1_000) return `${trimNumber(value / 1_000)}K`
  return String(value)
}

function trimNumber(value: number) {
  return Number(value.toFixed(8)).toString()
}

onMounted(loadProviders)
</script>

<style scoped>
.provider-alert {
  margin-bottom: 18px;
  border-radius: 10px;
}

.provider-table :deep(.ant-empty),
.model-table :deep(.ant-empty) {
  margin: 12px 0;
}

.provider-table :deep(.ant-table-cell),
.model-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.model-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 16px;
}

.model-search {
  width: 320px;
}

.price-cell {
  display: flex;
  flex-direction: column;
  gap: 2px;
  line-height: 1.5;
}

.muted-text {
  color: rgba(0, 0, 0, 0.45);
}

@media (max-width: 768px) {
  .model-toolbar {
    align-items: stretch;
    flex-direction: column;
  }

  .model-search {
    width: 100%;
  }
}
</style>

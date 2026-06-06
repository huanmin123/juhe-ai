<template>
  <a-card class="page-card responsive-page-card">
    <ResponsiveListToolbar :show-search="false" :show-reset="false" :refresh-loading="loading" @refresh="loadProviders" />
    <ResponsiveDataList table-class="page-table provider-table" :columns="columns" :data-source="providers" row-key="code" :loading="loading" :scroll-x="1320" pull-refresh-enabled :refreshing="loading" @mobile-refresh="loadProviders">
      <template #emptyText>
        <a-empty class="page-empty-card" description="当前仅内置 OpenAI 供应商，后续新供应商会在这里扩展。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'status'">
          <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'accountTypes'">
          <a-space wrap>
            <a-tag v-for="type in record.accountTypes" :key="type" color="processing">{{ type }}</a-tag>
          </a-space>
        </template>
        <template v-else-if="column.key === 'capabilities'">
          <a-space wrap>
            <a-tag v-for="capability in visibleProviderCapabilities(record.capabilities)" :key="capability" color="blue">{{ formatProviderCapability(capability) }}</a-tag>
            <span v-if="!visibleProviderCapabilities(record.capabilities).length" class="muted-text">-</span>
          </a-space>
        </template>
        <template v-else-if="column.key === 'description'">
          <span>{{ record.description || '-' }}</span>
        </template>
        <template v-else-if="column.key === 'baseUrl'">
          <span class="mono-cell">{{ record.baseUrl }}</span>
        </template>
        <template v-else-if="column.key === 'defaultTestModel'">
          <span class="mono-cell">{{ record.defaultTestModel }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <RowActions :actions="providerActions" @action-click="handleProviderAction($event, record)" />
        </template>
      </template>
      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">{{ record.name }}</div>
            <div class="mobile-list-card-tags">
              <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
            </div>
          </div>
          <div class="mobile-list-meta-grid">
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>账户类型</span>
              <strong>{{ record.accountTypes.join(' / ') }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>能力</span>
              <strong>{{ formatCapabilitiesSummary(record.capabilities) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>说明</span>
              <strong>{{ record.description || '-' }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>默认 Base URL</span>
              <strong class="mono-cell">{{ record.baseUrl }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>默认测试模型</span>
              <strong class="mono-cell">{{ record.defaultTestModel }}</strong>
            </div>
          </div>
          <div class="mobile-list-card-actions">
            <RowActions variant="button" :actions="providerActions" @action-click="handleProviderAction($event, record)" />
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <a-modal v-model:open="modelModalOpen" :title="modelModalTitle" width="1180px" wrap-class-name="model-price-modal-wrap" :footer="null" @cancel="resetModelModal">
      <div class="model-modal-content">
        <div class="model-toolbar">
          <a-input-search v-model:value="modelKeyword" allow-clear placeholder="搜索模型名称、用途或接口协议" class="model-search" />
          <a-space wrap>
            <a-tag color="blue">{{ filteredModels.length }} / {{ currentCategoryModels.length }} 个模型</a-tag>
            <a-tag color="purple">价格单位：USD / 1M tokens</a-tag>
          </a-space>
        </div>
        <a-tabs v-model:activeKey="selectedModelCategory" class="model-tabs" size="small">
          <a-tab-pane v-for="tab in modelCategoryTabs" :key="tab.key" :tab="tab.label" />
        </a-tabs>
        <ResponsiveDataList
          class="model-table"
          table-class="model-table"
          size="small"
          :columns="modelColumns"
          :data-source="filteredModels"
          row-key="model"
          :loading="modelLoading"
          :pagination="{ pageSize: 20, hideOnSinglePage: true, showSizeChanger: false }"
          :scroll-x="1500"
          :lock-body-scroll="false"
        >
          <template #emptyText>
            <a-empty class="page-empty-card" description="这个供应商暂未配置模型价格。" />
          </template>
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'model'">
              <a-space size="small">
                <span class="mono-cell">{{ record.model }}</span>
                <a-tag v-if="record.shutdownDate" color="orange">将停用 {{ record.shutdownDate }}</a-tag>
              </a-space>
            </template>
            <template v-else-if="column.key === 'releaseDate'">
              <span>{{ record.releaseDate || '-' }}</span>
            </template>
            <template v-else-if="column.key === 'category'">
              <a-tag>{{ formatModelCategory(record) }}</a-tag>
            </template>
            <template v-else-if="column.key === 'protocols'">
              <a-space wrap size="small">
                <a-tag v-for="protocol in record.supportedApiProtocols" :key="protocol" :color="getApiProtocolTagColor(protocol)">{{ formatApiProtocol(protocol) }}</a-tag>
                <span v-if="!record.supportedApiProtocols?.length" class="muted-text">-</span>
              </a-space>
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
            <template v-else-if="column.key === 'imageTokenPrice'">
              <div class="price-cell">
                <span>输入 {{ formatPrice(record.imageInputUsdPer1M) }}</span>
                <span>输出 {{ formatPrice(record.imageOutputUsdPer1M) }}</span>
              </div>
            </template>
            <template v-else-if="column.key === 'imageUnitPrice'">
              <span>{{ formatUnitPrice(record.outputUsdPerImage) }}</span>
            </template>
            <template v-else-if="column.key === 'context'">
              <div class="price-cell">
                <span>输入 {{ formatTokens(record.maxInputTokens) }}</span>
                <span>输出 {{ formatTokens(record.maxOutputTokens) }}</span>
              </div>
            </template>
          </template>
          <template #card="{ record }">
            <article class="model-mobile-card">
              <div class="model-mobile-card-head">
                <strong class="mono-cell">{{ record.model }}</strong>
                <a-tag>{{ formatModelCategory(record) }}</a-tag>
              </div>
              <div class="model-mobile-card-grid">
                <span>发布时间</span>
                <strong>{{ record.releaseDate || '-' }}</strong>
                <span>接口协议</span>
                <strong>{{ (record.supportedApiProtocols ?? []).map(formatApiProtocol).join(' / ') || '-' }}</strong>
                <span>上下文</span>
                <strong>{{ formatTokens(record.maxInputTokens) }} / {{ formatTokens(record.maxOutputTokens) }}</strong>
              </div>
            </article>
          </template>
        </ResponsiveDataList>
      </div>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { computed, onMounted, ref } from 'vue'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import type { ProviderDefinition, ProviderModelPricing } from '@/types/domain'

const modelCategoryOrder = ['text', 'image', 'audio', 'other'] as const
type ModelCategoryKey = typeof modelCategoryOrder[number]

const loading = ref(false)
const modelLoading = ref(false)
const providers = ref<ProviderDefinition[]>([])
const providerModels = ref<ProviderModelPricing[]>([])
const modelKeyword = ref('')
const selectedModelCategory = ref<ModelCategoryKey>('text')
const modelModalOpen = ref(false)
const activeProvider = ref<ProviderDefinition | null>(null)

const modelCategoryLabels: Record<ModelCategoryKey, string> = {
  text: '对话 / 编码',
  image: '图像',
  audio: '音频',
  other: '其他'
}

const apiProtocolLabels: Record<string, string> = {
  chat_completions: 'Chat Completions',
  responses: 'Responses',
  completions: 'Completions',
  images: 'Images API',
  audio: 'Audio API',
  realtime: 'Realtime API'
}

const hiddenProviderCapabilities = new Set(['passthrough'])

const providerCapabilityLabels: Record<string, string> = {
  models: '模型',
  responses: 'Responses',
  stream: '流式'
}

const columns = [
  { title: '名称', dataIndex: 'name', key: 'name', width: 160 },
  { title: '状态', key: 'status', width: 90 },
  { title: '账户类型', key: 'accountTypes', width: 180 },
  { title: '能力', key: 'capabilities', width: 360 },
  { title: '默认 Base URL', dataIndex: 'baseUrl', key: 'baseUrl', width: 240 },
  { title: '默认测试模型', dataIndex: 'defaultTestModel', key: 'defaultTestModel', width: 160 },
  { title: '说明', dataIndex: 'description', key: 'description', width: 200 },
  { title: '操作', key: 'actions', fixed: 'right' }
]

const providerActions: RowActionItem[] = [
  { key: 'models', label: '查看模型', icon: 'detail', tone: 'info' }
]

const baseModelColumns = [
  { title: '模型', key: 'model', width: 260 },
  { title: '发布时间', key: 'releaseDate', width: 120 },
  { title: '用途', key: 'category', width: 120 },
  { title: '接口协议', key: 'protocols', width: 230 },
  { title: 'Token 价格', key: 'prices', width: 230 },
  { title: '缓存写入', key: 'cacheWrite', width: 180 },
  { title: '图片 token 价格', key: 'imageTokenPrice', width: 180 },
  { title: '每张价格', key: 'imageUnitPrice', width: 130 },
  { title: '上下文', key: 'context', width: 180 }
]

const currentCategoryModels = computed(() => {
  const category = selectedModelCategory.value
  return providerModels.value.filter((item) => getModelCategory(item) === category)
})

const modelColumns = computed(() => {
  const rows = currentCategoryModels.value
  const visibleKeys = new Set(['model', 'releaseDate', 'category', 'protocols'])

  if (rows.some((item) => hasAnyNumber(item.inputUsdPer1M, item.outputUsdPer1M, item.cachedInputUsdPer1M))) {
    visibleKeys.add('prices')
  }
  if (rows.some((item) => hasAnyNumber(item.cacheWriteUsdPer1M, item.cacheWrite1hUsdPer1M))) {
    visibleKeys.add('cacheWrite')
  }
  if (rows.some((item) => hasAnyNumber(item.imageInputUsdPer1M, item.imageOutputUsdPer1M))) {
    visibleKeys.add('imageTokenPrice')
  }
  if (rows.some((item) => typeof item.outputUsdPerImage === 'number')) {
    visibleKeys.add('imageUnitPrice')
  }
  if (rows.some((item) => hasAnyNumber(item.maxInputTokens, item.maxOutputTokens))) {
    visibleKeys.add('context')
  }

  return baseModelColumns.filter((column) => visibleKeys.has(column.key))
})

const modelModalTitle = computed(() => activeProvider.value ? `${activeProvider.value.name} 模型价格` : '模型价格')

const modelCategoryTabs = computed(() => {
  const counts = new Map<ModelCategoryKey, number>()
  for (const key of modelCategoryOrder) {
    counts.set(key, 0)
  }

  for (const item of providerModels.value) {
    const key = getModelCategory(item)
    counts.set(key, (counts.get(key) ?? 0) + 1)
  }

  return modelCategoryOrder
    .filter((key) => (counts.get(key) ?? 0) > 0)
    .map((key) => ({
      key,
      label: `${modelCategoryLabels[key]} (${counts.get(key) ?? 0})`
    }))
})

const filteredModels = computed(() => {
  const keyword = modelKeyword.value.trim().toLowerCase()
  return currentCategoryModels.value.filter((item) => {
    const keywordMatches = !keyword
      || item.model.toLowerCase().includes(keyword)
      || formatModelCategory(item).toLowerCase().includes(keyword)
      || (item.supportedApiProtocols ?? []).some((protocol) => formatApiProtocol(protocol).toLowerCase().includes(keyword))
    return keywordMatches
  })
})

function hasAnyNumber(...values: Array<number | undefined>) {
  return values.some((value) => typeof value === 'number')
}

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
  selectedModelCategory.value = 'text'
  modelLoading.value = true
  try {
    providerModels.value = await api.providers.models(provider.code)
    selectedModelCategory.value = findFirstModelCategory(providerModels.value)
  } catch (error) {
    console.error(error)
    providerModels.value = []
    message.error('加载模型价格失败')
  } finally {
    modelLoading.value = false
  }
}

function handleProviderAction(key: string, provider: ProviderDefinition) {
  if (key === 'models') {
    void openModelModal(provider)
  }
}

function resetModelModal() {
  activeProvider.value = null
  modelKeyword.value = ''
  selectedModelCategory.value = 'text'
  providerModels.value = []
}

function findFirstModelCategory(models: ProviderModelPricing[]): ModelCategoryKey {
  for (const key of modelCategoryOrder) {
    if (models.some((item) => getModelCategory(item) === key)) {
      return key
    }
  }
  return 'text'
}

function getModelCategory(item: ProviderModelPricing): ModelCategoryKey {
  const model = item.model.toLowerCase()
  const mode = (item.mode ?? '').trim()

  if (mode === 'image_generation' || model.startsWith('gpt-image') || model.startsWith('dall-e')) {
    return 'image'
  }

  if (
    mode === 'audio_speech'
    || mode === 'audio_transcription'
    || model.includes('audio')
    || model.includes('realtime')
    || model.includes('transcribe')
    || model.includes('tts')
    || model.includes('whisper')
  ) {
    return 'audio'
  }

  if (
    mode === 'chat'
    || mode === 'responses'
    || mode === 'completion'
    || model.includes('codex')
    || model.startsWith('gpt-')
    || model.startsWith('o')
  ) {
    return 'text'
  }

  return 'other'
}

function formatModelCategory(item: ProviderModelPricing) {
  return modelCategoryLabels[getModelCategory(item)]
}

function formatApiProtocol(protocol?: string) {
  return apiProtocolLabels[protocol ?? ''] ?? protocol ?? '-'
}

function getApiProtocolTagColor(protocol?: string) {
  switch (protocol) {
    case 'chat_completions':
      return 'blue'
    case 'responses':
      return 'purple'
    case 'images':
      return 'cyan'
    case 'audio':
      return 'green'
    case 'realtime':
      return 'orange'
    default:
      return 'default'
  }
}

function visibleProviderCapabilities(capabilities: string[]) {
  return capabilities.filter((capability) => !hiddenProviderCapabilities.has(capability))
}

function formatProviderCapability(capability: string) {
  return providerCapabilityLabels[capability] ?? capability
}

function formatCapabilitiesSummary(capabilities: string[]) {
  const visibleCapabilities = visibleProviderCapabilities(capabilities)
  return visibleCapabilities.length ? visibleCapabilities.map(formatProviderCapability).join(' / ') : '-'
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
.provider-table :deep(.ant-empty),
.model-table :deep(.ant-empty) {
  margin: 12px 0;
}

.provider-table :deep(.ant-table-cell),
.model-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

:global(.model-price-modal-wrap .ant-modal) {
  top: 30px;
  max-width: calc(100vw - 60px);
  padding-bottom: 30px;
}

:global(.model-price-modal-wrap .ant-modal-body) {
  max-height: none;
  overflow: hidden;
}

.model-modal-content {
  display: flex;
  height: calc(100dvh - 156px);
  min-height: 0;
  flex-direction: column;
}

.model-toolbar {
  display: flex;
  align-items: center;
  flex: 0 0 auto;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 16px;
}

.model-tabs {
  flex: 0 0 auto;
  margin-bottom: 8px;
}

.model-tabs :deep(.ant-tabs-content-holder) {
  display: none;
}

.model-tabs :deep(.ant-tabs-nav) {
  margin-bottom: 0;
}

.model-search {
  width: 320px;
}

.model-table {
  min-height: 0;
  flex: 1 1 auto;
}

.price-cell {
  display: flex;
  flex-direction: column;
  gap: 2px;
  line-height: 1.5;
}

.model-mobile-card {
  display: grid;
  gap: 10px;
  padding: 12px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
}

.model-mobile-card-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 8px;
}

.model-mobile-card-grid {
  display: grid;
  grid-template-columns: minmax(76px, auto) minmax(0, 1fr);
  gap: 6px 10px;
  color: #64748b;
  font-size: 12px;
}

.model-mobile-card-grid strong {
  min-width: 0;
  overflow: hidden;
  color: #0f172a;
  text-overflow: ellipsis;
}

.muted-text {
  color: rgba(0, 0, 0, 0.45);
}

@media (max-width: 768px) {
  :global(.model-price-modal-wrap .ant-modal) {
    top: 12px;
    max-width: calc(100vw - 24px);
    padding-bottom: 12px;
  }

  .model-modal-content {
    height: calc(100dvh - 140px);
  }

  .model-toolbar {
    align-items: stretch;
    flex-direction: column;
  }

  .model-search {
    width: 100%;
  }
}
</style>

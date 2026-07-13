<template>
  <a-modal
    v-model:open="open"
    :title="title"
    width="1180px"
    wrap-class-name="model-price-modal-wrap"
    :footer="null"
    @cancel="emit('cancel')"
  >
    <div class="model-modal-content">
      <div class="model-toolbar">
        <div class="model-toolbar-filters">
          <a-input-search v-model:value="keyword" allow-clear placeholder="搜索模型名称、用途或接口协议" class="model-search" />
          <SystemPrincipalSelect
            v-if="isManagementView"
            :value="systemAccountFilter"
            :accounts="systemAccounts"
            :active-only="false"
            :filter-option="false"
            :loading="systemAccountsLoading"
            :selected-principal="systemAccountFilterSelection"
            class="model-owner-select"
            placeholder="选择模型归属用户"
            @update:value="handleSystemAccountUpdate"
            @update:selected-principal="emit('update:systemAccountFilterSelection', $event)"
            @change="emit('system-account-change')"
            @dropdown-visible-change="emit('system-account-dropdown', $event)"
            @search="emit('system-account-search', $event)"
          />
        </div>
        <a-space wrap>
          <a-button type="primary" @click="emit('create')">新增模型</a-button>
          <a-tag color="blue">{{ models.length }} / {{ currentCategoryCount }} 个模型</a-tag>
          <a-tag color="purple">Token：USD / 1M；图片：USD / 张</a-tag>
        </a-space>
      </div>
      <a-tabs v-model:activeKey="selectedCategory" class="model-tabs" size="small">
        <a-tab-pane v-for="tab in categoryTabs" :key="tab.key" :tab="tab.label" />
      </a-tabs>
      <ResponsiveDataList
        class="model-table"
        table-class="model-table"
        size="small"
        :columns="columns"
        :data-source="models"
        row-key="model"
        :loading="loading"
        :pagination="{ pageSize: 20, hideOnSinglePage: true, showSizeChanger: false }"
        :scroll-x="1500"
        :lock-body-scroll="false"
      >
        <template #emptyText>
          <a-empty class="page-empty-card" :description="loadError || '这个供应商暂未配置模型价格。'" />
        </template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'model'">
            <a-space size="small">
              <span class="mono-cell">{{ record.model }}</span>
              <a-tag v-if="isDefaultHealthCheckModel(record)" color="green">默认检查</a-tag>
              <a-tag color="default">{{ record.providerCode }}</a-tag>
              <a-tag v-if="record.shutdownDate" color="orange">将停用 {{ record.shutdownDate }}</a-tag>
            </a-space>
          </template>
          <template v-else-if="column.key === 'scope'">
            <a-tag :color="modelScopeColor(record.scope)">{{ formatModelScope(record.scope) }}</a-tag>
          </template>
          <template v-else-if="column.key === 'status'">
            <a-tag :color="modelStatusColor(record.status)">{{ formatModelStatus(record.status) }}</a-tag>
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
          <template v-else-if="column.key === 'modalities'">
            <div class="price-cell">
              <span>输入 {{ formatModelModalities(record.inputModalities) }}</span>
              <span>输出 {{ formatModelModalities(record.outputModalities) }}</span>
              <span v-if="record.supportedTools?.length">工具 {{ formatModelTools(record.supportedTools) }}</span>
            </div>
          </template>
          <template v-else-if="column.key === 'serviceTiers'">
            <a-space wrap size="small">
              <a-tag v-for="tier in record.supportedServiceTiers" :key="tier" :color="tier === 'priority' ? 'blue' : 'cyan'">
                {{ formatModelServiceTier(tier) }}
              </a-tag>
              <span v-if="!record.supportedServiceTiers?.length" class="muted-text">-</span>
            </a-space>
          </template>
          <template v-else-if="column.key === 'reasoningEfforts'">
            <div class="capability-tag-list">
              <a-tag
                v-for="effort in record.supportedReasoningEfforts"
                :key="effort"
              >
                {{ formatModelReasoningEffort(effort) }}
              </a-tag>
              <span v-if="!record.supportedReasoningEfforts?.length" class="muted-text">-</span>
            </div>
          </template>
          <template v-else-if="column.key === 'prices'">
            <div class="price-cell">
              <span v-if="record.pricingModel">计价 {{ record.pricingModel }}</span>
              <template v-else>
                <span>输入 {{ formatPrice(record.inputUsdPer1M) }}</span>
                <span>输出 {{ formatPrice(record.outputUsdPer1M) }}</span>
                <span>缓存读 {{ formatPrice(record.cachedInputUsdPer1M) }}</span>
              </template>
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
          <template v-else-if="column.key === 'audioTokenPrice'">
            <div class="price-cell">
              <span>输入 {{ formatPrice(record.audioInputUsdPer1M) }}</span>
              <span>输出 {{ formatPrice(record.audioOutputUsdPer1M) }}</span>
            </div>
          </template>
          <template v-else-if="column.key === 'imageUnitPrice'">
            <span>{{ formatUnitPrice(record.outputUsdPerImage) }}</span>
          </template>
          <template v-else-if="column.key === 'context'">
            <div class="price-cell">
              <span>窗口 {{ formatTokens(record.contextWindowTokens) }}</span>
              <span>最大输入 {{ formatTokens(record.maxInputTokens) }}</span>
              <span>最大输出 {{ formatTokens(record.maxOutputTokens) }}</span>
            </div>
          </template>
          <template v-else-if="column.key === 'actions'">
            <RowActions :actions="rowActions(record)" @action-click="emit('model-action', $event, record)" />
          </template>
        </template>
        <template #card="{ record }">
          <article class="model-mobile-card">
            <div class="model-mobile-card-head">
              <strong class="mono-cell">{{ record.model }}</strong>
              <a-space size="small" wrap>
                <a-tag color="default">{{ record.providerCode }}</a-tag>
                <a-tag v-if="isDefaultHealthCheckModel(record)" color="green">默认检查</a-tag>
                <a-tag>{{ formatModelCategory(record) }}</a-tag>
                <a-tag :color="modelStatusColor(record.status)">{{ formatModelStatus(record.status) }}</a-tag>
              </a-space>
            </div>
            <div class="model-mobile-card-grid">
              <span>供应商</span>
              <strong>{{ record.providerCode }}</strong>
              <span>来源</span>
              <strong>{{ formatModelScope(record.scope) }}</strong>
              <span>发布时间</span>
              <strong>{{ record.releaseDate || '-' }}</strong>
              <span>接口协议</span>
              <strong>{{ (record.supportedApiProtocols ?? []).map(formatApiProtocol).join(' / ') || '-' }}</strong>
              <span>输入 / 输出模态</span>
              <strong>{{ formatModelModalities(record.inputModalities) }} / {{ formatModelModalities(record.outputModalities) }}</strong>
              <span>工具</span>
              <strong>{{ formatModelTools(record.supportedTools) }}</strong>
              <span>服务等级</span>
              <strong>{{ (record.supportedServiceTiers ?? []).map(formatModelServiceTier).join(' / ') || '-' }}</strong>
              <span>思考级别</span>
              <strong>{{ formatModelReasoningCapabilities(record) }}</strong>
              <span>价格</span>
              <strong>{{ formatModelPriceSummary(record) }}</strong>
              <span>上下文</span>
              <strong>窗口 {{ formatTokens(record.contextWindowTokens) }} / 输入 {{ formatTokens(record.maxInputTokens) }} / 输出 {{ formatTokens(record.maxOutputTokens) }}</strong>
            </div>
            <RowActions v-if="rowActions(record).length" variant="button" :actions="rowActions(record)" @action-click="emit('model-action', $event, record)" />
          </article>
        </template>
      </ResponsiveDataList>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import RowActions from '@/components/RowActions.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import type { RowActionItem } from '@/components/rowActions'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type { ProviderModelPricing, SystemAccountPrincipalSummary } from '@/types/domain'

import {
  formatApiProtocol,
  formatModelCategory,
  formatModelModalities,
  formatModelTools,
  formatModelPriceSummary,
  formatModelReasoningCapabilities,
  formatModelReasoningEffort,
  formatModelServiceTier,
  formatModelScope,
  formatModelStatus,
  formatPrice,
  formatTokens,
  formatUnitPrice,
  getApiProtocolTagColor,
  modelScopeColor,
  modelStatusColor,
  type ModelCategoryKey
} from './providerModelFormatters'

const open = defineModel<boolean>('open', { required: true })
const keyword = defineModel<string>('keyword', { required: true })
const selectedCategory = defineModel<ModelCategoryKey>('selectedCategory', { required: true })

const props = withDefaults(defineProps<{
  title: string
  loading: boolean
  categoryTabs: Array<{ key: ModelCategoryKey; label: string }>
  columns: Array<Record<string, any>>
  models: ProviderModelPricing[]
  currentCategoryCount: number
  defaultHealthCheckModel?: string
  loadError?: string
  rowActions: (record: ProviderModelPricing) => RowActionItem[]
  isManagementView?: boolean
  systemAccounts?: SystemAccountPrincipalSummary[]
  systemAccountsLoading?: boolean
  systemAccountFilter?: string
  systemAccountFilterSelection?: PrincipalSelection
}>(), {
  isManagementView: false,
  systemAccounts: () => [],
  systemAccountsLoading: false,
  systemAccountFilter: '',
  systemAccountFilterSelection: undefined,
  loadError: ''
})

const emit = defineEmits<{
  cancel: []
  create: []
  'model-action': [key: string, record: ProviderModelPricing]
  'update:systemAccountFilter': [value: string]
  'update:systemAccountFilterSelection': [value: PrincipalSelection | undefined]
  'system-account-change': []
  'system-account-dropdown': [open: boolean]
  'system-account-search': [value: string]
}>()

function isDefaultHealthCheckModel(record: ProviderModelPricing): boolean {
  const defaultModel = props.defaultHealthCheckModel?.trim()
  return Boolean(defaultModel && record.model.trim() === defaultModel)
}

function handleSystemAccountUpdate(value: string | string[] | undefined): void {
  emit('update:systemAccountFilter', typeof value === 'string' ? value : '')
}
</script>

<style scoped>
.model-table :deep(.ant-empty) {
  margin: 12px 0;
}

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

.model-toolbar-filters {
  display: flex;
  align-items: center;
  min-width: 0;
  gap: 12px;
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

.model-owner-select {
  width: 260px;
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

.capability-tag-list {
  display: flex;
  min-width: 0;
  flex-wrap: wrap;
  gap: 4px 2px;
}

.capability-tag-list :deep(.ant-tag) {
  margin-inline-end: 0;
  white-space: nowrap;
}

.model-mobile-card {
  display: grid;
  gap: 10px;
  padding: 12px;
  background: #fff;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
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

  .model-toolbar-filters {
    align-items: stretch;
    flex-direction: column;
  }

  .model-search {
    width: 100%;
  }

  .model-owner-select {
    width: 100%;
  }

}
</style>

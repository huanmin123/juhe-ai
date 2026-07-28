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
            :disabled="loading"
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
          <a-button type="primary" :disabled="loading" @click="emit('create')">新增模型</a-button>
          <a-tag color="blue">{{ models.length }} / {{ currentCategoryCount }} 个模型</a-tag>
          <a-tag>USD 结算</a-tag>
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
        :row-key="modelRowKey"
        :loading="loading"
        :pagination="{ pageSize: 20, hideOnSinglePage: true, showSizeChanger: false }"
        :scroll-x="tableScrollX"
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
          <template v-else-if="column.catalogDisplaySectionKey">
            <div
              v-if="modelCatalogDisplaySection(record, column.catalogDisplaySectionKey)"
              class="catalog-display-cell"
            >
              <div
                v-for="item in modelCatalogDisplaySection(record, column.catalogDisplaySectionKey)?.items"
                :key="item.key"
                class="catalog-display-item"
              >
                <span>{{ item.label }}</span>
                <strong>{{ formatModelCatalogDisplayValue(item) }}</strong>
              </div>
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
              <template v-for="section in modelCatalogDisplaySections(record)" :key="section.key">
                <span>{{ section.label }}</span>
                <strong class="model-mobile-catalog-value">
                  <span v-for="item in section.items" :key="item.key">
                    <span>{{ item.label }}</span>
                    {{ formatModelCatalogDisplayValue(item) }}
                  </span>
                </strong>
              </template>
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
import { computed } from 'vue'

import {
  formatApiProtocol,
  formatModelCatalogDisplayValue,
  formatModelCategory,
  formatModelScope,
  formatModelStatus,
  getApiProtocolTagColor,
  modelCatalogDisplaySection,
  modelCatalogDisplaySections,
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

const tableScrollX = computed(() => Math.max(1500, props.columns.reduce((total, column) => (
  total + (typeof column.width === 'number' ? column.width : 180)
), 0)))

function isDefaultHealthCheckModel(record: ProviderModelPricing): boolean {
  const defaultModel = props.defaultHealthCheckModel?.trim()
  return Boolean(defaultModel && record.model.trim() === defaultModel)
}

function handleSystemAccountUpdate(value: string | string[] | undefined): void {
  emit('update:systemAccountFilter', typeof value === 'string' ? value : '')
}

function modelRowKey(record: ProviderModelPricing): string {
  return record.id || [record.providerCode, record.scope, record.systemAccountId ?? '', record.model].join(':')
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

.catalog-display-cell {
  display: grid;
  gap: 4px;
}

.catalog-display-item {
  display: grid;
  align-items: flex-start;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 12px;
  line-height: 1.5;
  white-space: normal;
}

.catalog-display-item > span,
.model-mobile-catalog-value > span > span {
  color: #94a3b8;
  font-size: 11px;
  font-weight: 500;
  overflow-wrap: anywhere;
  white-space: normal;
}

.catalog-display-item strong {
  min-width: 0;
  color: #0f172a;
  font-weight: 600;
  overflow-wrap: anywhere;
  text-align: right;
  white-space: normal;
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

.model-mobile-card-grid .model-mobile-catalog-value {
  display: grid;
  gap: 2px;
  overflow: visible;
  text-overflow: clip;
  white-space: normal;
}

.model-mobile-catalog-value > span {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
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

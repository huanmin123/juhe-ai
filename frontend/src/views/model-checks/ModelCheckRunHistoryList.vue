<template>
  <a-card class="page-card model-checks-history-card" title="历史检测">
    <div class="history-toolbar">
      <a-space wrap>
        <SystemPrincipalSelect
          v-if="isManagementView"
          :value="systemAccountFilter"
          :selected-principal="systemAccountFilterSelection"
          :accounts="systemAccounts"
          :active-only="false"
          include-all
          allow-clear
          class="history-filter history-system-account-filter"
          :filter-option="false"
          :loading="systemAccountOptionsLoading"
          placeholder="请选择系统账户"
          @change="handleSystemAccountChange"
          @dropdown-visible-change="emit('system-account-dropdown-visible-change', $event)"
          @search="emit('system-account-search', $event)"
          @update:selected-principal="emit('update:systemAccountFilterSelection', $event)"
          @update:value="emit('update:systemAccountFilter', selectStringValue($event))"
        />
        <a-select
          :value="filters.model"
          allow-clear
          class="history-filter"
          :options="modelOptions"
          placeholder="全部模型"
          @change="handleModelChange"
        />
        <a-select
          :value="filters.status"
          allow-clear
          class="history-filter"
          :options="statusOptions"
          placeholder="全部状态"
          @change="handleStatusChange"
        />
        <a-select
          :value="filters.level"
          allow-clear
          class="history-filter"
          :options="levelOptions"
          placeholder="全部级别"
          @change="handleLevelChange"
        />
        <AccountSelect
          :value="filters.targetId"
          :selected-account="selectedHistoryTargetAccount"
          show-search
          allow-clear
          class="history-target-filter"
          :disabled="submitting"
          :filter-option="false"
          :loading="historyTargetOptionsLoading"
          :options="historyTargetOptions"
          placeholder="全部账户"
          @change="handleTargetChange"
          @dropdown-visible-change="emit('history-target-dropdown-visible-change', $event)"
          @search="emit('history-target-search', $event)"
          @update:selected-account="emit('update:selectedHistoryTargetAccount', $event)"
          @update:value="emit('update:targetId', selectStringValue($event))"
        />
      </a-space>
      <a-button :loading="loading" @click="emit('reload')">
        <template #icon>
          <ReloadOutlined />
        </template>
        刷新
      </a-button>
    </div>

    <ResponsiveDataList
      class="model-checks-responsive-list"
      table-class="model-checks-table"
      size="middle"
      row-key="id"
      :columns="columns"
      :data-source="runs"
      :mobile-data-source="runs"
      :loading="loading"
      :pagination="tablePagination"
      :scroll-x="1470"
      :loading-more="mobileLoadingMore"
      :mobile-has-more="mobileHasMore"
      mobile-pagination
      pull-refresh-enabled
      :refreshing="loading"
      @change="emit('table-change', $event)"
      @mobile-load-more="emit('mobile-load-more')"
      @mobile-refresh="emit('mobile-refresh')"
    >
      <template #emptyText>
        <a-empty description="暂无模型检测历史" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'target'">
          <div class="target-cell">
            <span class="target-name-cell">{{ targetDisplayName(record) }}</span>
          </div>
        </template>
        <template v-else-if="column.key === 'targetType'">
          <a-tag>{{ targetTypeText(record.targetType) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'providerCode'">
          <a-tag color="geekblue">{{ providerText(record.providerCode) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag :color="statusColor(record.status)">{{ statusText(record.status) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'level'">
          <a-tag :color="levelColor(record.level)">{{ levelText(record.level) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'model'">
          {{ modelText(record.model) }}
        </template>
        <template v-else-if="column.key === 'profile'">
          <a-tag :color="modelCheckProfileColor(record.profile)">{{ modelCheckProfileText(record.profile) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'createdAt'">
          {{ formatDateTime(record.createdAt) }}
        </template>
        <template v-else-if="column.key === 'summary'">
          <span class="summary-cell">{{ record.message || record.errorMessage || '-' }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <a-button type="link" size="small" @click="emit('view-detail', record.id)">查看</a-button>
        </template>
      </template>
      <template #card="{ record }">
        <article class="model-check-mobile-card">
          <div class="model-check-mobile-head">
            <div>
              <div class="model-check-mobile-title">{{ targetDisplayName(record) }}</div>
            </div>
            <a-tag :color="statusColor(record.status)">{{ statusText(record.status) }}</a-tag>
          </div>
          <div class="model-check-mobile-tags">
            <a-tag>{{ targetTypeText(record.targetType) }}</a-tag>
            <a-tag color="geekblue">{{ providerText(record.providerCode) }}</a-tag>
            <a-tag>{{ modelText(record.model) }}</a-tag>
            <a-tag :color="modelCheckProfileColor(record.profile)">{{ modelCheckProfileText(record.profile) }}</a-tag>
            <a-tag :color="levelColor(record.level)">{{ levelText(record.level) }}</a-tag>
            <a-tag v-if="runTrustedComparison(record)" color="blue">可信对比</a-tag>
          </div>
          <div class="model-check-mobile-grid">
            <div class="model-check-mobile-metric">
              <span>得分</span>
              <strong>{{ record.score }} / {{ record.maxScore }}</strong>
            </div>
            <div class="model-check-mobile-metric">
              <span>耗时</span>
              <strong>{{ formatDuration(record.durationMs) }}</strong>
            </div>
            <div class="model-check-mobile-metric model-check-mobile-wide">
              <span>创建时间</span>
              <strong>{{ formatDateTime(record.createdAt) }}</strong>
            </div>
            <div class="model-check-mobile-metric model-check-mobile-wide">
              <span>结论</span>
              <strong>{{ record.message || record.errorMessage || '-' }}</strong>
            </div>
          </div>
          <div class="model-check-mobile-actions">
            <a-button size="small" type="primary" @click="emit('view-detail', record.id)">查看</a-button>
          </div>
        </article>
      </template>
    </ResponsiveDataList>
  </a-card>
</template>

<script setup lang="ts">
import { ReloadOutlined } from '@ant-design/icons-vue'

import AccountSelect from '@/components/AccountSelect.vue'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import { formatDateTime } from '@/shared/formatters'
import type { AccountSelection, SelectOption } from '@/shared/accountLabelCache'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type {
  ModelCheckLevel,
  ModelCheckModel,
  ModelCheckOption,
  ModelCheckRunSummary,
  ModelCheckStatus,
  SystemAccountPrincipalSummary
} from '@/types/domain'
import {
  formatModelCheckDuration as formatDuration,
  levelColor,
  levelText,
  modelCheckLevelOptions as levelOptions,
  modelCheckModelText,
  modelCheckProfileColor,
  modelCheckProfileText,
  modelCheckStatusOptions as statusOptions,
  providerText,
  runTrustedComparison,
  statusColor,
  statusText,
  targetTypeText
} from './modelCheckFormatters'
import { modelCheckHistoryColumns } from './modelCheckPageConfig'

type SelectValue = string | string[] | undefined

export interface ModelCheckRunHistoryFilters {
  targetId?: string
  model?: ModelCheckModel
  level?: ModelCheckLevel
  status?: ModelCheckStatus
}

const props = defineProps<{
  filters: ModelCheckRunHistoryFilters
  historyTargetOptions: SelectOption[]
  historyTargetOptionsLoading: boolean
  isManagementView: boolean
  loading: boolean
  mobileHasMore: boolean
  mobileLoadingMore: boolean
  modelOptions: Array<{ label: string; value: string }>
  runs: ModelCheckRunSummary[]
  selectedHistoryTargetAccount?: AccountSelection
  submitting: boolean
  supportedModels: ModelCheckOption[]
  systemAccountFilter: string
  systemAccountFilterSelection?: PrincipalSelection
  systemAccountOptionsLoading: boolean
  systemAccounts: SystemAccountPrincipalSummary[]
  tablePagination: Record<string, any>
  targetDisplayName: (run: Pick<ModelCheckRunSummary, 'targetName' | 'targetId'>) => string
}>()

const emit = defineEmits<{
  (event: 'history-target-dropdown-visible-change', open: boolean): void
  (event: 'history-target-search', value: string): void
  (event: 'mobile-load-more'): void
  (event: 'mobile-refresh'): void
  (event: 'reload'): void
  (event: 'system-account-change'): void
  (event: 'system-account-dropdown-visible-change', open: boolean): void
  (event: 'system-account-search', value: string): void
  (event: 'table-change', paginationInfo: unknown): void
  (event: 'update:level', value?: ModelCheckLevel): void
  (event: 'update:model', value?: ModelCheckModel): void
  (event: 'update:selectedHistoryTargetAccount', value?: AccountSelection): void
  (event: 'update:status', value?: ModelCheckStatus): void
  (event: 'update:systemAccountFilter', value?: string): void
  (event: 'update:systemAccountFilterSelection', value?: PrincipalSelection): void
  (event: 'update:targetId', value?: string): void
  (event: 'view-detail', id: string): void
}>()

const columns = modelCheckHistoryColumns

function handleModelChange(value: SelectValue) {
  emit('update:model', typeof value === 'string' ? value as ModelCheckModel : undefined)
  emit('reload')
}

function handleStatusChange(value: SelectValue) {
  emit('update:status', typeof value === 'string' ? value as ModelCheckStatus : undefined)
  emit('reload')
}

function handleLevelChange(value: SelectValue) {
  emit('update:level', typeof value === 'string' ? value as ModelCheckLevel : undefined)
  emit('reload')
}

function handleSystemAccountChange(value: SelectValue) {
  emit('update:systemAccountFilter', selectStringValue(value))
  emit('system-account-change')
}

function handleTargetChange(value: SelectValue) {
  emit('update:targetId', selectStringValue(value))
  emit('reload')
}

function selectStringValue(value: SelectValue): string | undefined {
  return typeof value === 'string' ? value : undefined
}

function modelText(value: string) {
  return modelCheckModelText(value, props.supportedModels)
}
</script>

<style scoped>
.model-checks-history-card {
  display: flex;
  min-height: 0;
  flex: 1 1 auto;
  flex-direction: column;
  border: 1px solid #e8edf5;
  border-radius: 16px;
}

.model-checks-history-card :deep(.ant-card-body) {
  display: flex;
  min-height: 0;
  flex: 1 1 auto;
  flex-direction: column;
}

.history-toolbar {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
  margin-bottom: 14px;
}

.model-checks-responsive-list {
  min-height: 0;
  flex: 1 1 auto;
}

.history-filter {
  width: 140px;
}

.history-target-filter {
  width: 240px;
}

.history-system-account-filter {
  width: 220px;
}

.target-cell {
  display: inline-flex;
  max-width: 100%;
  align-items: center;
  gap: 8px;
}

.target-name-cell {
  display: block;
  min-width: 0;
  max-width: 240px;
  overflow: hidden;
  color: #0f172a;
  font-weight: 400;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.summary-cell {
  display: block;
  max-width: 360px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.model-checks-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.model-check-mobile-card {
  display: grid;
  gap: 12px;
  padding: 12px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
}

.model-check-mobile-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 10px;
}

.model-check-mobile-title {
  color: #0f172a;
  font-weight: 400;
  line-height: 1.35;
}

.model-check-mobile-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.model-check-mobile-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}

.model-check-mobile-metric {
  min-width: 0;
  padding: 10px;
  border: 1px solid #eef2f7;
  border-radius: 8px;
  background: #f8fafc;
}

.model-check-mobile-wide {
  grid-column: 1 / -1;
}

.model-check-mobile-metric span {
  display: block;
  color: #64748b;
  font-size: 12px;
}

.model-check-mobile-metric strong {
  display: block;
  margin-top: 4px;
  overflow: hidden;
  color: #0f172a;
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.model-check-mobile-actions {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
}

.model-check-mobile-actions :deep(.ant-btn) {
  width: 100%;
  min-height: 36px;
  white-space: normal;
}

@media (max-width: 900px) {
  .history-toolbar {
    align-items: flex-start;
    flex-direction: column;
  }

  .history-filter,
  .history-system-account-filter,
  .history-target-filter {
    width: 100%;
  }
}
</style>

<template>
  <a-card class="page-card ai-performance-header-card">
    <div class="page-toolbar ai-performance-toolbar">
      <div class="ai-performance-filters">
        <SystemPrincipalSelect
          v-if="isManagementView"
          v-model:value="selectedSystemAccountId"
          v-model:selected-principal="selectedSystemAccount"
          :accounts="systemAccounts"
          :active-only="false"
          :disabled="loading"
          :filter-option="false"
          :loading="systemAccountOptionsLoading"
          all-label="全部用户"
          class="ai-performance-system-account-select"
          include-all
          placeholder="筛选用户"
          @change="emit('system-account-change')"
          @dropdown-visible-change="emit('system-account-dropdown-visible-change', $event)"
          @search="emit('system-account-search', $event)"
        />
        <a-range-picker
          v-model:value="dateRange"
          :allow-clear="false"
          :disabled="loading"
          :disabled-date="disabledDate"
          class="ai-performance-range-picker"
          format="YYYY-MM-DD"
          @calendar-change="emit('calendar-change', $event)"
          @change="emit('date-range-change')"
          @open-change="emit('date-range-open-change', $event)"
        />
        <AccountAppendSelect
          v-model:value="addedAccountIds"
          :accounts="accounts"
          :selected-accounts="addedAccountSelections"
          class="ai-performance-account-select"
          :hidden-account-ids="accountPickerHiddenValues"
          :loading="accountsLoading"
          :disabled="loading"
          :max="20"
          max-tag-count="responsive"
          placeholder="输入账户名称添加账户"
          @change="handleAddedAccountsChange"
          @search="emit('account-search', $event)"
          @dropdown-visible-change="emit('account-dropdown-visible-change', $event)"
        />
      </div>
      <div class="page-toolbar-actions">
        <a-button :disabled="loading" @click="emit('reset')">重置</a-button>
        <a-button :loading="loading" @click="emit('refresh')">
          <template #icon>
            <ReloadOutlined />
          </template>
          刷新
        </a-button>
      </div>
    </div>
    <div v-if="accountFilterItems.length" class="ai-performance-account-list" aria-label="性能账户筛选">
      <span
        v-for="item in accountFilterItems"
        :key="item.account.id"
        class="ai-performance-account-filter-entry"
        :class="{ active: item.selected, muted: hasActiveAccountFilter && !item.selected }"
      >
        <button
          class="ai-performance-account-filter-item"
          type="button"
          :aria-pressed="item.selected"
          @click="emit('toggle-account', item.account.id)"
        >
          <span class="ai-performance-legend-dot" :style="{ backgroundColor: item.color }" />
          <span class="ai-performance-legend-name">{{ item.label }}</span>
        </button>
        <a-tooltip v-if="item.removable" title="移除">
          <button
            class="ai-performance-account-filter-remove"
            type="button"
            :aria-label="`移除${item.label}`"
            @click.stop="emit('remove-account', item.account.id)"
          >
            <CloseOutlined />
          </button>
        </a-tooltip>
      </span>
    </div>
  </a-card>
</template>

<script setup lang="ts">
import { CloseOutlined, ReloadOutlined } from '@ant-design/icons-vue'
import type { Dayjs } from 'dayjs'

import AccountAppendSelect from '@/components/AccountAppendSelect.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import type { AccountSelection } from '@/shared/accountLabelCache'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type { AiPerformanceAccountOption, AiPerformanceOverview, SystemAccountPrincipalSummary } from '@/types/domain'

type AiPerformanceAccountFilterItem = {
  account: AiPerformanceOverview['accounts'][number]
  label: string
  color: string
  selected: boolean
  removable: boolean
}

const selectedSystemAccountId = defineModel<string>('selectedSystemAccountId', { required: true })
const selectedSystemAccount = defineModel<PrincipalSelection | undefined>('selectedSystemAccount')
const dateRange = defineModel<[Dayjs, Dayjs]>('dateRange', { required: true })
const addedAccountIds = defineModel<string[]>('addedAccountIds', { required: true })

defineProps<{
  isManagementView: boolean
  loading: boolean
  systemAccounts: SystemAccountPrincipalSummary[]
  systemAccountOptionsLoading: boolean
  disabledDate: (current: Dayjs) => boolean
  accounts: AiPerformanceAccountOption[]
  addedAccountSelections: AccountSelection[]
  accountPickerHiddenValues: Array<string | undefined>
  accountsLoading: boolean
  accountFilterItems: AiPerformanceAccountFilterItem[]
  hasActiveAccountFilter: boolean
}>()

const emit = defineEmits<{
  'system-account-change': []
  'system-account-dropdown-visible-change': [open: boolean]
  'system-account-search': [value: string]
  'calendar-change': [value: Array<Dayjs | null> | null]
  'date-range-change': []
  'date-range-open-change': [open: boolean]
  'added-accounts-change': [value: string[], previousValue: string[]]
  'account-search': [value: string]
  'account-dropdown-visible-change': [open: boolean]
  reset: []
  refresh: []
  'toggle-account': [id: string]
  'remove-account': [id: string]
}>()

function handleAddedAccountsChange(value: string[], previousValue: string[]) {
  emit('added-accounts-change', value, previousValue)
}
</script>

<style scoped>
.ai-performance-header-card :deep(.ant-card-body) {
  padding: 16px 18px;
}

.ai-performance-toolbar {
  margin: 0;
}

.ai-performance-filters {
  display: flex;
  flex: 1 1 720px;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
  min-width: 0;
}

.ai-performance-system-account-select {
  width: 240px;
}

.ai-performance-range-picker {
  width: 250px;
}

.ai-performance-account-select {
  flex: 1 1 320px;
  width: auto;
  min-width: 280px;
  max-width: none;
}

.ai-performance-account-list {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 10px;
  margin-top: 12px;
}

.ai-performance-account-filter-entry {
  display: inline-flex;
  align-items: center;
  max-width: min(360px, 100%);
  border: 1px solid transparent;
  border-radius: 6px;
  transition: background-color 0.16s ease, border-color 0.16s ease, opacity 0.16s ease;
}

.ai-performance-account-filter-entry:hover,
.ai-performance-account-filter-entry.active {
  border-color: #91caff;
  background: #e6f4ff;
}

.ai-performance-account-filter-entry.muted {
  opacity: 0.46;
}

.ai-performance-account-filter-item {
  display: inline-flex;
  align-items: center;
  min-width: 0;
  gap: 6px;
  padding: 2px 8px;
  border: 0;
  color: #334155;
  background: transparent;
  font-size: 13px;
  line-height: 20px;
  cursor: pointer;
}

.ai-performance-account-filter-remove {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  margin-left: -4px;
  padding: 0;
  border: 0;
  border-radius: 5px;
  color: #64748b;
  background: transparent;
  font-size: 12px;
  cursor: pointer;
  transition: background-color 0.16s ease, color 0.16s ease;
}

.ai-performance-account-filter-remove:hover {
  color: #cf1322;
  background: #fff1f0;
}

.ai-performance-legend-dot {
  width: 10px;
  height: 10px;
  flex: 0 0 auto;
  border-radius: 50%;
}

.ai-performance-legend-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

@media (max-width: 900px) {
  .ai-performance-filters {
    width: 100%;
    flex: none;
    flex-direction: column;
    align-items: stretch;
  }

  .ai-performance-system-account-select,
  .ai-performance-range-picker,
  .ai-performance-account-select {
    flex: none;
    width: 100%;
    min-width: 0;
    max-width: none;
  }
}
</style>

<template>
  <a-card class="page-card ai-performance-header-card">
    <div class="page-toolbar ai-performance-toolbar ai-performance-toolbar-desktop">
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
          :record-preference="false"
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
    <!-- 手机端紧凑条：当前时间范围（点按开抽屉）+ 筛选按钮；账户 chips 保留在下方 -->
    <div class="ai-performance-mobile-bar">
      <button type="button" class="ai-performance-mobile-range" @click="mobileFilterOpen = true">
        {{ mobileRangeLabel }}
      </button>
      <button type="button" class="ai-performance-mobile-filter-button" @click="mobileFilterOpen = true">
        <filter-outlined />
        筛选
        <span v-if="mobileFilterActive" class="ai-performance-mobile-filter-dot" />
      </button>
    </div>
    <div
      v-if="accountFilterItems.length"
      class="ai-performance-account-list"
      :class="{ collapsed: mobileChipsCollapsed }"
      aria-label="性能账户筛选"
    >
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
    <button
      v-if="accountFilterItems.length > mobileChipCollapseThreshold"
      type="button"
      class="ai-performance-chips-toggle"
      @click="mobileChipsCollapsed = !mobileChipsCollapsed"
    >
      {{ mobileChipsCollapsed ? `展开全部 ${accountFilterItems.length} 个账户` : '收起账户列表' }}
      <caret-down-outlined class="ai-performance-chips-toggle-icon" :class="{ open: !mobileChipsCollapsed }" />
    </button>
  </a-card>

  <a-drawer
    v-model:open="mobileFilterOpen"
    title="筛选性能"
    placement="bottom"
    height="min(66vh, 520px)"
    class="ai-performance-mobile-filter-drawer"
    :body-style="{ padding: '14px 16px 16px' }"
  >
    <div class="ai-performance-mobile-filter-body">
      <div v-if="isManagementView" class="ai-performance-mobile-filter-field">
        <span class="ai-performance-mobile-filter-label">用户</span>
        <SystemPrincipalSelect
          v-model:value="selectedSystemAccountId"
          v-model:selected-principal="selectedSystemAccount"
          :accounts="systemAccounts"
          :active-only="false"
          :disabled="loading"
          :filter-option="false"
          :loading="systemAccountOptionsLoading"
          all-label="全部用户"
          class="ai-performance-mobile-filter-control"
          include-all
          placeholder="筛选用户"
          @change="emit('system-account-change')"
          @dropdown-visible-change="emit('system-account-dropdown-visible-change', $event)"
          @search="emit('system-account-search', $event)"
        />
      </div>
      <div class="ai-performance-mobile-filter-field">
        <span class="ai-performance-mobile-filter-label">时间范围</span>
        <a-range-picker
          v-model:value="dateRange"
          :allow-clear="false"
          :disabled="loading"
          :disabled-date="disabledDate"
          class="ai-performance-mobile-filter-control"
          format="YYYY-MM-DD"
          @calendar-change="emit('calendar-change', $event)"
          @change="emit('date-range-change')"
          @open-change="emit('date-range-open-change', $event)"
        />
      </div>
      <div class="ai-performance-mobile-filter-field">
        <span class="ai-performance-mobile-filter-label">添加账户</span>
        <AccountAppendSelect
          v-model:value="addedAccountIds"
          :accounts="accounts"
          :selected-accounts="addedAccountSelections"
          class="ai-performance-mobile-filter-control"
          :hidden-account-ids="accountPickerHiddenValues"
          :loading="accountsLoading"
          :disabled="loading"
          :max="20"
          max-tag-count="responsive"
          placeholder="输入账户名称添加账户"
          :record-preference="false"
          @change="handleAddedAccountsChange"
          @search="emit('account-search', $event)"
          @dropdown-visible-change="emit('account-dropdown-visible-change', $event)"
        />
      </div>
      <div class="ai-performance-mobile-filter-actions">
        <a-button :disabled="loading" @click="emit('reset')">重置</a-button>
        <a-button type="primary" :loading="loading" @click="emit('refresh')">
          <template #icon>
            <ReloadOutlined />
          </template>
          刷新
        </a-button>
      </div>
    </div>
  </a-drawer>
</template>

<script setup lang="ts">
import { CaretDownOutlined, CloseOutlined, FilterOutlined, ReloadOutlined } from '@ant-design/icons-vue'
import { computed, ref } from 'vue'
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

const props = defineProps<{
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

// 手机端：筛选抽屉开合；账户 chips 超 4 条折叠。
// 打点：有生效账户筛选，或指定到具体用户（'all' 是全部用户哨兵）。
const mobileFilterOpen = ref(false)
const mobileChipsCollapsed = ref(true)
const mobileChipCollapseThreshold = 4
const mobileFilterActive = computed(() => props.hasActiveAccountFilter
  || (Boolean(selectedSystemAccountId.value) && selectedSystemAccountId.value !== 'all'))

const mobileRangeLabel = computed(() => {
  const [start, end] = dateRange.value ?? []
  if (!start || !end) return '时间范围'
  return `${start.month() + 1}月${start.date()}日 至 ${end.month() + 1}月${end.date()}日`
})

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
  flex: 0 1 380px;
  width: min(380px, 100%);
  min-width: 0;
  max-width: 100%;
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
  border-color: var(--juhe-border-strong);
  background: var(--juhe-accent-soft);
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
  color: var(--juhe-fg-soft);
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
  color: var(--juhe-muted);
  background: transparent;
  font-size: 12px;
  cursor: pointer;
  transition: background-color 0.16s ease, color 0.16s ease;
}

.ai-performance-account-filter-remove:hover {
  color: var(--juhe-danger);
  background: var(--juhe-danger-soft);
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

/* 手机端紧凑条 + chips 折叠（桌面隐藏） */
.ai-performance-mobile-bar {
  display: none;
}

.ai-performance-mobile-range {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  text-align: left;
  border: 0;
  background: transparent;
  color: var(--juhe-fg-soft);
  font-size: 13px;
  cursor: pointer;
  padding: 6px 0;
}

.ai-performance-mobile-filter-button {
  position: relative;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-height: 36px;
  padding: 6px 14px;
  border: 1px solid var(--juhe-border);
  border-radius: var(--juhe-radius-sm, 8px);
  background: transparent;
  color: var(--juhe-fg-soft);
  font-size: 13px;
  cursor: pointer;
}

.ai-performance-mobile-filter-dot {
  position: absolute;
  top: 5px;
  right: 5px;
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--juhe-coral, var(--juhe-coral));
}

.ai-performance-chips-toggle {
  display: none;
  align-items: center;
  justify-content: center;
  gap: 4px;
  width: 100%;
  min-height: 32px;
  margin-top: 8px;
  border: 0;
  border-radius: var(--juhe-radius-sm, 8px);
  background: transparent;
  color: var(--juhe-muted);
  font-size: 12px;
  cursor: pointer;
}

.ai-performance-chips-toggle-icon {
  font-size: 10px;
  transition: transform 0.18s ease;
}

.ai-performance-chips-toggle-icon.open {
  transform: rotate(180deg);
}

.ai-performance-mobile-filter-field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.ai-performance-mobile-filter-label {
  color: var(--juhe-muted);
  font-size: 12px;
}

.ai-performance-mobile-filter-control {
  width: 100%;
}

.ai-performance-mobile-filter-actions {
  display: flex;
  gap: 12px;
  margin-top: 4px;
}

.ai-performance-mobile-filter-actions .ant-btn {
  flex: 1 1 0;
}

@media (max-width: 900px) {
  .ai-performance-toolbar-desktop {
    display: none;
  }

  .ai-performance-mobile-bar {
    display: flex;
    align-items: center;
    justify-content: flex-end;
  }

  .ai-performance-account-list.collapsed {
    max-height: 84px;
    overflow: hidden;
  }

  .ai-performance-chips-toggle {
    display: flex;
  }
}

@media (min-width: 901px) {
  .ai-performance-mobile-filter-drawer {
    display: none;
  }
}
</style>

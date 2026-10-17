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
        <a-select
          v-model:value="capabilityDomain"
          :disabled="loading"
          class="ai-performance-domain-select"
          :options="capabilityDomainOptions"
          @change="emit('capability-domain-change')"
        />
        <AccountManagePanelPopover
          :collection-items="panelCollectionItems"
          :options="accountOptions"
          :max-added-accounts="20"
          :loading="accountsLoading"
          :disabled="loading"
          search-hint="输入账户名称搜索添加（默认已展示活跃前 10）"
          :count-text="accountCountText"
          @open-change="emit('account-open-change', $event)"
          @add="emit('add-account', $event)"
          @remove="emit('remove-account', $event)"
          @toggle="emit('toggle-account', $event)"
          @search="emit('account-search', $event)"
          @reset-defaults="emit('reset-accounts')"
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
    <!-- 手机端紧凑条：当前时间范围（点按开抽屉）+ 筛选按钮；账户管理收进筛选抽屉面板 -->
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
        <span class="ai-performance-mobile-filter-label">能力域</span>
        <a-select
          v-model:value="capabilityDomain"
          :disabled="loading"
          class="ai-performance-mobile-filter-control"
          :options="capabilityDomainOptions"
          @change="emit('capability-domain-change')"
        />
      </div>
      <div class="ai-performance-mobile-filter-field">
        <span class="ai-performance-mobile-filter-label">监控账户（{{ accountFilterItems.length }}）</span>
        <AccountManagePanel
          class="ai-performance-mobile-filter-control"
          :collection-items="panelCollectionItems"
          :options="accountOptions"
          :max-added-accounts="20"
          :loading="accountsLoading"
          :disabled="loading"
          search-hint="输入账户名称搜索添加（默认已展示活跃前 10）"
          @add="emit('add-account', $event)"
          @remove="emit('remove-account', $event)"
          @toggle="emit('toggle-account', $event)"
          @search="emit('account-search', $event)"
          @reset-defaults="emit('reset-accounts')"
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
import { FilterOutlined, ReloadOutlined } from '@ant-design/icons-vue'
import { computed, ref } from 'vue'
import type { Dayjs } from 'dayjs'

import AccountManagePanel from '@/components/AccountManagePanel.vue'
import AccountManagePanelPopover from '@/components/AccountManagePanelPopover.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type { AiPerformanceOverview, SystemAccountPrincipalSummary } from '@/types/domain'
import { ACCOUNT_CAPABILITY_DOMAINS, ACCOUNT_CAPABILITY_DOMAIN_LABELS, type AccountCapabilityDomain } from '@/views/accounts/accountCapabilityDomains'

type AiPerformanceAccountFilterItem = {
  account: AiPerformanceOverview['accounts'][number]
  label: string
  color: string
  selected: boolean
  removable: boolean
  isDefault: boolean
}

// 能力域筛选值：'all' 哨兵 = 全部（不下发 domain），其余为四枚举域。
export type CapabilityDomainFilterValue = AccountCapabilityDomain | 'all'

const capabilityDomainOptions: Array<{ label: string; value: CapabilityDomainFilterValue }> = [
  { label: '全部', value: 'all' },
  ...ACCOUNT_CAPABILITY_DOMAINS.map((domain) => ({ label: ACCOUNT_CAPABILITY_DOMAIN_LABELS[domain], value: domain }))
]

const selectedSystemAccountId = defineModel<string>('selectedSystemAccountId', { required: true })
const selectedSystemAccount = defineModel<PrincipalSelection | undefined>('selectedSystemAccount')
const dateRange = defineModel<[Dayjs, Dayjs]>('dateRange', { required: true })
const capabilityDomain = defineModel<CapabilityDomainFilterValue>('capabilityDomain', { required: true })

const props = defineProps<{
  isManagementView: boolean
  loading: boolean
  systemAccounts: SystemAccountPrincipalSummary[]
  systemAccountOptionsLoading: boolean
  disabledDate: (current: Dayjs) => boolean
  accountFilterItems: AiPerformanceAccountFilterItem[]
  accountOptions: Array<{ id: string; name: string; providerCode?: string; providerName?: string }>
  accountsLoading: boolean
  hasActiveAccountFilter: boolean
}>()

// 共享管理面板条目：selected 字段语义即真实可见性（未隐藏任何账户时全部可见）。
const panelCollectionItems = computed(() => props.accountFilterItems.map((item) => ({
  id: item.account.id,
  label: item.label,
  color: item.color,
  visible: item.selected,
  removable: item.removable,
  isDefault: item.isDefault
})))
const accountCountText = computed(() => `${props.accountFilterItems.length}`)

// 手机端：筛选抽屉开合。
// 打点：有生效账户筛选，或指定到具体用户（'all' 是全部用户哨兵）。
const mobileFilterOpen = ref(false)
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
  'capability-domain-change': []
  'account-open-change': [open: boolean]
  'add-account': [id: string]
  'account-search': [value: string]
  reset: []
  refresh: []
  'toggle-account': [id: string]
  'remove-account': [id: string]
  'reset-accounts': []
}>()
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

.ai-performance-domain-select {
  width: 120px;
}

/* 手机端紧凑条（桌面隐藏） */
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
}

@media (min-width: 901px) {
  .ai-performance-mobile-filter-drawer {
    display: none;
  }
}
</style>

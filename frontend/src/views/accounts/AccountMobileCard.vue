<template>
  <article class="account-mobile-card">
    <div class="account-mobile-card-head">
      <a-checkbox :checked="selected" :disabled="!canSelect" @change="$emit('toggle-selection')" />
      <div class="account-mobile-card-title">
        <div class="account-mobile-name-row">
          <span class="account-mobile-name">{{ account.name }}</span>
          <a-tooltip v-if="isAuthorizedAccount(account)">
            <template #title>
              <span class="authorized-tooltip-text">{{ authorizedTooltip }}</span>
            </template>
            <InfoCircleOutlined class="authorized-account-icon" :class="authorizedIconClass" />
          </a-tooltip>
        </div>
        <div class="account-mobile-tags">
          <a-tag color="processing">{{ accountTypeText(account.type) }}</a-tag>
          <a-tag color="geekblue">{{ providerName }}</a-tag>
          <a-tooltip :title="accountScheduleSummary(account.availabilitySchedule)">
            <a-tag :color="accountScheduleTagColor(account.availabilitySchedule)">{{ accountScheduleSummary(account.availabilitySchedule) }}</a-tag>
          </a-tooltip>
          <AccountStatusTag :account="account" />
        </div>
      </div>
    </div>

    <div class="account-mobile-meta-grid">
      <div v-if="isManagementView" class="account-mobile-meta-item">
        <span>系统账户</span>
        <strong>{{ account.systemAccountName || '-' }}</strong>
      </div>
      <div class="account-mobile-meta-item">
        <span>加入分组</span>
        <strong>{{ groupName || '未加入' }}</strong>
      </div>
      <div class="account-mobile-meta-item">
        <span>代理</span>
        <a-tooltip v-if="proxyText" :title="proxyTooltip">
          <strong :class="proxyToneClass">{{ proxyText }}</strong>
        </a-tooltip>
        <strong v-else>不使用</strong>
      </div>
      <div class="account-mobile-meta-item">
        <span>实时并发</span>
        <a-tooltip :title="concurrencyTooltip">
          <strong>{{ concurrencyText }}</strong>
        </a-tooltip>
      </div>
      <div class="account-mobile-meta-item">
        <span>优先级</span>
        <strong>{{ priorityText }}</strong>
      </div>
      <div class="account-mobile-meta-item account-mobile-meta-wide">
        <span>用量(日)</span>
        <AccountUsageCell :account="account" />
      </div>
      <div class="account-mobile-meta-item">
        <span>最近使用</span>
        <strong>{{ formatDateTime(accountLastUsedAt(account)) }}</strong>
      </div>
      <div v-if="accountDisplayExpiresAt(account)" class="account-mobile-meta-item account-mobile-meta-wide">
        <span>到期时间</span>
        <strong :class="isAccountDisplayExpired(account) ? 'expired-cell' : ''">{{ formatDateTime(accountDisplayExpiresAt(account)) }}</strong>
      </div>
      <div class="account-mobile-meta-item account-mobile-meta-wide">
        <span>说明</span>
        <strong>{{ account.notes || '-' }}</strong>
      </div>
    </div>

    <div class="account-mobile-card-actions">
      <template v-if="isAuthorizedAccount(account)">
        <RowActions variant="button" :actions="authorizedActions" :more-actions="menuItems" @action-click="handleActionClick" />
      </template>
      <template v-else>
        <RowActions variant="button" :actions="actions" :more-actions="moreActions" @action-click="handleActionClick" />
      </template>
    </div>
  </article>
</template>

<script setup lang="ts">
import { InfoCircleOutlined } from '@ant-design/icons-vue'
import { computed } from 'vue'

import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import type { AccountSummary, ProxyProfileOptionSummary } from '@/types/domain'
import AccountStatusTag from './AccountStatusTag.vue'
import AccountUsageCell from './AccountUsageCell.vue'
import type { AccountMenuItem } from './accountActionTypes'
import {
  accountLastUsedAt,
  accountDisplayExpiresAt,
  accountTypeText,
  formatDateTime,
  isAccountDisplayExpired,
  isAuthorizedAccount
} from './accountFormatters'
import { accountScheduleSummary, accountScheduleTagColor } from './accountAvailabilitySchedule'
import { accountMenuItemsWithClone, authorizedAccountSourceToneClass } from './accountRules'

const props = defineProps<{
  account: AccountSummary
  authorizedTooltip: string
  canClone: boolean
  canDelete: boolean
  canEdit: boolean
  canSelect: boolean
  groupName?: string
  isManagementView: boolean
  menuItems: AccountMenuItem[]
  providerName: string
  proxy?: ProxyProfileOptionSummary
  selected: boolean
}>()

const emit = defineEmits<{
  (event: 'clone'): void
  (event: 'delete'): void
  (event: 'edit'): void
  (event: 'bind-group'): void
  (event: 'menu-click', menuEvent: { key: string | number }): void
  (event: 'test'): void
  (event: 'toggle-selection'): void
}>()

const proxyText = computed(() => {
  if (!props.account.proxyProfileId) return ''
  return props.proxy?.name ?? '代理已配置'
})
const authorizedIconClass = computed(() => authorizedAccountSourceToneClass(props.account))
const proxyTooltip = computed(() => {
  if (props.account.proxyProfileErrorMessage) return props.account.proxyProfileErrorMessage
  if (props.account.proxyProfileUnavailable) return '代理不可用，请到代理管理确认配置'
  if (props.proxy?.enabled === false) return '代理已停用，请启用代理或更换账户代理'
  if (props.proxy) return `${props.proxy.name}（${props.proxy.type}）`
  return '代理配置不存在或当前不可见'
})
const proxyToneClass = computed(() => (props.account.proxyProfileUnavailable || props.proxy?.enabled === false ? 'proxy-error' : ''))
const priorityText = computed(() => {
  if (!props.account.fallbackEnabled) return String(props.account.priority)
  return `${props.account.priority} / ${props.account.status === 'active' && props.account.schedulable ? '备用' : '备用暂停'}`
})
const concurrencyAvailable = computed(() => props.account.currentConcurrencyAvailable !== false)
const concurrencyText = computed(() => concurrencyAvailable.value ? `${props.account.currentConcurrency}/${props.account.concurrencyLimit}` : '暂不可用')
const concurrencyTooltip = computed(() => concurrencyAvailable.value
  ? `当前正在转发 ${props.account.currentConcurrency} 个请求，配置上限 ${props.account.concurrencyLimit}`
  : '实时并发快照暂不可用')
const actions = computed<RowActionItem[]>(() => {
  const list: RowActionItem[] = []
  if (props.canEdit) {
    list.push({ key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' })
  }
  if (props.canDelete) {
    list.push({
      key: 'delete',
      label: '删除',
      icon: 'delete',
      tone: 'danger',
      confirmTitle: '确认删除这个账户？',
      confirmOkText: '删除'
    })
  }
  return list
})
const moreActions = computed<AccountMenuItem[]>(() => {
  return accountMenuItemsWithClone(props.menuItems, props.canClone)
})
const authorizedActions = computed<RowActionItem[]>(() => {
  const list: RowActionItem[] = []
  if (props.canEdit) {
    list.push({ key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' })
  }
  if (props.canDelete) {
    list.push({
      key: 'delete',
      label: '删除',
      icon: 'delete',
      tone: 'danger',
      confirmTitle: '确认删除这个授权账户？',
      confirmOkText: '删除'
    })
  }
  return list
})

function handleActionClick(key: string) {
  if (key === 'bind-group') {
    emit('bind-group')
    return
  }
  if (key === 'delete') {
    emit('delete')
    return
  }
  if (key === 'edit') {
    emit('edit')
    return
  }
  if (key === 'clone') {
    emit('clone')
    return
  }
  if (key === 'test') {
    emit('test')
    return
  }
  emit('menu-click', { key })
}
</script>

<style scoped>
.account-mobile-card {
  display: grid;
  gap: 12px;
  padding: 14px;
  border: 1px solid #e8edf5;
  border-radius: 14px;
  background: #fff;
}

.account-mobile-card-head {
  display: flex;
  gap: 10px;
  align-items: flex-start;
}

.account-mobile-card-title {
  display: grid;
  min-width: 0;
  flex: 1;
  gap: 8px;
}

.account-mobile-name-row {
  display: flex;
  min-width: 0;
  gap: 6px;
  align-items: center;
}

.account-mobile-name {
  min-width: 0;
  overflow: hidden;
  color: #0f172a;
  font-weight: 700;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.account-mobile-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.account-mobile-tags :deep(.ant-tag) {
  margin-inline-end: 0;
}

.account-mobile-meta-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}

.account-mobile-meta-item {
  display: grid;
  min-width: 0;
  gap: 2px;
  padding: 8px 10px;
  border-radius: 10px;
  background: #f8fafc;
}

.account-mobile-meta-wide {
  grid-column: 1 / -1;
}

.account-mobile-meta-item span {
  color: #64748b;
  font-size: 12px;
}

.account-mobile-meta-item strong {
  min-width: 0;
  overflow: hidden;
  color: #0f172a;
  font-size: 13px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.account-mobile-card-actions {
  display: block;
}

.account-mobile-card-actions :deep(.ant-btn),
.account-mobile-card-actions :deep(.ant-dropdown-trigger),
.account-mobile-card-actions :deep(.ant-popconfirm-open) {
  width: 100%;
}

.authorized-account-icon {
  flex: none;
  color: #1677ff;
  font-size: 14px;
}

.authorized-account-icon.source-warning {
  color: #fa8c16;
}

.authorized-account-icon.source-danger {
  color: #cf1322;
}

.authorized-tooltip-text {
  white-space: pre-line;
}

.expired-cell {
  color: #cf1322;
}

.proxy-error {
  color: #dc2626 !important;
}
</style>

<template>
  <article class="account-mobile-card">
    <div class="account-mobile-card-head">
      <a-checkbox :checked="selected" :disabled="!canSelect" @change="$emit('toggle-selection')" />
      <div class="account-mobile-card-title">
        <div class="account-mobile-name-row">
          <span class="account-mobile-name">{{ accountDisplayName(account) }}</span>
          <span v-if="isAuthorizedAccount(account)" class="authorized-account-badge">（{{ authorizedAccountOwnerBadgeText(account) }}）</span>
          <a-tooltip v-if="isAuthorizedAccount(account)">
            <template #title>
              <span class="authorized-account-tooltip-text">{{ authorizedAccountTooltip(account) }}</span>
            </template>
            <InfoCircleOutlined class="authorized-account-icon" :class="authorizedAccountSourceToneClass(account)" />
          </a-tooltip>
        </div>
        <div class="account-mobile-tags">
          <a-tag color="processing">{{ accountTypeText(account.type) }}</a-tag>
          <a-tag color="geekblue">{{ providerName }}</a-tag>
          <a-tooltip :title="accountScheduleSummary(account.availabilitySchedule)">
            <a-tag :color="accountScheduleTagColor(account.availabilitySchedule)">{{ accountScheduleSummary(account.availabilitySchedule) }}</a-tag>
          </a-tooltip>
          <AccountStatusTag :account="account" />
          <a-tag
            v-for="tag in account.tags ?? []"
            :key="tag.id || tag.name"
            class="account-mobile-tag-chip"
            color="blue"
          >
            {{ tag.name }}
          </a-tag>
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
      <div class="account-mobile-meta-item">
        <span>用量(日)</span>
        <AccountUsageCell :account="account" :refreshing="balanceRefreshing" @refresh-balance="$emit('refresh-balance')" />
      </div>
      <div v-if="accountDisplayExpiresAt(account)" class="account-mobile-meta-item account-mobile-meta-wide">
        <span>到期时间</span>
        <strong :class="isAccountDisplayExpired(account) ? 'expired-cell' : ''">{{ formatDateTime(accountDisplayExpiresAt(account)) }}</strong>
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
  accountDisplayName,
  accountDisplayExpiresAt,
  accountTypeText,
  isAccountDisplayExpired
} from './accountBasicFormatters'
import { formatDateTime, isAuthorizedAccount } from './accountFormatters'
import { accountScheduleSummary, accountScheduleTagColor } from './accountAvailabilitySchedule'
import { accountMenuItemsWithClone, authorizedAccountOwnerBadgeText, authorizedAccountSourceToneClass, authorizedAccountTooltip, canReturnAuthorizedAccount } from './accountRules'

const props = defineProps<{
  account: AccountSummary
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
  balanceRefreshing?: boolean
}>()

const emit = defineEmits<{
  (event: 'clone'): void
  (event: 'delete'): void
  (event: 'edit'): void
  (event: 'bind-group'): void
  (event: 'menu-click', menuEvent: { key: string | number }): void
  (event: 'return-authorization'): void
  (event: 'refresh-balance'): void
  (event: 'test'): void
  (event: 'toggle-selection'): void
}>()

const proxyText = computed(() => {
  if (!props.account.proxyProfileId) return ''
  return props.proxy?.name ?? '代理已配置'
})
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
      confirmTitle: `确认删除账户 ${accountDisplayName(props.account)}？`,
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
  if (canReturnAuthorizedAccount(props.account)) {
    list.push({
      key: 'return-authorization',
      label: '归还',
      icon: 'revoke',
      tone: 'danger',
      confirmTitle: '确认归还这个授权账户？归还后你将不再看到或使用它，不影响授权方原账户。',
      confirmOkText: '归还'
    })
  }
  return list
})

function handleActionClick(key: string) {
  if (key === 'bind-group') {
    emit('bind-group')
    return
  }
  if (key === 'return-authorization') {
    emit('return-authorization')
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
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  color: #0f172a;
  font-weight: 700;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.authorized-account-badge {
  flex: 0 1 auto;
  min-width: 0;
  overflow: hidden;
  color: #64748b;
  font-size: 12px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.authorized-account-icon {
  flex: none;
  color: #08979c;
  cursor: help;
  font-size: 14px;
}

.authorized-account-icon.source-danger {
  color: #cf1322;
}

.authorized-account-icon.source-warning {
  color: #d48806;
}

.authorized-account-tooltip-text {
  white-space: pre-line;
}

.account-mobile-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.account-mobile-tags :deep(.ant-tag) {
  margin-inline-end: 0;
}

.account-mobile-tag-chip {
  max-width: 100%;
  margin-inline-end: 0;
  overflow: hidden;
  text-overflow: ellipsis;
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

.expired-cell {
  color: #cf1322;
}

.proxy-error {
  color: #dc2626 !important;
}
</style>

<template>
  <article class="account-mobile-card">
    <div class="account-mobile-card-head">
      <a-checkbox :checked="selected" :disabled="!canEdit" @change="$emit('toggle-selection')" />
      <div class="account-mobile-card-title">
        <div class="account-mobile-name-row">
          <span class="account-mobile-name">{{ account.name }}</span>
          <a-tooltip v-if="isAuthorizedAccount(account)" :title="authorizedTooltip">
            <InfoCircleOutlined class="authorized-account-icon" :class="{ 'owner-disabled': isOwnerDisabledAuthorizedAccount(account) }" />
          </a-tooltip>
        </div>
        <div class="account-mobile-tags">
          <a-tag color="processing">{{ accountTypeText(account.type) }}</a-tag>
          <a-tag color="geekblue">{{ providerName }}</a-tag>
          <AccountStatusTag :account="account" />
        </div>
      </div>
    </div>

    <div class="account-mobile-meta-grid">
      <div v-if="isManagementView" class="account-mobile-meta-item">
        <span>系统账户</span>
        <strong>{{ account.systemAccountName || account.systemAccountId || '-' }}</strong>
      </div>
      <div class="account-mobile-meta-item">
        <span>归属分组</span>
        <strong>{{ groupName || '未归属' }}</strong>
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
        <strong>{{ account.currentConcurrency }}/{{ account.concurrencyLimit }}</strong>
      </div>
      <div class="account-mobile-meta-item">
        <span>优先级</span>
        <strong>{{ account.fallbackEnabled ? `${account.priority} / 备用` : account.priority }}</strong>
      </div>
      <div class="account-mobile-meta-item account-mobile-meta-wide">
        <span>用量(日)</span>
        <AccountUsageCell :account="account" />
      </div>
      <div class="account-mobile-meta-item">
        <span>最近使用</span>
        <strong>{{ formatDateTime(accountLastUsedAt(account)) }}</strong>
      </div>
      <div v-if="account.accountExpiresAt" class="account-mobile-meta-item account-mobile-meta-wide">
        <span>到期时间</span>
        <strong :class="isAccountPackageExpired(account) ? 'expired-cell' : ''">{{ formatDateTime(account.accountExpiresAt) }}</strong>
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
        <RowActions variant="button" :actions="actions" :more-actions="menuItems" @action-click="handleActionClick" />
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
  accountTypeText,
  formatDateTime,
  isAccountPackageExpired,
  isAuthorizedAccount,
  isOwnerDisabledAuthorizedAccount
} from './accountFormatters'

const props = defineProps<{
  account: AccountSummary
  authorizedTooltip: string
  canDelete: boolean
  canEdit: boolean
  groupName?: string
  isManagementView: boolean
  menuItems: AccountMenuItem[]
  providerName: string
  proxy?: ProxyProfileOptionSummary
  selected: boolean
}>()

const emit = defineEmits<{
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
const proxyTooltip = computed(() => {
  if (props.account.proxyProfileErrorMessage) return props.account.proxyProfileErrorMessage
  if (props.account.proxyProfileUnavailable) return '代理不可用，请到代理管理确认配置'
  if (props.proxy?.enabled === false) return '代理已停用，请启用代理或更换账户代理'
  if (props.proxy) return `${props.proxy.name}（${props.proxy.type}）`
  return '代理配置不存在或当前不可见'
})
const proxyToneClass = computed(() => (props.account.proxyProfileUnavailable || props.proxy?.enabled === false ? 'proxy-error' : ''))
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
const authorizedActions = computed<RowActionItem[]>(() => {
  const list: RowActionItem[] = []
  if (props.account.status !== 'disabled') {
    list.push({ key: 'test', label: '测试', icon: 'test', tone: 'info' })
  }
  list.push({ key: 'bind-group', label: props.groupName ? '调整分组' : '绑定分组', icon: 'bind', tone: 'purple' })
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
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 8px;
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

.authorized-account-icon.owner-disabled {
  color: #fa8c16;
}

.expired-cell {
  color: #cf1322;
}

.proxy-error {
  color: #dc2626 !important;
}
</style>

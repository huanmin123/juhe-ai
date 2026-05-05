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
      <div v-if="isAdmin" class="account-mobile-meta-item">
        <span>系统账户</span>
        <strong>{{ account.systemAccountName || account.systemAccountId || '-' }}</strong>
      </div>
      <div class="account-mobile-meta-item">
        <span>归属分组</span>
        <strong>{{ groupName || '未归属' }}</strong>
      </div>
      <div class="account-mobile-meta-item">
        <span>并发</span>
        <strong>{{ account.currentConcurrency }}/{{ account.concurrencyLimit }}</strong>
      </div>
      <div class="account-mobile-meta-item">
        <span>优先级</span>
        <strong>{{ account.priority }}</strong>
      </div>
      <div class="account-mobile-meta-item">
        <span>用量(日)</span>
        <strong>{{ formatAccountUsageSummary(account.todayUsage) }}</strong>
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
        <a-button @click="$emit('test')">测试</a-button>
        <a-button type="primary" @click="$emit('bind-group')">{{ groupName ? '调整分组' : '绑定分组' }}</a-button>
      </template>
      <template v-else>
        <a-button v-if="canEdit" type="primary" @click="$emit('edit')">编辑</a-button>
        <a-popconfirm v-if="canDelete" title="确认删除这个账户？" @confirm="$emit('delete')">
          <a-button danger>删除</a-button>
        </a-popconfirm>
        <a-dropdown v-if="menuItems.length">
          <a-button>更多</a-button>
          <template #overlay>
            <a-menu @click="$emit('menu-click', $event)">
              <a-menu-item v-for="item in menuItems" :key="item.key" :danger="item.danger">{{ item.label }}</a-menu-item>
            </a-menu>
          </template>
        </a-dropdown>
      </template>
    </div>
  </article>
</template>

<script setup lang="ts">
import { InfoCircleOutlined } from '@ant-design/icons-vue'

import type { AccountSummary } from '@/types/domain'
import AccountStatusTag from './AccountStatusTag.vue'
import {
  accountLastUsedAt,
  accountTypeText,
  formatAccountUsageSummary,
  formatDateTime,
  isAccountPackageExpired,
  isAuthorizedAccount,
  isOwnerDisabledAuthorizedAccount
} from './accountFormatters'

interface AccountMenuItem {
  key: string
  label: string
  danger?: boolean
}

defineProps<{
  account: AccountSummary
  authorizedTooltip: string
  canDelete: boolean
  canEdit: boolean
  groupName?: string
  isAdmin: boolean
  menuItems: AccountMenuItem[]
  providerName: string
  selected: boolean
}>()

defineEmits<{
  (event: 'delete'): void
  (event: 'edit'): void
  (event: 'bind-group'): void
  (event: 'menu-click', menuEvent: { key: string | number }): void
  (event: 'test'): void
  (event: 'toggle-selection'): void
}>()
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
</style>

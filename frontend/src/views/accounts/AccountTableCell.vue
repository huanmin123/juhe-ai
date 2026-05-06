<template>
  <div v-if="columnKey === 'name'" class="resource-name-cell">
    <span class="resource-name-line">
      <span>{{ account.name }}</span>
      <a-tooltip v-if="isAuthorizedAccount(account)" :title="authorizedTooltip">
        <InfoCircleOutlined class="authorized-account-icon" :class="{ 'owner-disabled': isOwnerDisabledAuthorizedAccount(account) }" />
      </a-tooltip>
    </span>
  </div>
  <a-tag v-else-if="columnKey === 'type'" color="processing">{{ accountTypeText(account.type) }}</a-tag>
  <a-tag v-else-if="columnKey === 'providerCode'" color="geekblue">{{ providerName }}</a-tag>
  <span v-else-if="columnKey === 'systemAccount'" :class="account.systemAccountName ? 'name-cell' : 'muted-cell'">
    {{ account.systemAccountName || account.systemAccountId || '-' }}
  </span>
  <span v-else-if="columnKey === 'notes'" class="account-notes-text">{{ account.notes || '-' }}</span>
  <template v-else-if="columnKey === 'group'">
    <a-tooltip v-if="groupName" :title="groupName">
      <span class="account-group-text">{{ groupName }}</span>
    </a-tooltip>
    <span v-else class="muted-cell">未归属</span>
  </template>
  <AccountStatusTag v-else-if="columnKey === 'status'" :account="account" />
  <a-tag v-else-if="columnKey === 'concurrency'" color="blue">{{ account.currentConcurrency }}/{{ account.concurrencyLimit }}</a-tag>
  <AccountUsageCell v-else-if="columnKey === 'usage'" :account="account" />
  <template v-else-if="columnKey === 'lastUsedAt'">
    {{ formatDateTime(accountLastUsedAt(account)) }}
  </template>
  <span v-else-if="columnKey === 'accountExpiresAt'" :class="isAccountPackageExpired(account) ? 'expired-cell' : 'muted-cell'">
    {{ formatDateTime(account.accountExpiresAt) }}
  </span>
  <AccountRowActions
    v-else-if="columnKey === 'actions'"
    :account="account"
    :can-delete="canDelete"
    :can-edit="canEdit"
    :group-name="groupName"
    :menu-items="menuItems"
    @bind-group="$emit('bind-group', account)"
    @delete="$emit('delete', account)"
    @edit="$emit('edit', account)"
    @menu-click="$emit('menu-click', $event, account)"
    @test="$emit('test', account)"
  />
</template>

<script setup lang="ts">
import { InfoCircleOutlined } from '@ant-design/icons-vue'

import type { AccountSummary } from '@/types/domain'
import AccountRowActions from './AccountRowActions.vue'
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

defineProps<{
  account: AccountSummary
  authorizedTooltip: string
  canDelete: boolean
  canEdit: boolean
  columnKey: string
  groupName?: string
  menuItems: AccountMenuItem[]
  providerName: string
}>()

defineEmits<{
  (event: 'bind-group', account: AccountSummary): void
  (event: 'delete', account: AccountSummary): void
  (event: 'edit', account: AccountSummary): void
  (event: 'menu-click', menuEvent: { key: string | number }, account: AccountSummary): void
  (event: 'test', account: AccountSummary): void
}>()
</script>

<style scoped>
.account-group-text {
  display: block;
  width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.account-notes-text {
  display: block;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.expired-cell {
  color: #dc2626;
}

.resource-name-cell {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.resource-name-line {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.authorized-account-icon {
  color: #08979c;
  cursor: help;
  font-size: 14px;
}

.authorized-account-icon.owner-disabled {
  color: #d48806;
}
</style>

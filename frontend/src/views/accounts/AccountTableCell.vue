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
  <template v-else-if="columnKey === 'proxy'">
    <a-tooltip v-if="proxyText" :title="proxyTooltip">
      <a-tag :color="proxyTagColor">{{ proxyText }}</a-tag>
    </a-tooltip>
    <span v-else class="muted-cell">不使用</span>
  </template>
  <a-tag v-else-if="columnKey === 'concurrency'" color="blue">{{ account.currentConcurrency }}/{{ account.concurrencyLimit }}</a-tag>
  <AccountUsageCell v-else-if="columnKey === 'usage'" :account="account" />
  <span v-else-if="columnKey === 'priority'">{{ account.priority }}</span>
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
import { computed } from 'vue'

import type { AccountSummary, ProxyProfileOptionSummary } from '@/types/domain'
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

defineEmits<{
  (event: 'bind-group', account: AccountSummary): void
  (event: 'delete', account: AccountSummary): void
  (event: 'edit', account: AccountSummary): void
  (event: 'menu-click', menuEvent: { key: string | number }, account: AccountSummary): void
  (event: 'test', account: AccountSummary): void
}>()

const props = defineProps<{
  account: AccountSummary
  authorizedTooltip: string
  canDelete: boolean
  canEdit: boolean
  columnKey: string
  groupName?: string
  menuItems: AccountMenuItem[]
  providerName: string
  proxy?: ProxyProfileOptionSummary
}>()

const proxyText = computed(() => {
  if (!props.account.proxyProfileId) return ''
  return props.proxy?.name ?? '代理已配置'
})
const proxyTagColor = computed(() => {
  if (props.account.proxyProfileUnavailable) return 'red'
  if (props.proxy?.enabled === false) return 'red'
  return props.proxy ? 'cyan' : 'orange'
})
const proxyTooltip = computed(() => {
  if (props.account.proxyProfileErrorMessage) return props.account.proxyProfileErrorMessage
  if (props.account.proxyProfileUnavailable) return '代理不可用，请到代理管理确认配置'
  if (props.proxy?.enabled === false) return '代理已停用，请启用代理或更换账户代理'
  if (props.proxy) return `${props.proxy.name}（${props.proxy.type}）`
  return '代理配置不存在或当前不可见'
})
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

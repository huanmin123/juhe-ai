<template>
  <div v-if="columnKey === 'name'" class="resource-name-cell">
    <span class="resource-name-line">
      <span class="resource-name-text">{{ accountDisplayName(account) }}</span>
      <span v-if="isAuthorizedAccount(account)" class="authorized-account-badge">（{{ authorizedAccountOwnerBadgeText(account) }}）</span>
      <a-tooltip v-if="isAuthorizedAccount(account)">
        <template #title>
          <span class="authorized-account-tooltip-text">{{ authorizedAccountTooltip(account) }}</span>
        </template>
        <InfoCircleOutlined class="authorized-account-icon" :class="authorizedAccountSourceToneClass(account)" />
      </a-tooltip>
    </span>
  </div>
  <a-tag v-else-if="columnKey === 'type'" color="processing">{{ accountTypeText(account.type) }}</a-tag>
  <a-tag v-else-if="columnKey === 'providerCode'" color="geekblue">{{ providerName(account.providerCode) }}</a-tag>
  <span v-else-if="columnKey === 'systemAccount'" :class="account.systemAccountName ? 'name-cell' : 'muted-cell'">
    {{ account.systemAccountName || '-' }}
  </span>
  <span v-else-if="columnKey === 'notes'" class="account-notes-text">{{ account.notes || '-' }}</span>
  <template v-else-if="columnKey === 'group'">
    <a-tooltip v-if="currentGroupName" :title="currentGroupName">
      <span class="account-group-text">{{ currentGroupName }}</span>
    </a-tooltip>
    <span v-else class="muted-cell">未加入</span>
  </template>
  <AccountStatusTag v-else-if="columnKey === 'status'" :account="account" />
  <template v-else-if="columnKey === 'proxy'">
    <a-tooltip v-if="proxyText" :title="proxyTooltip">
      <a-tag :color="proxyTagColor">{{ proxyText }}</a-tag>
    </a-tooltip>
    <span v-else class="muted-cell">不使用</span>
  </template>
  <a-tooltip v-else-if="columnKey === 'concurrency'" :title="concurrencyTooltip">
    <a-tag :color="concurrencyAvailable ? 'blue' : 'default'">{{ concurrencyText }}</a-tag>
  </a-tooltip>
  <AccountUsageCell v-else-if="columnKey === 'usage'" :account="account" />
  <AccountTagsCell v-else-if="columnKey === 'tags'" :account="account" />
  <span v-else-if="columnKey === 'priority'">{{ account.priority }}</span>
  <template v-else-if="columnKey === 'lastUsedAt'">
    {{ formatDateTime(accountLastUsedAt(account)) }}
  </template>
  <span v-else-if="columnKey === 'accountExpiresAt'" :class="isAccountDisplayExpired(account) ? 'expired-cell' : 'muted-cell'">
    {{ formatDateTime(accountDisplayExpiresAt(account)) }}
  </span>
  <template v-else-if="columnKey === 'availabilitySchedule'">
    <a-tooltip :title="accountScheduleSummary(account.availabilitySchedule)">
      <a-tag class="schedule-tag" :color="accountScheduleTagColor(account.availabilitySchedule)">
        {{ accountScheduleSummary(account.availabilitySchedule) }}
      </a-tag>
    </a-tooltip>
  </template>
  <AccountRowActions
    v-else-if="columnKey === 'actions'"
    :account="account"
    :can-clone="canClone(account)"
    :can-delete="canDelete(account)"
    :can-edit="canEdit(account)"
    :group-name="groupName(account.id)"
    :menu-items="menuItems(account)"
    @bind-group="$emit('bind-group', account)"
    @clone="$emit('clone', account)"
    @delete="$emit('delete', account)"
    @edit="$emit('edit', account)"
    @menu-click="$emit('menu-click', $event, account)"
    @return-authorization="$emit('return-authorization', account)"
    @test="$emit('test', account)"
  />
</template>

<script setup lang="ts">
import { InfoCircleOutlined } from '@ant-design/icons-vue'
import { computed } from 'vue'

import type { AccountSummary, ProxyProfileOptionSummary } from '@/types/domain'
import AccountRowActions from './AccountRowActions.vue'
import AccountStatusTag from './AccountStatusTag.vue'
import AccountTagsCell from './AccountTagsCell.vue'
import AccountUsageCell from './AccountUsageCell.vue'
import type { AccountMenuItem } from './accountActionTypes'
import {
  accountDisplayName,
  accountLastUsedAt,
  accountDisplayExpiresAt,
  accountTypeText,
  isAccountDisplayExpired
} from './accountBasicFormatters'
import { formatDateTime, isAuthorizedAccount } from './accountFormatters'
import { accountScheduleSummary, accountScheduleTagColor } from './accountAvailabilitySchedule'
import { authorizedAccountOwnerBadgeText, authorizedAccountSourceToneClass, authorizedAccountTooltip } from './accountRules'

defineEmits<{
  (event: 'bind-group', account: AccountSummary): void
  (event: 'clone', account: AccountSummary): void
  (event: 'delete', account: AccountSummary): void
  (event: 'edit', account: AccountSummary): void
  (event: 'menu-click', menuEvent: { key: string | number }, account: AccountSummary): void
  (event: 'return-authorization', account: AccountSummary): void
  (event: 'test', account: AccountSummary): void
}>()

const props = defineProps<{
  account: AccountSummary
  canClone: (account: AccountSummary) => boolean
  canDelete: (account: AccountSummary) => boolean
  canEdit: (account: AccountSummary) => boolean
  columnKey: string
  groupName: (accountId: string) => string | undefined
  menuItems: (account: AccountSummary) => AccountMenuItem[]
  providerName: (providerCode?: string) => string
  proxy: (proxyProfileId?: string) => ProxyProfileOptionSummary | undefined
}>()

const currentGroupName = computed(() => props.groupName(props.account.id))
const currentProxy = computed(() => props.proxy(props.account.proxyProfileId))

const proxyText = computed(() => {
  if (!props.account.proxyProfileId) return ''
  return currentProxy.value?.name ?? '代理已配置'
})
const proxyTagColor = computed(() => {
  if (props.account.proxyProfileUnavailable) return 'red'
  if (currentProxy.value?.enabled === false) return 'red'
  return currentProxy.value ? 'cyan' : 'orange'
})
const proxyTooltip = computed(() => {
  if (props.account.proxyProfileErrorMessage) return props.account.proxyProfileErrorMessage
  if (props.account.proxyProfileUnavailable) return '代理不可用，请到代理管理确认配置'
  if (currentProxy.value?.enabled === false) return '代理已停用，请启用代理或更换账户代理'
  if (currentProxy.value) return `${currentProxy.value.name}（${currentProxy.value.type}）`
  return '代理配置不存在或当前不可见'
})
const concurrencyAvailable = computed(() => props.account.currentConcurrencyAvailable !== false)
const concurrencyText = computed(() => concurrencyAvailable.value ? `${props.account.currentConcurrency}/${props.account.concurrencyLimit}` : '暂不可用')
const concurrencyTooltip = computed(() => concurrencyAvailable.value
  ? `当前正在转发 ${props.account.currentConcurrency} 个请求，配置上限 ${props.account.concurrencyLimit}`
  : '实时并发快照暂不可用')
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

.schedule-tag {
  max-width: 100%;
  margin-inline-end: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
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
  max-width: 100%;
}

.resource-name-text {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.authorized-account-badge {
  flex: 0 1 auto;
  min-width: 0;
  overflow: hidden;
  color: #64748b;
  font-size: 12px;
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

</style>

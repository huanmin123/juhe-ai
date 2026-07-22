<template>
  <ResponsiveDataList
    class="account-responsive-list"
    table-class="account-table"
    :columns="columns"
    :data-source="accounts"
    :mobile-data-source="mobileAccounts"
    row-key="id"
    :loading="loading"
    :scroll-x="tableScrollX"
    :table-scroll-y="tableScrollY"
    :pagination="pagination"
    :row-selection="rowSelection"
    mobile-pagination
    pull-refresh-enabled
    :mobile-has-more="mobileHasMore"
    :loading-more="loadingMore"
    :refreshing="refreshing"
    @change="(...args) => $emit('change', ...args)"
    @sort-change="$emit('sort-change', $event)"
    @mobile-load-more="$emit('mobile-load-more')"
    @mobile-refresh="$emit('mobile-refresh')"
  >
    <template #emptyText>
      <a-empty class="page-empty-card" description="还没有账户。点击「添加账户」，再选择供应商和账户类型。" />
    </template>
    <template #bodyCell="{ column, record }">
      <AccountTableCell
        :account="record"
        :can-clone="canClone"
        :can-delete="canDelete"
        :can-edit="canEdit"
        :column-key="tableColumnKey(column)"
        :group-name="groupName"
        :menu-items="menuItems"
        :provider-name="providerName"
        :proxy="proxy"
        :balance-refreshing="balanceRefreshingIds.has(record.id)"
        @clone="$emit('clone', $event)"
        @delete="$emit('delete', $event.id)"
        @edit="$emit('edit', $event)"
        @menu-click="$emit('menu-click', $event, record)"
        @return-authorization="$emit('return-authorization', $event.id)"
        @refresh-balance="$emit('refresh-balance', $event)"
        @test="$emit('test', $event)"
      />
    </template>
    <template #card="{ record }">
      <AccountMobileCard
        :account="record"
        :can-clone="canClone(record)"
        :can-delete="canDelete(record)"
        :can-edit="canEdit(record)"
        :can-select="canSelect(record)"
        :group-name="groupName(record.id)"
        :is-management-view="isManagementView"
        :menu-items="menuItems(record)"
        :provider-name="providerName(record.providerCode)"
        :proxy="proxy(record.proxyProfileId)"
        :selected="isSelected(record.id)"
        :balance-refreshing="balanceRefreshingIds.has(record.id)"
        @delete="$emit('delete', record.id)"
        @clone="$emit('clone', record)"
        @edit="$emit('edit', record)"
        @menu-click="$emit('menu-click', $event, record)"
        @return-authorization="$emit('return-authorization', record.id)"
        @refresh-balance="$emit('refresh-balance', record.id)"
        @test="$emit('test', record)"
        @toggle-selection="$emit('toggle-selection', record)"
      />
    </template>
  </ResponsiveDataList>
</template>

<script setup lang="ts">
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import type { ResponsiveDataListSort } from '@/components/responsiveDataListSorting'
import type { AccountSummary, ProxyProfileOptionSummary } from '@/types/domain'
import AccountMobileCard from './AccountMobileCard.vue'
import AccountTableCell from './AccountTableCell.vue'
import type { AccountMenuItem } from './accountActionTypes'
import { tableColumnKey } from './accountTableColumns'

defineProps<{
  accounts: AccountSummary[]
  canClone: (account: AccountSummary) => boolean
  canDelete: (account: AccountSummary) => boolean
  canEdit: (account: AccountSummary) => boolean
  canSelect: (account: AccountSummary) => boolean
  columns: Array<Record<string, unknown>>
  groupName: (accountId: string) => string | undefined
  isManagementView: boolean
  isSelected: (accountId: string) => boolean
  loading: boolean
  loadingMore: boolean
  menuItems: (account: AccountSummary) => AccountMenuItem[]
  mobileAccounts: AccountSummary[]
  mobileHasMore: boolean
  pagination: Record<string, unknown>
  providerName: (providerCode?: string) => string
  proxy: (proxyProfileId?: string) => ProxyProfileOptionSummary | undefined
  refreshing: boolean
  rowSelection: Record<string, unknown>
  tableScrollX: number
  tableScrollY: string
  balanceRefreshingIds: Set<string>
}>()

defineEmits<{
  (event: 'change', ...args: unknown[]): void
  (event: 'clone', account: AccountSummary): void
  (event: 'delete', accountId: string): void
  (event: 'edit', account: AccountSummary): void
  (event: 'menu-click', menuEvent: { key: string | number }, account: AccountSummary): void
  (event: 'mobile-load-more'): void
  (event: 'mobile-refresh'): void
  (event: 'return-authorization', accountId: string): void
  (event: 'refresh-balance', accountId: string): void
  (event: 'sort-change', sorts: ResponsiveDataListSort[]): void
  (event: 'test', account: AccountSummary): void
  (event: 'toggle-selection', account: AccountSummary): void
}>()
</script>

<style scoped>
.account-table {
  border: 1px solid #e8edf5;
  border-radius: 14px;
}

.account-table :deep(.ant-table-tbody > tr > td) {
  vertical-align: middle;
}

.account-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.account-table :deep(.ant-empty) {
  margin: 12px 0;
}

@media (max-width: 900px) {
  .account-table :deep(.ant-table-cell-fix-right),
  .account-table :deep(.ant-table-cell-fix-right-first),
  .account-table :deep(.ant-table-cell-fix-right-last) {
    position: static !important;
    box-shadow: none !important;
  }
}
</style>

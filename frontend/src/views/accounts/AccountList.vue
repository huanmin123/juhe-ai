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
    @change="$emit('change', $event)"
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
        :authorized-tooltip="authorizedTooltip(record)"
        :can-delete="canDelete(record)"
        :can-edit="canEdit(record)"
        :column-key="tableColumnKey(column)"
        :group-name="groupName(record.id)"
        :menu-items="menuItems(record)"
        :provider-name="providerName(record.providerCode)"
        @bind-group="$emit('bind-group', $event)"
        @delete="$emit('delete', $event.id)"
        @edit="$emit('edit', $event)"
        @menu-click="$emit('menu-click', $event, record)"
        @test="$emit('test', $event)"
      />
    </template>
    <template #card="{ record }">
      <AccountMobileCard
        :account="record"
        :authorized-tooltip="authorizedTooltip(record)"
        :can-delete="canDelete(record)"
        :can-edit="canEdit(record)"
        :group-name="groupName(record.id)"
        :is-management-view="isManagementView"
        :menu-items="menuItems(record)"
        :provider-name="providerName(record.providerCode)"
        :selected="isSelected(record.id)"
        @delete="$emit('delete', record.id)"
        @edit="$emit('edit', record)"
        @bind-group="$emit('bind-group', record)"
        @menu-click="$emit('menu-click', $event, record)"
        @test="$emit('test', record)"
        @toggle-selection="$emit('toggle-selection', record)"
      />
    </template>
  </ResponsiveDataList>
</template>

<script setup lang="ts">
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import type { ResponsiveDataListSort } from '@/components/responsiveDataListSorting'
import type { AccountSummary } from '@/types/domain'
import AccountMobileCard from './AccountMobileCard.vue'
import AccountTableCell from './AccountTableCell.vue'
import type { AccountMenuItem } from './accountActionTypes'
import { tableColumnKey } from './accountTableColumns'

defineProps<{
  accounts: AccountSummary[]
  authorizedTooltip: (account: AccountSummary) => string
  canDelete: (account: AccountSummary) => boolean
  canEdit: (account: AccountSummary) => boolean
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
  refreshing: boolean
  rowSelection: Record<string, unknown>
  tableScrollX: number
  tableScrollY: string
}>()

defineEmits<{
  (event: 'bind-group', account: AccountSummary): void
  (event: 'change', paginationInfo: unknown): void
  (event: 'delete', accountId: string): void
  (event: 'edit', account: AccountSummary): void
  (event: 'menu-click', menuEvent: { key: string | number }, account: AccountSummary): void
  (event: 'mobile-load-more'): void
  (event: 'mobile-refresh'): void
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

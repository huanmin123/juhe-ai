import { z } from 'zod'

import { queryTextList } from '../../shared/query-values.js'
import { type AccountListOptions, listAccountsPage, listAccountsPageAsync } from '../../storage/repositories.js'
import { accountImportMaxAccounts } from './account-import.service.js'
import { exportAccountsAsImportDocument, exportAccountsAsImportDocumentAsync } from './account-export.service.js'
import { accountListSortFieldValues, schedulableQueryValue, statusQueryValue } from './account-list-query.js'
import type { RequestAccessScope } from '../auth/request-context.js'

export const accountExportFilterSchema = z.object({
  sorts: z.array(z.object({
    field: z.enum(accountListSortFieldValues),
    order: z.enum(['asc', 'desc'])
  }).strict()).max(accountListSortFieldValues.length).optional(),
  keyword: z.string().trim().max(200).optional(),
  providerCode: z.string().trim().max(80).optional(),
  groupId: z.string().trim().max(120).optional(),
  tagIds: z.union([
    z.string().trim(),
    z.array(z.string().trim()).max(100)
  ]).optional(),
  type: z.string().trim().max(80).optional(),
  status: z.union([
    z.string().trim(),
    z.array(z.string().trim()).max(20)
  ]).optional(),
  schedulable: z.enum(['all', 'enabled', 'disabled', 'cooling']).optional()
}).strict()

export const accountExportByIdsRequestSchema = z.object({
  accountIds: z.array(z.string().trim().min(1)).min(1).max(accountImportMaxAccounts)
}).strict()

export const accountExportByFiltersRequestSchema = z.object({
  filters: accountExportFilterSchema
}).strict()

export const accountExportRequestSchema = z.union([
  accountExportByIdsRequestSchema,
  accountExportByFiltersRequestSchema
])

export type AccountExportRequest = z.infer<typeof accountExportRequestSchema>

export function exportAccountsForRequest(request: AccountExportRequest, access: RequestAccessScope) {
  if ('accountIds' in request) {
    return exportAccountsAsImportDocument({ accountIds: request.accountIds }, access)
  }

  const page = listAccountsPage(access, accountExportListOptions(request.filters))
  const accountIds = page.items.map((account) => account.id)
  if (!accountIds.length) {
    throw new Error('当前筛选条件下没有匹配的 AI 账户')
  }
  return exportAccountsAsImportDocument({
    accountIds,
    matchedAccounts: page.total,
    truncated: page.hasMore
  }, access)
}

export async function exportAccountsForRequestAsync(request: AccountExportRequest, access: RequestAccessScope) {
  if ('accountIds' in request) {
    return await exportAccountsAsImportDocumentAsync({ accountIds: request.accountIds }, access)
  }

  const page = await listAccountsPageAsync(access, accountExportListOptions(request.filters))
  const accountIds = page.items.map((account) => account.id)
  if (!accountIds.length) {
    throw new Error('当前筛选条件下没有匹配的 AI 账户')
  }
  return await exportAccountsAsImportDocumentAsync({
    accountIds,
    matchedAccounts: page.total,
    truncated: page.hasMore
  }, access)
}

export function accountExportListOptions(filters: z.infer<typeof accountExportFilterSchema>): AccountListOptions {
  return {
    sorts: filters.sorts,
    page: 1,
    pageSize: accountImportMaxAccounts,
    keyword: accountExportTextFilter(filters.keyword),
    providerCode: accountExportAllFilter(filters.providerCode),
    groupId: accountExportTextFilter(filters.groupId),
    tagIds: queryTextList(filters.tagIds, 100),
    type: accountExportAllFilter(filters.type),
    status: statusQueryValue(filters.status),
    schedulable: schedulableQueryValue(filters.schedulable)
  }
}

function accountExportTextFilter(value: string | undefined): string | undefined {
  const text = value?.trim()
  return text || undefined
}

function accountExportAllFilter(value: string | undefined): string | undefined {
  const text = accountExportTextFilter(value)
  return text && text !== 'all' ? text : undefined
}

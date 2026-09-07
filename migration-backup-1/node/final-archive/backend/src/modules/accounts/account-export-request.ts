import { z } from 'zod'

import { queryTextList } from '../../shared/query-values.js'
import { type AccountListOptions, listAccountsPage, listAccountsPageAsync } from '../../storage/repositories.js'
import { accountExportMaxAccounts, exportAccountsAsImportDocument, exportAccountsAsImportDocumentAsync } from './account-export.service.js'
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
  accountIds: z.array(z.string().trim().min(1)).min(1).max(accountExportMaxAccounts)
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

  const matched = collectAccountExportIds(access, request.filters)
  const accountIds = matched.accountIds
  if (!accountIds.length) {
    throw new Error('当前筛选条件下没有匹配的 AI 账户')
  }
  return exportAccountsAsImportDocument({
    accountIds,
    matchedAccounts: matched.matchedAccounts
  }, access)
}

export async function exportAccountsForRequestAsync(request: AccountExportRequest, access: RequestAccessScope) {
  if ('accountIds' in request) {
    return await exportAccountsAsImportDocumentAsync({ accountIds: request.accountIds }, access)
  }

  const matched = await collectAccountExportIdsAsync(access, request.filters)
  const accountIds = matched.accountIds
  if (!accountIds.length) {
    throw new Error('当前筛选条件下没有匹配的 AI 账户')
  }
  return await exportAccountsAsImportDocumentAsync({
    accountIds,
    matchedAccounts: matched.matchedAccounts
  }, access)
}

export function assertAccountExportMatchCount(total: number): void {
  if (total > accountExportMaxAccounts) {
    throw new Error(`当前筛选匹配 ${total} 个 AI 账户，超过单次导出上限 ${accountExportMaxAccounts} 个，请先筛选或分批次导出`)
  }
}

interface AccountExportMatchedIds {
  accountIds: string[]
  matchedAccounts: number
}

const accountExportListPageSize = 200

function collectAccountExportIds(
  access: RequestAccessScope,
  filters: z.infer<typeof accountExportFilterSchema>
): AccountExportMatchedIds {
  const accountIds: string[] = []
  let page = 1
  while (true) {
    const result = listAccountsPage(access, accountExportListOptions(filters, page))
    accountIds.push(...result.items.map((account) => account.id))
    assertAccountExportMatchCount(accountIds.length)
    if (!result.hasMore) break
    page += 1
  }
  return { accountIds, matchedAccounts: accountIds.length }
}

async function collectAccountExportIdsAsync(
  access: RequestAccessScope,
  filters: z.infer<typeof accountExportFilterSchema>
): Promise<AccountExportMatchedIds> {
  const accountIds: string[] = []
  let page = 1
  while (true) {
    const result = await listAccountsPageAsync(access, accountExportListOptions(filters, page))
    accountIds.push(...result.items.map((account) => account.id))
    assertAccountExportMatchCount(accountIds.length)
    if (!result.hasMore) break
    page += 1
  }
  return { accountIds, matchedAccounts: accountIds.length }
}

export function accountExportListOptions(
  filters: z.infer<typeof accountExportFilterSchema>,
  page = 1
): AccountListOptions {
  return {
    sorts: filters.sorts,
    page,
    pageSize: accountExportListPageSize,
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

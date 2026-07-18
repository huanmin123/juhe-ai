import { randomUUID } from 'node:crypto'

import type { UsageRecordInput } from '../../storage/usage-records.repository.js'
import { listAccountAuthorizationGranteeIdsAsync } from '../../storage/resource-authorization-read.repository.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import type {
  PageDataChangeEvent,
  PageDataChangeOperation
} from './page-data-change.service.js'
import { PAGE_DATA_MAX_EVENT_OWNERS } from './page-data-change.service.js'
import { publishPageDataChange } from './page-data-change.runtime.js'

type Schedule = (callback: () => Promise<void>, delayMs: number) => unknown
type Cancel = (handle: unknown) => void

export interface AccountPageDataChangeInput {
  accountId: string
  ownerSystemAccountIds: string[]
  operation?: Extract<PageDataChangeOperation, 'upsert' | 'delete'>
  fieldMask?: string[]
  membershipChanged?: boolean
  orderChanged?: boolean
  filterChanged?: boolean
  pageChanged?: boolean
  allScopes?: boolean
}

export interface AnnouncementPageDataChangeInput {
  announcementId: string
  operation: Extract<PageDataChangeOperation, 'upsert' | 'delete'>
  fieldMask?: string[]
}

export interface PageDataChangePublisher {
  publishAccountStatic(input: AccountPageDataChangeInput): Promise<void>
  publishAccountRuntime(input: AccountPageDataChangeInput): Promise<void>
  publishUsageRecords(inputs: Array<Pick<UsageRecordInput, 'id' | 'systemAccountId'>>): void
  flushUsageRecords(): Promise<void>
  publishAnnouncementPublic(input: AnnouncementPageDataChangeInput): Promise<void>
  publishDomainReset(domain: 'accounts.static' | 'accounts.runtime' | 'usage.records', ownerSystemAccountIds: string[], allScopes?: boolean): Promise<void>
}

export function createPageDataChangePublisher(options: {
  publish: (event: PageDataChangeEvent) => Promise<void>
  now?: () => Date
  usageThrottleMs?: number
  schedule?: Schedule
  cancel?: Cancel
}): PageDataChangePublisher {
  const now = options.now ?? (() => new Date())
  const usageThrottleMs = Math.max(1, Math.trunc(options.usageThrottleMs ?? 250))
  const schedule = options.schedule ?? ((callback, delayMs) => setTimeout(() => void callback(), delayMs))
  const cancel = options.cancel ?? ((handle) => clearTimeout(handle as NodeJS.Timeout))
  const pendingUsageOwnerIds = new Set<string>()
  let pendingUsageChange = false
  let usageTimer: unknown

  const event = (input: Omit<PageDataChangeEvent, 'eventId' | 'occurredAt'>): PageDataChangeEvent => ({
    ...input,
    eventId: randomUUID(),
    occurredAt: now().toISOString()
  })

  const flushUsageRecords = async (): Promise<void> => {
    if (usageTimer !== undefined) {
      cancel(usageTimer)
      usageTimer = undefined
    }
    if (!pendingUsageChange) return
    pendingUsageChange = false
    const ownerSystemAccountIds = [...pendingUsageOwnerIds].sort()
    pendingUsageOwnerIds.clear()
    const ownerChunks = ownerSystemAccountIds.length === 0
      ? [[]]
      : chunks(ownerSystemAccountIds, PAGE_DATA_MAX_EVENT_OWNERS)
    for (const owners of ownerChunks) {
      await options.publish(event({
        domain: 'usage.records',
        operation: 'append',
        fieldMask: ['createdAt'],
        ownerSystemAccountIds: owners,
        membershipChanged: true,
        orderChanged: true,
        filterChanged: false,
        pageChanged: true
      }))
    }
  }

  return {
    async publishAccountStatic(input) {
      await options.publish(event(accountEvent('accounts.static', input)))
    },
    async publishAccountRuntime(input) {
      await options.publish(event(accountEvent('accounts.runtime', input)))
    },
    publishUsageRecords(inputs) {
      if (inputs.length === 0) return
      pendingUsageChange = true
      for (const input of inputs) {
        const ownerId = input.systemAccountId?.trim()
        if (ownerId) pendingUsageOwnerIds.add(ownerId)
      }
      if (usageTimer !== undefined) return
      usageTimer = schedule(flushUsageRecords, usageThrottleMs)
    },
    flushUsageRecords,
    async publishAnnouncementPublic(input) {
      await options.publish(event({
        domain: 'announcements.public',
        entityId: input.announcementId,
        operation: input.operation,
        fieldMask: input.fieldMask ?? [],
        ownerSystemAccountIds: [],
        membershipChanged: true,
        orderChanged: true,
        filterChanged: false,
        pageChanged: true
      }))
    },
    async publishDomainReset(domain, ownerSystemAccountIds, allScopes = false) {
      const owners = uniqueIds(ownerSystemAccountIds)
      const ownerChunks = owners.length === 0 ? [[]] : chunks(owners, PAGE_DATA_MAX_EVENT_OWNERS)
      for (const scopedOwners of ownerChunks) {
        await options.publish(event({
          domain,
          operation: 'range_reset',
          fieldMask: [],
          ownerSystemAccountIds: scopedOwners,
          membershipChanged: true,
          orderChanged: true,
          filterChanged: true,
          pageChanged: true,
          ...(allScopes ? { allScopes: true } : {})
        }))
      }
    }
  }
}

const runtimePublisher = createPageDataChangePublisher({
  publish: async (event) => {
    try {
      await publishPageDataChange(event)
    } catch (error) {
      reportPageDataPublishFailure(error, { domain: event.domain })
    }
  }
})

export async function publishAccountStaticChange(input: AccountPageDataChangeInput): Promise<void> {
  const owners = await resolveAccountPageDataOwners(input)
  await runtimePublisher.publishAccountStatic({ ...input, ...owners })
}

export async function publishAccountRuntimeChange(input: AccountPageDataChangeInput): Promise<void> {
  const owners = await resolveAccountPageDataOwners(input)
  await runtimePublisher.publishAccountRuntime({ ...input, ...owners })
}

export async function resolveAccountPageDataOwners(input: {
  accountId: string
  ownerSystemAccountIds: string[]
  loadGranteeIds?: (accountId: string) => Promise<string[]>
}): Promise<{ ownerSystemAccountIds: string[]; allScopes: boolean }> {
  try {
    const granteeIds = await (input.loadGranteeIds ?? listAccountAuthorizationGranteeIdsAsync)(input.accountId)
    return { ownerSystemAccountIds: uniqueIds([...input.ownerSystemAccountIds, ...granteeIds]), allScopes: false }
  } catch (error) {
    reportPageDataPublishFailure(error, { domain: 'accounts', operation: 'resolve_authorization_owners', accountId: input.accountId })
    return { ownerSystemAccountIds: uniqueIds(input.ownerSystemAccountIds), allScopes: true }
  }
}

export async function resolveAccountsPageDataOwners(inputs: Array<{
  accountId: string
  ownerSystemAccountIds: string[]
}>): Promise<{ ownerSystemAccountIds: string[]; allScopes: boolean }> {
  const resolved = await Promise.all(inputs.map((input) => resolveAccountPageDataOwners(input)))
  return {
    ownerSystemAccountIds: uniqueIds(resolved.flatMap((item) => item.ownerSystemAccountIds)),
    allScopes: resolved.some((item) => item.allScopes)
  }
}

export function publishUsageRecordBatchChange(inputs: Array<Pick<UsageRecordInput, 'id' | 'systemAccountId'>>): void {
  runtimePublisher.publishUsageRecords(inputs)
}

export async function publishAnnouncementPublicChange(input: AnnouncementPageDataChangeInput): Promise<void> {
  await runtimePublisher.publishAnnouncementPublic(input)
}

export async function publishAccountStaticReset(ownerSystemAccountIds: string[], allScopes = false): Promise<void> {
  await runtimePublisher.publishDomainReset('accounts.static', ownerSystemAccountIds, allScopes)
}

export async function publishAccountRuntimeReset(ownerSystemAccountIds: string[], allScopes = false): Promise<void> {
  await runtimePublisher.publishDomainReset('accounts.runtime', ownerSystemAccountIds, allScopes)
}

export async function publishUsageRecordsReset(ownerSystemAccountIds: string[]): Promise<void> {
  await runtimePublisher.publishDomainReset('usage.records', ownerSystemAccountIds)
}

export async function publishUsageRecordsGlobalReset(): Promise<void> {
  await runtimePublisher.publishDomainReset('usage.records', [], true)
}

export async function publishAccountsGlobalReset(): Promise<void> {
  await Promise.all([
    runtimePublisher.publishDomainReset('accounts.static', [], true),
    runtimePublisher.publishDomainReset('accounts.runtime', [], true)
  ])
}

export function reportPageDataPublishFailure(error: unknown, context: Record<string, unknown>): void {
  logger.warn(errorLogFields(error, {
    event: 'page_data_change_publish_failed',
    ...context
  }), '页面数据变更通知失败')
}

export function accountPageDataOwnerIds(
  account: {
    systemAccountId?: string
    ownerSystemAccountId?: string
    bindingSystemAccountId?: string
    authorizationInstanceOwnerSystemAccountId?: string
  } | undefined,
  fallbackSystemAccountId?: string
): string[] {
  return uniqueIds([
    account?.systemAccountId ?? '',
    account?.ownerSystemAccountId ?? '',
    account?.bindingSystemAccountId ?? '',
    account?.authorizationInstanceOwnerSystemAccountId ?? '',
    fallbackSystemAccountId ?? ''
  ])
}

function accountEvent(
  domain: 'accounts.static' | 'accounts.runtime',
  input: AccountPageDataChangeInput
): Omit<PageDataChangeEvent, 'eventId' | 'occurredAt'> {
  return {
    domain,
    entityId: input.accountId,
    operation: input.operation ?? 'upsert',
    fieldMask: input.fieldMask ?? [],
    ownerSystemAccountIds: uniqueIds(input.ownerSystemAccountIds),
    membershipChanged: input.membershipChanged ?? input.operation === 'delete',
    orderChanged: input.orderChanged ?? false,
    filterChanged: input.filterChanged ?? false,
    pageChanged: input.pageChanged ?? false,
    ...(input.allScopes ? { allScopes: true } : {})
  }
}

function uniqueIds(values: string[]): string[] {
  return [...new Set(values.map((value) => value.trim()).filter(Boolean))].sort()
}

function chunks<T>(values: T[], size: number): T[][] {
  const result: T[][] = []
  for (let index = 0; index < values.length; index += size) {
    result.push(values.slice(index, index + size))
  }
  return result
}

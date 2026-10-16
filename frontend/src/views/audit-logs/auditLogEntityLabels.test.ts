import { describe, expect, it, vi } from 'vitest'

import { accountLabelForId } from '@/shared/accountLabelCache'
import { groupLabelForId } from '@/shared/groupLabelCache'
import { principalLabelForId } from '@/shared/principalLabelCache'
import { createAuditLogEntityLabelHydrator, type AuditLogEntityLabelApi, type AuditLogEntityLabelRecord, type AuditLogEntityNameOption } from './auditLogEntityLabels'

function entityApi(partial: Partial<AuditLogEntityLabelApi> = {}): AuditLogEntityLabelApi {
  return {
    accountOptions: vi.fn(async (): Promise<AuditLogEntityNameOption[]> => []),
    groupOptions: vi.fn(async (): Promise<AuditLogEntityNameOption[]> => []),
    systemAccountOptions: vi.fn(async (): Promise<AuditLogEntityNameOption[]> => []),
    ...partial
  }
}

async function flushMicrotasks(): Promise<void> {
  await Promise.resolve()
  await Promise.resolve()
}

describe('createAuditLogEntityLabelHydrator', () => {
  it('批量解析缺失名称并写回行对象与缓存', async () => {
    const api = entityApi({
      accountOptions: vi.fn(async () => [{ id: 'acct-h1', name: ' GPT 主账户 ' }]),
      groupOptions: vi.fn(async () => [{ id: 'grp-h1', name: '默认 GPT 分组' }]),
      systemAccountOptions: vi.fn(async () => [{ id: 'sys-h1', name: 'admin' }])
    })
    const hydrate = createAuditLogEntityLabelHydrator(api)
    const item: AuditLogEntityLabelRecord = {
      accountId: 'acct-h1',
      groupId: 'grp-h1',
      systemAccountId: 'sys-h1'
    }

    hydrate([item])
    await flushMicrotasks()

    expect(item.accountName).toBe('GPT 主账户')
    expect(item.groupName).toBe('默认 GPT 分组')
    expect(item.systemAccountName).toBe('admin')
    expect(accountLabelForId('acct-h1')).toBe('GPT 主账户')
    expect(groupLabelForId('grp-h1')).toBe('默认 GPT 分组')
    expect(principalLabelForId('system_account', 'sys-h1')).toBe('admin')
  })

  it('同一 ID 出现在多行时全部写回', async () => {
    const api = entityApi({
      groupOptions: vi.fn(async () => [{ id: 'grp-multi', name: '共享分组' }])
    })
    const hydrate = createAuditLogEntityLabelHydrator(api)
    const items: AuditLogEntityLabelRecord[] = [{ groupId: 'grp-multi' }, { groupId: 'grp-multi', groupName: undefined }]

    hydrate(items)
    await flushMicrotasks()

    expect(items[0].groupName).toBe('共享分组')
    expect(items[1].groupName).toBe('共享分组')
  })

  it('名称已在行上或缓存命中时不再发起请求', async () => {
    const api = entityApi({
      groupOptions: vi.fn(async () => [{ id: 'grp-cached', name: '默认 GPT 分组' }])
    })
    const hydrate = createAuditLogEntityLabelHydrator(api)
    const first = [{ groupId: 'grp-cached' }]

    hydrate(first)
    await flushMicrotasks()
    expect(api.groupOptions).toHaveBeenCalledTimes(1)

    const second = [{ groupId: 'grp-cached' }, { groupId: 'grp-cached', groupName: '行上已有名称' }]
    hydrate(second)
    await flushMicrotasks()

    expect(api.groupOptions).toHaveBeenCalledTimes(1)
    expect(second[1].groupName).toBe('行上已有名称')
  })

  it('解析不到的 ID 记入跳过集合，翻页重复出现时不再请求', async () => {
    const api = entityApi({
      accountOptions: vi.fn(async () => [{ id: 'acct-live', name: '存活账户' }])
    })
    const hydrate = createAuditLogEntityLabelHydrator(api)

    hydrate([{ accountId: 'acct-gone' }, { accountId: 'acct-live' }])
    await flushMicrotasks()
    expect(api.accountOptions).toHaveBeenCalledTimes(1)

    hydrate([{ accountId: 'acct-gone' }])
    await flushMicrotasks()

    expect(api.accountOptions).toHaveBeenCalledTimes(1)
    expect(accountLabelForId('acct-live')).toBe('存活账户')
  })

  it('请求失败时不写回且下次仍会重试', async () => {
    let fail = true
    const api = entityApi({
      groupOptions: vi.fn(async () => {
        if (fail) throw new Error('network down')
        return [{ id: 'grp-retry', name: '重试后分组' }]
      })
    })
    const hydrate = createAuditLogEntityLabelHydrator(api)
    const item: AuditLogEntityLabelRecord = { groupId: 'grp-retry' }

    hydrate([item])
    await flushMicrotasks()
    expect(item.groupName).toBeUndefined()

    fail = false
    hydrate([item])
    await flushMicrotasks()

    expect(item.groupName).toBe('重试后分组')
  })
})

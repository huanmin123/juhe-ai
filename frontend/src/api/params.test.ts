import { describe, expect, it } from 'vitest'

import type { AccountListParams, AccountOptionParams, OperationLogListParams } from './contracts'
import {
  accountListParams,
  accountOptionsParams,
  accountUsageStatsOptionParams,
  accountUsageStatsParams,
  aiPerformanceAccountOptionsParams,
  aiPerformanceParams,
  aiPerformanceSeriesParams,
  authorizationGranteeGroupOptionsParams,
  authorizationPrincipalOptionsParams,
  boundedAuthorizationListParams,
  groupListParams,
  groupOptionParams,
  modelCheckRunListParams,
  proxyOptionParams,
  scopedListParams,
  stripAdminOperationLogParams,
  stripSystemAccountParam,
  systemAccountListParams,
  systemAccountOptionsParams,
  teamListParams
} from './params'

describe('stripSystemAccountParam', () => {
  it('无参数时返回 undefined', () => {
    expect(stripSystemAccountParam(undefined)).toBeUndefined()
  })

  it('删除 systemAccountId；剩余为空时返回 undefined', () => {
    expect(stripSystemAccountParam({ systemAccountId: 'sa-1' })).toBeUndefined()
    expect(stripSystemAccountParam({ systemAccountId: 'sa-1', page: 2 })).toEqual({ page: 2 })
  })

  it('无 systemAccountId 时原样浅拷贝', () => {
    expect(stripSystemAccountParam({ page: 1, keyword: 'k' })).toEqual({ page: 1, keyword: 'k' })
  })
})

describe('boundedAuthorizationListParams', () => {
  it('无参数时补默认 page=1、pageSize=500', () => {
    expect(boundedAuthorizationListParams(undefined)).toEqual({ page: 1, pageSize: 500 })
  })

  it('保留已提供的分页值并合并其余字段', () => {
    expect(boundedAuthorizationListParams({ page: 3, pageSize: 20, status: 'active' })).toEqual({ page: 3, pageSize: 20, status: 'active' })
  })

  it('仅提供 page 时 pageSize 回退默认值', () => {
    expect(boundedAuthorizationListParams({ page: 2 })).toEqual({ page: 2, pageSize: 500 })
  })
})

describe('stripAdminOperationLogParams', () => {
  it('无参数时返回 undefined', () => {
    expect(stripAdminOperationLogParams(undefined)).toBeUndefined()
  })

  it('删除三个管理员专属过滤字段', () => {
    const params: OperationLogListParams = {
      page: 1,
      actorSystemAccountId: 'actor-1',
      affectedSystemAccountId: 'affected-1',
      operationScopeSystemAccountId: 'scope-1',
      module: 'account'
    }
    expect(stripAdminOperationLogParams(params)).toEqual({ page: 1, module: 'account' })
  })

  it('全部字段被删除后返回 undefined', () => {
    expect(stripAdminOperationLogParams({ actorSystemAccountId: 'actor-1' })).toBeUndefined()
  })
})

describe('accountListParams', () => {
  it('无参数时返回 undefined', () => {
    expect(accountListParams(undefined)).toBeUndefined()
  })

  it('归一化列表字段的完整形状', () => {
    const params: AccountListParams = {
      systemAccountId: 'sa-1',
      ids: ['a-1', 'a-2'],
      page: 2,
      pageSize: 50,
      keyword: 'k',
      providerCode: 'openai',
      groupId: 'g-1',
      tagIds: ['t-1', 't-2', 't-1'],
      type: 'oauth',
      status: ['active', 'all', 'disabled', ' '],
      schedulable: 'enabled',
      sorts: [
        { field: 'name', order: 'desc' },
        { field: 'priority', order: 'asc' }
      ]
    }
    expect(accountListParams(params)).toEqual({
      systemAccountId: 'sa-1',
      ids: 'a-1,a-2',
      page: 2,
      pageSize: 50,
      keyword: 'k',
      providerCode: 'openai',
      groupId: 'g-1',
      tagIds: 't-1,t-2',
      type: 'oauth',
      status: 'active,disabled',
      schedulable: 'enabled',
      sorts: 'name:desc,priority:asc'
    })
  })

  it('过滤 all 值与空串', () => {
    expect(accountListParams({ providerCode: 'all', type: 'all', schedulable: 'all', status: 'all', keyword: '' })).toBeUndefined()
  })

  it('includeSystemAccount=false 时剥离 systemAccountId', () => {
    expect(accountListParams({ systemAccountId: 'sa-1', keyword: 'k' }, false)).toEqual({ keyword: 'k' })
  })

  it('逗号分隔的 status 字符串会拆分、去重并过滤 all', () => {
    expect(accountListParams({ status: 'active,all,active, disabled' })).toEqual({ status: 'active,disabled' })
  })

  it('空 ids 与空 sorts 不输出', () => {
    expect(accountListParams({ ids: [], sorts: [] })).toBeUndefined()
  })
})

describe('accountOptionsParams', () => {
  it('无参数时返回 undefined', () => {
    expect(accountOptionsParams(undefined)).toBeUndefined()
  })

  it('保留关键字、limit、ids 并过滤 all', () => {
    const params: AccountOptionParams = {
      systemAccountId: 'sa-1',
      ids: ['a-1'],
      keyword: 'k',
      limit: 20,
      providerCode: 'all',
      type: 'oauth',
      schedulable: 'enabled'
    }
    expect(accountOptionsParams(params)).toEqual({
      systemAccountId: 'sa-1',
      ids: 'a-1',
      keyword: 'k',
      limit: 20,
      type: 'oauth',
      schedulable: 'enabled'
    })
  })

  it('无有效字段时返回 undefined', () => {
    expect(accountOptionsParams({ providerCode: 'all' })).toBeUndefined()
  })

  it('includeSystemAccount=false 时剥离 systemAccountId', () => {
    expect(accountOptionsParams({ systemAccountId: 'sa-1', keyword: 'k' }, false)).toEqual({ keyword: 'k' })
  })
})

describe('accountUsageStatsOptionParams', () => {
  it('无参数时返回 undefined', () => {
    expect(accountUsageStatsOptionParams(undefined)).toBeUndefined()
  })

  it('合并 keyword/limit/selectedIds', () => {
    expect(accountUsageStatsOptionParams({ systemAccountId: 'sa-1', keyword: 'k', limit: 10, selectedIds: ['a', 'b'] })).toEqual({
      systemAccountId: 'sa-1',
      keyword: 'k',
      limit: 10,
      selectedIds: 'a,b'
    })
  })

  it('includeSystemAccount=false 时剥离 systemAccountId', () => {
    expect(accountUsageStatsOptionParams({ systemAccountId: 'sa-1', limit: 1 }, false)).toEqual({ limit: 1 })
  })
})

describe('groupListParams', () => {
  it('无参数时返回 undefined', () => {
    expect(groupListParams(undefined)).toBeUndefined()
  })

  it('保留分页与 systemAccountId', () => {
    expect(groupListParams({ systemAccountId: 'sa-1', page: 2, pageSize: 30 })).toEqual({ systemAccountId: 'sa-1', page: 2, pageSize: 30 })
  })

  it('空对象返回 undefined', () => {
    expect(groupListParams({})).toBeUndefined()
  })

  it('includeSystemAccount=false 时剥离 systemAccountId', () => {
    expect(groupListParams({ systemAccountId: 'sa-1', page: 1 }, false)).toEqual({ page: 1 })
  })
})

describe('groupOptionParams', () => {
  it('无参数时返回 undefined', () => {
    expect(groupOptionParams(undefined)).toBeUndefined()
  })

  it('保留 ids/keyword/providerCode（trim）/布尔字段与 purpose', () => {
    expect(groupOptionParams({
      systemAccountId: 'sa-1',
      ids: ['g-1', 'g-2'],
      keyword: '  关键  ',
      providerCode: ' openai ',
      limit: 10,
      manageableOnly: false,
      preferDefault: true,
      purpose: 'select'
    })).toEqual({
      systemAccountId: 'sa-1',
      ids: 'g-1,g-2',
      keyword: '关键',
      providerCode: 'openai',
      limit: 10,
      manageableOnly: false,
      preferDefault: true,
      purpose: 'select'
    })
  })

  it('参数不含 systemAccountId 键时（Pick 形状）不输出该字段', () => {
    expect(groupOptionParams({ keyword: 'k' })).toEqual({ keyword: 'k' })
  })

  it('includeSystemAccount=false 时剥离 systemAccountId', () => {
    expect(groupOptionParams({ systemAccountId: 'sa-1', limit: 5 }, false)).toEqual({ limit: 5 })
  })
})

describe('proxyOptionParams', () => {
  it('无参数时返回 undefined', () => {
    expect(proxyOptionParams(undefined)).toBeUndefined()
  })

  it('keyword trim、selectedIds trim+去重+排序', () => {
    expect(proxyOptionParams({ keyword: ' 代理 ', limit: 5, selectedIds: [' b ', 'a', 'b', ''] })).toEqual({
      keyword: '代理',
      limit: 5,
      selectedIds: ['a', 'b']
    })
  })

  it('全空白 selectedIds 归一化后不输出该字段', () => {
    expect(proxyOptionParams({ selectedIds: [' ', ''] })).toBeUndefined()
  })
})

describe('scopedListParams', () => {
  it('无参数时返回 undefined', () => {
    expect(scopedListParams(undefined)).toBeUndefined()
  })

  it('删除 undefined/null/空串/all 值', () => {
    expect(scopedListParams({ page: 1, keyword: undefined, module: null, action: '', status: 'all' })).toEqual({ page: 1 })
  })

  it('includeSystemAccount=false 时删除 systemAccountId', () => {
    expect(scopedListParams({ systemAccountId: 'sa-1', page: 1 }, false)).toEqual({ page: 1 })
  })

  it('includeSystemAccount=true 时保留 systemAccountId', () => {
    expect(scopedListParams({ systemAccountId: 'sa-1', page: 1 })).toEqual({ systemAccountId: 'sa-1', page: 1 })
  })

  it('清理后无字段时返回 undefined', () => {
    expect(scopedListParams({ keyword: '', status: 'all' })).toBeUndefined()
  })
})

describe('teamListParams', () => {
  it('无参数时返回 undefined', () => {
    expect(teamListParams(undefined)).toBeUndefined()
  })

  it('保留分页与 trim 后的 keyword', () => {
    expect(teamListParams({ systemAccountId: 'sa-1', page: 1, pageSize: 20, keyword: ' 团队 ' })).toEqual({
      systemAccountId: 'sa-1',
      page: 1,
      pageSize: 20,
      keyword: '团队'
    })
  })

  it('Omit 形状（无 systemAccountId 键）不输出该字段', () => {
    expect(teamListParams({ page: 2 })).toEqual({ page: 2 })
  })

  it('includeSystemAccount=false 时剥离 systemAccountId', () => {
    expect(teamListParams({ systemAccountId: 'sa-1', page: 2 }, false)).toEqual({ page: 2 })
  })
})

describe('authorizationPrincipalOptionsParams', () => {
  it('无参数时返回 undefined', () => {
    expect(authorizationPrincipalOptionsParams(undefined)).toBeUndefined()
  })

  it('合并 ids/keyword/limit', () => {
    expect(authorizationPrincipalOptionsParams({ ids: ['u-1'], keyword: ' 用户 ', limit: 8 })).toEqual({ ids: 'u-1', keyword: '用户', limit: 8 })
  })
})

describe('authorizationGranteeGroupOptionsParams', () => {
  it('总是携带 granteeSystemAccountId 并合并主选项参数', () => {
    expect(authorizationGranteeGroupOptionsParams({ granteeSystemAccountId: 'sa-9', ids: ['g-1'], limit: 5 })).toEqual({
      ids: 'g-1',
      limit: 5,
      granteeSystemAccountId: 'sa-9'
    })
  })

  it('providerCode trim、preferDefault 布尔保留', () => {
    expect(authorizationGranteeGroupOptionsParams({
      granteeSystemAccountId: 'sa-9',
      providerCode: ' openai ',
      preferDefault: false
    })).toEqual({
      granteeSystemAccountId: 'sa-9',
      providerCode: 'openai',
      preferDefault: false
    })
  })
})

describe('systemAccountOptionsParams', () => {
  it('无参数时返回 undefined', () => {
    expect(systemAccountOptionsParams(undefined)).toBeUndefined()
  })

  it('合并 ids/keyword/limit', () => {
    expect(systemAccountOptionsParams({ ids: ['sa-1', 'sa-2'], keyword: ' adm ', limit: 3 })).toEqual({ ids: 'sa-1,sa-2', keyword: 'adm', limit: 3 })
  })
})

describe('systemAccountListParams', () => {
  it('无参数时返回 undefined', () => {
    expect(systemAccountListParams(undefined)).toBeUndefined()
  })

  it('合并分页与 trim 后的 keyword', () => {
    expect(systemAccountListParams({ page: 1, pageSize: 100, keyword: ' root ' })).toEqual({ page: 1, pageSize: 100, keyword: 'root' })
  })
})

describe('accountUsageStatsParams', () => {
  it('无参数时返回 undefined', () => {
    expect(accountUsageStatsParams(undefined)).toBeUndefined()
  })

  it('合并日期区间、accountIds 与 schedulable', () => {
    expect(accountUsageStatsParams({
      systemAccountId: 'sa-1',
      page: 1,
      pageSize: 20,
      keyword: ' k ',
      startDate: '2026-01-01',
      endDate: '2026-01-31',
      accountIds: ['a-1', 'a-2'],
      schedulable: 'disabled'
    })).toEqual({
      systemAccountId: 'sa-1',
      page: 1,
      pageSize: 20,
      keyword: 'k',
      startDate: '2026-01-01',
      endDate: '2026-01-31',
      accountIds: 'a-1,a-2',
      schedulable: 'disabled'
    })
  })

  it('includeSystemAccount=false 时剥离 systemAccountId 并过滤 all', () => {
    expect(accountUsageStatsParams({ systemAccountId: 'sa-1', schedulable: 'all' }, false)).toBeUndefined()
  })
})

describe('aiPerformanceParams', () => {
  it('无参数时返回 undefined', () => {
    expect(aiPerformanceParams(undefined)).toBeUndefined()
  })

  it('仅保留日期区间与 systemAccountId', () => {
    expect(aiPerformanceParams({ systemAccountId: 'sa-1', startDate: '2026-01-01', endDate: '2026-01-02' })).toEqual({
      systemAccountId: 'sa-1',
      startDate: '2026-01-01',
      endDate: '2026-01-02'
    })
  })

  it('includeSystemAccount=false 时剥离 systemAccountId', () => {
    expect(aiPerformanceParams({ systemAccountId: 'sa-1' }, false)).toBeUndefined()
  })
})

describe('aiPerformanceSeriesParams', () => {
  it('输出 URLSearchParams，accountIds 去重、trim、过滤空值', () => {
    const result = aiPerformanceSeriesParams({ systemAccountId: 'sa-1', startDate: 's', endDate: 'e', accountIds: [' a ', 'a', '', 'b'] })
    expect(result).toBeInstanceOf(URLSearchParams)
    expect(result.toString()).toBe('systemAccountId=sa-1&startDate=s&endDate=e&accountIds=a&accountIds=b')
  })

  it('accountIds 最多取 20 个', () => {
    const accountIds = Array.from({ length: 25 }, (_, index) => `a-${index}`)
    const result = aiPerformanceSeriesParams({ accountIds })
    expect(result.getAll('accountIds')).toHaveLength(20)
    expect(result.getAll('accountIds')[0]).toBe('a-0')
    expect(result.getAll('accountIds')[19]).toBe('a-19')
  })

  it('includeSystemAccount=false 时不输出 systemAccountId', () => {
    const result = aiPerformanceSeriesParams({ systemAccountId: 'sa-1', accountIds: ['a'] }, false)
    expect(result.get('systemAccountId')).toBeNull()
  })
})

describe('aiPerformanceAccountOptionsParams', () => {
  it('无参数时返回 undefined', () => {
    expect(aiPerformanceAccountOptionsParams(undefined)).toBeUndefined()
  })

  it('合并 keyword/accountIds/limit', () => {
    expect(aiPerformanceAccountOptionsParams({ systemAccountId: 'sa-1', keyword: ' k ', accountIds: ['a', 'b'], limit: 7 })).toEqual({
      systemAccountId: 'sa-1',
      keyword: 'k',
      accountIds: 'a,b',
      limit: 7
    })
  })
})

describe('modelCheckRunListParams', () => {
  it('无参数时返回 undefined', () => {
    expect(modelCheckRunListParams(undefined)).toBeUndefined()
  })

  it('trim 系统账号、目标与时间字段', () => {
    expect(modelCheckRunListParams({ systemAccountId: ' sa-1 ', targetId: ' acc-1 ', startAt: ' 2026-01-01 ', endAt: '' })).toEqual({
      systemAccountId: 'sa-1',
      targetId: 'acc-1',
      startAt: '2026-01-01'
    })
  })

  it('保留枚举过滤字段', () => {
    expect(modelCheckRunListParams({ page: 1, pageSize: 10, targetType: 'account', model: 'gpt-4o', level: 'suspicious', status: 'failed', triggerKind: 'manual' })).toEqual({
      page: 1,
      pageSize: 10,
      targetType: 'account',
      model: 'gpt-4o',
      level: 'suspicious',
      status: 'failed',
      triggerKind: 'manual'
    })
  })
})

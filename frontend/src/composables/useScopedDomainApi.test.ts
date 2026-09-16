import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Mock } from 'vitest'
import { ref, type Ref } from 'vue'

import { api } from '@/api/client'
import {
  useScopedAccountsApi,
  useScopedApiKeysApi,
  useScopedGroupsApi,
  useScopedModelCheckAccountOptionsApi,
  useScopedModelChecksApi,
  useScopedOperationLogsApi,
  useScopedRouteStrategiesApi,
  useScopedSystemTeamsApi,
  useScopedUsageRecordsApi
} from './useScopedDomainApi'

// 全命名空间 mock：每个方法返回统一结果，测试只断言路由目标。
vi.mock('@/api/client', () => {
  const fn = () => vi.fn(async () => 'ok')
  const ns = (methods: string[]) => Object.fromEntries(methods.map((method) => [method, fn()])) as Record<string, Mock>
  return {
    api: {
      apiKeys: ns(['list', 'create', 'update', 'secret', 'refreshKey', 'delete']),
      myApiKeys: ns(['list', 'create', 'update', 'secret', 'refreshKey', 'delete']),
      accounts: ns(['options']),
      myAccounts: ns(['options']),
      modelChecks: ns(['accountOptions', 'options', 'active', 'run', 'runStream', 'stop', 'list', 'detail', 'qualityPolicy', 'saveQualityPolicy', 'qualitySchedules', 'saveQualitySchedule', 'patchQualitySchedule', 'deleteQualitySchedule']),
      myModelChecks: ns(['accountOptions', 'options', 'active', 'run', 'runStream', 'stop', 'list', 'detail', 'qualityPolicy', 'saveQualityPolicy', 'qualitySchedules', 'saveQualitySchedule', 'patchQualitySchedule', 'deleteQualitySchedule']),
      groups: ns(['listPage', 'detail', 'editBasicDetail', 'options', 'routeStrategyOptions', 'create', 'update', 'returnAuthorization', 'delete']),
      myGroups: ns(['listPage', 'detail', 'editBasicDetail', 'options', 'routeStrategyOptions', 'create', 'update', 'returnAuthorization', 'delete']),
      usageRecords: ns(['list']),
      myUsageRecords: ns(['list']),
      operationLogs: ns(['list', 'detail']),
      myOperationLogs: ns(['list', 'detail']),
      routeStrategies: ns(['list', 'options', 'detail', 'speedFirstRuntime', 'editBasicDetail', 'create', 'update', 'delete']),
      myRouteStrategies: ns(['list', 'options', 'detail', 'speedFirstRuntime', 'editBasicDetail', 'create', 'update', 'delete']),
      systemTeams: ns(['list', 'detail', 'members', 'memberHistory']),
      myTeams: ns(['list', 'detail', 'members', 'memberHistory'])
    }
  }
})

type ScopedFactory = (isManagementView: Ref<boolean>) => Record<string, (...args: unknown[]) => unknown>

interface MethodSpec {
  args: unknown[]
  /** self 分支实际转发参数不同时显式覆盖 */
  selfArgs?: unknown[]
  /** 命名空间上的目标方法名与 scoped 方法名不同时覆盖 */
  target?: string
}

interface ScopeSpec {
  title: string
  factory: ScopedFactory
  managementNs: Record<string, Mock>
  selfNs: Record<string, Mock>
  methods: Record<string, MethodSpec>
}

const scopes: ScopeSpec[] = [
  {
    title: 'useScopedApiKeysApi',
    factory: useScopedApiKeysApi as unknown as ScopedFactory,
    managementNs: api.apiKeys as unknown as Record<string, Mock>,
    selfNs: api.myApiKeys as unknown as Record<string, Mock>,
    methods: {
      list: { args: [{ page: 1 }] },
      create: { args: [{ name: 'k' }, { systemAccountId: 'u1' }], selfArgs: [{ name: 'k' }] },
      update: { args: ['id-1', { name: 'k2' }, { systemAccountId: 'u1' }], selfArgs: ['id-1', { name: 'k2' }] },
      secret: { args: ['id-1', undefined], selfArgs: ['id-1'] },
      refreshKey: { args: ['id-1', undefined], selfArgs: ['id-1'] },
      delete: { args: ['id-1', undefined], selfArgs: ['id-1'] }
    }
  },
  {
    title: 'useScopedAccountsApi',
    factory: useScopedAccountsApi as unknown as ScopedFactory,
    managementNs: api.accounts as unknown as Record<string, Mock>,
    selfNs: api.myAccounts as unknown as Record<string, Mock>,
    methods: { options: { args: [{ keyword: 'k' }] } }
  },
  {
    title: 'useScopedModelCheckAccountOptionsApi',
    factory: useScopedModelCheckAccountOptionsApi as unknown as ScopedFactory,
    managementNs: api.modelChecks as unknown as Record<string, Mock>,
    selfNs: api.myModelChecks as unknown as Record<string, Mock>,
    methods: { options: { args: [{ scope: 1 }, undefined], target: 'accountOptions' } }
  },
  {
    title: 'useScopedGroupsApi',
    factory: useScopedGroupsApi as unknown as ScopedFactory,
    managementNs: api.groups as unknown as Record<string, Mock>,
    selfNs: api.myGroups as unknown as Record<string, Mock>,
    methods: {
      listPage: { args: [{ page: 1 }] },
      detail: { args: ['id-1', undefined], selfArgs: ['id-1'] },
      editBasicDetail: { args: ['id-1', undefined], selfArgs: ['id-1'] },
      options: { args: [{}] },
      routeStrategyOptions: { args: [{}] },
      create: { args: [{ name: 'g' }, undefined], selfArgs: [{ name: 'g' }] },
      update: { args: ['id-1', { name: 'g2' }, undefined], selfArgs: ['id-1', { name: 'g2' }] },
      returnAuthorization: { args: ['id-1', undefined], selfArgs: ['id-1'] },
      delete: { args: ['id-1', undefined], selfArgs: ['id-1'] }
    }
  },
  {
    title: 'useScopedUsageRecordsApi',
    factory: useScopedUsageRecordsApi as unknown as ScopedFactory,
    managementNs: api.usageRecords as unknown as Record<string, Mock>,
    selfNs: api.myUsageRecords as unknown as Record<string, Mock>,
    methods: { list: { args: [{ page: 1 }] } }
  },
  {
    title: 'useScopedOperationLogsApi',
    factory: useScopedOperationLogsApi as unknown as ScopedFactory,
    managementNs: api.operationLogs as unknown as Record<string, Mock>,
    selfNs: api.myOperationLogs as unknown as Record<string, Mock>,
    methods: {
      list: { args: [{ page: 1 }] },
      detail: { args: ['id-1'] }
    }
  },
  {
    title: 'useScopedRouteStrategiesApi',
    factory: useScopedRouteStrategiesApi as unknown as ScopedFactory,
    managementNs: api.routeStrategies as unknown as Record<string, Mock>,
    selfNs: api.myRouteStrategies as unknown as Record<string, Mock>,
    methods: {
      list: { args: [{ page: 1 }] },
      options: { args: [{}] },
      detail: { args: ['id-1', undefined], selfArgs: ['id-1'] },
      speedFirstRuntime: { args: ['id-1', { systemAccountId: 'u1' }, { signal: undefined }], selfArgs: ['id-1', { signal: undefined }] },
      editBasicDetail: { args: ['id-1', undefined], selfArgs: ['id-1'] },
      create: { args: [{ name: 's' }, undefined], selfArgs: [{ name: 's' }] },
      update: { args: ['id-1', { name: 's2' }, undefined], selfArgs: ['id-1', { name: 's2' }] },
      delete: { args: ['id-1', undefined], selfArgs: ['id-1'] }
    }
  },
  {
    title: 'useScopedModelChecksApi',
    factory: useScopedModelChecksApi as unknown as ScopedFactory,
    managementNs: api.modelChecks as unknown as Record<string, Mock>,
    selfNs: api.myModelChecks as unknown as Record<string, Mock>,
    methods: {
      options: { args: [{ systemAccountId: 'u1' }], selfArgs: [] },
      active: { args: [{ systemAccountId: 'u1' }], selfArgs: [] },
      run: { args: [{ model: 'm' }, undefined], selfArgs: [{ model: 'm' }] },
      runStream: { args: [{ model: 'm' }, { signal: undefined }, undefined], selfArgs: [{ model: 'm' }, { signal: undefined }] },
      stop: { args: [{ systemAccountId: 'u1' }], selfArgs: [] },
      list: { args: [{ page: 1 }] },
      detail: { args: ['id-1', undefined], selfArgs: ['id-1'] },
      qualityPolicy: { args: [{ systemAccountId: 'u1' }], selfArgs: [] },
      saveQualityPolicy: { args: [{ enabled: true }, undefined], selfArgs: [{ enabled: true }] },
      qualitySchedules: { args: [{ page: 1 }] },
      saveQualitySchedule: { args: [{ id: 'q1' }, undefined], selfArgs: [{ id: 'q1' }] },
      patchQualitySchedule: { args: ['q1', { enabled: false }, undefined], selfArgs: ['q1', { enabled: false }] },
      deleteQualitySchedule: { args: ['q1', undefined], selfArgs: ['q1'] }
    }
  },
  {
    title: 'useScopedSystemTeamsApi',
    factory: useScopedSystemTeamsApi as unknown as ScopedFactory,
    managementNs: api.systemTeams as unknown as Record<string, Mock>,
    selfNs: api.myTeams as unknown as Record<string, Mock>,
    methods: {
      list: { args: [{ page: 1 }] },
      detail: { args: ['id-1', undefined], selfArgs: ['id-1'] },
      members: { args: ['id-1', undefined], selfArgs: ['id-1'] },
      memberHistory: { args: ['id-1', { page: 1 }] }
    }
  }
]

beforeEach(() => {
  vi.clearAllMocks()
})

for (const spec of scopes) {
  describe(spec.title, () => {
    for (const [method, methodSpec] of Object.entries(spec.methods)) {
      const target = methodSpec.target ?? method
      it(`${method} 按管理视图路由到对应命名空间`, async () => {
        const isManagementView = ref(true)
        const scoped = spec.factory(isManagementView)
        await scoped[method](...methodSpec.args)
        expect(spec.managementNs[target]).toHaveBeenCalledTimes(1)
        expect(spec.managementNs[target]).toHaveBeenCalledWith(...methodSpec.args)
        expect(spec.selfNs[target]).not.toHaveBeenCalled()
      })

      it(`${method} 个人视图路由到 my 命名空间`, async () => {
        const isManagementView = ref(false)
        const scoped = spec.factory(isManagementView)
        await scoped[method](...methodSpec.args)
        expect(spec.selfNs[target]).toHaveBeenCalledTimes(1)
        expect(spec.selfNs[target]).toHaveBeenCalledWith(...(methodSpec.selfArgs ?? methodSpec.args))
        expect(spec.managementNs[target]).not.toHaveBeenCalled()
      })
    }
  })
}

describe('管理视图切换实时生效', () => {
  it('同一实例随 isManagementView 变化切换目标', async () => {
    const isManagementView = ref(false)
    const scoped = useScopedUsageRecordsApi(isManagementView)
    await scoped.list({})
    expect(api.myUsageRecords.list).toHaveBeenCalledTimes(1)
    isManagementView.value = true
    await scoped.list({})
    expect(api.usageRecords.list).toHaveBeenCalledTimes(1)
  })
})

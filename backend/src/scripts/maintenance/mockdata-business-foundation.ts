import type {
  SystemAccountRole,
  SystemAccountSummary
} from '../../domain/types.js'
import type { AccessScope } from '../../storage/access-scope.js'
import * as repositories from '../../storage/repositories.js'
import {
  mockPassword,
  namePrefix,
  type MockSystemAccounts
} from './mockdata-shared.js'

export function createMockUsers(admin: SystemAccountSummary): MockSystemAccounts {
  return {
    admin,
    manager: ensureSystemAccount({
      username: 'mockdata_admin',
      displayName: `${namePrefix}管理员用户`,
      description: 'Mockdata 普通管理员账号，用于管理员模式下验证管理员自有资源、筛选和创建目标',
      role: 'admin',
      status: 'active',
      imageGenerationEnabled: true
    }),
    ops: ensureSystemAccount({
      username: 'mockdata_ops',
      displayName: `${namePrefix}运维用户`,
      description: 'Mockdata 运维协作用户，用于账户授权、操作日志和调用方统计',
      status: 'active'
    }),
    dev: ensureSystemAccount({
      username: 'mockdata_dev',
      displayName: `${namePrefix}研发用户`,
      description: 'Mockdata 研发协作用户，用于分组授权和团队授权',
      status: 'active'
    }),
    tester: ensureSystemAccount({
      username: 'mockdata_tester',
      displayName: `${namePrefix}测试用户`,
      description: 'Mockdata 测试协作用户，用于团队授权和回归验证',
      status: 'active',
      imageGenerationEnabled: true
    }),
    finance: ensureSystemAccount({
      username: 'mockdata_finance',
      displayName: `${namePrefix}财务用户`,
      description: 'Mockdata 财务观察用户，用于额度和授权展示',
      status: 'active'
    }),
    viewer: ensureSystemAccount({
      username: 'mockdata_viewer',
      displayName: `${namePrefix}只读观察用户`,
      description: 'Mockdata 观察用户，用于公告已读和操作可见性',
      status: 'active'
    }),
    disabled: ensureSystemAccount({
      username: 'mockdata_disabled',
      displayName: `${namePrefix}停用用户`,
      description: 'Mockdata 停用用户，用于系统账号状态展示',
      status: 'disabled'
    })
  }
}

function ensureSystemAccount(input: {
  username: string
  displayName: string
  description: string
  role?: SystemAccountRole
  status: 'active' | 'disabled'
  imageGenerationEnabled?: boolean
}): SystemAccountSummary {
  const role = input.role ?? 'user'
  const existing = repositories.findSystemAccountByUsername(input.username)
  if (existing) {
    const updated = repositories.updateSystemAccount(existing.id, {
      displayName: input.displayName,
      description: input.description,
      role,
      status: input.status,
      mustChangePassword: false,
      imageGenerationEnabled: input.imageGenerationEnabled ?? false,
      password: mockPassword
    })
    if (!updated) throw new Error(`更新 Mockdata 用户失败：${input.username}`)
    return updated
  }
  return repositories.createSystemAccount({
    username: input.username,
    displayName: input.displayName,
    description: input.description,
    password: mockPassword,
    role,
    status: input.status,
    mustChangePassword: false,
    imageGenerationEnabled: input.imageGenerationEnabled ?? false
  })
}

export function createProxies(adminAccess: AccessScope): { http: string; socks: string; disabled: string } {
  const http = repositories.createProxy({
    name: `${namePrefix}HTTP 代理`,
    description: 'Mockdata HTTP 代理，绑定到主力 API Key 账户',
    type: 'http',
    host: '127.0.0.1',
    port: 7890,
    username: 'mock_proxy',
    password: 'mock_proxy_password',
    enabled: true
  }, adminAccess)
  repositories.updateProxyTestState(http.id, {
    testStatus: 'passed',
    latencyMs: 82,
    outboundIp: '203.0.113.10',
    outboundRegion: '本地测试出口',
    lastTestMessage: 'Mockdata 代理连通正常'
  })

  const socks = repositories.createProxy({
    name: `${namePrefix}SOCKS 代理`,
    description: 'Mockdata SOCKS 代理，绑定到 OAuth 账户',
    type: 'socks5h',
    host: '127.0.0.1',
    port: 1080,
    enabled: true
  }, adminAccess)
  repositories.updateProxyTestState(socks.id, {
    testStatus: 'passed',
    latencyMs: 118,
    outboundIp: '203.0.113.11',
    outboundRegion: '本地备用出口',
    lastTestMessage: 'Mockdata SOCKS 代理连通正常'
  })

  const disabled = repositories.createProxy({
    name: `${namePrefix}停用代理`,
    description: 'Mockdata 停用代理，用于代理状态展示',
    type: 'http',
    host: '127.0.0.1',
    port: 18080,
    enabled: false
  }, adminAccess)

  return { http: http.id, socks: socks.id, disabled: disabled.id }
}

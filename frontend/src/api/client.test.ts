import { describe, expect, it } from 'vitest'

import { api, apiUrl, setMustChangePasswordHandler, setUnauthorizedHandler } from './client'

describe('api 客户端 barrel', () => {
  it('聚合全部逻辑域命名空间', () => {
    expect(Object.keys(api).sort()).toEqual([
      'accounts',
      'announcements',
      'anthropicOAuth',
      'apiKeys',
      'auditLogs',
      'auth',
      'authorizationOptions',
      'authorizations',
      'chat',
      'externalIntegrationSources',
      'geminiOAuth',
      'groups',
      'grokOAuth',
      'ipStats',
      'modelChecks',
      'myAccounts',
      'myAnthropicOAuth',
      'myApiKeys',
      'myAuthorizationOptions',
      'myAuthorizations',
      'myGeminiOAuth',
      'myGroups',
      'myGrokOAuth',
      'myModelChecks',
      'myOpenaiOAuth',
      'myOperationLogs',
      'myRouteStrategies',
      'myStats',
      'myTeams',
      'myUsageRecords',
      'myUiBootstrap',
      'oauthApplications',
      'openaiOAuth',
      'operationLogs',
      'providers',
      'proxies',
      'publicApiLogs',
      'responseInspectionPolicies',
      'routeStrategies',
      'runtimeLogs',
      'settings',
      'stats',
      'systemAccounts',
      'systemTeams',
      'tableMonitor',
      'uiBootstrap',
      'usageRecords'
    ].sort())
  })

  it('每个命名空间都是非空对象', () => {
    for (const [name, namespace] of Object.entries(api)) {
      expect(typeof namespace, `域 ${name} 应为对象`).toBe('object')
      expect(Object.keys(namespace as object).length, `域 ${name} 应包含方法`).toBeGreaterThan(0)
    }
  })

  it('re-export http 公开工具与 handler 注入函数', () => {
    expect(typeof apiUrl).toBe('function')
    expect(apiUrl('/x')).toContain('/__aisys__/api/x')
    expect(typeof setUnauthorizedHandler).toBe('function')
    expect(typeof setMustChangePasswordHandler).toBe('function')
  })
})

import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'

import { loadCurrentUser } from '@/composables/useAuth'
import { getPreferredEntryPath } from '@/composables/useMenuMode'

declare module 'vue-router' {
  interface RouteMeta {
    title: string
    description: string
    keepAlive?: boolean
    heavy?: boolean
    menuGroup?: string
    menuGroupTitle?: string
    public?: boolean
    viewScope?: 'admin' | 'self'
    roles?: Array<'admin' | 'user'>
  }
}

export const menuRoutes: RouteRecordRaw[] = [
  {
    path: '/my-stats',
    component: () => import('@/views/stats/StatsView.vue'),
    meta: {
      title: '统计概览',
      description: '查看自己的请求数、失败趋势、Token 消耗、平均总耗时、模型分布、错误 Top 10 和成本概览。',
      viewScope: 'self',
      heavy: true
    }
  },
  {
    path: '/my-accounts',
    component: () => import('@/views/accounts/AccountsView.vue'),
    meta: {
      title: '我的 AI 账户',
      description: '创建和维护自己的 OpenAI OAuth / API Key 账户。',
      viewScope: 'self',
      heavy: true
    }
  },
  {
    path: '/my-groups',
    component: () => import('@/views/groups/GroupsView.vue'),
    meta: {
      title: '我的分组',
      description: '维护自己的账户分组，API Key 再绑定分组统一调度。',
      viewScope: 'self'
    }
  },
  {
    path: '/my-api-keys',
    component: () => import('@/views/api-keys/ApiKeysView.vue'),
    meta: {
      title: '我的 API Key',
      description: '管理自己的 API Key，绑定自己的分组。',
      viewScope: 'self',
      heavy: true
    }
  },
  {
    path: '/my-model-checks',
    component: () => import('@/views/model-checks/ModelChecksView.vue'),
    meta: {
      title: '模型检测',
      description: '对我的 AI 账户发起目标模型可信度检测，并查看历史检测结果。',
      viewScope: 'self',
      heavy: true
    }
  },
  {
    path: '/my-authorizations',
    component: () => import('@/views/authorizations/AuthorizationsView.vue'),
    meta: {
      title: '授权操作',
      description: '管理自己授权出去或授权给自己的账户、分组和团队授权，不在这里展示消耗统计。',
      menuGroup: 'my-authorization',
      menuGroupTitle: '我的授权',
      viewScope: 'self',
      heavy: true
    }
  },
  {
    path: '/my-authorization-team-usage',
    component: () => import('@/views/authorizations/AuthorizationTeamUsageView.vue'),
    meta: {
      title: '团队消耗明细',
      description: '按被授权团队查看自己授权出去资源的日期范围团队消耗，并可跳转到成员用户消耗。',
      menuGroup: 'my-authorization',
      menuGroupTitle: '我的授权',
      viewScope: 'self',
      heavy: true
    }
  },
  {
    path: '/my-authorization-user-usage',
    component: () => import('@/views/authorizations/AuthorizationUserUsageView.vue'),
    meta: {
      title: '用户消耗明细',
      description: '按被授权用户查看自己授权出去资源的日期范围消耗，包含被授权团队里的成员用户。',
      menuGroup: 'my-authorization',
      menuGroupTitle: '我的授权',
      viewScope: 'self',
      heavy: true
    }
  },
  {
    path: '/my-teams',
    component: () => import('@/views/system-teams/SystemTeamsView.vue'),
    meta: {
      title: '我的授权团队',
      description: '查看自己加入的授权团队和团队成员。',
      menuGroup: 'my-authorization',
      menuGroupTitle: '我的授权',
      viewScope: 'self'
    }
  },
  {
    path: '/my-usage-stats',
    component: () => import('@/views/usage-stats/UsageStatsView.vue'),
    meta: {
      title: '我的用量',
      description: '查看你可使用账户的每日消耗趋势，仅统计你自己发起的调用。',
      viewScope: 'self',
      heavy: true
    }
  },
  {
    path: '/my-ai-performance',
    component: () => import('@/views/ai-performance/AiPerformanceView.vue'),
    meta: {
      title: 'AI性能监控',
      description: '追踪你可使用 AI 账户的小时级性能趋势，包含授权给你的账户实例。',
      viewScope: 'self',
      heavy: true
    }
  },
  {
    path: '/my-usage-records',
    component: () => import('@/views/usage-records/UsageRecordsView.vue'),
    meta: {
      title: '我的使用记录',
      description: '查看自己的网关请求、命中账户、Token 用量、成本和错误状态。',
      viewScope: 'self',
      heavy: true
    }
  },
  {
    path: '/my-operation-logs',
    component: () => import('@/views/operation-logs/OperationLogsView.vue'),
    meta: {
      title: '我的操作日志',
      description: '查看自己发起、管理员代操作和影响到自己的业务变更记录。',
      viewScope: 'self',
      heavy: true
    }
  },
  {
    path: '/stats',
    component: () => import('@/views/stats/StatsView.vue'),
    meta: {
      title: '统计概览',
      description: '按监控窗口查看请求数、失败趋势、Token 消耗、平均总耗时、模型分布、错误 Top 10 和系统性能。',
      viewScope: 'admin',
      roles: ['admin'],
      heavy: true
    }
  },
  {
    path: '/providers',
    component: () => import('@/views/providers/ProvidersView.vue'),
    meta: {
      title: '供应商',
      description: '管理当前支持的供应商能力与模型定价，当前启用 OpenAI。',
      menuGroup: 'ai-management',
      menuGroupTitle: 'AI 管理',
      viewScope: 'admin',
      roles: ['admin']
    }
  },
  {
    path: '/proxies',
    component: () => import('@/views/proxies/ProxiesView.vue'),
    meta: {
      title: '代理管理',
      description: '管理可绑定到账户的代理配置，支持刷新、测试和后续转发。',
      menuGroup: 'ai-management',
      menuGroupTitle: 'AI 管理',
      viewScope: 'admin',
      roles: ['admin']
    }
  },
  {
    path: '/accounts',
    component: () => import('@/views/accounts/AccountsView.vue'),
    meta: {
      title: 'AI 账户管理',
      description: '按系统账户管理 OpenAI OAuth / API Key 账户，统一查看状态、并发、代理和错误策略。',
      menuGroup: 'ai-management',
      menuGroupTitle: 'AI 管理',
      viewScope: 'admin',
      roles: ['admin'],
      heavy: true
    }
  },
  {
    path: '/groups',
    component: () => import('@/views/groups/GroupsView.vue'),
    meta: {
      title: 'AI 分组管理',
      description: '按系统账户管理账户分组、授权分组和调度边界。',
      menuGroup: 'ai-management',
      menuGroupTitle: 'AI 管理',
      viewScope: 'admin',
      roles: ['admin']
    }
  },
  {
    path: '/authorizations',
    component: () => import('@/views/authorizations/AuthorizationsView.vue'),
    meta: {
      title: '统一授权',
      description: '管理谁把哪些 AI 账户或分组授权给个人或团队，不在这里展示消耗统计。',
      menuGroup: 'authorization',
      menuGroupTitle: '统一授权管理',
      viewScope: 'admin',
      roles: ['admin'],
      heavy: true
    }
  },
  {
    path: '/authorization-team-usage',
    component: () => import('@/views/authorizations/AuthorizationTeamUsageView.vue'),
    meta: {
      title: '团队消耗明细',
      description: '按被授权团队查看日期范围内的团队总消耗，并可跳转到团队成员用户消耗。',
      menuGroup: 'authorization',
      menuGroupTitle: '统一授权管理',
      viewScope: 'admin',
      roles: ['admin'],
      heavy: true
    }
  },
  {
    path: '/authorization-user-usage',
    component: () => import('@/views/authorizations/AuthorizationUserUsageView.vue'),
    meta: {
      title: '用户消耗明细',
      description: '按被授权用户查看日期范围内的授权消耗，包含被授权团队里的成员用户。',
      menuGroup: 'authorization',
      menuGroupTitle: '统一授权管理',
      viewScope: 'admin',
      roles: ['admin'],
      heavy: true
    }
  },
  {
    path: '/authorization-teams',
    component: () => import('@/views/system-teams/SystemTeamsView.vue'),
    meta: {
      title: '授权团队',
      description: '管理授权团队和成员，支持把多个系统账户归纳到一个团队内承接授权。',
      menuGroup: 'authorization',
      menuGroupTitle: '统一授权管理',
      viewScope: 'admin',
      roles: ['admin']
    }
  },
  {
    path: '/api-keys',
    component: () => import('@/views/api-keys/ApiKeysView.vue'),
    meta: {
      title: 'API Key 管理',
      description: '按系统账户管理 API Key 和分组绑定。',
      menuGroup: 'ai-management',
      menuGroupTitle: 'AI 管理',
      viewScope: 'admin',
      roles: ['admin'],
      heavy: true
    }
  },
  {
    path: '/model-checks',
    component: () => import('@/views/model-checks/ModelChecksView.vue'),
    meta: {
      title: '模型检测',
      description: '对 AI 账户发起目标模型可信度检测，并查看历史检测结果。',
      menuGroup: 'ai-management',
      menuGroupTitle: 'AI 管理',
      viewScope: 'admin',
      roles: ['admin'],
      heavy: true
    }
  },
  {
    path: '/usage-stats',
    component: () => import('@/views/usage-stats/UsageStatsView.vue'),
    meta: {
      title: '用量统计管理',
      description: '按系统账户查看可使用账户的每日消耗趋势，仅统计所选用户发起的调用。',
      menuGroup: 'ai-management',
      menuGroupTitle: 'AI 管理',
      viewScope: 'admin',
      roles: ['admin'],
      heavy: true
    }
  },
  {
    path: '/ai-performance',
    component: () => import('@/views/ai-performance/AiPerformanceView.vue'),
    meta: {
      title: 'AI性能监控',
      description: '按系统账户查看其可使用 AI 账户的小时级性能趋势，包含授权实例。',
      menuGroup: 'ai-management',
      menuGroupTitle: 'AI 管理',
      viewScope: 'admin',
      roles: ['admin'],
      heavy: true
    }
  },
  {
    path: '/usage-records',
    component: () => import('@/views/usage-records/UsageRecordsView.vue'),
    meta: {
      title: '使用记录管理',
      description: '按系统账户查看网关请求、命中账户、Token 用量、成本和错误状态。',
      menuGroup: 'ai-management',
      menuGroupTitle: 'AI 管理',
      viewScope: 'admin',
      roles: ['admin'],
      heavy: true
    }
  },
  {
    path: '/operation-logs',
    component: () => import('@/views/operation-logs/OperationLogsView.vue'),
    meta: {
      title: '操作日志管理',
      description: '查看所有用户的业务变更操作日志，追溯操作人、资源、影响用户和 traceId。',
      menuGroup: 'log-management',
      menuGroupTitle: '日志管理',
      viewScope: 'admin',
      roles: ['admin'],
      heavy: true
    }
  },
  {
    path: '/audit-logs',
    component: () => import('@/views/audit-logs/AuditLogsView.vue'),
    meta: {
      title: '审计日志',
      description: '按 traceId 追溯原始请求、上游尝试、响应头和完整 payload，失败请求全量记录，成功请求按采样保存。',
      menuGroup: 'log-management',
      menuGroupTitle: '日志管理',
      viewScope: 'admin',
      roles: ['admin'],
      heavy: true
    }
  },
  {
    path: '/runtime-logs',
    component: () => import('@/views/runtime-logs/RuntimeLogsView.vue'),
    meta: {
      title: '日志搜索',
      description: '索引查询检索最近 3 天运行日志，关键字只模糊匹配消息列；grep 模式由后端 rg 按任意关键字扫描文件日志。',
      menuGroup: 'log-management',
      menuGroupTitle: '日志管理',
      viewScope: 'admin',
      roles: ['admin'],
      heavy: true
    }
  },
  {
    path: '/table-monitor',
    component: () => import('@/views/table-monitor/TableMonitorView.vue'),
    meta: {
      title: '表监控',
      description: '查看业务库、数据集目录库和统计结果库的表大小、行数、文件空闲空间和近期增长；usage shard 文件集合观测仍在后续增强阶段。',
      menuGroup: 'system-operations',
      menuGroupTitle: '系统运维',
      viewScope: 'admin',
      roles: ['admin'],
      heavy: true
    }
  },
  {
    path: '/ip-stats',
    component: () => import('@/views/ip-stats/IpStatsView.vue'),
    meta: {
      title: 'IP管理',
      description: '查看来源 IP 的请求、Token、成本、失败率、活跃天数和封禁状态。',
      menuGroup: 'system-operations',
      menuGroupTitle: '系统运维',
      viewScope: 'admin',
      roles: ['admin'],
      heavy: true
    }
  },
  {
    path: '/stream-intercept-policies',
    component: () => import('@/views/stream-intercept-policies/StreamInterceptPoliciesView.vue'),
    meta: {
      title: '流式拦截策略',
      description: '管理 200 + SSE 内部事件、广告污染和协议内失败的拦截策略。',
      menuGroup: 'system-operations',
      menuGroupTitle: '系统运维',
      viewScope: 'admin',
      roles: ['admin'],
      heavy: true
    }
  },
  {
    path: '/external-integration-sources',
    component: () => import('@/views/external-integration-sources/ExternalIntegrationSourcesView.vue'),
    meta: {
      title: '公开接口授权',
      description: '管理允许调用公开接口的来源系统、Token、到期时间、启停状态和限频规则。',
      menuGroup: 'system-operations',
      menuGroupTitle: '系统运维',
      viewScope: 'admin',
      roles: ['admin'],
      heavy: true
    }
  },
  {
    path: '/announcements',
    component: () => import('@/views/announcements/AnnouncementsView.vue'),
    meta: {
      title: '公告管理',
      description: '维护面向所有登录用户展示的平台公告，支持重要性标记、发布和下线。',
      menuGroup: 'system-operations',
      menuGroupTitle: '系统运维',
      viewScope: 'admin',
      roles: ['admin']
    }
  },
  {
    path: '/system-accounts',
    component: () => import('@/views/system-accounts/SystemAccountsView.vue'),
    meta: {
      title: '系统账户管理',
      description: '管理后台登录账号、角色、状态和初始密码。',
      menuGroup: 'system-operations',
      menuGroupTitle: '系统运维',
      viewScope: 'admin',
      roles: ['admin']
    }
  },
  {
    path: '/settings',
    component: () => import('@/views/settings/SettingsView.vue'),
    meta: {
      title: '系统设置',
      description: '配置本地网关与账户调度的默认策略，不覆盖账号里的显式配置。',
      menuGroup: 'system-operations',
      menuGroupTitle: '系统运维',
      viewScope: 'admin',
      roles: ['admin']
    }
  }
]

export const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    {
      path: '/',
      component: { render: () => null },
      meta: {
        title: '入口',
        description: '根据登录用户偏好进入控制台',
        keepAlive: false
      }
    },
    {
      path: '/login',
      component: () => import('@/views/login/LoginView.vue'),
      meta: {
        title: '登录',
        description: '登录聚合 AI 控制台',
        public: true
      }
    },
    ...menuRoutes
  ]
})

router.beforeEach(async (to) => {
  const user = await loadCurrentUser()
  if (to.meta.public) {
    if (to.path === '/login' && user) return getPreferredEntryPath(user)
    return true
  }
  if (!user) {
    return { path: '/login', query: { redirect: to.fullPath } }
  }
  if (to.path === '/') {
    return getPreferredEntryPath(user)
  }
  if (to.meta.roles?.length && !to.meta.roles.includes(user.role)) {
    return getPreferredEntryPath(user)
  }
  return true
})

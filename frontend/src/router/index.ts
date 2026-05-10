import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'

import { loadCurrentUser } from '@/composables/useAuth'
import { getPreferredEntryPath } from '@/composables/useMenuMode'

declare module 'vue-router' {
  interface RouteMeta {
    title: string
    description: string
    keepAlive?: boolean
    public?: boolean
    viewScope?: 'admin' | 'self'
    roles?: Array<'admin' | 'user'>
  }
}

export const menuRoutes: RouteRecordRaw[] = [
  {
    path: '/my-accounts',
    component: () => import('@/views/accounts/AccountsView.vue'),
    meta: {
      title: '我的 AI 账户',
      description: '创建和维护自己的 OpenAI OAuth / API Key 账户。',
      viewScope: 'self'
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
    path: '/my-teams',
    component: () => import('@/views/system-teams/SystemTeamsView.vue'),
    meta: {
      title: '我的团队',
      description: '查看自己加入的团队和团队成员。',
      viewScope: 'self'
    }
  },
  {
    path: '/my-authorizations',
    component: () => import('@/views/authorizations/AuthorizationsView.vue'),
    meta: {
      title: '我的授权',
      description: '管理自己授权出去或授权给自己的账户、分组和团队授权。',
      viewScope: 'self'
    }
  },
  {
    path: '/my-api-keys',
    component: () => import('@/views/api-keys/ApiKeysView.vue'),
    meta: {
      title: '我的 API Key',
      description: '管理自己的 API Key，绑定自有或授权给自己的分组。',
      viewScope: 'self'
    }
  },
  {
    path: '/my-usage-stats',
    component: () => import('@/views/usage-stats/UsageStatsView.vue'),
    meta: {
      title: '我的用量',
      description: '查看自己账户、授权资源和 API Key 的用量统计。',
      viewScope: 'self'
    }
  },
  {
    path: '/my-usage-records',
    component: () => import('@/views/usage-records/UsageRecordsView.vue'),
    meta: {
      title: '我的使用记录',
      description: '查看自己的网关请求、命中账户、Token 用量、成本和错误状态。',
      viewScope: 'self'
    }
  },
  {
    path: '/stats',
    component: () => import('@/views/stats/StatsView.vue'),
    meta: {
      title: '统计概览',
      description: '按监控窗口查看有效请求、Token 使用趋势、模型分布、消耗错误和系统性能。',
      viewScope: 'admin',
      roles: ['admin']
    }
  },
  {
    path: '/providers',
    component: () => import('@/views/providers/ProvidersView.vue'),
    meta: {
      title: '供应商',
      description: '管理当前支持的供应商能力与模型定价，当前启用 OpenAI。',
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
      viewScope: 'admin',
      roles: ['admin']
    }
  },
  {
    path: '/groups',
    component: () => import('@/views/groups/GroupsView.vue'),
    meta: {
      title: 'AI 分组管理',
      description: '按系统账户管理账户分组、授权分组和调度边界。',
      viewScope: 'admin',
      roles: ['admin']
    }
  },
  {
    path: '/system-teams',
    component: () => import('@/views/system-teams/SystemTeamsView.vue'),
    meta: {
      title: '系统团队管理',
      description: '管理团队和成员，支持把多个系统账户归纳到一个团队内。',
      viewScope: 'admin',
      roles: ['admin']
    }
  },
  {
    path: '/authorizations',
    component: () => import('@/views/authorizations/AuthorizationsView.vue'),
    meta: {
      title: '统一授权管理',
      description: '按系统账户统一管理账户、分组、团队授权，并查看授权用量明细。',
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
      viewScope: 'admin',
      roles: ['admin']
    }
  },
  {
    path: '/usage-stats',
    component: () => import('@/views/usage-stats/UsageStatsView.vue'),
    meta: {
      title: '用量统计管理',
      description: '按系统账户查看多日累计用量，并查看授权用户与团队的消耗情况。',
      viewScope: 'admin',
      roles: ['admin']
    }
  },
  {
    path: '/usage-records',
    component: () => import('@/views/usage-records/UsageRecordsView.vue'),
    meta: {
      title: '使用记录管理',
      description: '按系统账户查看网关请求、命中账户、Token 用量、成本和错误状态。',
      viewScope: 'admin',
      roles: ['admin']
    }
  },
  {
    path: '/audit-logs',
    component: () => import('@/views/audit-logs/AuditLogsView.vue'),
    meta: {
      title: '审计日志',
      description: '按 traceId 追溯原始请求、上游尝试、响应头和完整 payload，失败请求全量记录，成功请求按采样保存。',
      viewScope: 'admin',
      roles: ['admin']
    }
  },
  {
    path: '/runtime-logs',
    component: () => import('@/views/runtime-logs/RuntimeLogsView.vue'),
    meta: {
      title: '日志搜索',
      description: '索引查询检索最近 3 天运行日志，grep 模式按关键字扫描日志文件，结果默认展示最新记录。',
      viewScope: 'admin',
      roles: ['admin']
    }
  },
  {
    path: '/announcements',
    component: () => import('@/views/announcements/AnnouncementsView.vue'),
    meta: {
      title: '公告管理',
      description: '维护面向所有登录用户展示的平台公告，支持重要性标记、发布和下线。',
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
      viewScope: 'admin',
      roles: ['admin']
    }
  }
]

export const router = createRouter({
  history: createWebHistory(),
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

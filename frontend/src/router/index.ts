import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'

import { loadCurrentUser } from '@/composables/useAuth'

declare module 'vue-router' {
  interface RouteMeta {
    title: string
    description: string
    public?: boolean
    roles?: Array<'admin' | 'user'>
  }
}

export const menuRoutes: RouteRecordRaw[] = [
  {
    path: '/stats',
    component: () => import('@/views/stats/StatsView.vue'),
    meta: {
      title: '统计概览',
      description: '查看今日请求、Token 使用趋势、模型分布、错误情况和系统性能。',
      roles: ['admin']
    }
  },
  {
    path: '/usage-stats',
    component: () => import('@/views/usage-stats/UsageStatsView.vue'),
    meta: {
      title: '用量统计',
      description: '按账户查看多日累计用量，并查看授权用户与团队的消耗情况。'
    }
  },
  {
    path: '/providers',
    component: () => import('@/views/providers/ProvidersView.vue'),
    meta: {
      title: '供应商',
      description: '管理当前支持的供应商能力与模型定价，第一期仅启用 OpenAI。',
      roles: ['admin']
    }
  },
  {
    path: '/accounts',
    component: () => import('@/views/accounts/AccountsView.vue'),
    meta: {
      title: 'AI账户管理',
      description: '创建和维护 OpenAI OAuth / API Key 账户，统一管理状态、并发、代理和错误策略。'
    }
  },
  {
    path: '/groups',
    component: () => import('@/views/groups/GroupsView.vue'),
    meta: {
      title: 'AI账户分组',
      description: '按供应商划分账户，账户主动选择归属分组，API Key 再绑定分组统一调度。'
    }
  },
  {
    path: '/api-keys',
    component: () => import('@/views/api-keys/ApiKeysView.vue'),
    meta: {
      title: 'API 密钥',
      description: 'API Key 绑定分组，外部请求按分组边界完成中转调度。'
    }
  },
  {
    path: '/proxies',
    component: () => import('@/views/proxies/ProxiesView.vue'),
    meta: {
      title: '代理管理',
      description: '管理可绑定到账户的代理配置，支持刷新、测试和后续转发。',
      roles: ['admin']
    }
  },
  {
    path: '/usage-records',
    component: () => import('@/views/usage-records/UsageRecordsView.vue'),
    meta: {
      title: '使用记录',
      description: '记录网关请求、命中账户、token 用量、成本和错误状态。'
    }
  },
  {
    path: '/system-accounts',
    component: () => import('@/views/system-accounts/SystemAccountsView.vue'),
    meta: {
      title: '系统账户管理',
      description: '管理后台登录账号、角色、状态和初始密码。',
      roles: ['admin']
    }
  },
  {
    path: '/system-teams',
    component: () => import('@/views/system-teams/SystemTeamsView.vue'),
    meta: {
      title: '系统团队管理',
      description: '管理团队和成员，支持把多个系统账户归纳到一个团队内。'
    }
  },
  {
    path: '/authorizations',
    component: () => import('@/views/authorizations/AuthorizationsView.vue'),
    meta: {
      title: '统一授权管理',
      description: '统一管理账户、分组、团队授权，并查看团队与系统账户维度的用量明细。'
    }
  },
  {
    path: '/settings',
    component: () => import('@/views/settings/SettingsView.vue'),
    meta: {
      title: '系统设置',
      description: '配置本地网关与账户调度的默认策略，不覆盖账号里的显式配置。'
    }
  }
]

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    {
      path: '/login',
      component: () => import('@/views/login/LoginView.vue'),
      meta: {
        title: '登录',
        description: '登录聚合 AI 控制台',
        public: true
      }
    },
    { path: '/', redirect: '/accounts' },
    ...menuRoutes
  ]
})

router.beforeEach(async (to) => {
  const user = await loadCurrentUser()
  if (to.meta.public) {
    if (to.path === '/login' && user) return '/accounts'
    return true
  }
  if (!user) {
    return { path: '/login', query: { redirect: to.fullPath } }
  }
  if (to.meta.roles?.length && !to.meta.roles.includes(user.role)) {
    return '/accounts'
  }
  return true
})

import { createRouter, createWebHistory } from 'vue-router'

declare module 'vue-router' {
  interface RouteMeta {
    title: string
    description: string
  }
}

export const menuRoutes = [
  {
    path: '/providers',
    component: () => import('@/views/providers/ProvidersView.vue'),
    meta: {
      title: '供应商',
      description: '第一期只启用 OpenAI，账户创建方式支持 OAuth 和 API Key；模型价格来自供应商模型目录，网关计费复用同一份数据。'
    }
  },
  {
    path: '/accounts',
    component: () => import('@/views/accounts/AccountsView.vue'),
    meta: {
      title: '账户管理',
      description: '创建和维护 OpenAI OAuth / API Key 账户，配置状态、并发、代理和错误策略。'
    }
  },
  {
    path: '/groups',
    component: () => import('@/views/groups/GroupsView.vue'),
    meta: {
      title: '分组',
      description: '分组归属于供应商；绑定账户时只允许选择同供应商账户。'
    }
  },
  {
    path: '/api-keys',
    component: () => import('@/views/api-keys/ApiKeysView.vue'),
    meta: {
      title: 'API 密钥',
      description: 'API Key 绑定分组，外部请求会在分组内选择可用账户完成中转。'
    }
  },
  {
    path: '/proxies',
    component: () => import('@/views/proxies/ProxiesView.vue'),
    meta: {
      title: '代理管理',
      description: '代理可绑定到 OpenAI OAuth 账户，用于刷新、测试和后续真实转发。'
    }
  },
  {
    path: '/usage-records',
    component: () => import('@/views/usage-records/UsageRecordsView.vue'),
    meta: {
      title: '使用记录',
      description: '记录网关请求、命中账户、token 用量、成本和错误状态，包含每一次上游尝试（含重试）。'
    }
  },
  {
    path: '/settings',
    component: () => import('@/views/settings/SettingsView.vue'),
    meta: {
      title: '系统设置',
      description: '这些设置只作为本地网关和账户调度的默认策略，不覆盖账号编辑里的显式配置。'
    }
  }
]

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/providers' },
    ...menuRoutes
  ]
})

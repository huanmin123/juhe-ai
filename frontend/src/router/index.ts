import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'

import { setMustChangePasswordHandler, setUnauthorizedHandler } from '@/api/client'
import { clearAuthState, loadCurrentUser } from '@/composables/useAuth'
import { deploymentCapabilityEnabled, type RouteCapability } from '@/composables/useCapabilities'
import { getPreferredEntryPath } from '@/composables/useMenuMode'
import type { SystemAccountRole } from '@/types/domain'
import { PROFILE_PATH, requiredPasswordProfileLocation } from '@/views/profile/profileNavigation'
import { installRouteLoadRecovery, recoverRouteAssetLoadError } from './routeLoadRecovery'

export { recoverRouteAssetLoadError }

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
    roles?: SystemAccountRole[]
    // 部署级能力位（2026-11-09）：false 时菜单隐藏且路由守卫重定向。
    requiresCapability?: RouteCapability
  }
}

// 模型检测是常驻功能（2026-09-21 起前端与后端均无总开关）：菜单与路由
// 始终可用，访问控制由登录角色与后端管理面权限承担。
const managementRoles: SystemAccountRole[] = ['super_admin', 'admin']

export const menuRoutes: RouteRecordRaw[] = [
  {
    path: '/my-stats',
    component: () => import('@/views/stats/StatsView.vue'),
    meta: {
      title: '统计概览',
      description: '查看自己的请求数、失败趋势、Token 消耗、平均总耗时、模型分布、错误 Top 10 和成本概览。',
      viewScope: 'self',
      keepAlive: true,
      heavy: true
    }
  },
  {
    path: '/my-accounts',
    component: () => import('@/views/accounts/AccountsView.vue'),
    meta: {
      title: 'AI 账户',
      description: '创建和维护自己的 AI 账户；按场景类型筛选，一个账户只属于一个场景。',
      menuGroup: 'my-accounts',
      menuGroupTitle: 'AI 账户',
      viewScope: 'self',
      heavy: true
    }
  },
  {
    path: '/my-media-jobs',
    component: () => import('@/views/media-jobs/MyMediaJobsView.vue'),
    meta: {
      title: '任务中心',
      description: '查看自己名下的异步媒体任务类型、状态、成本与返回内容定位。',
      viewScope: 'self',
      heavy: true
    }
  },
  {
    path: '/my-groups',
    component: () => import('@/views/groups/GroupsView.vue'),
    meta: {
      title: '我的分组',
      description: '维护自己的账户分组，策略路由再绑定分组统一调度。',
      viewScope: 'self'
    }
  },
  {
    path: '/my-api-keys',
    component: () => import('@/views/api-keys/ApiKeysView.vue'),
    meta: {
      title: '我的 API Key',
      description: '管理自己的 API Key，绑定自己的策略路由。',
      viewScope: 'self',
      heavy: true
    }
  },
  {
    path: '/my-route-strategies',
    component: () => import('@/views/route-strategies/RouteStrategiesView.vue'),
    meta: {
      title: '我的策略路由',
      description: '维护自己的策略路由，统一配置分组绑定和路由模式。',
      viewScope: 'self',
      heavy: true
    }
  },
  {
    path: '/my-scheduling-preferences',
    component: () => import('@/views/scheduling-preferences/SchedulingPreferencesView.vue'),
    meta: {
      title: '我的调度偏好',
      description: '维护自己的对话域调度偏好（成本优先 / 速度优先），供 API Key 绑定。',
      viewScope: 'self',
      heavy: true
    }
  },
  {
    path: '/my-security-policies',
    component: () => import('@/views/security-policies/SecurityPoliciesView.vue'),
    meta: {
      title: '我的安全策略',
      description: '维护自己的出站内容脱敏策略（打码 / 占位符），供 API Key 绑定。',
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
    path: '/my-models',
    component: () => import('@/views/providers/ModelPlazaView.vue'),
    meta: {
      title: '模型广场',
      description: '跨供应商浏览全部可用模型，并维护自己的个人模型、价格和映射目标候选。',
      viewScope: 'self'
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
    path: '/my-ai-health',
    component: () => import('@/views/ai-health/AiHealthView.vue'),
    meta: {
      title: 'AI健康监控',
      description: '按小时查看你可使用 AI 账户最近一个月的健康检查结果。',
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
      roles: managementRoles,
      keepAlive: true,
      heavy: true
    }
  },
  {
    path: '/api-keys',
    component: () => import('@/views/api-keys/ApiKeysView.vue'),
    meta: {
      title: 'API Key 管理',
      description: '按系统账户管理 API Key，并为 API Key 选择策略路由。',
      menuGroup: 'ai-dispatch',
      menuGroupTitle: '密钥与调度',
      viewScope: 'admin',
      roles: managementRoles,
      heavy: true
    }
  },
  {
    path: '/accounts',
    component: () => import('@/views/accounts/AccountsView.vue'),
    meta: {
      title: 'AI 账户',
      description: '按系统账户管理 AI 账户；按场景类型筛选，一个账户只属于一个场景。',
      menuGroup: 'ai-assets',
      menuGroupTitle: 'AI 账户',
      viewScope: 'admin',
      roles: managementRoles,
      heavy: true
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
      roles: managementRoles,
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
      roles: managementRoles,
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
      roles: managementRoles,
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
      roles: managementRoles
    }
  },
  {
    path: '/model-checks',
    component: () => import('@/views/model-checks/ModelChecksView.vue'),
    meta: {
      title: '模型检测',
      description: '对 AI 账户发起目标模型可信度检测，并查看历史检测结果。',
      viewScope: 'admin',
      roles: managementRoles,
      heavy: true
    }
  },
  {
    path: '/usage-stats',
    component: () => import('@/views/usage-stats/UsageStatsView.vue'),
    meta: {
      title: '用量统计管理',
      description: '按系统账户查看可使用账户的每日消耗趋势，仅统计所选用户发起的调用。',
      menuGroup: 'ai-observability',
      menuGroupTitle: '监控与任务',
      viewScope: 'admin',
      roles: managementRoles,
      heavy: true
    }
  },
  {
    path: '/ai-performance',
    component: () => import('@/views/ai-performance/AiPerformanceView.vue'),
    meta: {
      title: 'AI性能监控',
      description: '按系统账户查看其可使用 AI 账户的小时级性能趋势，包含授权实例。',
      menuGroup: 'ai-observability',
      menuGroupTitle: '监控与任务',
      viewScope: 'admin',
      roles: managementRoles,
      heavy: true
    }
  },
  {
    path: '/ai-health',
    component: () => import('@/views/ai-health/AiHealthView.vue'),
    meta: {
      title: 'AI健康监控',
      description: '按账户查看最近一个月逐小时的健康检查结果。',
      menuGroup: 'ai-observability',
      menuGroupTitle: '监控与任务',
      viewScope: 'admin',
      roles: managementRoles,
      heavy: true
    }
  },
  {
    path: '/usage-records',
    component: () => import('@/views/usage-records/UsageRecordsView.vue'),
    meta: {
      title: '使用记录管理',
      description: '按系统账户查看网关请求、命中账户、Token 用量、成本和错误状态。',
      viewScope: 'admin',
      roles: managementRoles,
      heavy: true
    }
  },
  {
    path: '/media-jobs',
    component: () => import('@/views/media-jobs/MediaJobsView.vue'),
    meta: {
      title: '媒体任务',
      description: '查看异步媒体任务的类型、状态、归属 API Key / AI 账户、成本和错误摘要。',
      menuGroup: 'ai-observability',
      menuGroupTitle: '监控与任务',
      viewScope: 'admin',
      roles: managementRoles,
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
      roles: managementRoles,
      heavy: true
    }
  },
  {
    path: '/public-api-logs',
    component: () => import('@/views/public-api-logs/PublicApiLogsView.vue'),
    meta: {
      title: '公开接口日志',
      description: '查看外部来源系统公开接口的调用时间、耗时、状态码、请求摘要和响应摘要。',
      menuGroup: 'log-management',
      menuGroupTitle: '日志管理',
      viewScope: 'admin',
      roles: managementRoles,
      heavy: true
    }
  },
  {
    path: '/audit-logs',
    component: () => import('@/views/audit-logs/AuditLogsView.vue'),
    meta: {
      title: '审计日志',
      description: '按 traceId 追溯原始请求、请求链路、响应头和完整 payload，成功请求最近 1 小时全量热保留，之后按采样保存。',
      menuGroup: 'log-management',
      menuGroupTitle: '日志管理',
      viewScope: 'admin',
      roles: managementRoles,
      requiresCapability: 'auditLogs',
      heavy: true
    }
  },
  {
    path: '/models',
    component: () => import('@/views/providers/ModelPlazaView.vue'),
    meta: {
      title: '模型广场',
      description: '跨供应商浏览与维护模型目录：新增、改配、删除与归属筛选。',
      menuGroup: 'system-operations',
      menuGroupTitle: '系统运维',
      viewScope: 'admin',
      roles: managementRoles
    }
  },
  {
    path: '/proxies',
    component: () => import('@/views/proxies/ProxiesView.vue'),
    meta: {
      title: '代理管理',
      description: '管理可绑定到账户的代理配置，支持刷新、测试和后续转发。',
      menuGroup: 'system-operations',
      menuGroupTitle: '系统运维',
      viewScope: 'admin',
      roles: managementRoles
    }
  },
  {
    path: '/ip-stats',
    component: () => import('@/views/ip-stats/IpStatsView.vue'),
    meta: {
      title: 'IP管理',
      description: '查看来源 IP 的请求、Token、成本、失败率、活跃天数、封禁和白名单状态。',
      menuGroup: 'system-operations',
      menuGroupTitle: '系统运维',
      viewScope: 'admin',
      roles: managementRoles,
      heavy: true
    }
  },
  {
    path: '/response-inspection-policies',
    component: () => import('@/views/response-inspection-policies/ResponseInspectionPoliciesView.vue'),
    meta: {
      title: '响应检查策略',
      description: '管理 OpenAI v1 Chat / Responses 的 JSON 与 SSE 响应语义检查策略。',
      menuGroup: 'system-operations',
      menuGroupTitle: '系统运维',
      viewScope: 'admin',
      roles: managementRoles,
      heavy: true
    }
  },
  {
    path: '/route-strategies',
    component: () => import('@/views/route-strategies/RouteStrategiesView.vue'),
    meta: {
      title: '策略路由',
      description: '按系统账户管理策略路由，配置普通、混合、权重、故障回退和轮询路由。',
      menuGroup: 'ai-dispatch',
      menuGroupTitle: '密钥与调度',
      viewScope: 'admin',
      roles: managementRoles,
      heavy: true
    }
  },
  {
    path: '/groups',
    component: () => import('@/views/groups/GroupsView.vue'),
    meta: {
      title: 'AI 分组管理',
      description: '按系统账户管理账户分组、授权分组和调度边界。',
      menuGroup: 'ai-dispatch',
      menuGroupTitle: '密钥与调度',
      viewScope: 'admin',
      roles: managementRoles
    }
  },
  {
    path: '/scheduling-preferences',
    component: () => import('@/views/scheduling-preferences/SchedulingPreferencesView.vue'),
    meta: {
      title: '调度偏好',
      description: '按系统账户管理对话域调度偏好（成本优先 / 速度优先），供 API Key 绑定。',
      menuGroup: 'ai-dispatch',
      menuGroupTitle: '密钥与调度',
      viewScope: 'admin',
      roles: managementRoles,
      heavy: true
    }
  },
  {
    path: '/security-policies',
    component: () => import('@/views/security-policies/SecurityPoliciesView.vue'),
    meta: {
      title: '安全策略',
      description: '按系统账户管理出站内容脱敏策略（打码 / 占位符），供 API Key 绑定。',
      menuGroup: 'ai-dispatch',
      menuGroupTitle: '密钥与调度',
      viewScope: 'admin',
      roles: managementRoles,
      heavy: true
    }
  },
  {
    path: '/external-integration-sources',
    component: () => import('@/views/external-integration-sources/ExternalIntegrationSourcesView.vue'),
    meta: {
      title: '公开接口授权',
      description: '为外部来源后端签发来源 Token，并按接口资源授权控制公开接口访问、到期和限频。',
      menuGroup: 'system-operations',
      menuGroupTitle: '系统运维',
      viewScope: 'admin',
      roles: managementRoles,
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
      roles: managementRoles
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
      roles: managementRoles
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
      roles: managementRoles
    }
  },
  // 演练场（docs/functions/演练场设计.md §2）：模型能力试玩台一级菜单，
  // 四页均为一次性试玩——消息与产物只存页面内存，keepAlive 保证导航间保留页内状态。
  {
    path: '/playground/chat',
    component: () => import('@/views/playground/PlaygroundChatView.vue'),
    meta: {
      title: '对话试玩',
      description: '选择 AI 账户与模型做一次性流式对话试玩，消息只保存在页面内存中。',
      viewScope: 'self',
      menuGroup: 'playground',
      menuGroupTitle: '演练场',
      keepAlive: true,
      heavy: false
    }
  },
  {
    path: '/playground/images',
    component: () => import('@/views/playground/PlaygroundImageView.vue'),
    meta: {
      title: '图片生成',
      description: '选择账户与生图模型，按提示词同步生成图片并直接下载。',
      viewScope: 'self',
      menuGroup: 'playground',
      menuGroupTitle: '演练场',
      keepAlive: true,
      heavy: false
    }
  },
  {
    path: '/playground/videos',
    component: () => import('@/views/playground/PlaygroundVideoView.vue'),
    meta: {
      title: '视频生成',
      description: '创建异步视频生成任务并轮询进度，完成后内联播放与下载。',
      viewScope: 'self',
      menuGroup: 'playground',
      menuGroupTitle: '演练场',
      keepAlive: true,
      heavy: false
    }
  },
  {
    path: '/playground/speech',
    component: () => import('@/views/playground/PlaygroundSpeechView.vue'),
    meta: {
      title: '语音合成',
      description: '选择账户与语音模型，把文本合成为语音并播放下载。',
      viewScope: 'self',
      menuGroup: 'playground',
      menuGroupTitle: '演练场',
      keepAlive: true,
      heavy: false
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
    {
      path: '/service-recovering',
      component: () => import('@/views/login/ServiceRecoveringView.vue'),
      meta: {
        title: '服务正在恢复',
        description: '发布切换期间认证服务暂不可用，请稍后重试',
        public: true
      }
    },
    {
      path: PROFILE_PATH,
      component: () => import('@/views/profile/ProfileView.vue'),
      meta: {
        title: '个人信息',
        description: '查看账号资料、权限能力并维护用户名称与登录密码。',
        keepAlive: false,
        viewScope: 'self'
      }
    },
    // D13：旧四域路径不做兼容，统一由 /accounts 与 /my-accounts 承担；
    // 旧入口不再注册 redirect，避免与新单菜单路径互相遮蔽。
    ...menuRoutes,
    {
      path: '/:pathMatch(.*)*',
      name: 'not-found',
      component: { render: () => null },
      meta: {
        title: '页面不存在',
        description: '错误路径会回到当前用户默认入口'
      }
    }
  ]
})

installRouteLoadRecovery(router)

const AUTH_RETRY_DELAYS_MS = [200, 400, 800, 1600] as const
let navigationGeneration = 0

async function loadCurrentUserForNavigation(generation: number) {
  for (let attempt = 0; ; attempt += 1) {
    try {
      const user = await loadCurrentUser()
      if (generation !== navigationGeneration) throw new Error('navigation superseded')
      return user
    } catch (error: unknown) {
      if (generation !== navigationGeneration) throw error
      const delay = AUTH_RETRY_DELAYS_MS[attempt]
      if (delay === undefined) throw error
      await new Promise((resolve) => setTimeout(resolve, delay))
      if (generation !== navigationGeneration) throw error
    }
  }
}

router.beforeEach(async (to) => {
  const generation = ++navigationGeneration
  let user
  try {
    user = to.meta.public ? await loadCurrentUser() : await loadCurrentUserForNavigation(generation)
  } catch {
    if (generation !== navigationGeneration) return false
    if (to.meta.public) return true
    return {
      path: '/service-recovering',
      query: { redirect: to.fullPath }
    }
  }
  if (generation !== navigationGeneration) return false
  if (to.name === 'not-found') {
    if (user?.mustChangePassword) return requiredPasswordProfileLocation('/')
    if (user) return getPreferredEntryPath(user)
    return { path: '/login', query: { redirect: '/' } }
  }
  if (to.meta.public) {
    if (to.path === '/login' && user?.mustChangePassword) return requiredPasswordProfileLocation('/')
    if (to.path === '/login' && user) return getPreferredEntryPath(user)
    return true
  }
  if (!user) {
    return { path: '/login', query: { redirect: to.fullPath } }
  }
  if (to.meta.requiresCapability && !deploymentCapabilityEnabled(user.capabilities, to.meta.requiresCapability)) {
    return getPreferredEntryPath(user)
  }
  if (to.path === '/') {
    if (user.mustChangePassword) return requiredPasswordProfileLocation('/')
    return getPreferredEntryPath(user)
  }
  if (user.mustChangePassword && to.path !== PROFILE_PATH) {
    return requiredPasswordProfileLocation(to.fullPath)
  }
  if (to.meta.roles?.length && !to.meta.roles.includes(user.role)) {
    return getPreferredEntryPath(user)
  }
  return true
})

setUnauthorizedHandler(() => {
  clearAuthState()
  if (router.currentRoute.value.path !== '/login') {
    void router.replace({
      path: '/login',
      query: { redirect: router.currentRoute.value.fullPath }
    })
  }
})

let mustChangePasswordRefreshRunning = false

setMustChangePasswordHandler(() => {
  if (mustChangePasswordRefreshRunning) return
  mustChangePasswordRefreshRunning = true
  void loadCurrentUser(true)
    .then((user) => {
      if (user?.mustChangePassword && router.currentRoute.value.path !== PROFILE_PATH) {
        void router.replace(requiredPasswordProfileLocation(router.currentRoute.value.fullPath))
      }
    })
    .catch(() => undefined)
    .finally(() => {
      mustChangePasswordRefreshRunning = false
    })
})

import type { CurrentUserCapabilities } from '@/types/domain'

/** 部署级能力位（与用户角色正交）；新增能力时在此登记。 */
export type RouteCapability = 'auditLogs'

/**
 * deploymentCapabilityEnabled 判断当前部署是否提供某项能力。
 * capabilities 缺失（旧后端 / 旧会话快照）按开启处理，避免升级窗口误隐藏菜单。
 */
export function deploymentCapabilityEnabled(
  capabilities: CurrentUserCapabilities | undefined,
  capability: RouteCapability
): boolean {
  if (!capabilities) return true
  return capabilities[capability] !== false
}

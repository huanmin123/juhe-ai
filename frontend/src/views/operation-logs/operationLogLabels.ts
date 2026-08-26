export function moduleText(value: string): string {
  return moduleTextMap[value] ?? value
}

export function actionText(value: string): string {
  return actionTextMap[value] ?? value
}

export function actionColor(value: string): string {
  if (value.includes('delete') || value.includes('revoke') || value.includes('remove') || value.includes('cleanup')) return 'red'
  if (value.includes('create') || value.includes('publish') || value.includes('add')) return 'green'
  if (value.includes('test') || value.includes('refresh')) return 'cyan'
  if (value.includes('password') || value.includes('restore')) return 'orange'
  return 'blue'
}

export function resourceTypeText(value: string): string {
  return resourceTypeTextMap[value] ?? value
}

export function visibilityText(value: string): string {
  if (value === 'all_users') return '所有用户'
  if (value === 'admin_only') return '仅管理员'
  return '相关用户'
}

export function relationText(value: string): string {
  return relationTextMap[value] ?? value
}

export function visibilityReasonText(value: string): string {
  return visibilityReasonTextMap[value] ?? value
}

const moduleTextMap: Record<string, string> = {
  accounts: 'AI 账户',
  announcements: '公告中心',
  api_keys: 'API Key',
  authorizations: '统一授权',
  client_ip_stats: 'IP 管理',
  groups: '分组',
  openai_oauth: 'OpenAI OAuth',
  proxies: '代理',
  settings: '系统设置',
  table_monitor: '表监控',
  system_accounts: '系统账户',
  system_teams: '系统团队'
}

const actionTextMap: Record<string, string> = {
  add_members: '添加成员',
  allowlist: '加入白名单',
  bind_group: '绑定分组',
  blacklist: '封禁',
  cleanup_non_business_data: '清理非业务数据',
  cleanup_usage_records: '清理使用记录',
  create: '创建',
  create_account: '创建账户',
  create_from_code: '授权码创建账户',
  create_from_refresh_token: 'Refresh Token 创建账户',
  delete: '删除',
  publish: '发布',
  reauthorize_from_code: '重新授权',
  reauthorize_from_refresh_token: '重新授权',
  refresh_token: '刷新 Token',
  remove_member: '移除成员',
  reset_password: '重置密码',
  restore: '恢复',
  revoke: '回收',
  test: '检测',
  test_status_changed: '测试改状态',
  traffic_migration: '流量迁移',
  unallowlist: '移出白名单',
  unblock: '解封',
  unpublish: '下线',
  update: '更新',
  update_expire: '更新有效期',
  update_global: '更新全局设置',
  update_settings: '更新系统设置'
}

const resourceTypeTextMap: Record<string, string> = {
  account: 'AI 账户',
  announcement: '公告',
  api_key: 'API Key',
  authorization: '授权',
  client_ip: '客户端 IP',
  global_settings: '全局设置',
  group: '分组',
  proxy: '代理',
  system_account: '系统账户',
  system_settings: '系统设置',
  system_team: '系统团队',
  non_business_data: '非业务数据',
  usage_records: '使用记录'
}

const relationTextMap: Record<string, string> = {
  affected: '受影响',
  bound_resource: '绑定资源',
  created: '新建',
  deleted: '删除',
  grantee: '被授权',
  owner: '所有者',
  primary: '主资源',
  team_member: '团队成员'
}

const visibilityReasonTextMap: Record<string, string> = {
  actor_self: '本人操作',
  admin_managed_my_resource: '管理员代操作',
  authorization_grantee: '被授权用户',
  authorization_owner: '资源所有者',
  bound_resource_affected: '绑定资源影响',
  global_affected: '全局影响',
  resource_owner: '资源所有者',
  team_authorization: '团队授权',
  team_member: '团队成员'
}

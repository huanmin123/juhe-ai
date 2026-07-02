import type { RowActionItem } from '@/components/rowActions'

export const detailActions: RowActionItem[] = [{ key: 'detail', label: '详情', icon: 'detail', tone: 'info' }]

export const moduleOptions = [
  { label: '全部模块', value: 'all' },
  { label: '系统账户', value: 'system_accounts' },
  { label: 'AI 账户', value: 'accounts' },
  { label: '分组', value: 'groups' },
  { label: 'API Key', value: 'api_keys' },
  { label: '统一授权', value: 'authorizations' },
  { label: 'IP 管理', value: 'client_ip_stats' },
  { label: '系统团队', value: 'system_teams' },
  { label: '代理', value: 'proxies' },
  { label: '系统设置', value: 'settings' },
  { label: '公告中心', value: 'announcements' },
  { label: 'OpenAI OAuth', value: 'openai_oauth' },
  { label: '表监控', value: 'table_monitor' }
]

export const actionOptions = [
  { label: '全部动作', value: 'all' },
  { label: '创建', value: 'create' },
  { label: '创建账户', value: 'create_account' },
  { label: '授权码创建账户', value: 'create_from_code' },
  { label: 'Refresh Token 创建账户', value: 'create_from_refresh_token' },
  { label: '更新', value: 'update' },
  { label: '更新有效期', value: 'update_expire' },
  { label: '更新全局设置', value: 'update_global' },
  { label: '更新系统设置', value: 'update_settings' },
  { label: '删除', value: 'delete' },
  { label: '封禁', value: 'blacklist' },
  { label: '解封', value: 'unblock' },
  { label: '加入白名单', value: 'allowlist' },
  { label: '移出白名单', value: 'unallowlist' },
  { label: '绑定分组', value: 'bind_group' },
  { label: '流量迁移', value: 'traffic_migration' },
  { label: '回收授权', value: 'revoke' },
  { label: '添加成员', value: 'add_members' },
  { label: '移除成员', value: 'remove_member' },
  { label: '发布', value: 'publish' },
  { label: '下线', value: 'unpublish' },
  { label: '刷新 Token', value: 'refresh_token' },
  { label: '重新授权（授权码）', value: 'reauthorize_from_code' },
  { label: '重新授权（Refresh Token）', value: 'reauthorize_from_refresh_token' },
  { label: '恢复', value: 'restore' },
  { label: '重置密码', value: 'reset_password' },
  { label: '检测', value: 'test' },
  { label: '测试改状态', value: 'test_status_changed' },
  { label: '清理非业务数据', value: 'cleanup_non_business_data' },
  { label: '清理使用记录', value: 'cleanup_usage_records' }
]

export const resourceTypeOptions = [
  { label: '全部资源类型', value: 'all' },
  { label: 'AI 账户', value: 'account' },
  { label: '公告', value: 'announcement' },
  { label: 'API Key', value: 'api_key' },
  { label: '授权', value: 'authorization' },
  { label: '客户端 IP', value: 'client_ip' },
  { label: '全局设置', value: 'global_settings' },
  { label: '分组', value: 'group' },
  { label: '代理', value: 'proxy' },
  { label: '系统账户', value: 'system_account' },
  { label: '系统设置', value: 'system_settings' },
  { label: '系统团队', value: 'system_team' },
  { label: '非业务数据', value: 'non_business_data' },
  { label: '使用记录', value: 'usage_records' }
]

export const changeColumns = [
  { title: '字段', key: 'field', dataIndex: 'field', width: 160 },
  { title: '名称', key: 'label', dataIndex: 'label', width: 160 },
  { title: '变更前', key: 'before', width: 240 },
  { title: '变更后', key: 'after', width: 240 }
]

export const targetColumns = [
  { title: '对象', key: 'target', width: 220 },
  { title: '类型', key: 'type', width: 120 },
  { title: '归属用户', key: 'owner', width: 180 },
  { title: '关系', key: 'relation', width: 120 }
]

export const viewerColumns = [
  { title: '用户', key: 'user', width: 220 },
  { title: '可见原因', key: 'reason', width: 180 },
  { title: '详情级别', key: 'level', width: 100 }
]
